// Package email defines a minimal transactional-mail sender abstraction
// used by authentication flows.
package email

import "context"

// Message is an outgoing email. Text and HTML carry the same content in
// the two representation formats. IdempotencyKey is optional; providers that
// support it use it to deduplicate retried deliveries. Mailpit ignores it.
type Message struct {
	To             string
	Subject        string
	Text           string
	HTML           string
	IdempotencyKey string
}

// Sender delivers email messages.
type Sender interface {
	Send(ctx context.Context, msg Message) error
}
