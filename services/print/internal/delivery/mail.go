// Package delivery sends a rendered document to the destinations of its output plan (#46, 46-2): an e-mail address, the customer
// portal, a printer queue. The e-invoice network and the archive are not delivered here (see Deliverer).
package delivery

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

// Message is one e-mail with the PDF attached.
type Message struct {
	To         string
	Subject    string
	Body       string
	Filename   string
	Attachment []byte
}

// Mailer is the e-mail port. Implementations: LogMailer (records the message in the log, sends nothing) and SMTPMailer.
type Mailer interface {
	Send(ctx context.Context, m Message) error
}

// LogMailer writes one log line per message and sends nothing: the default adapter, so a deployment without a mail relay
// still shows what would have gone out.
type LogMailer struct{ Log *slog.Logger }

// Send logs the message without its body.
func (l LogMailer) Send(_ context.Context, m Message) error {
	log := l.Log
	if log == nil {
		log = slog.Default()
	}
	log.Info("e-mail not sent (log adapter)", "to", m.To, "subject", m.Subject, "attachment", m.Filename, "bytes", len(m.Attachment))
	return nil
}

// SMTPMailer sends through an SMTP relay with net/smtp (PLAIN auth when a user is set, STARTTLS when the server offers it).
type SMTPMailer struct {
	Addr     string // host:port
	From     string
	Username string
	Password string
	Timeout  time.Duration

	// send replaces smtp.SendMail in tests.
	send func(addr string, a smtp.Auth, from string, to []string, msg []byte) error
}

// Send builds a MIME message with the PDF as an attachment and hands it to the relay.
func (s SMTPMailer) Send(ctx context.Context, m Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := mail.ParseAddress(m.To); err != nil {
		return fmt.Errorf("e-mail address %q is not valid", m.To)
	}
	msg, err := mime(s.From, m)
	if err != nil {
		return err
	}
	var auth smtp.Auth
	if s.Username != "" {
		host := s.Addr
		if i := strings.LastIndexByte(host, ':'); i >= 0 {
			host = host[:i]
		}
		auth = smtp.PlainAuth("", s.Username, s.Password, host)
	}
	send := s.send
	if send == nil {
		send = smtp.SendMail
	}
	done := make(chan error, 1)
	go func() { done <- send(s.Addr, auth, s.From, []string{m.To}, msg) }()
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		return errors.New("the mail relay did not answer in time")
	case <-ctx.Done():
		return ctx.Err()
	}
}

// header refuses a value that could start another header.
func header(v string) (string, error) {
	if strings.ContainsAny(v, "\r\n") {
		return "", errors.New("a mail header holds a line break")
	}
	return v, nil
}

func mime(from string, m Message) ([]byte, error) {
	to, err := header(m.To)
	if err != nil {
		return nil, err
	}
	subject, err := header(m.Subject)
	if err != nil {
		return nil, err
	}
	fname, err := header(m.Filename)
	if err != nil {
		return nil, err
	}
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, err
	}
	boundary := "passage-" + hex.EncodeToString(raw[:])
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=%q\r\n\r\n", from, to, subject, boundary)
	fmt.Fprintf(&b, "--%s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n", boundary, m.Body)
	if len(m.Attachment) > 0 {
		fmt.Fprintf(&b, "--%s\r\nContent-Type: application/pdf\r\nContent-Transfer-Encoding: base64\r\nContent-Disposition: attachment; filename=%q\r\n\r\n", boundary, fname)
		enc := base64.StdEncoding.EncodeToString(m.Attachment)
		for len(enc) > 76 {
			b.WriteString(enc[:76] + "\r\n")
			enc = enc[76:]
		}
		b.WriteString(enc + "\r\n")
	}
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return b.Bytes(), nil
}
