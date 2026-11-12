package email

import (
	"context"
	"net"
	"net/smtp"
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

	if err := client.Mail(envelopeAddress(s.from)); err != nil {
		return err
	}
	if err := client.Rcpt(envelopeAddress(msg.To)); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := writer.Write(renderMessage(s.from, "", msg)); err != nil {
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

// render builds the message with the shared renderer.
func (s *MailpitSender) render(msg Message) []byte {
	return renderMessage(s.from, "", msg)
}
