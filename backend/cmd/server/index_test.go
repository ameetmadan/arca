package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ameetmadan/arca/internal/config"
	"github.com/ameetmadan/arca/pkg/parser"
	"github.com/ameetmadan/arca/pkg/vector"
)

type indexParser struct {
	chunks []parser.Chunk
	err    error
}

func (p indexParser) Parse(string) ([]parser.Chunk, error) { return p.chunks, p.err }

type indexEmbed struct{ err error }

func (e indexEmbed) Embed(context.Context, string) ([]float32, error) {
	if e.err != nil {
		return nil, e.err
	}
	return []float32{1}, nil
}

type indexStore struct {
	mu  sync.Mutex
	ids []string
	err error
}

func (s *indexStore) Upsert(_ context.Context, id string, _ []float32, _ map[string]interface{}) error {
	if s.err != nil {
		return s.err
	}
	s.mu.Lock()
	s.ids = append(s.ids, id)
	s.mu.Unlock()
	return nil
}
func (s *indexStore) Search(context.Context, []float32, int) ([]vector.SearchResult, error) {
	return nil, nil
}
func (s *indexStore) Delete(context.Context, string) error { return nil }

func postIndex(t *testing.T, s *server, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleIndex(rec, req)
	return rec
}

func indexServer(p indexParser, e indexEmbed, st *indexStore) *server {
	return &server{app: &config.App{Parser: p, EmbedProvider: e, VectorStore: st}}
}

func TestHandleIndex_IndexesChunksAndKeepsPathVerbatim(t *testing.T) {
	st := &indexStore{}
	s := indexServer(indexParser{chunks: []parser.Chunk{{Content: "a"}, {Content: "b"}}}, indexEmbed{}, st)

	path := "/tmp/we\"ird\nname.txt"
	body, _ := json.Marshal(map[string]string{"path": path})
	rec := postIndex(t, s, "/api/v1/index", string(body))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if len(st.ids) != 2 || st.ids[0] != path+"#0" || st.ids[1] != path+"#1" {
		t.Fatalf("ids = %q", st.ids)
	}
	if !strings.Contains(rec.Body.String(), `"indexed":2`) {
		t.Fatalf("body = %s", rec.Body)
	}
}

func TestHandleIndex_StatusPerFailingStage(t *testing.T) {
	boom := errors.New("boom")
	chunks := []parser.Chunk{{Content: "a"}}
	cases := []struct {
		name string
		s    *server
		want int
	}{
		{"parse", indexServer(indexParser{err: boom}, indexEmbed{}, &indexStore{}), http.StatusUnprocessableEntity},
		{"embed", indexServer(indexParser{chunks: chunks}, indexEmbed{err: boom}, &indexStore{}), http.StatusServiceUnavailable},
		{"upsert", indexServer(indexParser{chunks: chunks}, indexEmbed{}, &indexStore{err: boom}), http.StatusInternalServerError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := postIndex(t, c.s, "/api/v1/index", `{"path":"/a"}`)
			if rec.Code != c.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, c.want, rec.Body)
			}
		})
	}
}

func TestHandleIndex_RequiresPath(t *testing.T) {
	rec := postIndex(t, indexServer(indexParser{}, indexEmbed{}, &indexStore{}), "/api/v1/index", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d", rec.Code)
	}
}

// The legacy /api/index alias and /api/v1/index share one handler and so one
// indexing implementation.
func TestIndexRoutes_V1AndLegacyAliasBothIndex(t *testing.T) {
	for _, target := range []string{"/api/v1/index", "/api/index"} {
		st := &indexStore{}
		s := indexServer(indexParser{chunks: []parser.Chunk{{Content: "a"}}}, indexEmbed{}, st)
		req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(`{"path":"/doc.txt"}`))
		rec := httptest.NewRecorder()
		s.routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || len(st.ids) != 1 || st.ids[0] != "/doc.txt#0" {
			t.Errorf("%s: status %d ids %q body %s", target, rec.Code, st.ids, rec.Body)
		}
	}
}
