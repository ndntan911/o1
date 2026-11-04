// Package email defines a minimal transactional-mail sender abstraction
// used by authentication flows.
package email

import "context"

// Message is an outgoing email. Text and HTML carry the same content in
// the two representation formats.
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// Sender delivers email messages.
type Sender interface {
	Send(ctx context.Context, msg Message) error
}
