package auth_test

import (
	"strings"
	"testing"

	"o1/internal/auth"
)

func TestHashPassword_RoundTrip(t *testing.T) {
	hash, err := auth.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=65536,t=1,p=4$") {
		t.Fatalf("unexpected PHC prefix: %s", hash)
	}
	ok, err := auth.VerifyPassword(hash, "correct horse battery staple")
	if err != nil || !ok {
		t.Fatalf("verify = %v, %v", ok, err)
	}
	ok, err = auth.VerifyPassword(hash, "wrong password")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("wrong password verified")
	}
}

func TestHashPassword_RejectsShortPassword(t *testing.T) {
	if _, err := auth.HashPassword("short"); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestHashPassword_LengthBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		length  int
		wantErr bool
	}{
		{"eleven", 11, true},
		{"minimum", 12, false},
		{"maximum", 128, false},
		{"over maximum", 129, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := auth.HashPassword(strings.Repeat("a", tc.length))
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Fatalf("length %d: err = %v, wantErr = %v", tc.length, err, tc.wantErr)
			}
		})
	}
}
