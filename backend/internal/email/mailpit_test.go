package email

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
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
