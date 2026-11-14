// Package hosted implements the optional "hosted" decision engine (engine 4 in
// ADR-0006, ADR-0008): a last-resort call to a hosted decision model for files
// that rules, semantic matching and the local LLM could not place.
//
// It is off by default and sends data off this Mac, so it is built to send as
// little as possible and to fail closed:
//
//   - It runs only for files the Allow function accepts. internal/config
//     derives Allow from the per-folder opt-in list; with no opted-in folder
//     the engine never touches the network.
//   - Only a short snippet, the file extension and the candidate folder names
//     leave the machine. Never the whole file, its path or its name.
//   - The API key comes from a KeySource (the macOS Keychain in production)
//     and travels only in the Authorization header. Neither the key nor any
//     file content is logged or put in an error.
//   - Requests have a timeout, transient failures are retried with exponential
//     backoff, and a circuit breaker stops calling an unhealthy service. Every
//     failure is reported as ErrUnavailable, which wraps decision.ErrNoDecision,
//     so the chain degrades to AskUser instead of failing.
//
// The wire format below (see request and response) is an adapter assumption: it
// follows the "Choice" question shape described in ADR-0008 (options in,
// a choice plus confidence and optional per-option probabilities out). It must
// be checked against the vendor's official API documentation before use.
package hosted

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ameetmadan/arca/pkg/decision"
)

// Defaults for Config zero values.
const (
	DefaultMaxConfidence    = 0.9
	DefaultSnippetLen       = 200
	DefaultMaxCandidates    = 100
	DefaultTimeout          = 10 * time.Second // per HTTP request
	DefaultMaxAttempts      = 3
	DefaultBackoff          = 250 * time.Millisecond
	DefaultMaxBackoff       = 4 * time.Second
	DefaultBreakerThreshold = 5
	DefaultBreakerCooldown  = 60 * time.Second

	maxResponseBytes = 1 << 20
	sourceName       = "hosted"
)

// ErrUnavailable means the hosted service could not give an answer (no key,
// network error, timeout, 4xx/5xx, open circuit). It wraps
// decision.ErrNoDecision, so a Chain falls through to AskUser.
var ErrUnavailable = fmt.Errorf("%w: hosted service unavailable", decision.ErrNoDecision)

// ErrCircuitOpen is returned without any network call while the breaker is open.
// It wraps ErrUnavailable.
var ErrCircuitOpen = fmt.Errorf("%w: circuit open", ErrUnavailable)

// CandidateFunc lists the existing folders the model may choose from, most
// relevant first.
type CandidateFunc func(ctx context.Context, f decision.Facts) ([]string, error)

// Config configures an Engine. Endpoint, Key, Allow and Candidates are required.
type Config struct {
	// Endpoint is the https URL requests are POSTed to. Plain http is accepted
	// only for loopback hosts (tests).
	Endpoint string
	// Key supplies the API key.
	Key KeySource
	// Allow reports whether the file may be sent off this Mac, i.e. whether
	// its watched folder opted in. It is required: there is no allow-all default.
	Allow func(path string) bool
	// Candidates supplies the folder list the model chooses from.
	Candidates CandidateFunc

	// MaxConfidence caps Decision.Confidence, in (0, 1]. Default 0.9.
	MaxConfidence float64
	// SnippetLen is the maximum number of snippet runes sent. Default 200.
	SnippetLen int
	// MaxCandidates bounds the folder list sent. Default 100.
	MaxCandidates int
	// Timeout bounds one HTTP request. Default 10s.
	Timeout time.Duration
	// MaxAttempts is the number of tries per Decide for transient failures
	// (network error, timeout, 429, 5xx). Default 3.
	MaxAttempts int
	// Backoff is the delay before the second attempt; it doubles each retry up
	// to MaxBackoff. Defaults 250ms and 4s.
	Backoff    time.Duration
	MaxBackoff time.Duration
	// BreakerThreshold is how many Decide calls in a row must fail before the
	// circuit opens (default 5); BreakerCooldown is how long it stays open
	// before one probe is allowed (default 60s).
	BreakerThreshold int
	BreakerCooldown  time.Duration

	// HTTPClient defaults to a client with no redirects followed.
	HTTPClient *http.Client
	// Logf receives short status messages. They never contain file content,
	// paths, the key or response bodies. Default: no logging.
	Logf func(format string, args ...any)

	// Now and Sleep are for tests.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
}

// Engine is a decision.Engine backed by a hosted decision model. It is safe for
// concurrent use.
type Engine struct {
	cfg Config
	brk *breaker
	// warnOnce limits the "no key" log line to once per process.
	warnOnce sync.Once
}

var _ decision.Engine = (*Engine)(nil)

// New validates cfg, applies defaults and returns an Engine. It makes no
// network call and does not read the key.
func New(cfg Config) (*Engine, error) {
	if cfg.Key == nil {
		return nil, errors.New("hosted: nil Key")
	}
	if cfg.Allow == nil {
		return nil, errors.New("hosted: nil Allow (the per-folder opt-in check is required)")
	}
	if cfg.Candidates == nil {
		return nil, errors.New("hosted: nil Candidates")
	}
	if err := validateEndpoint(cfg.Endpoint); err != nil {
		return nil, err
	}
	if cfg.MaxConfidence == 0 {
		cfg.MaxConfidence = DefaultMaxConfidence
	}
	if math.IsNaN(cfg.MaxConfidence) || cfg.MaxConfidence < 0 || cfg.MaxConfidence > 1 {
		return nil, fmt.Errorf("hosted: MaxConfidence %v outside (0, 1]", cfg.MaxConfidence)
	}
	if cfg.SnippetLen < 0 || cfg.MaxCandidates < 0 || cfg.Timeout < 0 || cfg.MaxAttempts < 0 ||
		cfg.Backoff < 0 || cfg.MaxBackoff < 0 || cfg.BreakerThreshold < 0 || cfg.BreakerCooldown < 0 {
		return nil, errors.New("hosted: numeric options must not be negative")
	}
	def := func(v *int, d int) {
		if *v == 0 {
			*v = d
		}
	}
	defd := func(v *time.Duration, d time.Duration) {
		if *v == 0 {
			*v = d
		}
	}
	def(&cfg.SnippetLen, DefaultSnippetLen)
	def(&cfg.MaxCandidates, DefaultMaxCandidates)
	def(&cfg.MaxAttempts, DefaultMaxAttempts)
	def(&cfg.BreakerThreshold, DefaultBreakerThreshold)
	defd(&cfg.Timeout, DefaultTimeout)
	defd(&cfg.Backoff, DefaultBackoff)
	defd(&cfg.MaxBackoff, DefaultMaxBackoff)
	defd(&cfg.BreakerCooldown, DefaultBreakerCooldown)
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{
			// A redirect could carry the Authorization header elsewhere.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleepCtx
	}
	return &Engine{
		cfg: cfg,
		brk: &breaker{threshold: cfg.BreakerThreshold, cooldown: cfg.BreakerCooldown, now: cfg.Now},
	}, nil
}

func validateEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("hosted: endpoint must be an absolute https URL")
	}
	if u.User != nil {
		return errors.New("hosted: endpoint must not contain credentials")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if ip := net.ParseIP(u.Hostname()); u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback()) {
			return nil
		}
	}
	return errors.New("hosted: endpoint must use https")
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// request is the JSON body sent to the service: a "choice" question over the
// candidate folders. It deliberately has no path or file name.
type request struct {
	Question string   `json:"question"`
	Input    input    `json:"input"`
	Options  []string `json:"options"`
}

type input struct {
	Ext     string `json:"ext,omitempty"`
	Snippet string `json:"snippet"`
}

// response is the JSON body expected back. Confidence is a pointer so that a
// missing value can be told from zero.
type response struct {
	Choice        string             `json:"choice"`
	Confidence    *float64           `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
}

// Decide asks the hosted model where to put the file and returns a Move
// decision. It returns an error wrapping decision.ErrNoDecision when the file's
// folder has not opted in (without any network call), when there is nothing to
// send, when the service is unavailable (ErrUnavailable) or when its answer
// is unusable. Only the caller's own context error is returned as such.
func (e *Engine) Decide(ctx context.Context, f decision.Facts) (decision.Decision, error) {
	if err := ctx.Err(); err != nil {
		return decision.Decision{}, err
	}
	if !e.cfg.Allow(f.Path) {
		return decision.Decision{}, fmt.Errorf("%w: folder has not opted in to hosted decisions", decision.ErrNoDecision)
	}
	snippet := truncateRunes(strings.TrimSpace(f.Snippet), e.cfg.SnippetLen)
	if snippet == "" {
		return decision.Decision{}, fmt.Errorf("%w: nothing to send", decision.ErrNoDecision)
	}
	cands, err := e.cfg.Candidates(ctx, f)
	if err != nil {
		return decision.Decision{}, fmt.Errorf("hosted: list candidate folders: %w", err)
	}
	cands = normalize(cands, e.cfg.MaxCandidates)
	if len(cands) == 0 {
		return decision.Decision{}, fmt.Errorf("%w: no candidate folders", decision.ErrNoDecision)
	}

	if !e.brk.allow() {
		return decision.Decision{}, ErrCircuitOpen
	}
	key, err := e.cfg.Key.APIKey(ctx)
	if err != nil || key == "" {
		e.brk.release()
		if cerr := ctx.Err(); cerr != nil {
			return decision.Decision{}, cerr
		}
		e.warnOnce.Do(func() { e.cfg.Logf("hosted: no API key available; hosted decisions are skipped") })
		return decision.Decision{}, fmt.Errorf("%w: no API key", ErrUnavailable)
	}

	body, err := json.Marshal(request{
		Question: "choice",
		Input:    input{Ext: f.Ext, Snippet: snippet},
		Options:  cands,
	})
	if err != nil {
		e.brk.release()
		return decision.Decision{}, fmt.Errorf("hosted: encode request: %w", err)
	}

	resp, healthy, err := e.call(ctx, key, body)
	if cerr := ctx.Err(); cerr != nil {
		e.brk.release()
		return decision.Decision{}, cerr
	}
	if err != nil {
		if healthy {
			e.brk.success()
		} else {
			e.brk.failure()
		}
		return decision.Decision{}, err
	}
	e.brk.success()
	return e.toDecision(resp, cands)
}

// call performs the request with retries. healthy reports whether the service
// answered at all (a 4xx other than auth errors counts as answered), which
// decides whether the breaker counts the call as a failure.
func (e *Engine) call(ctx context.Context, key string, body []byte) (resp response, healthy bool, err error) {
	backoff := e.cfg.Backoff
	var last error
	for attempt := 1; attempt <= e.cfg.MaxAttempts; attempt++ {
		var retry bool
		resp, retry, healthy, last = e.once(ctx, key, body)
		if last == nil {
			return resp, true, nil
		}
		if !retry || attempt == e.cfg.MaxAttempts {
			break
		}
		if err := e.cfg.Sleep(ctx, backoff); err != nil {
			return response{}, false, err
		}
		if backoff *= 2; backoff > e.cfg.MaxBackoff {
			backoff = e.cfg.MaxBackoff
		}
	}
	return response{}, healthy, last
}

// once makes one request. The returned errors never include the underlying
// transport error text, the response body or the key.
func (e *Engine) once(ctx context.Context, key string, body []byte) (resp response, retry, healthy bool, err error) {
	rctx, cancel := context.WithTimeout(ctx, e.cfg.Timeout)
	defer cancel()
	req, rerr := http.NewRequestWithContext(rctx, http.MethodPost, e.cfg.Endpoint, bytes.NewReader(body))
	if rerr != nil {
		return resp, false, false, fmt.Errorf("%w: bad request", ErrUnavailable)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)

	hr, derr := e.cfg.HTTPClient.Do(req)
	if derr != nil {
		kind := "network error"
		if errors.Is(derr, context.DeadlineExceeded) {
			kind = "timeout"
		}
		e.cfg.Logf("hosted: request failed: %s", kind)
		return resp, true, false, fmt.Errorf("%w: %s", ErrUnavailable, kind)
	}
	defer hr.Body.Close()

	switch code := hr.StatusCode; {
	case code >= 200 && code < 300:
		data, rerr := io.ReadAll(io.LimitReader(hr.Body, maxResponseBytes))
		if rerr != nil {
			e.cfg.Logf("hosted: reading response failed")
			return resp, true, false, fmt.Errorf("%w: truncated response", ErrUnavailable)
		}
		if jerr := json.Unmarshal(data, &resp); jerr != nil {
			// The service answered, but not in a shape we understand.
			e.cfg.Logf("hosted: unparseable response")
			return resp, false, true, fmt.Errorf("%w: unparseable response", decision.ErrNoDecision)
		}
		return resp, false, true, nil
	case code == http.StatusTooManyRequests || code >= 500:
		e.cfg.Logf("hosted: service returned HTTP %d", code)
		return resp, true, false, fmt.Errorf("%w: HTTP %d", ErrUnavailable, code)
	default: // 3xx (redirects are not followed) and 4xx: retrying will not help
		e.cfg.Logf("hosted: service returned HTTP %d", code)
		// 401/403 mean our key is wrong, so back off via the breaker; other
		// client errors say nothing about service health.
		authFail := code == http.StatusUnauthorized || code == http.StatusForbidden
		return resp, false, !authFail, fmt.Errorf("%w: HTTP %d", ErrUnavailable, code)
	}
}

// toDecision validates the service's reply against the candidates. The service
// can only pick an existing folder; it cannot invent a path.
func (e *Engine) toDecision(r response, cands []string) (decision.Decision, error) {
	choice, conf, ok := pick(r)
	if !ok {
		return decision.Decision{}, fmt.Errorf("%w: hosted gave no usable choice", decision.ErrNoDecision)
	}
	folder, ok := match(choice, cands)
	if !ok {
		return decision.Decision{}, fmt.Errorf("%w: hosted chose a folder that is not a candidate", decision.ErrNoDecision)
	}
	if conf > e.cfg.MaxConfidence {
		conf = e.cfg.MaxConfidence
	}
	return decision.Decision{
		Action:     decision.Move,
		Target:     folder,
		Confidence: conf,
		Reason:     "hosted model chose an existing folder",
		Source:     sourceName,
	}, nil
}

// pick extracts the chosen option and its confidence. With no explicit choice
// it takes the single most probable option; ties are not a choice.
func pick(r response) (choice string, conf float64, ok bool) {
	choice = strings.TrimSpace(r.Choice)
	if choice == "" {
		best, tie := math.Inf(-1), false
		for name, p := range r.Probabilities {
			if math.IsNaN(p) {
				return "", 0, false
			}
			switch {
			case p > best:
				best, choice, tie = p, name, false
			case p == best:
				tie = true
			}
		}
		if choice == "" || tie {
			return "", 0, false
		}
	}
	switch {
	case r.Confidence != nil:
		conf = *r.Confidence
	default:
		p, has := r.Probabilities[choice]
		if !has {
			return "", 0, false
		}
		conf = p
	}
	if math.IsNaN(conf) || conf < 0 || conf > 1 {
		return "", 0, false
	}
	return choice, conf, true
}

func match(name string, cands []string) (string, bool) {
	for _, c := range cands {
		if c == name {
			return c, true
		}
	}
	found, n := "", 0
	for _, c := range cands {
		if strings.EqualFold(c, name) {
			found, n = c, n+1
		}
	}
	return found, n == 1
}

func normalize(in []string, limit int) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, c := range in {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if _, dup := seen[c]; dup {
			continue
		}
		seen[c] = struct{}{}
		if out = append(out, c); len(out) == limit {
			break
		}
	}
	return out
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n]))
}
