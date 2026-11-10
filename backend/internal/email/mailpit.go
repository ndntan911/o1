package email

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

const (
	// defaultSMTPTimeout bounds one delivery when the caller supplies no
	// deadline.
	defaultSMTPTimeout = 30 * time.Second
	// smtpDialTimeout bounds the TCP connect step.
	smtpDialTimeout = 10 * time.Second
)

// MailpitSender delivers mail over unauthenticated SMTP, sized for the
// local Mailpit development server.
type MailpitSender struct {
	addr string
	from string
}

// NewMailpitSender returns a sender that posts messages to the SMTP
// endpoint at addr, sent from from.
func NewMailpitSender(addr, from string) *MailpitSender {
	return &MailpitSender{addr: addr, from: from}
}

// Send delivers msg to its To address via SMTP. Dialing and all SMTP I/O honor
// ctx: cancellation closes down the attempt and returns ctx.Err(). A default
// 30-second connection deadline applies when the caller supplies none.
func (s *MailpitSender) Send(ctx context.Context, msg Message) (err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateMessage(s.from, msg); err != nil {
		return err
	}
	defer func() {
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = ctxErr
		}
	}()

	host, _, splitErr := net.SplitHostPort(s.addr)
	if splitErr != nil {
		host = s.addr
	}
	deadline := time.Now().Add(defaultSMTPTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}

	dialer := net.Dialer{Timeout: smtpDialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", s.addr)
	if err != nil {
		return err
	}
	defer func() {
		_ = conn.Close()
	}()
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}

	// Interrupt blocked SMTP reads/writes if the caller cancels after dialing.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.SetDeadline(time.Now())
		case <-done:
		}
	}()

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer func() {
		_ = client.Close()
	}()

	if err := client.Mail(s.from); err != nil {
		return err
	}
	if err := client.Rcpt(msg.To); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := writer.Write(s.render(msg)); err != nil {
		_ = writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if err := client.Quit(); err != nil {
		return err
	}
	return nil
}

// maxSubjectLength bounds the RFC 5322 header line length.
const maxSubjectLength = 998

// validateMessage rejects malformed messages and header injection attempts
// before any network I/O. It is shared by every sender so validation behavior
// is identical across providers.
func validateMessage(from string, msg Message) error {
	for _, field := range []string{from, msg.To, msg.Subject, msg.IdempotencyKey} {
		if strings.ContainsAny(field, "\r\n") {
			return &DeliveryError{
				Message:   "email: message header contains CR/LF",
				Permanent: true,
			}
		}
	}
	if strings.TrimSpace(msg.To) == "" {
		return &DeliveryError{
			Message:   "email: recipient address is empty",
			Permanent: true,
		}
	}
	addr, err := mail.ParseAddress(strings.TrimSpace(msg.To))
	if err != nil || addr.Address == "" {
		// The underlying parser error is intentionally not surfaced: it can
		// echo the recipient address.
		return &DeliveryError{
			Message:   "email: invalid recipient address",
			Permanent: true,
		}
	}
	if len(msg.Subject) > maxSubjectLength {
		return &DeliveryError{
			Message:   "email: subject is too long",
			Permanent: true,
		}
	}
	return nil
}

// render builds the RFC 5322 message with a text/plain and a text/html
// alternative part. Header values are defensively stripped even though Send
// rejects CR/LF, and each message gets a fresh random boundary.
func (s *MailpitSender) render(msg Message) []byte {
	from := sanitizeHeader(s.from)
	to := sanitizeHeader(msg.To)
	subject := sanitizeHeader(msg.Subject)
	subject = mime.QEncoding.Encode("utf-8", subject)
	boundary := newBoundary(msg.Text, msg.HTML)

	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	fmt.Fprintf(&b, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=%s\r\n", boundary)
	fmt.Fprintf(&b, "\r\n")
	fmt.Fprintf(&b, "--%s\r\n", boundary)
	fmt.Fprintf(&b, "Content-Type: text/plain; charset=utf-8\r\n\r\n")
	fmt.Fprintf(&b, "%s\r\n", msg.Text)
	fmt.Fprintf(&b, "--%s\r\n", boundary)
	fmt.Fprintf(&b, "Content-Type: text/html; charset=utf-8\r\n\r\n")
	fmt.Fprintf(&b, "%s\r\n", msg.HTML)
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return []byte(b.String())
}

func sanitizeHeader(value string) string {
	value = strings.ReplaceAll(value, "\r", "")
	value = strings.ReplaceAll(value, "\n", "")
	return strings.TrimSpace(value)
}

// newBoundary returns a cryptographically random boundary that does not occur
// in either generated body part, so user-controlled text cannot terminate the
// multipart message early.
func newBoundary(bodies ...string) string {
	for {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			// crypto/rand failures are effectively unrecoverable, but keeping
			// mail flowing is preferable to a crash in a best-effort sender.
			return fmt.Sprintf("o1-%x", time.Now().UnixNano())
		}
		boundary := "o1-" + hex.EncodeToString(raw[:])
		collides := false
		for _, body := range bodies {
			if strings.Contains(body, boundary) {
				collides = true
				break
			}
		}
		if !collides {
			return boundary
		}
	}
}
