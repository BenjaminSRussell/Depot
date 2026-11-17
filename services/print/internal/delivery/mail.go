// Package delivery sends a rendered document to the destinations of its output plan (#46, 46-2): an e-mail address, the customer
// portal, a printer queue. The e-invoice network and the archive are not delivered here (see Deliverer).
package delivery

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
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

// LogMailer writes one Debug log line per message and sends nothing: the default adapter, so a deployment without a mail relay
// still shows what would have gone out. The recipient is logged under the key "email", which the platform's log pseudonymization
// (logpseudo) replaces by a pseudonym when it is on; the subject is not logged.
type LogMailer struct{ Log *slog.Logger }

// Send logs the message without its body.
func (l LogMailer) Send(ctx context.Context, m Message) error {
	log := l.Log
	if log == nil {
		log = slog.Default()
	}
	log.DebugContext(ctx, "e-mail not sent (log adapter)", "email", m.To, "attachment", m.Filename, "bytes", len(m.Attachment))
	return nil
}

// SMTPMailer sends through an SMTP relay with net/smtp. It dials with a deadline, drives the SMTP conversation itself and closes the
// connection when the deadline or the context ends, so a send that timed out never completes later. Port 465 (or ImplicitTLS) is TLS
// from the first byte; otherwise STARTTLS is used when the relay offers it, and with RequireTLS (the default in the service) a relay
// that does not is refused and nothing is sent.
type SMTPMailer struct {
	Addr        string // host:port
	From        string
	Username    string
	Password    string
	Timeout     time.Duration
	ImplicitTLS bool
	RequireTLS  bool

	// send replaces the SMTP conversation in tests of the message format.
	send func(addr string, a smtp.Auth, from string, to []string, msg []byte) error
}

// ErrTLSRequired is a relay that offers no TLS while RequireTLS is set.
var ErrTLSRequired = errors.New("the mail relay offers no TLS (STARTTLS) and PRINT_SMTP_REQUIRE_TLS is set")

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
	if s.send != nil {
		return s.send(s.Addr, nil, s.From, []string{m.To}, msg)
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	host, port, err := net.SplitHostPort(s.Addr)
	if err != nil {
		return fmt.Errorf("mail relay address: %w", err)
	}
	implicit := s.ImplicitTLS || port == "465"
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", s.Addr)
	if err != nil {
		return fmt.Errorf("the mail relay did not answer: %w", err)
	}
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	// The connection is closed when the deadline or the caller's context ends, which fails the conversation at once.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	defer func() { _ = conn.Close() }()
	if implicit {
		tc := tls.Client(conn, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
		if err := tc.HandshakeContext(ctx); err != nil {
			return fmt.Errorf("the mail relay refused TLS: %w", err)
		}
		conn = tc
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("the mail relay did not answer in time: %w", err)
	}
	defer func() { _ = c.Close() }()
	if !implicit {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
				return fmt.Errorf("the mail relay refused STARTTLS: %w", err)
			}
		} else if s.RequireTLS {
			return ErrTLSRequired
		}
	}
	if s.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", s.Username, s.Password, host)); err != nil {
			return fmt.Errorf("the mail relay refused the login: %w", err)
		}
	}
	if err := c.Mail(s.From); err != nil {
		return err
	}
	if err := c.Rcpt(m.To); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
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
	boundary := "depot-" + hex.EncodeToString(raw[:])
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
