package httpx

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// PageCursor is the transport-level keyset cursor: the last row's sort time
// and id. Services convert it to their own cursor type.
type PageCursor struct {
	Time time.Time
	ID   uuid.UUID
}

// cursorEnvelope is the encoded representation of a PageCursor. The short key
// names keep cursors compact; the field names are internal to this package.
type cursorEnvelope struct {
	Time string    `json:"t"`
	ID   uuid.UUID `json:"id"`
}

// ErrInvalidCursor is returned by DecodeCursor for malformed input.
var ErrInvalidCursor = errors.New("invalid cursor")

// EncodeCursor serializes c as base64url(JSON).
func EncodeCursor(c PageCursor) string {
	raw, err := json.Marshal(cursorEnvelope{
		Time: c.Time.UTC().Format(time.RFC3339Nano),
		ID:   c.ID,
	})
	if err != nil {
		// A PageCursor only contains a time and a UUID, both of which always
		// marshal successfully.
		panic(fmt.Sprintf("encode cursor: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// DecodeCursor parses a cursor produced by EncodeCursor. Every failure is
// reported as ErrInvalidCursor so callers can return a stable validation error.
func DecodeCursor(s string) (PageCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return PageCursor{}, ErrInvalidCursor
	}
	var env cursorEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return PageCursor{}, ErrInvalidCursor
	}
	if env.Time == "" || env.ID == uuid.Nil {
		return PageCursor{}, ErrInvalidCursor
	}
	t, err := time.Parse(time.RFC3339Nano, env.Time)
	if err != nil {
		return PageCursor{}, ErrInvalidCursor
	}
	return PageCursor{Time: t, ID: env.ID}, nil
}
