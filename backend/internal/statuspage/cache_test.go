package statuspage

import (
	"fmt"
	"testing"
	"time"
)

func TestPageCache_TTLDeleteAndBound(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	cache := newPageCache(15*time.Second, clock)

	cache.set("acme", PublicPage{Workspace: PublicWorkspace{Slug: "acme"}})
	got, ok := cache.get("acme")
	if !ok || got.Workspace.Slug != "acme" {
		t.Fatalf("cache hit = %+v, %v", got, ok)
	}

	now = now.Add(16 * time.Second)
	if _, ok := cache.get("acme"); ok {
		t.Fatal("expired entry still hit")
	}

	cache.set("acme", PublicPage{})
	cache.delete("acme")
	if _, ok := cache.get("acme"); ok {
		t.Fatal("deleted entry still hit")
	}

	for i := 0; i < 1100; i++ {
		cache.set(fmt.Sprintf("slug-%d", i), PublicPage{})
	}
	if got := cache.len(); got > 1024 {
		t.Fatalf("cache len = %d, want <= 1024", got)
	}
}
