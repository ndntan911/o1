package email

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRenderMessage_ReplyTo(t *testing.T) {
	raw := string(renderMessage("o1 <a@b.c>", "help@b.c", Message{To: "d@e.f", Subject: "s", Text: "t", HTML: "<p>t</p>"}))
	if !strings.Contains(raw, "Reply-To: help@b.c\r\n") {
		t.Fatalf("missing Reply-To:\n%s", raw)
	}
}

func TestRenderMessage_NoReplyToByDefault(t *testing.T) {
	raw := string(renderMessage("o1 <a@b.c>", "", Message{To: "d@e.f", Subject: "s"}))
	if strings.Contains(raw, "Reply-To:") {
		t.Fatalf("unexpected Reply-To:\n%s", raw)
	}
}

func TestSMTPSender_ClassifiesPermanentAndTransient(t *testing.T) {
	s := newSMTPSenderForTest(t, "none", "", "", 550)
	err := s.Send(context.Background(), Message{To: "d@e.f", Subject: "s", Text: "t"})
	if err == nil || !IsPermanent(err) {
		t.Fatalf("550 should be permanent, got %v", err)
	}
	if strings.Contains(err.Error(), "relay secret text") {
		t.Fatalf("relay text leaked: %v", err)
	}
	if !strings.Contains(err.Error(), "550") {
		t.Fatalf("error should carry the numeric code: %v", err)
	}

	s = newSMTPSenderForTest(t, "none", "", "", 451)
	err = s.Send(context.Background(), Message{To: "d@e.f", Subject: "s", Text: "t"})
	if err == nil || IsPermanent(err) {
		t.Fatalf("451 should be transient, got %v", err)
	}
	if strings.Contains(err.Error(), "relay secret text") {
		t.Fatalf("relay text leaked for 451: %v", err)
	}
	if !strings.Contains(err.Error(), "451") {
		t.Fatalf("error should carry the numeric code: %v", err)
	}
}

func TestSMTPSender_UsesBareEnvelopeAddresses(t *testing.T) {
	capture := &relayEnvelope{}
	addr, pool := startFakeRelay(t, relayOptions{mode: "none", capture: capture})
	s := newTestSMTPSender(t, addr, "none", "", "", pool)

	err := s.Send(context.Background(), Message{
		To:      "User Name <d@e.f>",
		Subject: "s",
		Text:    "t",
	})
	if err != nil {
		t.Fatal(err)
	}
	mailFrom, rcptTo := capture.snapshot()
	if mailFrom != "<noreply@example.com>" {
		t.Fatalf("MAIL FROM = %q, want bare address", mailFrom)
	}
	if rcptTo != "<d@e.f>" {
		t.Fatalf("RCPT TO = %q, want bare address", rcptTo)
	}
}

func TestSMTPSender_TransportErrorIsSanitized(t *testing.T) {
	s := newTestSMTPSender(t, "127.0.0.1:1", "none", "", "", nil)

	err := s.Send(context.Background(), Message{To: "d@e.f", Subject: "s", Text: "t"})
	if err == nil {
		t.Fatal("expected a connection error")
	}
	if IsPermanent(err) {
		t.Fatalf("transport errors must stay transient: %v", err)
	}
	if strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("host leaked: %v", err)
	}
	if !strings.Contains(err.Error(), "connection failed") {
		t.Fatalf("unexpected message: %v", err)
	}
}

func TestSMTPSender_STARTTLS(t *testing.T) {
	s := newSMTPSenderForTest(t, "starttls", "user", "pass", 0)
	if err := s.Send(context.Background(), Message{To: "d@e.f", Subject: "s", Text: "t"}); err != nil {
		t.Fatal(err)
	}
}

func TestSMTPSender_ImplicitTLS(t *testing.T) {
	s := newSMTPSenderForTest(t, "implicit", "", "", 0)
	if err := s.Send(context.Background(), Message{To: "d@e.f", Subject: "s", Text: "t"}); err != nil {
		t.Fatal(err)
	}
}

func TestSMTPSender_PlaintextAuthIsRejected(t *testing.T) {
	s := newSMTPSenderForTest(t, "none", "user", "pass", 0)
	if err := s.Send(context.Background(), Message{To: "d@e.f", Subject: "s", Text: "t"}); err == nil {
		t.Fatal("plaintext AUTH should not succeed")
	}
}

func TestSMTPSender_QuitFailureAfterAcceptIsSuccess(t *testing.T) {
	addr, pool := startFakeRelay(t, relayOptions{mode: "none", rejectQuit: true})
	s := newTestSMTPSender(t, addr, "none", "", "", pool)

	if err := s.Send(context.Background(), Message{To: "d@e.f", Subject: "s", Text: "t"}); err != nil {
		t.Fatalf("message was accepted, QUIT failure must not fail the send: %v", err)
	}
}

func TestSMTPSender_CanceledContextReturnsPromptly(t *testing.T) {
	addr := startStalledRelay(t)
	s := newTestSMTPSender(t, addr, "none", "", "", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := s.Send(ctx, Message{To: "d@e.f", Subject: "s", Text: "t"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("cancel took %v", time.Since(start))
	}
}

type relayOptions struct {
	mode       string
	username   string
	password   string
	rejectMail int
	rejectQuit bool
	capture    *relayEnvelope
}

type relayEnvelope struct {
	mu       sync.Mutex
	mailFrom string
	rcptTo   string
}

func (e *relayEnvelope) snapshot() (string, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.mailFrom, e.rcptTo
}

func (e *relayEnvelope) setMailFrom(value string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.mailFrom = value
}

func (e *relayEnvelope) setRcptTo(value string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rcptTo = value
}

func newTestSMTPSender(t *testing.T, addr, tlsMode, username, password string, pool *x509.CertPool) *SMTPSender {
	t.Helper()
	s, err := newSMTPSenderWithTLS(addr, "o1 <noreply@example.com>", "", username, password, tlsMode,
		&tls.Config{ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12, RootCAs: pool})
	if err != nil {
		t.Fatalf("new sender: %v", err)
	}
	return s
}

func newSMTPSenderForTest(t *testing.T, tlsMode, username, password string, rejectCode int) *SMTPSender {
	t.Helper()
	addr, pool := startFakeRelay(t, relayOptions{
		mode:       tlsMode,
		username:   username,
		password:   password,
		rejectMail: rejectCode,
	})
	return newTestSMTPSender(t, addr, tlsMode, username, password, pool)
}

func startFakeRelay(t *testing.T, opts relayOptions) (string, *x509.CertPool) {
	t.Helper()
	cert, pool := testCertificate(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveFakeRelay(conn, cert, opts)
		}
	}()
	return ln.Addr().String(), pool
}

func startStalledRelay(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				<-time.After(10 * time.Second)
			}()
		}
	}()
	return ln.Addr().String()
}

func serveFakeRelay(conn net.Conn, cert tls.Certificate, opts relayOptions) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	if opts.mode == "implicit" {
		tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}})
		if err := tlsConn.Handshake(); err != nil {
			return
		}
		conn = tlsConn
	}

	reader := bufio.NewReader(conn)
	write := func(format string, args ...any) {
		_, _ = fmt.Fprintf(conn, format, args...)
	}
	write("220 fake ESMTP\r\n")

	tlsActive := opts.mode == "implicit"
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		upper := strings.ToUpper(line)

		switch {
		case strings.HasPrefix(upper, "EHLO"):
			write("250-fake\r\n")
			if opts.mode == "starttls" && !tlsActive {
				write("250-STARTTLS\r\n")
			}
			// AUTH is only advertised once the connection is encrypted.
			if tlsActive && opts.username != "" {
				write("250-AUTH PLAIN\r\n")
			}
			write("250 OK\r\n")
		case strings.HasPrefix(upper, "HELO"):
			write("250 fake\r\n")
		case strings.HasPrefix(upper, "STARTTLS"):
			write("220 ready\r\n")
			tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}})
			if err := tlsConn.Handshake(); err != nil {
				return
			}
			conn = tlsConn
			_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
			reader = bufio.NewReader(conn)
			tlsActive = true
		case strings.HasPrefix(upper, "AUTH PLAIN"):
			if !tlsActive {
				write("530 must issue STARTTLS first\r\n")
				continue
			}
			fields := strings.Fields(line)
			ok := false
			if len(fields) == 3 {
				if raw, err := base64.StdEncoding.DecodeString(fields[2]); err == nil {
					parts := strings.Split(string(raw), "\x00")
					ok = len(parts) == 3 && parts[1] == opts.username && parts[2] == opts.password
				}
			}
			if ok {
				write("235 ok\r\n")
			} else {
				write("535 auth failed\r\n")
			}
		case strings.HasPrefix(upper, "MAIL FROM"):
			if opts.capture != nil {
				opts.capture.setMailFrom(strings.TrimSpace(strings.TrimPrefix(line, "MAIL FROM:")))
			}
			if opts.rejectMail != 0 {
				write("%d relay secret text\r\n", opts.rejectMail)
			} else {
				write("250 ok\r\n")
			}
		case strings.HasPrefix(upper, "RCPT TO"):
			if opts.capture != nil {
				opts.capture.setRcptTo(strings.TrimSpace(strings.TrimPrefix(line, "RCPT TO:")))
			}
			write("250 ok\r\n")
		case strings.HasPrefix(upper, "DATA"):
			write("354 go\r\n")
			for {
				dataLine, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(dataLine, "\r\n") == "." {
					break
				}
			}
			write("250 queued\r\n")
		case strings.HasPrefix(upper, "QUIT"):
			if opts.rejectQuit {
				write("500 no quit here\r\n")
				return
			}
			write("221 bye\r\n")
			return
		default:
			write("250 ok\r\n")
		}
	}
}

func testCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}
