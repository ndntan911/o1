package email

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"mime"
	"net/mail"
	"strings"
	"time"
)

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

// renderMessage builds the RFC 5322 message with a text/plain and a text/html
// alternative part. Header values are defensively stripped even though
// validateMessage rejects CR/LF, and each message gets a fresh random boundary.
// A non-empty replyTo adds a Reply-To header.
func renderMessage(from, replyTo string, msg Message) []byte {
	from = sanitizeHeader(from)
	to := sanitizeHeader(msg.To)
	subject := sanitizeHeader(msg.Subject)
	subject = mime.QEncoding.Encode("utf-8", subject)
	boundary := newBoundary(msg.Text, msg.HTML)

	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	if replyTo != "" {
		fmt.Fprintf(&b, "Reply-To: %s\r\n", sanitizeHeader(replyTo))
	}
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
