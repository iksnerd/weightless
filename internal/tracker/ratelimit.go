package tracker

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// RateLimiter implements a thread-safe token bucket rate limiter with an IP-based map.
// It uses a simple eviction strategy to prevent memory bloat.
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64 // tokens per second
	burst   float64 // max tokens
	maxIPs  int     // maximum number of IPs to track before clearing (prevent memory leak)
}

type bucket struct {
	tokens   float64
	lastSeen time.Time
}

func NewRateLimiter(rate, burst float64, maxIPs int) *RateLimiter {
	return &RateLimiter{
		buckets: make(map[string]*bucket),
		rate:    rate,
		burst:   burst,
		maxIPs:  maxIPs,
	}
}

// Allow checks if an IP is allowed to proceed based on the rate limit.
func (rl *RateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Simple eviction: if the map gets too big, reset it.
	// An LRU would be more precise, but periodic resets keep memory bounded.
	if len(rl.buckets) >= rl.maxIPs {
		rl.buckets = make(map[string]*bucket)
	}

	b, ok := rl.buckets[ip]
	if !ok {
		b = &bucket{tokens: rl.burst, lastSeen: time.Now()}
		rl.buckets[ip] = b
	}

	now := time.Now()
	elapsed := now.Sub(b.lastSeen).Seconds()
	b.lastSeen = now

	// Refill tokens based on elapsed time
	b.tokens += elapsed * rl.rate
	if b.tokens > rl.burst {
		b.tokens = rl.burst
	}

	if b.tokens >= 1 {
		b.tokens -= 1
		return true
	}

	return false
}

// LimitMiddleware wraps an http.Handler to apply rate limiting.
func (rl *RateLimiter) LimitMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			// Fallback to RemoteAddr if split fails
			ip = r.RemoteAddr
		}

		if !rl.Allow(ip) {
			// For trackers, we return a 200 with a bencoded failure for /announce,
			// but for API we return a standard 429.
			if r.URL.Path == "/announce" {
				TrackerError(w, "Rate limit exceeded. Please slow down.")
			} else {
				http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			}
			return
		}

		next(w, r)
	}
}
