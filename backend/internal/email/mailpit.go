package email

import (
	"context"
	"fmt"
	"net/smtp"
	"strings"
)

// multipartBoundary separates the alternative text/HTML body parts. It is
// fixed and cannot appear inside our generated bodies.
const multipartBoundary = "o1-mail-boundary"

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

// Send delivers msg to its To address via SMTP.
func (s *MailpitSender) Send(ctx context.Context, msg Message) error {
	raw := s.render(msg)
	return smtp.SendMail(s.addr, nil, s.from, []string{msg.To}, raw)
}

// render builds the RFC 5322 message with a text/plain and a text/html
// alternative part.
func (s *MailpitSender) render(msg Message) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", s.from)
	fmt.Fprintf(&b, "To: %s\r\n", msg.To)
	fmt.Fprintf(&b, "Subject: %s\r\n", msg.Subject)
	fmt.Fprintf(&b, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=%s\r\n", multipartBoundary)
	fmt.Fprintf(&b, "\r\n")
	fmt.Fprintf(&b, "--%s\r\n", multipartBoundary)
	fmt.Fprintf(&b, "Content-Type: text/plain; charset=utf-8\r\n\r\n")
	fmt.Fprintf(&b, "%s\r\n", msg.Text)
	fmt.Fprintf(&b, "--%s\r\n", multipartBoundary)
	fmt.Fprintf(&b, "Content-Type: text/html; charset=utf-8\r\n\r\n")
	fmt.Fprintf(&b, "%s\r\n", msg.HTML)
	fmt.Fprintf(&b, "--%s--\r\n", multipartBoundary)
	return []byte(b.String())
}
