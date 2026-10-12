package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ameetmadan/arca/internal/decisions"
	"github.com/ameetmadan/arca/internal/pipeline"
	"github.com/ameetmadan/arca/internal/policy"
	"github.com/ameetmadan/arca/internal/sorter"
	"github.com/ameetmadan/arca/pkg/decision"
	"github.com/ameetmadan/arca/pkg/parser"
)

// sortParser returns a file's text as its only chunk.
type sortParser struct{}

func (sortParser) Parse(path string) ([]parser.Chunk, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return []parser.Chunk{{Content: string(b)}}, nil
}

// sortEmbed puts "report" texts and all others on two different axes.
type sortEmbed struct{}

func (sortEmbed) Embed(_ context.Context, text string) ([]float32, error) {
	if strings.Contains(strings.ToLower(text), "report") {
		return []float32{0.99, 0.14}, nil
	}
	return []float32{0.14, 0.99}, nil
}

type sortLLM struct{}

func (sortLLM) GenerateSync(_ context.Context, prompt string) (string, error) {
	if strings.Contains(strings.ToLower(prompt), "report") {
		return "Reports", nil
	}
	return "Invoices", nil
}
func (sortLLM) Generate(context.Context, string) (io.Reader, error) { return strings.NewReader(""), nil }

func newSortEnv(t *testing.T) *decEnv {
	t.Helper()
	e := newDecEnv(t)
	e.srv.sorter = &sorter.Sorter{LLM: sortLLM{}, Embed: sortEmbed{}, Parser: sortParser{}}
	e.srv.recent = pipeline.NewRecentMoves(0)
	for name, content := range map[string]string{
		"q1-report.txt":   "quarterly sales report revenue",
		"q2-report.txt":   "report quarterly earnings summary",
		"invoice-jan.txt": "invoice payment due amount",
		"invoice-feb.txt": "invoice billing statement total",
	} {
		writeFile(t, filepath.Join(e.root, name), content)
	}
	return e
}

func (e *decEnv) sortBody(submit bool) string {
	b := `{"folder":` + quote(e.root)
	if submit {
		b += `,"submit":true`
	}
	return b + `}`
}

func quote(s string) string { return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"` }

func TestSort_WithoutSubmitKeepsLegacyShapeAndQueuesNothing(t *testing.T) {
	e := newSortEnv(t)
	for _, path := range []string{"/api/sort", "/api/v1/sort"} {
		r := e.do(http.MethodPost, path, e.sortBody(false))
		r.wantStatus(t, http.StatusOK)
		moves, ok := r.data["moves"].([]any)
		if !ok || len(moves) != 4 {
			t.Fatalf("%s: moves = %v", path, r.data["moves"])
		}
		first := moves[0].(map[string]any)
		if first["file"] == "" || first["suggested_folder"] == "" {
			t.Fatalf("%s: move = %v, want file and suggested_folder", path, first)
		}
		if _, has := r.data["decisions"]; has {
			t.Fatalf("%s: decisions must only appear when asked for", path)
		}
	}
	if p, _ := e.svc.List(context.Background(), decisions.ListFilter{}); p.Total != 0 {
		t.Fatalf("queued %d decisions without submit", p.Total)
	}
}

func TestSort_SubmitQueuesClusterDecisions(t *testing.T) {
	e := newSortEnv(t)
	r := e.do(http.MethodPost, "/api/v1/sort", e.sortBody(true))
	r.wantStatus(t, http.StatusOK)

	if moves, _ := r.data["moves"].([]any); len(moves) != 4 {
		t.Fatalf("moves must still be returned for the current client: %v", r.data)
	}
	sum, _ := r.data["decisions"].(map[string]any)
	if sum == nil || num(t, sum["queued"]) != 4 || num(t, sum["applied"]) != 0 || num(t, sum["skipped"]) != 0 {
		t.Fatalf("decisions summary = %v", r.data["decisions"])
	}

	page, err := e.svc.List(context.Background(), decisions.ListFilter{Status: decisions.StatusPending, Root: e.root})
	if err != nil || page.Total != 4 {
		t.Fatalf("pending = %+v, %v", page, err)
	}
	for _, rec := range page.Items {
		if rec.Source != "cluster" || rec.Action != decision.Move || !strings.Contains(strings.ToLower(rec.Reason), "cluster") {
			t.Errorf("record = %+v, want a cluster move decision", rec)
		}
		if filepath.IsAbs(rec.Target) || filepath.IsAbs(rec.File) {
			t.Errorf("record paths must be root-relative: %+v", rec)
		}
		if !exists(filepath.Join(e.root, rec.File)) {
			t.Errorf("submitting must not move %s", rec.File)
		}
	}

	// Asking again must not pile duplicates onto the queue.
	r = e.do(http.MethodPost, "/api/v1/sort", e.sortBody(true))
	r.wantStatus(t, http.StatusOK)
	sum, _ = r.data["decisions"].(map[string]any)
	if num(t, sum["queued"]) != 0 || num(t, sum["skipped"]) != 4 {
		t.Fatalf("second submit summary = %v, want 4 skipped", sum)
	}
	if page, _ := e.svc.List(context.Background(), decisions.ListFilter{Status: decisions.StatusPending}); page.Total != 4 {
		t.Fatalf("pending = %d after resubmitting, want 4", page.Total)
	}
}

func TestSort_SubmitFollowsFolderPolicy(t *testing.T) {
	e := newSortEnv(t)
	// A trusted folder with a low threshold applies cluster decisions itself.
	for i := 0; i < policy.MinDecisions; i++ {
		if _, err := e.store.Insert(context.Background(), decisions.Record{Root: e.root, File: "x", Action: decision.Move, Status: decisions.StatusAccepted}); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.store.PutPolicy(context.Background(), e.root, policy.Settings{Mode: policy.ModeAuto, AutoThreshold: 0.1}); err != nil {
		t.Fatal(err)
	}

	r := e.do(http.MethodPost, "/api/v1/sort", e.sortBody(true))
	r.wantStatus(t, http.StatusOK)
	sum, _ := r.data["decisions"].(map[string]any)
	if num(t, sum["applied"]) != 4 || num(t, sum["queued"]) != 0 {
		t.Fatalf("summary = %v, want 4 applied", sum)
	}
	if exists(filepath.Join(e.root, "q1-report.txt")) {
		t.Fatal("auto mode should have moved the file through the Executor")
	}
}

func TestSort_SubmitErrors(t *testing.T) {
	e := newSortEnv(t)
	e.do(http.MethodPost, "/api/v1/sort", `{"folder":"relative/dir","submit":true}`).
		wantError(t, http.StatusBadRequest, "absolute")

	e.srv.decisions = nil
	e.do(http.MethodPost, "/api/v1/sort", e.sortBody(true)).
		wantError(t, http.StatusServiceUnavailable, "decision queue unavailable")
	// Plain sorting does not need the queue.
	e.do(http.MethodPost, "/api/v1/sort", e.sortBody(false)).wantStatus(t, http.StatusOK)
}

func TestSortApply_RemembersMovesSoTheWatcherIgnoresThem(t *testing.T) {
	e := newSortEnv(t)
	e.srv.exec = nil
	s := applyServer()
	s.recent = pipeline.NewRecentMoves(0)

	res := postApply(t, s, e.root, []sorter.SortMove{{File: "q1-report.txt", SuggestedFolder: "Reports"}})
	if res.applied != 1 {
		t.Fatalf("result = %+v", res)
	}
	if !s.recent.Contains(filepath.Join(e.root, "Reports", "q1-report.txt")) {
		t.Fatal("the moved file's new path must be remembered")
	}
}
