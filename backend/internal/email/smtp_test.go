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

func TestSMTPSender_CanceledContextReturnsPromptly(t *testing.T) {
	addr := startStalledRelay(t)
	s, err := newSMTPSenderWithTLS(addr, "o1 <a@b.c>", "", "", "", "none",
		&tls.Config{ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	err = s.Send(ctx, Message{To: "d@e.f", Subject: "s", Text: "t"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("cancel took %v", time.Since(start))
	}
}

func newSMTPSenderForTest(t *testing.T, tlsMode, username, password string, rejectCode int) *SMTPSender {
	t.Helper()
	addr, pool := startFakeRelay(t, tlsMode, username, password, rejectCode)
	s, err := newSMTPSenderWithTLS(addr, "o1 <noreply@example.com>", "", username, password, tlsMode,
		&tls.Config{RootCAs: pool, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatalf("new sender: %v", err)
	}
	return s
}

func startFakeRelay(t *testing.T, mode, username, password string, rejectCode int) (string, *x509.CertPool) {
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
			go serveFakeRelay(conn, cert, mode, username, password, rejectCode)
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

func serveFakeRelay(conn net.Conn, cert tls.Certificate, mode, username, password string, rejectCode int) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	if mode == "implicit" {
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

	tlsActive := mode == "implicit"
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
			if mode == "starttls" && !tlsActive {
				write("250-STARTTLS\r\n")
			}
			if username != "" {
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
			fields := strings.Fields(line)
			ok := false
			if len(fields) == 3 {
				if raw, err := base64.StdEncoding.DecodeString(fields[2]); err == nil {
					parts := strings.Split(string(raw), "\x00")
					ok = len(parts) == 3 && parts[1] == username && parts[2] == password
				}
			}
			if ok {
				write("235 ok\r\n")
			} else {
				write("535 auth failed\r\n")
			}
		case strings.HasPrefix(upper, "MAIL FROM"):
			if rejectCode != 0 {
				write("%d relay secret text\r\n", rejectCode)
			} else {
				write("250 ok\r\n")
			}
		case strings.HasPrefix(upper, "RCPT TO"):
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
