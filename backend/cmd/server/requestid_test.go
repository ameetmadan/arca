package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestID_GeneratedWhenAbsent(t *testing.T) {
	var seen string
	h := requestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = requestIDFrom(r.Context())
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	got := rec.Header().Get("X-Request-ID")
	if got == "" {
		t.Fatal("X-Request-ID response header missing")
	}
	if seen != got {
		t.Fatalf("context id %q != response header %q", seen, got)
	}
}

func TestRequestID_GeneratedIDsAreUnique(t *testing.T) {
	h := requestIDMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	a, b := httptest.NewRecorder(), httptest.NewRecorder()
	h.ServeHTTP(a, httptest.NewRequest(http.MethodGet, "/", nil))
	h.ServeHTTP(b, httptest.NewRequest(http.MethodGet, "/", nil))
	if a.Header().Get("X-Request-ID") == b.Header().Get("X-Request-ID") {
		t.Fatal("two requests got the same generated id")
	}
}

func TestRequestID_IncomingIDIsPropagated(t *testing.T) {
	var seen string
	h := requestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = requestIDFrom(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("X-Request-ID", "client-abc_123.4")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Request-ID"); got != "client-abc_123.4" {
		t.Fatalf("response id = %q, want the client's id", got)
	}
	if seen != "client-abc_123.4" {
		t.Fatalf("context id = %q", seen)
	}
}

func TestRequestID_UnsafeIncomingIDIsReplaced(t *testing.T) {
	cases := map[string]string{
		"newline":  "abc\nINJECTED",
		"space":    "has space",
		"too long": strings.Repeat("a", 65),
		"quote":    `a"b`,
	}
	h := requestIDMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			req.Header["X-Request-Id"] = []string{bad}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			got := rec.Header().Get("X-Request-ID")
			if got == "" || got == bad {
				t.Fatalf("unsafe id %q was not replaced (got %q)", bad, got)
			}
		})
	}
}

func TestAccessLog_IncludesRequestIDAndOmitsQuery(t *testing.T) {
	var buf bytes.Buffer
	logger := newJSONLogger(&buf)

	h := requestIDMiddleware(accessLog(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/chat?q=my-secret-question", nil)
	req.Header.Set("X-Request-ID", "req-42")
	req.Header.Set("Authorization", "Bearer super-secret-token")
	h.ServeHTTP(httptest.NewRecorder(), req)

	out := buf.String()
	for _, secret := range []string{"my-secret-question", "super-secret-token"} {
		if strings.Contains(out, secret) {
			t.Fatalf("log leaked %q: %s", secret, out)
		}
	}

	var entry map[string]interface{}
	if err := json.Unmarshal(bytes.TrimSpace([]byte(out)), &entry); err != nil {
		t.Fatalf("log line is not JSON: %v: %s", err, out)
	}
	if entry["request_id"] != "req-42" {
		t.Fatalf("request_id = %v", entry["request_id"])
	}
	if entry["method"] != "GET" || entry["path"] != "/api/v1/chat" {
		t.Fatalf("method/path = %v %v", entry["method"], entry["path"])
	}
	if entry["status"] != float64(http.StatusTeapot) {
		t.Fatalf("status = %v", entry["status"])
	}
}

func TestRecoverer_ReturnsEnvelope500WithoutLeakingPanicValue(t *testing.T) {
	var buf bytes.Buffer
	logger := newJSONLogger(&buf)

	h := requestIDMiddleware(recoverer(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("contains-file-contents")
	})))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	env := decodeEnvelope(t, rec.Body.Bytes())
	if env.Error == nil {
		t.Fatal("error should be set")
	}
	if strings.Contains(rec.Body.String(), "contains-file-contents") || strings.Contains(buf.String(), "contains-file-contents") {
		t.Fatal("panic value leaked into response or log")
	}
	if !strings.Contains(buf.String(), rec.Header().Get("X-Request-ID")) {
		t.Fatal("panic log line should carry the request id")
	}
}
