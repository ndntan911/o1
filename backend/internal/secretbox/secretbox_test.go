package secretbox_test

import (
	"bytes"
	"strings"
	"testing"

	"o1/internal/secretbox"
)

func TestBox_RoundTripAndTamper(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	box, err := secretbox.New(key)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sealed, err := box.Seal([]byte(`{"Authorization":"Bearer x"}`))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if !strings.HasPrefix(sealed, "v1:") || strings.Contains(sealed, "Bearer") {
		t.Fatalf("sealed value = %q", sealed)
	}
	got, err := box.Open(sealed)
	if err != nil || string(got) != `{"Authorization":"Bearer x"}` {
		t.Fatalf("Open = %q, %v", got, err)
	}

	tampered := sealed[:len(sealed)-2] + "AA"
	if _, err := box.Open(tampered); err == nil {
		t.Fatal("Open accepted a tampered ciphertext")
	}
	if _, err := box.Open("v2:abc"); err == nil {
		t.Fatal("Open accepted an unknown key version")
	}
}

func TestBox_NonceIsFresh(t *testing.T) {
	box, err := secretbox.New(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		sealed, err := box.Seal([]byte("same"))
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}
		if seen[sealed] {
			t.Fatal("Seal reused a nonce")
		}
		seen[sealed] = true
	}
}
