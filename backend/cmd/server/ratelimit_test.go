package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestLimiter(rate float64, burst int) (*rateLimiter, *fakeClock) {
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	l := newRateLimiter(rate, burst)
	l.now = clk.now
	return l, clk
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiResponse(w, http.StatusOK, "ok", "")
	})
}

func doFrom(h http.Handler, remote string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/models", nil)
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRateLimit_AllowsBurstThenReturns429WithRetryAfter(t *testing.T) {
	l, _ := newTestLimiter(1, 3) // 1 token/s, burst 3
	h := l.middleware(okHandler())

	for i := 0; i < 3; i++ {
		if rec := doFrom(h, "127.0.0.1:5000"); rec.Code != http.StatusOK {
			t.Fatalf("request %d: status %d, want 200", i+1, rec.Code)
		}
	}

	rec := doFrom(h, "127.0.0.1:5000")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	secs, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	if err != nil || secs < 1 {
		t.Fatalf("Retry-After = %q, want integer seconds >= 1", rec.Header().Get("Retry-After"))
	}
	env := decodeEnvelope(t, rec.Body.Bytes())
	if env.Error == nil || *env.Error == "" {
		t.Fatal("429 must use the error envelope")
	}
	if string(env.Data) != "null" {
		t.Fatalf("data = %s, want null", env.Data)
	}
	assertTimestamp(t, env)
}

func TestRateLimit_RefillsOverTime(t *testing.T) {
	l, clk := newTestLimiter(2, 2) // 2 tokens/s
	h := l.middleware(okHandler())

	doFrom(h, "127.0.0.1:1")
	doFrom(h, "127.0.0.1:1")
	if rec := doFrom(h, "127.0.0.1:1"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after burst, got %d", rec.Code)
	}

	clk.advance(500 * time.Millisecond) // +1 token
	if rec := doFrom(h, "127.0.0.1:1"); rec.Code != http.StatusOK {
		t.Fatalf("expected 200 after refill, got %d", rec.Code)
	}
	if rec := doFrom(h, "127.0.0.1:1"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("only one token should have refilled, got %d", rec.Code)
	}
}

func TestRateLimit_BucketsArePerClient(t *testing.T) {
	l, _ := newTestLimiter(1, 1)
	h := l.middleware(okHandler())

	if rec := doFrom(h, "10.0.0.1:1111"); rec.Code != http.StatusOK {
		t.Fatalf("client A first request: %d", rec.Code)
	}
	if rec := doFrom(h, "10.0.0.1:2222"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("same host, different port must share a bucket, got %d", rec.Code)
	}
	if rec := doFrom(h, "10.0.0.2:1111"); rec.Code != http.StatusOK {
		t.Fatalf("client B must have its own bucket, got %d", rec.Code)
	}
}

func TestRateLimit_IgnoresForwardedForHeader(t *testing.T) {
	l, _ := newTestLimiter(1, 1)
	h := l.middleware(okHandler())

	for i, spoof := range []string{"1.1.1.1", "2.2.2.2"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "127.0.0.1:9"
		req.Header.Set("X-Forwarded-For", spoof)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		wantCode := http.StatusOK
		if i == 1 {
			wantCode = http.StatusTooManyRequests
		}
		if rec.Code != wantCode {
			t.Fatalf("request %d: %d, want %d (X-Forwarded-For must not pick the bucket)", i, rec.Code, wantCode)
		}
	}
}

func TestRateLimit_IdleBucketsAreSwept(t *testing.T) {
	l, clk := newTestLimiter(1, 1)
	h := l.middleware(okHandler())

	for i := 0; i < 50; i++ {
		doFrom(h, "10.0.1."+strconv.Itoa(i)+":1")
	}
	if got := l.size(); got != 50 {
		t.Fatalf("size = %d, want 50", got)
	}
	clk.advance(10 * time.Minute)
	doFrom(h, "10.9.9.9:1") // triggers the sweep
	if got := l.size(); got != 1 {
		t.Fatalf("size after sweep = %d, want 1", got)
	}
}
