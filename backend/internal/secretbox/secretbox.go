// Package secretbox encrypts small secrets at rest with AES-256-GCM.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const versionPrefix = "v1:"

// ErrInvalidCiphertext is returned when a sealed value is malformed, uses an
// unknown version, or fails authentication.
var ErrInvalidCiphertext = errors.New("secretbox: invalid ciphertext")

// Box seals and opens values with a single 32-byte AES-256 key.
type Box struct {
	aead cipher.AEAD
}

// New returns a Box using key, which must be exactly 32 bytes.
func New(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("secretbox: key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secretbox: new cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secretbox: new gcm: %w", err)
	}
	return &Box{aead: aead}, nil
}

// Seal encrypts plaintext and returns a "v1:"-prefixed base64 value. Empty
// plaintext seals to the empty string.
func (b *Box) Seal(plaintext []byte) (string, error) {
	if len(plaintext) == 0 {
		return "", nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("secretbox: generate nonce: %w", err)
	}
	sealed := b.aead.Seal(nonce, nonce, plaintext, nil)
	return versionPrefix + base64.RawURLEncoding.EncodeToString(sealed), nil
}

// Open decrypts a value produced by Seal. An empty value opens to empty
// plaintext. Malformed, unknown-version, or unauthentic values return
// ErrInvalidCiphertext.
func (b *Box) Open(value string) ([]byte, error) {
	if value == "" {
		return nil, nil
	}
	if !strings.HasPrefix(value, versionPrefix) {
		return nil, ErrInvalidCiphertext
	}
	sealed, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, versionPrefix))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCiphertext, err)
	}
	if len(sealed) < b.aead.NonceSize() {
		return nil, ErrInvalidCiphertext
	}
	nonce, ciphertext := sealed[:b.aead.NonceSize()], sealed[b.aead.NonceSize():]
	plaintext, err := b.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, ErrInvalidCiphertext
	}
	return plaintext, nil
}
