package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ameetmadan/arca/internal/config"
	"github.com/ameetmadan/arca/pkg/parser"
	"github.com/ameetmadan/arca/pkg/sqlengine"
	"github.com/ameetmadan/arca/pkg/vector"
)

type healthLLM struct{}

func (healthLLM) Generate(context.Context, string) (io.Reader, error) {
	return strings.NewReader(""), nil
}
func (healthLLM) GenerateSync(context.Context, string) (string, error) { return "", nil }

type healthEmbed struct{}

func (healthEmbed) Embed(context.Context, string) ([]float32, error) { return []float32{1}, nil }

type fakeStore struct{ pingErr error }

func (fakeStore) Upsert(context.Context, string, []float32, map[string]interface{}) error {
	return nil
}
func (fakeStore) Search(context.Context, []float32, int) ([]vector.SearchResult, error) {
	return nil, nil
}
func (fakeStore) Delete(context.Context, string) error { return nil }
func (f fakeStore) Ping(context.Context) error         { return f.pingErr }

type fakeSQL struct{}

func (fakeSQL) QueryNL(context.Context, string, string) (sqlengine.QueryResult, error) {
	return sqlengine.QueryResult{}, nil
}

type fakeParser struct{}

func (fakeParser) Parse(string) ([]parser.Chunk, error) { return nil, nil }

func healthyApp() *config.App {
	return &config.App{
		Cfg:           &config.File{},
		LLMProvider:   healthLLM{},
		EmbedProvider: healthEmbed{},
		VectorStore:   fakeStore{},
		SQLEngine:     fakeSQL{},
		Parser:        fakeParser{},
	}
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestHealth_AlwaysOKEvenWithoutProviders(t *testing.T) {
	// /health only says the process is up; it must not touch providers.
	s := &server{app: &config.App{}}
	rec := get(s.routes(), "/health")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	env := decodeEnvelope(t, rec.Body.Bytes())
	if env.Error != nil {
		t.Fatalf("error = %q, want null", *env.Error)
	}
	var data map[string]string
	if err := json.Unmarshal(env.Data, &data); err != nil || data["status"] != "ok" {
		t.Fatalf("data = %s", env.Data)
	}
	assertTimestamp(t, env)
}

func TestReady_AllChecksPass(t *testing.T) {
	s := &server{app: healthyApp()}
	rec := get(s.routes(), "/ready")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	env := decodeEnvelope(t, rec.Body.Bytes())
	if env.Error != nil {
		t.Fatalf("error = %q, want null", *env.Error)
	}
	var data struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.Status != "ready" {
		t.Fatalf("status = %q", data.Status)
	}
	for _, name := range []string{"llm", "embed", "vector", "sql", "parser", "database"} {
		if data.Checks[name] != "ok" {
			t.Errorf("checks[%s] = %q, want ok", name, data.Checks[name])
		}
	}
}

func TestReady_MissingProviderIsNotReady(t *testing.T) {
	app := healthyApp()
	app.EmbedProvider = nil
	s := &server{app: app}
	rec := get(s.routes(), "/ready")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	env := decodeEnvelope(t, rec.Body.Bytes())
	if env.Error == nil || *env.Error != "not ready" {
		t.Fatalf("error = %v, want \"not ready\"", env.Error)
	}
	var data struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.Status != "not_ready" || data.Checks["embed"] != "failed" || data.Checks["llm"] != "ok" {
		t.Fatalf("data = %+v", data)
	}
}

func TestReady_UnreachableDatabaseIsNotReady(t *testing.T) {
	app := healthyApp()
	app.VectorStore = fakeStore{pingErr: errors.New("open /Users/me/secret-path/db: unable to open")}
	s := &server{app: app}
	rec := get(s.routes(), "/ready")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "secret-path") {
		t.Fatalf("underlying error detail leaked to the client: %s", rec.Body)
	}
	env := decodeEnvelope(t, rec.Body.Bytes())
	var data struct {
		Checks map[string]string `json:"checks"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.Checks["database"] != "failed" {
		t.Fatalf("checks = %v", data.Checks)
	}
}

func TestReady_StoreWithoutPingerCountsAsOK(t *testing.T) {
	app := healthyApp()
	app.VectorStore = noPingStore{}
	s := &server{app: app}
	if rec := get(s.routes(), "/ready"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

type noPingStore struct{ fakeStore }

// Hide the embedded Ping so noPingStore does not satisfy vector.Pinger.
func (noPingStore) Ping() {}

func TestHealth_IsNotRateLimited(t *testing.T) {
	s := &server{app: healthyApp()}
	h := s.routes()
	for i := 0; i < defaultRateLimitBurst+20; i++ {
		if rec := get(h, "/health"); rec.Code != http.StatusOK {
			t.Fatalf("request %d: /health = %d", i, rec.Code)
		}
	}
}
