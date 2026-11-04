package email

import (
	"strings"
	"testing"
)

func TestMailpitSender_Render(t *testing.T) {
	s := NewMailpitSender("localhost:1025", "o1@localhost")
	raw := string(s.render(Message{
		To:      "user@example.com",
		Subject: "Verify your email",
		Text:    "line one",
		HTML:    "<p>line one</p>",
	}))
	for _, want := range []string{
		"From: o1@localhost",
		"To: user@example.com",
		"Subject: Verify your email",
		"MIME-Version: 1.0",
		"Content-Type: multipart/alternative; boundary=o1-mail-boundary",
		"Content-Type: text/plain; charset=utf-8",
		"Content-Type: text/html; charset=utf-8",
		"line one",
		"<p>line one</p>",
		"--o1-mail-boundary--",
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("message missing %q in:\n%s", want, raw)
		}
	}
}
