package tracker

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRateLimiter(t *testing.T) {
	// Rate: 10 per second, Burst: 2
	rl := NewRateLimiter(10.0, 2.0, 100)

	// First two should pass (burst)
	if !rl.Allow("1.1.1.1") {
		t.Error("First request should be allowed")
	}
	if !rl.Allow("1.1.1.1") {
		t.Error("Second request should be allowed (burst)")
	}

	// Third should be blocked immediately
	if rl.Allow("1.1.1.1") {
		t.Error("Third request should be blocked")
	}

	// Different IP should be allowed
	if !rl.Allow("2.2.2.2") {
		t.Error("Request from different IP should be allowed")
	}

	// Wait for refill (10 tokens/sec = 0.1s per token)
	time.Sleep(150 * time.Millisecond)
	if !rl.Allow("1.1.1.1") {
		t.Error("Request after refill should be allowed")
	}
}

func TestRateLimiterEviction(t *testing.T) {
	// Small map limit
	rl := NewRateLimiter(100.0, 10.0, 2)

	rl.Allow("1.1.1.1")
	rl.Allow("2.2.2.2")
	if len(rl.buckets) != 2 {
		t.Errorf("Expected 2 buckets, got %d", len(rl.buckets))
	}

	// Trigger eviction by adding 3rd IP
	rl.Allow("3.3.3.3")
	if len(rl.buckets) > 2 {
		t.Errorf("Buckets should have been cleared, got %d", len(rl.buckets))
	}
}

func TestLimitMiddleware(t *testing.T) {
	rl := NewRateLimiter(100.0, 1.0, 10)

	handler := rl.LimitMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// First request: OK
	req := httptest.NewRequest("GET", "/test", nil)
	req.RemoteAddr = "1.2.3.4:1234"
	w1 := httptest.NewRecorder()
	handler(w1, req)
	if w1.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", w1.Code)
	}

	// Second request: 429
	w2 := httptest.NewRecorder()
	handler(w2, req)
	if w2.Code != http.StatusTooManyRequests {
		t.Errorf("Expected 429, got %d", w2.Code)
	}

	// Announce request: Tracker error
	reqAnnounce := httptest.NewRequest("GET", "/announce", nil)
	reqAnnounce.RemoteAddr = "5.6.7.8:1234"
	w3 := httptest.NewRecorder()
	handler(w3, reqAnnounce) // OK

	w4 := httptest.NewRecorder()
	handler(w4, reqAnnounce) // Blocked
	if w4.Code != http.StatusOK {
		t.Errorf("Announce should return 200 even when blocked, got %d", w4.Code)
	}
	if !strings.Contains(w4.Body.String(), "failure reason") {
		t.Error("Announce block should return failure reason")
	}
}
