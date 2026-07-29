package auth

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// RateLimiter enforces a token-bucket rate limit per key (typically IP).
type RateLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*bucket
	rate     int           // max requests
	interval time.Duration // per interval
	cleanup  time.Time
}

type bucket struct {
	tokens   float64
	lastSeen time.Time
}

// NewRateLimiter returns a limiter allowing up to `rate` actions per `interval`
// per key. The limiter performs occasional cleanup of stale entries.
func NewRateLimiter(rate int, interval time.Duration) *RateLimiter {
	if rate <= 0 {
		rate = 1
	}
	return &RateLimiter{
		buckets:  make(map[string]*bucket),
		rate:     rate,
		interval: interval,
		cleanup:  time.Now(),
	}
}

// Allow reports whether the key is within its rate limit and consumes one token.
func (rl *RateLimiter) Allow(key string) bool {
	return rl.allowAt(key, time.Now())
}

func (rl *RateLimiter) allowAt(key string, now time.Time) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Periodic cleanup of stale entries.
	if now.Sub(rl.cleanup) > 10*time.Minute {
		for k, b := range rl.buckets {
			if now.Sub(b.lastSeen) > rl.interval*2 {
				delete(rl.buckets, k)
			}
		}
		rl.cleanup = now
	}

	b, ok := rl.buckets[key]
	if !ok {
		b = &bucket{tokens: float64(rl.rate)}
		rl.buckets[key] = b
	}

	// Refill tokens based on elapsed time.
	elapsed := now.Sub(b.lastSeen)
	b.tokens += elapsed.Seconds() * (float64(rl.rate) / rl.interval.Seconds())
	if b.tokens > float64(rl.rate) {
		b.tokens = float64(rl.rate)
	}
	b.lastSeen = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// AllowRequest extracts the client IP from the request and checks the rate limit.
func (rl *RateLimiter) AllowRequest(r *http.Request) bool {
	return rl.Allow(clientIP(r))
}

// clientIP extracts the immediate peer IP. Forwarded headers are intentionally
// ignored: without an explicit, deployment-owned trusted-proxy boundary they
// are attacker-controlled input and must not influence login throttling.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// LoginRateLimiter returns a rate limiter suitable for login attempts:
// 5 attempts per minute per IP.
func LoginRateLimiter() *RateLimiter {
	return NewRateLimiter(5, time.Minute)
}
