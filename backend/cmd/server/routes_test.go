package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ameetmadan/arca/internal/modelmanager"
)

func testServer(t *testing.T) *server {
	t.Helper()
	return &server{app: healthyApp(), mm: modelmanager.New(t.TempDir())}
}

func TestV1Routes_ServeEnvelope(t *testing.T) {
	h := testServer(t).routes()

	rec := get(h, "/api/v1/models")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	env := decodeEnvelope(t, rec.Body.Bytes())
	if env.Error != nil {
		t.Fatalf("error = %q, want null", *env.Error)
	}
	assertTimestamp(t, env)
}

func TestV1Routes_ValidationErrorUsesEnvelope(t *testing.T) {
	h := testServer(t).routes()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/index", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	env := decodeEnvelope(t, rec.Body.Bytes())
	if env.Error == nil || *env.Error != "path required" {
		t.Fatalf("error = %v", env.Error)
	}
	if string(env.Data) != "null" {
		t.Fatalf("data = %s, want null", env.Data)
	}
	assertTimestamp(t, env)
}

func TestLegacyAPIAliasesStillWork(t *testing.T) {
	h := testServer(t).routes()

	rec := get(h, "/api/models")
	if rec.Code != http.StatusOK {
		t.Fatalf("legacy /api/models status = %d, want 200", rec.Code)
	}
	if rec.Header().Get("Deprecation") != "true" {
		t.Error("legacy alias should send Deprecation: true")
	}
	if link := rec.Header().Get("Link"); !strings.Contains(link, "/api/v1/models") || !strings.Contains(link, "successor-version") {
		t.Errorf("Link header = %q, want successor-version pointing at /api/v1/models", link)
	}
	env := decodeEnvelope(t, rec.Body.Bytes())
	if env.Error != nil {
		t.Fatalf("error = %q", *env.Error)
	}

	v1 := get(h, "/api/v1/models")
	if v1.Header().Get("Deprecation") != "" {
		t.Error("v1 routes must not be marked deprecated")
	}
}

func TestLegacyAndV1ShareOneRateLimitBucket(t *testing.T) {
	l, _ := newTestLimiter(1, 2)
	h := testServer(t).routesWith(newJSONLogger(&bytes.Buffer{}), l)

	if rec := get(h, "/api/models"); rec.Code != http.StatusOK {
		t.Fatalf("1st: %d", rec.Code)
	}
	if rec := get(h, "/api/v1/models"); rec.Code != http.StatusOK {
		t.Fatalf("2nd: %d", rec.Code)
	}
	rec := get(h, "/api/models")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("3rd: %d, want 429 (alias must not be a way around the limit)", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("Retry-After missing")
	}
}

func TestRateLimit_AppliesThroughFullStackAndKeepsRequestID(t *testing.T) {
	l, clk := newTestLimiter(1, 1)
	h := testServer(t).routesWith(newJSONLogger(&bytes.Buffer{}), l)

	get(h, "/api/v1/models")
	rec := get(h, "/api/v1/models")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if rec.Header().Get("X-Request-ID") == "" {
		t.Error("429 responses should still carry X-Request-ID")
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("429 responses should still carry security headers")
	}

	clk.advance(2 * time.Second)
	if rec := get(h, "/api/v1/models"); rec.Code != http.StatusOK {
		t.Fatalf("after Retry-After the request should succeed, got %d", rec.Code)
	}
}

func TestUnknownRouteAndWrongMethodUseEnvelope(t *testing.T) {
	h := testServer(t).routes()

	cases := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{"unknown root path", http.MethodGet, "/nope", http.StatusNotFound},
		{"unknown v1 path", http.MethodGet, "/api/v1/nope", http.StatusNotFound},
		{"wrong method", http.MethodDelete, "/api/v1/models", http.StatusMethodNotAllowed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
			if rec.Code != c.want {
				t.Fatalf("status = %d, want %d", rec.Code, c.want)
			}
			env := decodeEnvelope(t, rec.Body.Bytes())
			if env.Error == nil {
				t.Fatal("error should be set")
			}
			assertTimestamp(t, env)
		})
	}
}

func TestRequestIDPropagatesEndToEnd(t *testing.T) {
	var logs bytes.Buffer
	h := testServer(t).routesWith(newJSONLogger(&logs), newRateLimiter(100, 100))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/models", nil)
	req.Header.Set("X-Request-ID", "trace-me-1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Request-ID"); got != "trace-me-1" {
		t.Fatalf("response X-Request-ID = %q", got)
	}
	if !strings.Contains(logs.String(), `"request_id":"trace-me-1"`) {
		t.Fatalf("access log lacks request id: %s", logs.String())
	}
}

func TestChatQueryNeverReachesLogs(t *testing.T) {
	var logs bytes.Buffer
	h := testServer(t).routesWith(newJSONLogger(&logs), newRateLimiter(100, 100))

	get(h, "/api/v1/chat?q=what+is+in+my+tax+return")
	if strings.Contains(logs.String(), "tax") {
		t.Fatalf("chat question leaked into logs: %s", logs.String())
	}
}
