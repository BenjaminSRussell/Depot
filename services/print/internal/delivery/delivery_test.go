package delivery

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/services/print/internal/domain"
	"github.com/BenjaminSRussell/depot/services/print/internal/store"
)

func TestSMTPMailer_BuildsAMimeMessageWithThePDFAttached(t *testing.T) {
	var got struct {
		addr, from string
		to         []string
		msg        string
	}
	m := SMTPMailer{Addr: "relay.example:587", From: "billing@cedar.example", send: func(addr string, _ smtp.Auth, from string, to []string, msg []byte) error {
		got.addr, got.from, got.to, got.msg = addr, from, to, string(msg)
		return nil
	}}
	err := m.Send(context.Background(), Message{To: "ap@bluewater.example", Subject: "Invoice INV-1", Body: "Attached.", Filename: "INV-1.pdf", Attachment: []byte("%PDF-1.4 test")})
	if err != nil || got.addr != "relay.example:587" || got.from != "billing@cedar.example" || len(got.to) != 1 || got.to[0] != "ap@bluewater.example" {
		t.Fatalf("send: %v %+v", err, got)
	}
	for _, want := range []string{"Subject: Invoice INV-1", "Content-Type: application/pdf", `filename="INV-1.pdf"`, "JVBERi0xLjQgdGVzdA=="} {
		if !strings.Contains(got.msg, want) {
			t.Errorf("message lacks %q:\n%s", want, got.msg)
		}
	}
}

func TestSMTPMailer_RefusesHeaderInjectionAndBadAddresses(t *testing.T) {
	called := false
	m := SMTPMailer{Addr: "relay.example:25", From: "a@b.example", send: func(string, smtp.Auth, string, []string, []byte) error { called = true; return nil }}
	for _, msg := range []Message{
		{To: "ap@bluewater.example", Subject: "x\r\nBcc: spy@evil.example", Filename: "a.pdf"},
		{To: "not-an-address", Subject: "x", Filename: "a.pdf"},
		{To: "ap@bluewater.example", Subject: "x", Filename: "a\r\n.pdf"},
	} {
		if err := m.Send(context.Background(), msg); err == nil {
			t.Errorf("%+v was sent", msg)
		}
	}
	if called {
		t.Fatal("the relay was called")
	}
}

// fakeRelay is a minimal plain SMTP server; with silent it accepts the connection and never greets.
func fakeRelay(t *testing.T, starttls, silent bool) (addr string, got chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	got = make(chan string, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				if silent {
					time.Sleep(3 * time.Second)
					return
				}
				r := bufio.NewReader(c)
				fmt.Fprint(c, "220 relay ESMTP\r\n")
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					cmd := strings.ToUpper(strings.TrimSpace(line))
					switch {
					case strings.HasPrefix(cmd, "EHLO"):
						if starttls {
							fmt.Fprint(c, "250-relay\r\n250 STARTTLS\r\n")
						} else {
							fmt.Fprint(c, "250 relay\r\n")
						}
					case strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "RCPT"):
						fmt.Fprint(c, "250 ok\r\n")
					case cmd == "DATA":
						fmt.Fprint(c, "354 go\r\n")
						var body strings.Builder
						for {
							l, err := r.ReadString('\n')
							if err != nil || l == ".\r\n" {
								break
							}
							body.WriteString(l)
						}
						got <- body.String()
						fmt.Fprint(c, "250 queued\r\n")
					case cmd == "QUIT":
						fmt.Fprint(c, "221 bye\r\n")
						return
					default:
						fmt.Fprint(c, "250 ok\r\n")
					}
				}
			}()
		}
	}()
	return ln.Addr().String(), got
}

func TestSMTPMailer_SendsThroughARelayAndRefusesOneWithoutTLSWhenRequired(t *testing.T) {
	addr, got := fakeRelay(t, false, false)
	msg := Message{To: "ap@bluewater.example", Subject: "x", Body: "hi", Filename: "a.pdf", Attachment: []byte("%PDF-")}
	if err := (SMTPMailer{Addr: addr, From: "a@b.example", RequireTLS: true}).Send(context.Background(), msg); !errors.Is(err, ErrTLSRequired) {
		t.Fatalf("relay without TLS and RequireTLS: %v", err)
	}
	select {
	case body := <-got:
		t.Fatalf("a message went out in plaintext: %s", body)
	default:
	}
	if err := (SMTPMailer{Addr: addr, From: "a@b.example"}).Send(context.Background(), msg); err != nil {
		t.Fatalf("send: %v", err)
	}
	select {
	case body := <-got:
		if !strings.Contains(body, "Subject: x") {
			t.Fatalf("body %s", body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nothing delivered")
	}
}

func TestSMTPMailer_GivesUpWhenTheRelayDoesNotAnswerAndClosesTheConnection(t *testing.T) {
	addr, _ := fakeRelay(t, false, true)
	start := time.Now()
	err := (SMTPMailer{Addr: addr, From: "a@b.example", Timeout: 150 * time.Millisecond}).Send(context.Background(), Message{To: "ap@bluewater.example", Subject: "x", Filename: "a.pdf"})
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("send: %v after %v", err, time.Since(start))
	}
}

func TestLogMailer_SendsNothing(t *testing.T) {
	if err := (LogMailer{}).Send(context.Background(), Message{To: "ap@bluewater.example", Subject: "x"}); err != nil {
		t.Fatal(err)
	}
}

// memLog is an in-memory store.DeliveryStore.
type memLog struct {
	mu      sync.Mutex
	rows    map[string]*store.Delivery
	portal  map[string]store.PortalDocument
	portalN int
}

func newMemLog() *memLog {
	return &memLog{rows: map[string]*store.Delivery{}, portal: map[string]store.PortalDocument{}}
}

func (m *memLog) ClaimDelivery(_ context.Context, d store.Delivery) (store.Delivery, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := d.SourceKey + "|" + d.RuleID
	if r, ok := m.rows[k]; ok {
		if r.Status == store.DeliveryFailed {
			r.Status = store.DeliveryPending
			return *r, true, nil
		}
		return *r, false, nil
	}
	d.ID, d.Status = k, store.DeliveryPending
	m.rows[k] = &d
	return d, true, nil
}

func (m *memLog) FinishDelivery(_ context.Context, id, status, detail string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[id].Status, m.rows[id].Detail = status, detail
	return nil
}

func (m *memLog) ListDeliveries(context.Context, string) ([]store.Delivery, error) { return nil, nil }

func (m *memLog) SavePortalDocument(_ context.Context, d store.PortalDocument) (store.PortalDocument, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.portal[d.SourceKey]; ok {
		return old, nil
	}
	m.portalN++
	d.ID = string(rune('a' + m.portalN))
	m.portal[d.SourceKey] = d
	return d, nil
}

func (m *memLog) ListPortalDocuments(context.Context, string, int) ([]store.PortalDocument, error) {
	return nil, nil
}

func (m *memLog) GetPortalDocument(context.Context, string, string) (store.PortalDocument, error) {
	return store.PortalDocument{}, errors.New("unused")
}

type memRules struct {
	store.DocStore
	rules []domain.OutputRule
}

func (m memRules) ListOutputRules(context.Context) ([]domain.OutputRule, error) { return m.rules, nil }

type recMailer struct {
	mu   sync.Mutex
	sent []Message
	fail error
}

func (r *recMailer) Send(_ context.Context, m Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return r.fail
	}
	r.sent = append(r.sent, m)
	return nil
}

const (
	acct = "0190a5f2-7b1c-7d3e-8a4f-123456789a02"
	le   = "0190a5f2-7b1c-7d3e-8a4f-123456789a01"
)

func TestDeliverer_SendsToEveryDestinationOnceAndSaysWhatItSkipped(t *testing.T) {
	rules := []domain.OutputRule{
		{ID: "r1", DocumentType: domain.DocCustomerInvoice, CopyKind: domain.CopyOriginal, Channel: domain.ChannelEmail, Destination: "ap@bluewater.example", Copies: 1, Sequence: 1},
		{ID: "r2", DocumentType: domain.DocCustomerInvoice, CopyKind: domain.CopyOriginal, Channel: domain.ChannelPortal, Copies: 1, Sequence: 1},
		{ID: "r3", DocumentType: domain.DocCustomerInvoice, CopyKind: domain.CopyCopy, Channel: domain.ChannelArchive, Copies: 1, Sequence: 1},
		{ID: "r4", DocumentType: domain.DocCustomerInvoice, CopyKind: domain.CopyOriginal, Channel: domain.ChannelEInvoice, Copies: 1, Sequence: 2},
		{ID: "r5", DocumentType: domain.DocCustomerInvoice, CopyKind: domain.CopyOriginal, Channel: domain.ChannelPrinter, Destination: "OFFICE", Copies: 2, Sequence: 3},
		{ID: "other", DocumentType: domain.DocPurchaseOrder, CopyKind: domain.CopyOriginal, Channel: domain.ChannelEmail, Destination: "x@y.example", Copies: 1, Sequence: 1},
	}
	mail := &recMailer{}
	var printed []string
	log := newMemLog()
	d := &Deliverer{Docs: memRules{rules: rules}, Log: log, Mailer: mail, Printer: func(_ context.Context, q string, _ []byte, key string) error {
		printed = append(printed, q+" "+key)
		return nil
	}}
	doc := Document{SourceKey: "invoice:1", Type: domain.DocCustomerInvoice, Number: "INV/1", LegalEntityID: le, AccountID: acct, AccountVerified: true, PDF: []byte("%PDF-1.4"), Pages: 1}
	res, err := d.Deliver(context.Background(), doc)
	if err != nil || len(res) != 5 {
		t.Fatalf("deliver: %v %+v", err, res)
	}
	byRule := map[string]Result{}
	for _, r := range res {
		byRule[r.RuleID] = r
	}
	if byRule["r1"].Status != store.DeliverySent || byRule["r2"].Status != store.DeliverySent || byRule["r5"].Status != store.DeliverySent ||
		byRule["r3"].Status != store.DeliverySkipped || !strings.Contains(byRule["r3"].Detail, "document-store") ||
		byRule["r4"].Status != store.DeliverySkipped || !strings.Contains(byRule["r4"].Detail, "e-invoice") {
		t.Fatalf("results: %+v", res)
	}
	if len(mail.sent) != 1 || mail.sent[0].Filename != "INV-1.pdf" || mail.sent[0].Subject != "Invoice INV/1" || len(printed) != 2 || len(log.portal) != 1 {
		t.Fatalf("effects: mail %+v printed %v portal %v", mail.sent, printed, log.portal)
	}
	// The same delivery again sends nothing a second time.
	if res, err = d.Deliver(context.Background(), doc); err != nil || len(res) != 5 || len(mail.sent) != 1 || len(printed) != 2 {
		t.Fatalf("repeat: %v %+v mail %d printed %d", err, res, len(mail.sent), len(printed))
	}
	// A failed destination is tried again on the next call, the others are not.
	mail.fail = errors.New("relay down")
	other := doc
	other.SourceKey = "invoice:2"
	if res, _ = d.Deliver(context.Background(), other); byRuleOf(res, "r1").Status != store.DeliveryFailed || byRuleOf(res, "r1").Detail != "relay down" {
		t.Fatalf("failed mail: %+v", res)
	}
	mail.fail = nil
	res, _ = d.Deliver(context.Background(), other)
	if byRuleOf(res, "r1").Status != store.DeliverySent || len(mail.sent) != 2 || len(printed) != 4 {
		t.Fatalf("retry: %+v mail %d printed %d", res, len(mail.sent), len(printed))
	}
}

func byRuleOf(res []Result, id string) Result {
	for _, r := range res {
		if r.RuleID == id {
			return r
		}
	}
	return Result{}
}

func TestDeliverer_FailsAChannelWithoutItsAdapterAndSkipsAPortalWithoutACustomer(t *testing.T) {
	rules := []domain.OutputRule{
		{ID: "r1", DocumentType: domain.DocPaymentNotice, CopyKind: domain.CopyOriginal, Channel: domain.ChannelEmail, Destination: "ap@bluewater.example", Copies: 1, Sequence: 1},
		{ID: "r2", DocumentType: domain.DocPaymentNotice, CopyKind: domain.CopyOriginal, Channel: domain.ChannelPortal, Copies: 1, Sequence: 1},
		{ID: "r3", DocumentType: domain.DocPaymentNotice, CopyKind: domain.CopyOriginal, Channel: domain.ChannelPrinter, Destination: "OFFICE", Copies: 1, Sequence: 1},
	}
	d := &Deliverer{Docs: memRules{rules: rules}, Log: newMemLog()}
	res, err := d.Deliver(context.Background(), Document{SourceKey: "n1", Type: domain.DocPaymentNotice, Number: "N1", PDF: []byte("%PDF-")})
	if err != nil {
		t.Fatal(err)
	}
	if byRuleOf(res, "r1").Status != store.DeliveryFailed || byRuleOf(res, "r2").Status != store.DeliverySkipped || byRuleOf(res, "r3").Status != store.DeliveryFailed {
		t.Fatalf("results: %+v", res)
	}
}

func TestDeliverer_AReusedSourceKeyForAnotherCustomerDelivers(t *testing.T) {
	rules := []domain.OutputRule{
		{ID: "r1", DocumentType: domain.DocCustomerInvoice, CopyKind: domain.CopyOriginal, Channel: domain.ChannelEmail, Destination: "ap@bluewater.example", Copies: 1, Sequence: 1},
		{ID: "r2", DocumentType: domain.DocCustomerInvoice, CopyKind: domain.CopyOriginal, Channel: domain.ChannelPortal, Copies: 1, Sequence: 1},
	}
	mail := &recMailer{}
	log := newMemLog()
	d := &Deliverer{Docs: memRules{rules: rules}, Log: log, Mailer: mail}
	first := Document{SourceKey: "INV-1", Type: domain.DocCustomerInvoice, Number: "INV-1", LegalEntityID: le, AccountID: acct, AccountVerified: true, PDF: []byte("%PDF-")}
	other := first
	other.AccountID = "0190a5f2-7b1c-7d3e-8a4f-123456789a77"
	if _, err := d.Deliver(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	res, err := d.Deliver(context.Background(), other)
	if err != nil || len(mail.sent) != 2 || len(log.portal) != 2 || byRuleOf(res, "r1").Status != store.DeliverySent {
		t.Fatalf("another customer under the same key: %v %+v mails %d portal %d", err, res, len(mail.sent), len(log.portal))
	}
	// An unverified customer is not shown the document.
	other.SourceKey, other.AccountVerified = "INV-2", false
	if res, _ = d.Deliver(context.Background(), other); byRuleOf(res, "r2").Status != store.DeliverySkipped {
		t.Fatalf("unverified: %+v", res)
	}
}
