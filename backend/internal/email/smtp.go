package email

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"
)

// SMTPSender delivers mail through an SMTP relay, optionally authenticated and
// wrapped in TLS.
type SMTPSender struct {
	addr      string
	from      string
	replyTo   string
	username  string
	password  string
	tlsMode   string
	tlsConfig *tls.Config
}

// NewSMTPSender returns a sender for the relay at addr. tlsMode is one of
// starttls, implicit, or none. Certificate verification is always on and TLS
// 1.2 is the floor.
func NewSMTPSender(addr, from, replyTo, username, password, tlsMode string) (*SMTPSender, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, &DeliveryError{Message: "smtp: relay address must be host:port", Permanent: true}
	}
	mode := strings.ToLower(strings.TrimSpace(tlsMode))
	switch mode {
	case "starttls", "implicit", "none":
	default:
		return nil, &DeliveryError{Message: "smtp: invalid TLS mode", Permanent: true}
	}
	return newSMTPSenderWithTLS(addr, from, replyTo, username, password, mode, &tls.Config{
		ServerName: host,
		MinVersion: tls.VersionTLS12,
	})
}

// newSMTPSenderWithTLS is the test seam; production callers use NewSMTPSender.
func newSMTPSenderWithTLS(addr, from, replyTo, username, password, tlsMode string, tlsConfig *tls.Config) (*SMTPSender, error) {
	if tlsConfig == nil {
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return &SMTPSender{
		addr:      addr,
		from:      from,
		replyTo:   replyTo,
		username:  username,
		password:  password,
		tlsMode:   tlsMode,
		tlsConfig: tlsConfig,
	}, nil
}

// Send delivers msg to its To address through the relay. Dialing and all SMTP
// I/O honor ctx: cancellation closes down the attempt and returns ctx.Err(). A
// default 30-second connection deadline applies when the caller supplies none.
func (s *SMTPSender) Send(ctx context.Context, msg Message) (err error) {
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

	if s.tlsMode == "implicit" {
		tlsConn := tls.Client(conn, s.tlsConfig)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return classifySMTPError(err)
		}
		conn = tlsConn
	}

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return classifySMTPError(err)
	}
	defer func() {
		_ = client.Close()
	}()

	if s.tlsMode == "starttls" {
		if err := client.StartTLS(s.tlsConfig); err != nil {
			return classifySMTPError(err)
		}
	}

	if s.username != "" {
		if err := client.Auth(smtp.PlainAuth("", s.username, s.password, host)); err != nil {
			return classifySMTPError(err)
		}
	}

	if err := client.Mail(s.from); err != nil {
		return classifySMTPError(err)
	}
	if err := client.Rcpt(msg.To); err != nil {
		return classifySMTPError(err)
	}
	writer, err := client.Data()
	if err != nil {
		return classifySMTPError(err)
	}
	if _, err := writer.Write(renderMessage(s.from, s.replyTo, msg)); err != nil {
		_ = writer.Close()
		return classifySMTPError(err)
	}
	if err := writer.Close(); err != nil {
		return classifySMTPError(err)
	}
	if err := client.Quit(); err != nil {
		return classifySMTPError(err)
	}
	return nil
}

// classifySMTPError turns a 5xx relay response into a permanent delivery
// failure. The relay's response text is never included because it can echo the
// recipient address or credentials.
func classifySMTPError(err error) error {
	var protoErr *textproto.Error
	if errors.As(err, &protoErr) && protoErr.Code >= 500 {
		return &DeliveryError{
			Message:   fmt.Sprintf("smtp: relay rejected the message (%d)", protoErr.Code),
			Permanent: true,
		}
	}
	return err
}
