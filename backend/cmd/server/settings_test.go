package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ameetmadan/arca/internal/config"
	"github.com/ameetmadan/arca/internal/modelmanager"
	"github.com/ameetmadan/arca/pkg/sqlengine"
	"github.com/ameetmadan/arca/pkg/vector"
)

// settingsServer returns a server whose config file and models live in a
// temp dir, with the default config written.
func settingsServer(t *testing.T) (*server, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir) // the default config points into ~/Library
	cfgPath := filepath.Join(dir, "config.yaml")
	if _, err := config.EnsureFile(cfgPath); err != nil {
		t.Fatal(err)
	}
	app := healthyApp()
	app.ConfigPath = cfgPath
	return &server{app: app, mm: modelmanager.New(filepath.Join(dir, "models"))}, dir
}

func do(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	return rec
}

func TestSettingsFolders_SavesAndReportsRestart(t *testing.T) {
	s, _ := settingsServer(t)
	watched := t.TempDir()

	rec := do(s.routes(), http.MethodPut, "/api/v1/settings/folders",
		`{"folders": ["`+watched+`", "`+watched+`/"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var data struct {
		Folders []string `json:"watched_folders"`
		Restart bool     `json:"restart_required"`
	}
	if err := json.Unmarshal(decodeEnvelope(t, rec.Body.Bytes()).Data, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Folders) != 1 || data.Folders[0] != watched || !data.Restart {
		t.Fatalf("data = %+v", data)
	}

	// The saved config loads with the folder, i.e. it survives a restart.
	reloaded, err := config.Load(s.app.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	if got := reloaded.Cfg.Watcher.Folders; len(got) != 1 || got[0] != watched {
		t.Fatalf("reloaded folders = %v", got)
	}

	rec = get(s.routes(), "/api/v1/settings")
	var settings settingsJSON
	if err := json.Unmarshal(decodeEnvelope(t, rec.Body.Bytes()).Data, &settings); err != nil {
		t.Fatal(err)
	}
	if len(settings.WatchedFolders) != 1 || settings.ActiveModel != "" || settings.EmbeddingReady {
		t.Fatalf("settings = %+v", settings)
	}
}

func TestSettingsFolders_RejectsBadFolders(t *testing.T) {
	s, dataDir := settingsServer(t)
	file := filepath.Join(t.TempDir(), "f.txt")
	os.WriteFile(file, []byte("x"), 0o644)
	for name, body := range map[string]string{
		"no body":        `{}`,
		"relative":       `{"folders": ["docs"]}`,
		"missing":        `{"folders": ["/definitely/not/here"]}`,
		"file":           `{"folders": ["` + file + `"]}`,
		"root":           `{"folders": ["/"]}`,
		"arca data dir":  `{"folders": ["` + dataDir + `"]}`,
		"parent of data": `{"folders": ["` + filepath.Dir(dataDir) + `"]}`,
	} {
		rec := do(s.routes(), http.MethodPut, "/api/v1/settings/folders", body)
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d", name, rec.Code)
		}
	}
}

func TestModelActivate_RequiresDownloadedLLM(t *testing.T) {
	s, _ := settingsServer(t)
	for body, want := range map[string]int{
		`{"id": "nope"}`:        http.StatusNotFound,
		`{"id": "nomic-embed"}`: http.StatusBadRequest,
		`{"id": "llama32-3b"}`:  http.StatusConflict, // not downloaded
		`{}`:                    http.StatusBadRequest,
	} {
		if rec := do(s.routes(), http.MethodPost, "/api/v1/models/activate", body); rec.Code != want {
			t.Errorf("%s: status = %d, want %d", body, rec.Code, want)
		}
	}
}

func TestModelActivate_PersistsAndMarksActive(t *testing.T) {
	s, _ := settingsServer(t)
	for _, id := range []string{"qwen25-3b", modelmanager.EmbeddingModelID} {
		p := s.mm.ModelPath(id)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("gguf"), 0o644)
	}
	if rec := do(s.routes(), http.MethodPost, "/api/v1/models/activate", `{"id": "qwen25-3b"}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	got := s.settings()
	if got.ActiveModel != "qwen25-3b" || !got.EmbeddingReady {
		t.Fatalf("settings = %+v", got)
	}
	reloaded, err := config.Load(s.app.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	if reloaded.Cfg.LLM.ModelPath != s.mm.ModelPath("qwen25-3b") {
		t.Fatalf("persisted llm path = %q", reloaded.Cfg.LLM.ModelPath)
	}

	var models []struct {
		ID     string `json:"id"`
		Kind   string `json:"kind"`
		Active bool   `json:"active"`
	}
	json.Unmarshal(decodeEnvelope(t, get(s.routes(), "/api/v1/models").Body.Bytes()).Data, &models)
	for _, m := range models {
		if m.Active != (m.ID == "qwen25-3b") || m.Kind == "" {
			t.Fatalf("models = %+v", models)
		}
	}
}

// ── chat ─────────────────────────────────────────────────────────────────────

type searchResults []vector.SearchResult

func (searchResults) Upsert(context.Context, string, []float32, map[string]interface{}) error {
	return nil
}
func (r searchResults) Search(context.Context, []float32, int) ([]vector.SearchResult, error) {
	return r, nil
}
func (searchResults) Delete(context.Context, string) error { return nil }

type finiteLLM struct{ prompt *string }

func (f finiteLLM) Generate(_ context.Context, p string) (io.Reader, error) {
	*f.prompt = p
	return strings.NewReader("the answer"), nil
}
func (finiteLLM) GenerateSync(context.Context, string) (string, error) { return "", nil }

type failingEmbed struct{}

func (failingEmbed) Embed(context.Context, string) ([]float32, error) {
	return nil, errors.New("no model")
}

func sseEvents(t *testing.T, body string) []map[string]json.RawMessage {
	t.Helper()
	var out []map[string]json.RawMessage
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev map[string]json.RawMessage
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			t.Fatalf("bad event %q: %v", line, err)
		}
		out = append(out, ev)
	}
	return out
}

func TestChat_SendsSourcesThenTokensThenDone(t *testing.T) {
	var prompt string
	s := &server{app: &config.App{
		EmbedProvider: fakeEmbed{},
		VectorStore: searchResults{
			{Metadata: map[string]interface{}{"content": "Rent is 1200 per month.", "source": "/docs/lease.pdf", "page": 2}},
			{Metadata: map[string]interface{}{"content": "Deposit is two months.", "source": "/docs/lease.pdf", "page": 3}},
			{Metadata: map[string]interface{}{"no content": true}},
		},
		LLMProvider: finiteLLM{&prompt},
	}}
	rec := get(s.routes(), "/api/v1/chat?q=what+is+the+rent")
	evs := sseEvents(t, rec.Body.String())
	if len(evs) != 3 {
		t.Fatalf("events = %v", rec.Body.String())
	}
	if string(evs[0]["sources"]) != `[{"path":"/docs/lease.pdf","name":"lease.pdf"}]` {
		t.Fatalf("sources = %s", evs[0]["sources"])
	}
	if string(evs[1]["token"]) != `"the answer"` || string(evs[2]["done"]) != "true" {
		t.Fatalf("events = %v", evs)
	}
	if !strings.Contains(prompt, "[1] lease.pdf (page 2):\nRent is 1200") || !strings.Contains(prompt, "Question: what is the rent") {
		t.Fatalf("prompt = %s", prompt)
	}
}

func TestChat_NothingFoundAnswersWithoutTheLLM(t *testing.T) {
	var prompt string
	s := &server{app: &config.App{EmbedProvider: fakeEmbed{}, VectorStore: searchResults{}, LLMProvider: finiteLLM{&prompt}}}
	evs := sseEvents(t, get(s.routes(), "/api/v1/chat?q=x").Body.String())
	if len(evs) != 2 || !strings.Contains(string(evs[0]["token"]), "couldn't find") || prompt != "" {
		t.Fatalf("events = %v, prompt = %q", evs, prompt)
	}
}

func TestChat_MissingEmbeddingModelIsExplained(t *testing.T) {
	s := &server{app: &config.App{Cfg: &config.File{}, EmbedProvider: failingEmbed{}, VectorStore: searchResults{}}}
	evs := sseEvents(t, get(s.routes(), "/api/v1/chat?q=x").Body.String())
	if len(evs) != 1 || !strings.Contains(string(evs[0]["error"]), "not downloaded") {
		t.Fatalf("events = %v", evs)
	}
}

func TestBuildExcerptsRespectsBudget(t *testing.T) {
	big := strings.Repeat("x", 100)
	ex, src := buildExcerpts([]vector.SearchResult{
		{Metadata: map[string]interface{}{"content": big, "source": "/a"}},
		{Metadata: map[string]interface{}{"content": big, "source": "/b"}},
	}, 150)
	if len(ex) != 1 || len(src) != 1 {
		t.Fatalf("excerpts=%d sources=%v", len(ex), src)
	}
	ex, _ = buildExcerpts([]vector.SearchResult{{Metadata: map[string]interface{}{"content": big}}}, 10)
	if len(ex) != 1 || !strings.HasSuffix(ex[0], "\n"+strings.Repeat("x", 10)) {
		t.Fatalf("oversized first chunk not trimmed: %q", ex)
	}
}

// ── tables ───────────────────────────────────────────────────────────────────

type fakeTables struct {
	fakeSQL
	imported []string
	queryErr error
	result   sqlengine.QueryResult
}

func (f *fakeTables) QueryNL(context.Context, string, string) (sqlengine.QueryResult, error) {
	return f.result, f.queryErr
}
func (f *fakeTables) CanImport(p string) bool { return strings.HasSuffix(p, ".csv") }
func (f *fakeTables) ImportFile(_ context.Context, p string) ([]sqlengine.Table, error) {
	f.imported = append(f.imported, p)
	return []sqlengine.Table{{Name: "t", Source: p, Rows: 1}}, nil
}
func (f *fakeTables) Tables(context.Context) ([]sqlengine.Table, error) { return nil, nil }
func (f *fakeTables) DropTable(_ context.Context, n string) error {
	if n != "t" {
		return sqlengine.ErrUnknownTable
	}
	return nil
}

func TestTables_ImportListDrop(t *testing.T) {
	ft := &fakeTables{}
	app := healthyApp()
	app.SQLEngine = ft
	s := &server{app: app}
	csv := filepath.Join(t.TempDir(), "a.csv")
	os.WriteFile(csv, []byte("a\n1\n"), 0o644)

	if rec := do(s.routes(), http.MethodPost, "/api/v1/tables/import", `{"path": "`+csv+`"}`); rec.Code != http.StatusCreated {
		t.Fatalf("import: %d %s", rec.Code, rec.Body)
	}
	for body, want := range map[string]int{
		`{"path": "a.csv"}`:             http.StatusBadRequest,
		`{"path": "/x/a.pdf"}`:          http.StatusUnprocessableEntity,
		`{"path": "/missing/file.csv"}`: http.StatusUnprocessableEntity,
	} {
		if rec := do(s.routes(), http.MethodPost, "/api/v1/tables/import", body); rec.Code != want {
			t.Errorf("%s: %d, want %d", body, rec.Code, want)
		}
	}
	if len(ft.imported) != 1 {
		t.Fatalf("imported %v", ft.imported)
	}
	rec := get(s.routes(), "/api/v1/tables")
	if rec.Code != http.StatusOK || string(decodeEnvelope(t, rec.Body.Bytes()).Data) != "[]" {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	if rec := do(s.routes(), http.MethodDelete, "/api/v1/tables/t", ""); rec.Code != http.StatusOK {
		t.Fatalf("drop: %d", rec.Code)
	}
	if rec := do(s.routes(), http.MethodDelete, "/api/v1/tables/zzz", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("drop unknown: %d", rec.Code)
	}
}

func TestQuery_ErrorMapping(t *testing.T) {
	ft := &fakeTables{}
	app := healthyApp()
	app.SQLEngine = ft
	s := &server{app: app}

	ft.queryErr = sqlengine.ErrNoTables
	if rec := do(s.routes(), http.MethodPost, "/api/v1/query", `{"question": "q"}`); rec.Code != http.StatusConflict {
		t.Fatalf("no tables: %d", rec.Code)
	}
	ft.queryErr, ft.result = errors.New("refused"), sqlengine.QueryResult{SQL: "DROP TABLE t"}
	rec := do(s.routes(), http.MethodPost, "/api/v1/query", `{"question": "q"}`)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(*decodeEnvelope(t, rec.Body.Bytes()).Error, "DROP TABLE t") {
		t.Fatalf("refused: %d %s", rec.Code, rec.Body)
	}
	ft.queryErr, ft.result = nil, sqlengine.QueryResult{Columns: []string{"a"}, Rows: [][]interface{}{{1}}, SQL: "SELECT 1"}
	rec = do(s.routes(), http.MethodPost, "/api/v1/query", `{"question": "q"}`)
	if !strings.Contains(rec.Body.String(), `"columns":["a"]`) || !strings.Contains(rec.Body.String(), `"sql":"SELECT 1"`) {
		t.Fatalf("result not in snake_case JSON: %s", rec.Body)
	}
}

// ── exit with parent ─────────────────────────────────────────────────────────

func TestWatchParent_FiresWhenParentChanges(t *testing.T) {
	fired := make(chan struct{})
	go watchParent(context.Background(), os.Getppid()+1, time.Millisecond, func() { close(fired) })
	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("onExit not called for a changed parent")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		watchParent(ctx, os.Getppid(), time.Millisecond, func() { t.Error("fired for the live parent") })
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	<-done
}
