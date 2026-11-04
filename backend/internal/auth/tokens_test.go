package auth_test

import (
	"testing"

	"o1/internal/auth"
)

func TestNewToken_UniqueAndHashed(t *testing.T) {
	a, ha, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	b, hb, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("tokens repeated")
	}
	if len(ha) != 32 || len(hb) != 32 {
		t.Fatalf("hash lengths = %d, %d", len(ha), len(hb))
	}
	if string(ha) == string(a) {
		t.Fatal("token stored in clear")
	}
	if string(auth.HashToken(a)) != string(ha) {
		t.Fatal("HashToken mismatch")
	}
}
