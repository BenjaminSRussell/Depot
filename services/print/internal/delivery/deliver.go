package delivery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/BenjaminSRussell/depot/services/print/internal/domain"
	"github.com/BenjaminSRussell/depot/services/print/internal/store"
)

// Printer sends a PDF to a print queue; the API implements it with its job pipeline. key makes the job idempotent.
type Printer func(ctx context.Context, queueCode string, pdf []byte, key string) error

// Document is a rendered document to deliver. SourceKey names it (an invoice id, a statement run) so a repeated delivery
// finds what already went out; ArchiveNote lists nothing: the archive is not delivered here.
type Document struct {
	SourceKey     string
	Type          string
	Number        string
	LegalEntityID string
	AccountID     string
	PDF           []byte
	Pages         int
	// AccountVerified says the caller checked that AccountID is a customer of LegalEntityID. The portal channel shows the document
	// to that customer, so it delivers only a verified one.
	AccountVerified bool
	// Subject and Body are the words of the e-mail; empty ones get a plain default.
	Subject string
	Body    string
}

// Result is what happened at one destination of the plan.
type Result struct {
	RuleID      string `json:"ruleId"`
	CopyKind    string `json:"copyKind"`
	Channel     string `json:"channel"`
	Destination string `json:"destination,omitempty"`
	Status      string `json:"status"`
	Detail      string `json:"detail,omitempty"`
}

// Deliverer sends a document to the destinations its output rules name.
type Deliverer struct {
	Docs    store.DocStore
	Log     store.DeliveryStore
	Mailer  Mailer
	Printer Printer
}

// Reasons a destination is not delivered here. Shown to the user as the detail of a SKIPPED destination.
const (
	reasonEInvoice = "the e-invoice network is reached through the e-invoice service, not from here"
	reasonArchive  = "the document archive is not available yet (waiting on the document-store decision)"
)

// key is the identity of the delivery: the caller's sourceKey together with the document type, legal entity and customer, so a
// sourceKey reused for another customer or entity is another delivery and never replays the first.
func (doc Document) key() string {
	sum := sha256.Sum256([]byte(doc.Type + "|" + doc.LegalEntityID + "|" + doc.AccountID))
	return doc.SourceKey + "#" + hex.EncodeToString(sum[:6])
}

// Deliver sends doc to every destination of the output plan for its type, legal entity and account, the original first. Each
// destination is claimed before it is tried, so repeating the call (a retry, a second replica) sends nothing twice; a failed
// destination is tried again on the next call. The error is only for a plan that cannot be read; a destination that fails is
// reported in its Result.
func (d *Deliverer) Deliver(ctx context.Context, doc Document) ([]Result, error) {
	rules, err := d.Docs.ListOutputRules(ctx)
	if err != nil {
		return nil, err
	}
	plan := domain.ResolveOutputs(rules, domain.OutputDocument{Type: doc.Type, LegalEntityID: doc.LegalEntityID, AccountID: doc.AccountID})
	out := make([]Result, 0, len(plan))
	for _, rule := range plan {
		res := Result{RuleID: rule.ID, CopyKind: rule.CopyKind, Channel: rule.Channel, Destination: rule.Destination}
		rec, claimed, err := d.Log.ClaimDelivery(ctx, store.Delivery{SourceKey: doc.key(), RuleID: rule.ID, CopyKind: rule.CopyKind, Channel: rule.Channel, Destination: rule.Destination})
		if err != nil {
			return out, err
		}
		if !claimed {
			res.Status, res.Detail = rec.Status, rec.Detail
			out = append(out, res)
			continue
		}
		status, detail := d.send(ctx, doc, rule)
		if err := d.Log.FinishDelivery(ctx, rec.ID, status, detail); err != nil {
			return out, err
		}
		res.Status, res.Detail = status, detail
		out = append(out, res)
	}
	return out, nil
}

func (d *Deliverer) send(ctx context.Context, doc Document, rule domain.OutputRule) (status, detail string) {
	switch rule.Channel {
	case domain.ChannelEmail:
		if d.Mailer == nil {
			return store.DeliveryFailed, "no mail adapter is configured"
		}
		subject, body := doc.Subject, doc.Body
		if subject == "" {
			subject = fmt.Sprintf("%s %s", docLabel(doc.Type), doc.Number)
		}
		if body == "" {
			body = fmt.Sprintf("Please find %s %s attached.", strings.ToLower(docLabel(doc.Type)), doc.Number)
		}
		if err := d.Mailer.Send(ctx, Message{To: rule.Destination, Subject: subject, Body: body, Filename: filename(doc), Attachment: doc.PDF}); err != nil {
			return store.DeliveryFailed, err.Error()
		}
		return store.DeliverySent, ""
	case domain.ChannelPortal:
		if doc.AccountID == "" {
			return store.DeliverySkipped, "the document has no customer to show it to"
		}
		if !doc.AccountVerified {
			return store.DeliverySkipped, "the customer could not be checked against the legal entity"
		}
		if doc.LegalEntityID == "" {
			return store.DeliverySkipped, "the document names no legal entity"
		}
		if _, err := d.Log.SavePortalDocument(ctx, store.PortalDocument{SourceKey: doc.key(), AccountID: doc.AccountID, LegalEntityID: doc.LegalEntityID,
			DocumentType: doc.Type, Number: doc.Number, Pages: doc.Pages, PDF: doc.PDF}); err != nil {
			return store.DeliveryFailed, err.Error()
		}
		return store.DeliverySent, ""
	case domain.ChannelPrinter:
		if d.Printer == nil {
			return store.DeliveryFailed, "no printer pipeline is configured"
		}
		for i := 1; i <= rule.Copies; i++ {
			if err := d.Printer(ctx, rule.Destination, doc.PDF, fmt.Sprintf("deliver:%s:%s:%d", doc.key(), rule.ID, i)); err != nil {
				return store.DeliveryFailed, err.Error()
			}
		}
		return store.DeliverySent, ""
	case domain.ChannelEInvoice:
		return store.DeliverySkipped, reasonEInvoice
	case domain.ChannelArchive:
		return store.DeliverySkipped, reasonArchive
	}
	return store.DeliveryFailed, "unknown channel " + rule.Channel
}

var typeLabels = map[string]string{
	domain.DocPurchaseOrder: "Purchase order", domain.DocCustomerInvoice: "Invoice", domain.DocPackingSlip: "Packing slip",
	domain.DocRemittanceAdvice: "Remittance advice", domain.DocPeriodicStatement: "Statement of account",
	domain.DocPaymentNotice: "Payment notice", domain.DocBalanceConfirmation: "Balance confirmation",
}

func docLabel(t string) string {
	if l, ok := typeLabels[t]; ok {
		return l
	}
	return "Document"
}

func filename(doc Document) string {
	clean := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, doc.Number)
	if clean == "" {
		clean = "document"
	}
	return clean + ".pdf"
}
