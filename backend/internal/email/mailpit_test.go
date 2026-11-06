package email

import (
	"context"
	"errors"
	"net"
	"regexp"
	"strings"
	"testing"
	"time"
)

func extractBoundary(t *testing.T, raw string) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^Content-Type: multipart/alternative; boundary=(\S+)\r?$`).FindStringSubmatch(raw)
	if m == nil {
		t.Fatalf("message has no multipart boundary:\n%s", raw)
	}
	return m[1]
}

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
		"Content-Type: text/plain; charset=utf-8",
		"Content-Type: text/html; charset=utf-8",
		"line one",
		"<p>line one</p>",
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("message missing %q in:\n%s", want, raw)
		}
	}

	boundary := extractBoundary(t, raw)
	if boundary == "" || boundary == "o1-mail-boundary" {
		t.Fatalf("boundary = %q, want a per-message random value", boundary)
	}
	for _, want := range []string{"--" + boundary + "\r\n", "--" + boundary + "--\r\n"} {
		if !strings.Contains(raw, want) {
			t.Errorf("message missing delimiter %q in:\n%s", want, raw)
		}
	}
}

func TestMailpitSender_RejectsHeaderInjection(t *testing.T) {
	s := NewMailpitSender("127.0.0.1:1", "o1@localhost")
	ctx := context.Background()

	for _, msg := range []Message{
		{To: "user@example.com\r\nBcc: evil@example.com", Subject: "hello", Text: "x", HTML: "<p>x</p>"},
		{To: "user@example.com", Subject: "hello\r\nBcc: evil@example.com", Text: "x", HTML: "<p>x</p>"},
		{To: "user@example.com", Subject: "hello\nBcc: evil@example.com", Text: "x", HTML: "<p>x</p>"},
	} {
		err := s.Send(ctx, msg)
		if err == nil {
			t.Fatalf("Send accepted header injection: %+v", msg)
		}
		if !strings.Contains(strings.ToLower(err.Error()), "header") {
			t.Fatalf("Send returned %v, want a header validation error", err)
		}
	}
}

func TestMailpitSender_BoundaryDiffersPerMessage(t *testing.T) {
	s := NewMailpitSender("localhost:1025", "o1@localhost")
	msg := Message{To: "user@example.com", Subject: "hello", Text: "x", HTML: "<p>x</p>"}
	first := extractBoundary(t, string(s.render(msg)))
	second := extractBoundary(t, string(s.render(msg)))
	if first == second {
		t.Fatalf("boundary reused across messages: %q", first)
	}
}

func TestMailpitSender_SubjectEncoding(t *testing.T) {
	s := NewMailpitSender("localhost:1025", "o1@localhost")
	raw := string(s.render(Message{
		To:      "user@example.com",
		Subject: "Überwachung ist down",
		Text:    "x",
		HTML:    "<p>x</p>",
	}))
	if !strings.Contains(strings.ToLower(raw), "=?utf-8?") {
		t.Fatalf("non-ASCII subject was not RFC 2047 encoded:\n%s", raw)
	}
	if strings.Contains(raw, "Subject: Überwachung") {
		t.Fatalf("non-ASCII subject was written raw:\n%s", raw)
	}
}

func TestMailpitSender_OldBoundaryInBodyCannotTerminate(t *testing.T) {
	s := NewMailpitSender("localhost:1025", "o1@localhost")
	raw := string(s.render(Message{
		To:      "user@example.com",
		Subject: "hello",
		Text:    "monitor o1-mail-boundary is down",
		HTML:    "<p>monitor o1-mail-boundary is down</p>",
	}))
	boundary := extractBoundary(t, raw)
	if boundary == "o1-mail-boundary" {
		t.Fatal("used the old fixed boundary")
	}
	if !strings.Contains(raw, "monitor o1-mail-boundary is down") {
		t.Fatalf("body content was truncated:\n%s", raw)
	}
	if !strings.Contains(raw, "--"+boundary+"--\r\n") {
		t.Fatalf("message has no proper closing delimiter:\n%s", raw)
	}
}

func TestMailpitSender_CanceledContextReturnsPromptly(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = listener.Close()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	accepted := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() {
			_ = conn.Close()
		}()
		close(accepted)
		// Say nothing: the SMTP client blocks waiting for the greeting.
		<-ctx.Done()
	}()

	sender := NewMailpitSender(listener.Addr().String(), "o1@localhost")
	done := make(chan error, 1)
	go func() {
		done <- sender.Send(ctx, Message{
			To:      "user@example.com",
			Subject: "Verify your email",
			Text:    "line one",
			HTML:    "<p>line one</p>",
		})
	}()

	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("sender never connected")
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Send hung after context cancellation")
	}
}
