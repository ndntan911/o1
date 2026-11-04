package httpx

import (
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	// rateLimitStaleAfter is how long an idle per-IP bucket is kept before a
	// sweep removes it.
	rateLimitStaleAfter = 10 * time.Minute
	// rateLimitSweepEvery bounds how often the map is scanned for stale
	// buckets on the request path.
	rateLimitSweepEvery = time.Minute
	// rateLimitMaxBuckets caps the per-IP map so a flood of unique addresses
	// cannot grow it without limit. When full, the least recently seen bucket
	// is evicted.
	rateLimitMaxBuckets = 10_000
)

// CSRF rejects unsafe requests whose Origin header is present and does not
// match one of allowedOrigins. Requests with no Origin are allowed: browsers
// always send it on cross-origin unsafe requests, and non-browser clients do
// not have an origin to forge. SameSite=Lax session cookies cover the rest.
func CSRF(allowedOrigins []string) func(http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		if origin = normalizeOrigin(origin); origin != "" {
			allowed[origin] = struct{}{}
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
				origin := normalizeOrigin(r.Header.Get("Origin"))
				if origin != "" {
					if _, ok := allowed[origin]; !ok {
						WriteProblem(w, Problem{
							Type:   "about:blank",
							Title:  "Forbidden",
							Status: http.StatusForbidden,
							Code:   CodeForbidden,
						})
						return
					}
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// normalizeOrigin trims surrounding whitespace and a trailing slash so
// configured origins and request headers compare consistently.
func normalizeOrigin(origin string) string {
	return strings.TrimRight(strings.TrimSpace(origin), "/")
}

// ipBucket is one token bucket and the last time it was used.
type ipBucket struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// ipBucketStore is a mutex-guarded, size-bounded map of per-IP buckets.
type ipBucketStore struct {
	mu        sync.Mutex
	buckets   map[string]*ipBucket
	lastSweep time.Time
}

func newIPBucketStore() *ipBucketStore {
	return &ipBucketStore{buckets: make(map[string]*ipBucket)}
}

// allow returns the bucket for key (creating it if needed) and whether a
// request may proceed. Callers must not retain the limiter after allow.
func (s *ipBucketStore) allow(key string, rps float64, burst int, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if now.Sub(s.lastSweep) >= rateLimitSweepEvery {
		s.sweepLocked(now)
	}

	bucket, ok := s.buckets[key]
	if !ok {
		if len(s.buckets) >= rateLimitMaxBuckets {
			s.evictOldestLocked()
		}
		bucket = &ipBucket{limiter: rate.NewLimiter(rate.Limit(rps), burst)}
		s.buckets[key] = bucket
	}
	bucket.lastSeen = now
	return bucket.limiter.Allow()
}

// sweepLocked removes buckets that have not been used recently.
func (s *ipBucketStore) sweepLocked(now time.Time) {
	for key, bucket := range s.buckets {
		if now.Sub(bucket.lastSeen) > rateLimitStaleAfter {
			delete(s.buckets, key)
		}
	}
	s.lastSweep = now
}

// evictOldestLocked makes room by deleting the least recently seen bucket.
// It runs at most once per new key once the map is at capacity.
func (s *ipBucketStore) evictOldestLocked() {
	var oldestKey string
	var oldest time.Time
	for key, bucket := range s.buckets {
		if oldestKey == "" || bucket.lastSeen.Before(oldest) {
			oldestKey = key
			oldest = bucket.lastSeen
		}
	}
	if oldestKey != "" {
		delete(s.buckets, oldestKey)
	}
}

// RateLimit limits requests with a token bucket. When keyByIP is true every
// client IP gets its own bucket; otherwise all requests share one bucket.
// A rejected request gets a 429 application/problem+json response with a
// Retry-After header.
func RateLimit(rps float64, burst int, keyByIP bool) func(http.Handler) http.Handler {
	if burst < 1 {
		burst = 1
	}
	store := newIPBucketStore()
	global := rate.NewLimiter(rate.Limit(rps), burst)
	retryAfter := strconv.Itoa(retryAfterSeconds(rps))

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var allowed bool
			if keyByIP {
				allowed = store.allow(clientIP(r), rps, burst, time.Now())
			} else {
				allowed = global.Allow()
			}
			if !allowed {
				w.Header().Set("Retry-After", retryAfter)
				WriteProblem(w, Problem{
					Type:   "about:blank",
					Title:  "Too Many Requests",
					Status: http.StatusTooManyRequests,
					Detail: "Rate limit exceeded; retry later.",
					Code:   CodeRateLimited,
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// retryAfterSeconds is the time until one token refills, rounded up to whole
// seconds with a floor of one.
func retryAfterSeconds(rps float64) int {
	if rps <= 0 {
		return 60
	}
	seconds := int(math.Ceil(1 / rps))
	if seconds < 1 {
		return 1
	}
	return seconds
}

// clientIP derives the rate-limit key. X-Forwarded-For is only trusted when
// the immediate peer is loopback or on a private network (the local Caddy
// proxy); otherwise a public client could spoof the header and evade limits.
func clientIP(r *http.Request) string {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		host = h
	}
	host = strings.TrimSpace(host)

	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsPrivate()) {
		if forwarded := firstForwardedIP(r.Header.Values("X-Forwarded-For")); forwarded != "" {
			return forwarded
		}
	}
	return host
}

// firstForwardedIP returns the first valid address in X-Forwarded-For values.
func firstForwardedIP(values []string) string {
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			candidate := strings.TrimSpace(part)
			if candidate == "" {
				continue
			}
			if ip := net.ParseIP(candidate); ip != nil {
				return ip.String()
			}
		}
	}
	return ""
}
