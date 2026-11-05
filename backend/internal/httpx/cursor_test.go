package httpx_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"o1/internal/httpx"
)

func TestCursorRoundTripAndRejectsGarbage(t *testing.T) {
	c := httpx.PageCursor{Time: time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC), ID: uuid.MustParse("0190a000-0000-7000-8000-000000000001")}
	enc := httpx.EncodeCursor(c)
	if strings.Contains(enc, "2026") {
		t.Fatalf("cursor leaks plaintext: %q", enc)
	}
	got, err := httpx.DecodeCursor(enc)
	if err != nil || !got.Time.Equal(c.Time) || got.ID != c.ID {
		t.Fatalf("DecodeCursor = %+v, %v", got, err)
	}
	if _, err := httpx.DecodeCursor("not-base64!"); err == nil {
		t.Fatal("DecodeCursor accepted garbage")
	}
}

func TestDecodeCursorRejectsMissingFields(t *testing.T) {
	for _, enc := range []string{
		"",
		"e30",                                    // {}
		"eyJ0IjoiMjAyNi0xMS0wNFQxMDowMDowMFoifQ", // missing id
	} {
		if _, err := httpx.DecodeCursor(enc); err == nil {
			t.Errorf("DecodeCursor(%q) succeeded", enc)
		}
	}
}
