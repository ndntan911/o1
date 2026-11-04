// Package auth provides authentication primitives: Argon2id password
// hashing, random single-use tokens, registration, and email verification.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Password length limits enforced by HashPassword.
const (
	minPasswordLen = 12
	maxPasswordLen = 128
)

// Argon2id parameters: 64 MiB memory, 1 iteration, 4 threads, 32-byte key,
// and a 16 random-byte salt.
const (
	argonKeyLen  = uint32(32)
	argonSaltLen = 16
)

// ErrValidation signals that user-supplied input failed validation.
var ErrValidation = errors.New("validation failed")

// HashPassword validates the password length (12-128 characters) and
// returns its PHC-formatted Argon2id hash of the form
// $argon2id$v=19$m=65536,t=1,p=4$<salt>$<key>.
func HashPassword(password string) (string, error) {
	if n := utf8.RuneCountInString(password); n < minPasswordLen || n > maxPasswordLen {
		return "", fmt.Errorf("%w: password must be between %d and %d characters", ErrValidation, minPasswordLen, maxPasswordLen)
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, 1, 64*1024, 4, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=65536,t=1,p=4$%s$%s",
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword recomputes the Argon2id key from the parameters and salt
// stored in the PHC string and compares it with the password in constant
// time. It returns false for any malformed hash.
func VerifyPassword(encoded, password string) (bool, error) {
	fields := strings.Split(encoded, "$")
	// ["", "argon2id", "v=19", "m=...,t=...,p=...", salt, key]
	if len(fields) != 6 || fields[0] != "" || fields[1] != "argon2id" || fields[2] != "v=19" {
		return false, errors.New("malformed PHC string")
	}
	var m, t uint32
	var p uint8
	n, err := fmt.Sscanf(fields[3], "m=%d,t=%d,p=%d", &m, &t, &p)
	if err != nil || n != 3 || m == 0 || t == 0 || p == 0 {
		return false, errors.New("malformed PHC parameters")
	}
	salt, err := base64.RawStdEncoding.DecodeString(fields[4])
	if err != nil || len(salt) != argonSaltLen {
		return false, errors.New("malformed PHC salt")
	}
	want, err := base64.RawStdEncoding.DecodeString(fields[5])
	if err != nil || len(want) != int(argonKeyLen) {
		return false, errors.New("malformed PHC key")
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, argonKeyLen)
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
