package hosted

import (
	"sync"
	"time"
)

// breaker is a consecutive-failure circuit breaker. After threshold failures in
// a row it opens for cooldown; then it lets exactly one probe call through
// (half-open). A success closes it, a failed probe re-opens it.
type breaker struct {
	threshold int
	cooldown  time.Duration
	now       func() time.Time

	mu        sync.Mutex
	failures  int
	openUntil time.Time
	probing   bool
}

// allow reports whether a call may go out now.
func (b *breaker) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failures < b.threshold {
		return true
	}
	if b.now().Before(b.openUntil) || b.probing {
		return false
	}
	b.probing = true // half-open: one probe
	return true
}

// success records a good call and closes the breaker.
func (b *breaker) success() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = 0
	b.probing = false
}

// failure records a call that failed after all retries.
func (b *breaker) failure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probing = false
	b.failures++
	if b.failures >= b.threshold {
		b.openUntil = b.now().Add(b.cooldown)
	}
}

// release gives back a half-open probe slot without counting a result, for a
// call abandoned because the caller's context ended.
func (b *breaker) release() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probing = false
}
