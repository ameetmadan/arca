package hosted

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ameetmadan/arca/pkg/decision"
)

const (
	testKey     = "sk-test-SECRET-KEY-123"
	testSnippet = "Quarterly invoice for ACME Corp, total due 4,200 EUR"
)

type logSink struct {
	mu   sync.Mutex
	logs []string
}

func (s *logSink) logf(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = append(s.logs, fmt.Sprintf(format, args...))
}

func (s *logSink) all() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.logs, "\n")
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type harness struct {
	eng   *Engine
	hits  *atomic.Int32
	logs  *logSink
	clock *fakeClock
	sleep []time.Duration
}

func newHarness(t *testing.T, h http.HandlerFunc, mutate func(*Config)) *harness {
	t.Helper()
	hits := new(atomic.Int32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	hn := &harness{hits: hits, logs: &logSink{}, clock: &fakeClock{t: time.Unix(1000, 0)}}
	cfg := Config{
		Endpoint: srv.URL,
		Key:      StaticKey(testKey),
		Allow:    func(string) bool { return true },
		Candidates: func(context.Context, decision.Facts) ([]string, error) {
			return []string{"Invoices", "Photos", "Taxes"}, nil
		},
		Logf: hn.logs.logf,
		Now:  hn.clock.now,
		Sleep: func(_ context.Context, d time.Duration) error {
			hn.sleep = append(hn.sleep, d)
			return nil
		},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	eng, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	hn.eng = eng
	return hn
}

func facts() decision.Facts {
	return decision.Facts{Path: "/Users/me/Downloads/secret-name.pdf", RelPath: "secret-name.pdf", Ext: ".pdf", Snippet: testSnippet}
}

func okHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	}
}

func TestSuccess(t *testing.T) {
	var gotAuth string
	var gotReq request
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Error(err)
		}
		okHandler(`{"choice":"invoices","confidence":0.95,"probabilities":{"Invoices":0.95}}`)(w, r)
	}, nil)
	d, err := h.eng.Decide(context.Background(), facts())
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != decision.Move || d.Target != "Invoices" || d.Source != "hosted" {
		t.Fatalf("decision = %+v", d)
	}
	if d.Confidence != DefaultMaxConfidence {
		t.Fatalf("confidence %v should be capped at %v", d.Confidence, DefaultMaxConfidence)
	}
	if gotAuth != "Bearer "+testKey {
		t.Fatalf("authorization header = %q", gotAuth)
	}
	if gotReq.Input.Snippet != testSnippet || gotReq.Input.Ext != ".pdf" || len(gotReq.Options) != 3 {
		t.Fatalf("request = %+v", gotReq)
	}
}

func TestRequestSendsMinimumOnly(t *testing.T) {
	var raw string
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		raw = string(b)
		okHandler(`{"choice":"Photos","confidence":0.8}`)(w, r)
	}, func(c *Config) { c.SnippetLen = 10 })
	if _, err := h.eng.Decide(context.Background(), facts()); err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"secret-name", "/Users/me", "ACME Corp", testKey} {
		if strings.Contains(raw, leak) {
			t.Errorf("request body leaks %q: %s", leak, raw)
		}
	}
	if !strings.Contains(raw, "Quarterly") {
		t.Errorf("truncated snippet missing from %s", raw)
	}
}

func TestProbabilitiesOnlyResponse(t *testing.T) {
	h := newHarness(t, okHandler(`{"probabilities":{"Invoices":0.2,"Taxes":0.7,"Photos":0.1}}`), nil)
	d, err := h.eng.Decide(context.Background(), facts())
	if err != nil {
		t.Fatal(err)
	}
	if d.Target != "Taxes" || d.Confidence != 0.7 {
		t.Fatalf("decision = %+v", d)
	}
}

func TestUnusableAnswersAreNoDecision(t *testing.T) {
	for name, body := range map[string]string{
		"not a candidate": `{"choice":"../../etc","confidence":0.9}`,
		"garbage":         `not json`,
		"empty":           `{}`,
		"tie":             `{"probabilities":{"Invoices":0.5,"Taxes":0.5}}`,
		"bad confidence":  `{"choice":"Invoices","confidence":7}`,
		"no confidence":   `{"choice":"Invoices"}`,
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, okHandler(body), nil)
			_, err := h.eng.Decide(context.Background(), facts())
			if !errors.Is(err, decision.ErrNoDecision) {
				t.Fatalf("err = %v, want ErrNoDecision", err)
			}
			if h.hits.Load() != 1 {
				t.Fatalf("hits = %d; an unusable answer must not be retried", h.hits.Load())
			}
		})
	}
}

func TestClientErrorsAreNotRetried(t *testing.T) {
	for _, code := range []int{400, 401, 403, 404, 422} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(code)
				io.WriteString(w, "echo: "+testSnippet+" "+testKey)
			}, nil)
			_, err := h.eng.Decide(context.Background(), facts())
			if !errors.Is(err, ErrUnavailable) || !errors.Is(err, decision.ErrNoDecision) {
				t.Fatalf("err = %v", err)
			}
			if h.hits.Load() != 1 {
				t.Fatalf("hits = %d, want 1", h.hits.Load())
			}
			assertNoLeak(t, h, err)
		})
	}
}

func TestServerErrorsRetryWithBackoffThenSucceed(t *testing.T) {
	var n atomic.Int32
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		okHandler(`{"choice":"Taxes","confidence":0.8}`)(w, r)
	}, func(c *Config) { c.Backoff = 100 * time.Millisecond })
	d, err := h.eng.Decide(context.Background(), facts())
	if err != nil || d.Target != "Taxes" {
		t.Fatalf("d=%+v err=%v", d, err)
	}
	if h.hits.Load() != 3 {
		t.Fatalf("hits = %d, want 3", h.hits.Load())
	}
	want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond}
	if fmt.Sprint(h.sleep) != fmt.Sprint(want) {
		t.Fatalf("backoff = %v, want %v", h.sleep, want)
	}
}

func TestServerErrorsExhaustRetries(t *testing.T) {
	for _, code := range []int{500, 502, 429} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			h := newHarness(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }, nil)
			_, err := h.eng.Decide(context.Background(), facts())
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("err = %v", err)
			}
			if h.hits.Load() != int32(DefaultMaxAttempts) {
				t.Fatalf("hits = %d, want %d", h.hits.Load(), DefaultMaxAttempts)
			}
		})
	}
}

func TestTimeoutDegradesToNoDecision(t *testing.T) {
	release := make(chan struct{})
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}, func(c *Config) { c.Timeout = 20 * time.Millisecond; c.MaxAttempts = 2 })
	defer close(release)
	_, err := h.eng.Decide(context.Background(), facts())
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("err = %v", err)
	}
	if h.hits.Load() != 2 {
		t.Fatalf("hits = %d, want 2 (timeouts are retried)", h.hits.Load())
	}
}

func TestCallerCancellationIsReturnedAsIs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body) // lets the server notice the client hanging up
		cancel()
		<-r.Context().Done()
	}, nil)
	_, err := h.eng.Decide(ctx, facts())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestCircuitOpensAndRecovers(t *testing.T) {
	var healthy atomic.Bool
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if healthy.Load() {
			okHandler(`{"choice":"Taxes","confidence":0.8}`)(w, r)
			return
		}
		w.WriteHeader(500)
	}, func(c *Config) {
		c.MaxAttempts = 1
		c.BreakerThreshold = 3
		c.BreakerCooldown = time.Minute
	})
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := h.eng.Decide(ctx, facts()); !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrCircuitOpen) {
			t.Fatalf("call %d: err = %v", i, err)
		}
	}
	before := h.hits.Load()
	_, err := h.eng.Decide(ctx, facts())
	if !errors.Is(err, ErrCircuitOpen) || !errors.Is(err, decision.ErrNoDecision) {
		t.Fatalf("err = %v, want circuit open", err)
	}
	if h.hits.Load() != before {
		t.Fatal("an open circuit must not make a network call")
	}

	// Cooldown over, service still down: the probe fails and re-opens it.
	h.clock.advance(time.Minute + time.Second)
	if _, err := h.eng.Decide(ctx, facts()); errors.Is(err, ErrCircuitOpen) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("probe err = %v", err)
	}
	if h.hits.Load() != before+1 {
		t.Fatalf("hits = %d, want exactly one probe", h.hits.Load())
	}
	if _, err := h.eng.Decide(ctx, facts()); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("err = %v, want open again", err)
	}

	// Service back: next probe succeeds and closes the circuit.
	healthy.Store(true)
	h.clock.advance(2 * time.Minute)
	if d, err := h.eng.Decide(ctx, facts()); err != nil || d.Target != "Taxes" {
		t.Fatalf("d=%+v err=%v", d, err)
	}
	if _, err := h.eng.Decide(ctx, facts()); err != nil {
		t.Fatalf("closed circuit should pass: %v", err)
	}
}

func TestNoNetworkWhenFolderNotOptedIn(t *testing.T) {
	h := newHarness(t, okHandler(`{"choice":"Taxes","confidence":0.8}`), func(c *Config) {
		c.Allow = func(string) bool { return false }
		c.Key = keyFunc(func(context.Context) (string, error) {
			t.Error("the key must not even be read when the folder has not opted in")
			return testKey, nil
		})
	})
	_, err := h.eng.Decide(context.Background(), facts())
	if !errors.Is(err, decision.ErrNoDecision) {
		t.Fatalf("err = %v", err)
	}
	if h.hits.Load() != 0 {
		t.Fatalf("made %d network calls while disabled", h.hits.Load())
	}
}

func TestNothingToSendMakesNoCall(t *testing.T) {
	h := newHarness(t, okHandler(`{}`), nil)
	f := facts()
	f.Snippet = "  \n "
	if _, err := h.eng.Decide(context.Background(), f); !errors.Is(err, decision.ErrNoDecision) {
		t.Fatalf("err = %v", err)
	}
	if h.hits.Load() != 0 {
		t.Fatal("network call with an empty snippet")
	}
}

func TestMissingKeyDegradesWithoutCall(t *testing.T) {
	h := newHarness(t, okHandler(`{}`), func(c *Config) { c.Key = &Keychain{goos: "linux"} })
	for i := 0; i < 2; i++ {
		_, err := h.eng.Decide(context.Background(), facts())
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("err = %v", err)
		}
	}
	if h.hits.Load() != 0 {
		t.Fatal("network call without a key")
	}
	if n := strings.Count(h.logs.all(), "no API key"); n != 1 {
		t.Fatalf("no-key warning logged %d times, want once", n)
	}
}

func TestNoContentOrKeyInLogsOrErrors(t *testing.T) {
	for _, handler := range []http.HandlerFunc{
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) },
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(401)
			io.WriteString(w, testSnippet+testKey)
		},
		okHandler("garbage " + testSnippet + testKey),
		okHandler(`{"choice":"` + testSnippet + `","confidence":0.9}`),
	} {
		h := newHarness(t, handler, func(c *Config) { c.MaxAttempts = 2 })
		_, err := h.eng.Decide(context.Background(), facts())
		assertNoLeak(t, h, err)
	}
	// Unreachable server: the transport error normally contains the URL.
	h := newHarness(t, okHandler(`{}`), func(c *Config) { c.Endpoint = "http://127.0.0.1:1/never"; c.MaxAttempts = 1 })
	_, err := h.eng.Decide(context.Background(), facts())
	assertNoLeak(t, h, err)
	if strings.Contains(h.logs.all(), "127.0.0.1") {
		t.Errorf("log contains the transport error: %s", h.logs.all())
	}
}

func assertNoLeak(t *testing.T, h *harness, err error) {
	t.Helper()
	text := h.logs.all()
	if err != nil {
		text += "\n" + err.Error()
	}
	for _, s := range []string{testKey, "ACME", "invoice", "secret-name", "/Users/me"} {
		if strings.Contains(text, s) {
			t.Errorf("logs/errors contain %q:\n%s", s, text)
		}
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var other atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		other.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Error("key was forwarded to the redirect target")
		}
	}))
	defer target.Close()
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}, nil)
	_, err := h.eng.Decide(context.Background(), facts())
	if !errors.Is(err, ErrUnavailable) || other.Load() != 0 {
		t.Fatalf("err=%v redirected hits=%d", err, other.Load())
	}
}

func TestNewValidation(t *testing.T) {
	ok := Config{
		Endpoint:   "https://example.com/v1",
		Key:        StaticKey("k"),
		Allow:      func(string) bool { return true },
		Candidates: func(context.Context, decision.Facts) ([]string, error) { return nil, nil },
	}
	if _, err := New(ok); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*Config){
		"no key":       func(c *Config) { c.Key = nil },
		"no allow":     func(c *Config) { c.Allow = nil },
		"no cands":     func(c *Config) { c.Candidates = nil },
		"plain http":   func(c *Config) { c.Endpoint = "http://example.com" },
		"no host":      func(c *Config) { c.Endpoint = "https://" },
		"credentials":  func(c *Config) { c.Endpoint = "https://u:p@example.com" },
		"bad scheme":   func(c *Config) { c.Endpoint = "file:///etc/passwd" },
		"conf > 1":     func(c *Config) { c.MaxConfidence = 1.5 },
		"negative cap": func(c *Config) { c.SnippetLen = -1 },
	} {
		c := ok
		mut(&c)
		if _, err := New(c); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestKeychainReadsViaSecurityTool(t *testing.T) {
	var calls int
	k := &Keychain{goos: "darwin", run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls++
		got := name + " " + strings.Join(args, " ")
		want := "/usr/bin/security find-generic-password -s " + KeychainService + " -a " + KeychainAccount + " -w"
		if got != want {
			t.Errorf("command = %q", got)
		}
		return []byte(testKey + "\n"), nil
	}}
	for i := 0; i < 2; i++ {
		key, err := k.APIKey(context.Background())
		if err != nil || key != testKey {
			t.Fatalf("key=%q err=%v", key, err)
		}
	}
	if calls != 1 {
		t.Fatalf("security ran %d times, want 1 (cached)", calls)
	}

	missing := &Keychain{goos: "darwin", run: func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("exit status 44: " + testKey)
	}}
	if _, err := missing.APIKey(context.Background()); !errors.Is(err, ErrNoKey) || strings.Contains(err.Error(), testKey) {
		t.Fatalf("err = %v", err)
	}
}

type keyFunc func(context.Context) (string, error)

func (f keyFunc) APIKey(ctx context.Context) (string, error) { return f(ctx) }
