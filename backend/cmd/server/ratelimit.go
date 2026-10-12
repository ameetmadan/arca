package main

import (
	"math"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Defaults are deliberately generous: the only legitimate callers are the
// local SwiftUI app and the folder watcher, and the limiter exists to stop a
// runaway client or a malicious local page, not to meter normal use.
const (
	defaultRateLimitPerSec = 50
	defaultRateLimitBurst  = 100
	rateLimitSweepEvery    = time.Minute
)

type bucket struct {
	tokens float64
	last   time.Time
}

// rateLimiter is a small per-client token bucket. Clients are keyed by the
// peer IP (RemoteAddr), never by X-Forwarded-For, which a caller could forge
// to dodge the limit.
type rateLimiter struct {
	rate  float64 // tokens added per second
	burst float64

	now func() time.Time

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

func newRateLimiter(ratePerSec float64, burst int) *rateLimiter {
	return &rateLimiter{
		rate:    ratePerSec,
		burst:   float64(burst),
		now:     time.Now,
		buckets: make(map[string]*bucket),
	}
}

// allow takes one token for key. When it refuses, retryAfter says how long
// until a token will be available.
func (l *rateLimiter) allow(key string) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.sweepLocked(now)

	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	} else {
		b.tokens = math.Min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
		b.last = now
	}

	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	missing := 1 - b.tokens
	return false, time.Duration(missing / l.rate * float64(time.Second))
}

// sweepLocked drops buckets that have been idle long enough to be full again,
// so the map cannot grow without bound.
func (l *rateLimiter) sweepLocked(now time.Time) {
	if now.Sub(l.lastSweep) < rateLimitSweepEvery {
		return
	}
	l.lastSweep = now
	idle := time.Duration(l.burst/l.rate*float64(time.Second)) + rateLimitSweepEvery
	for k, b := range l.buckets {
		if now.Sub(b.last) > idle {
			delete(l.buckets, k)
		}
	}
}

func (l *rateLimiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

func (l *rateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok, retry := l.allow(clientKey(r))
		if !ok {
			secs := int(math.Ceil(retry.Seconds()))
			if secs < 1 {
				secs = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(secs))
			apiResponse(w, http.StatusTooManyRequests, nil, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
