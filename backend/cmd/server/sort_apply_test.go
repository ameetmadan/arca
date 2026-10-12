package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ameetmadan/arca/internal/executor"
	"github.com/ameetmadan/arca/internal/sorter"
)

// applyJournal is an in-memory executor.Journal so these tests need no CGo.
type applyJournal struct {
	mu      sync.Mutex
	entries []executor.JournalEntry
}

func (j *applyJournal) Append(_ context.Context, e executor.JournalEntry) (executor.JournalEntry, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	e.ID = int64(len(j.entries) + 1)
	j.entries = append(j.entries, e)
	return e, nil
}

func (j *applyJournal) Get(_ context.Context, id int64) (executor.JournalEntry, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if id < 1 || int(id) > len(j.entries) {
		return executor.JournalEntry{}, executor.ErrJournalEntryNotFound
	}
	return j.entries[id-1], nil
}

func (j *applyJournal) MarkUndone(_ context.Context, id int64, at time.Time) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if id < 1 || int(id) > len(j.entries) {
		return executor.ErrJournalEntryNotFound
	}
	j.entries[id-1].UndoneAt = &at
	return nil
}

type applyResult struct {
	status  int
	applied int
	skipped int
	errors  []string
	hasErrs bool
	rawData map[string]any
	rawErr  *string
}

func postApply(t *testing.T, s *server, folder string, moves []sorter.SortMove) applyResult {
	t.Helper()
	body, err := json.Marshal(map[string]any{"folder": folder, "moves": moves})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sort/apply", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleSortApply(rec, req)

	var env struct {
		Data  map[string]any `json:"data"`
		Error *string        `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("bad envelope %q: %v", rec.Body.String(), err)
	}
	res := applyResult{status: rec.Code, rawData: env.Data, rawErr: env.Error}
	if v, ok := env.Data["applied"].(float64); ok {
		res.applied = int(v)
	}
	if v, ok := env.Data["skipped"].(float64); ok {
		res.skipped = int(v)
	}
	if list, ok := env.Data["errors"].([]any); ok {
		res.hasErrs = true
		for _, e := range list {
			res.errors = append(res.errors, e.(string))
		}
	}
	return res
}

func applyServer() *server {
	return &server{exec: executor.New(&applyJournal{})}
}

// applyDirs returns parent and a root folder inside it.
func applyDirs(t *testing.T) (parent, root string) {
	t.Helper()
	parent = t.TempDir()
	root = filepath.Join(parent, "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	return parent, root
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fileContent(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestSortApply_MovesFiles(t *testing.T) {
	_, root := applyDirs(t)
	writeFile(t, filepath.Join(root, "a.txt"), "A")

	res := postApply(t, applyServer(), root, []sorter.SortMove{{File: "a.txt", SuggestedFolder: "Docs"}})

	if res.status != http.StatusOK || res.applied != 1 || res.skipped != 0 || res.hasErrs {
		t.Fatalf("unexpected result: %+v", res)
	}
	if got := fileContent(t, filepath.Join(root, "Docs", "a.txt")); got != "A" {
		t.Fatalf("content = %q", got)
	}
}

func TestSortApply_ResponseShapeIsUnchanged(t *testing.T) {
	_, root := applyDirs(t)
	writeFile(t, filepath.Join(root, "a.txt"), "A")
	res := postApply(t, applyServer(), root, []sorter.SortMove{
		{File: "a.txt", SuggestedFolder: "Docs"},
		{File: "missing.txt", SuggestedFolder: "Docs"},
	})
	if res.rawErr != nil {
		t.Fatalf("error = %q, want null", *res.rawErr)
	}
	if _, ok := res.rawData["applied"]; !ok {
		t.Fatal("applied missing")
	}
	if _, ok := res.rawData["skipped"]; !ok {
		t.Fatal("skipped missing")
	}
	if _, ok := res.rawData["errors"]; ok {
		t.Fatal("errors must be omitted when there are none")
	}
	if res.applied != 1 || res.skipped != 1 {
		t.Fatalf("applied=%d skipped=%d, want 1 and 1", res.applied, res.skipped)
	}
}

// The tests below are regression tests for the path escape in /api/sort/apply:
// the old handler joined user-supplied paths without checking containment.

func TestSortApply_RejectsFileEscapingFolder(t *testing.T) {
	parent, root := applyDirs(t)
	writeFile(t, filepath.Join(parent, "outside.txt"), "outside")

	res := postApply(t, applyServer(), root, []sorter.SortMove{{File: "../outside.txt", SuggestedFolder: "Docs"}})

	if res.applied != 0 || !res.hasErrs {
		t.Fatalf("escape was not rejected: %+v", res)
	}
	if got := fileContent(t, filepath.Join(parent, "outside.txt")); got != "outside" {
		t.Fatal("file outside the folder was moved")
	}
	if _, err := os.Stat(filepath.Join(root, "Docs", "outside.txt")); err == nil {
		t.Fatal("outside file was pulled into the folder")
	}
}

func TestSortApply_RejectsDeepFileEscape(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "deep", "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(parent, "etc", "x"), "secret") // = root/../../etc/x

	res := postApply(t, applyServer(), root, []sorter.SortMove{{File: "../../etc/x", SuggestedFolder: "Docs"}})

	if res.applied != 0 || !res.hasErrs {
		t.Fatalf("escape was not rejected: %+v", res)
	}
	if got := fileContent(t, filepath.Join(parent, "etc", "x")); got != "secret" {
		t.Fatal("file outside the folder was touched")
	}
}

func TestSortApply_RejectsSuggestedFolderEscape(t *testing.T) {
	parent, root := applyDirs(t)
	writeFile(t, filepath.Join(root, "a.txt"), "A")

	res := postApply(t, applyServer(), root, []sorter.SortMove{{File: "a.txt", SuggestedFolder: "../escape"}})

	if res.applied != 0 || !res.hasErrs {
		t.Fatalf("escape was not rejected: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(parent, "escape")); err == nil {
		t.Fatal("directory created outside the folder")
	}
	if got := fileContent(t, filepath.Join(root, "a.txt")); got != "A" {
		t.Fatal("source file was moved")
	}
}

func TestSortApply_RejectsAbsoluteSuggestedFolder(t *testing.T) {
	parent, root := applyDirs(t)
	writeFile(t, filepath.Join(root, "a.txt"), "A")
	abs := filepath.Join(parent, "abs-target")

	res := postApply(t, applyServer(), root, []sorter.SortMove{{File: "a.txt", SuggestedFolder: abs}})

	if res.applied != 0 || !res.hasErrs {
		t.Fatalf("absolute target was not rejected: %+v", res)
	}
	if _, err := os.Stat(abs); err == nil {
		t.Fatal("directory created at the absolute path")
	}
	if got := fileContent(t, filepath.Join(root, "a.txt")); got != "A" {
		t.Fatal("source file was moved")
	}
}

func TestSortApply_RejectsSymlinkPointingOutsideFolder(t *testing.T) {
	parent, root := applyDirs(t)
	writeFile(t, filepath.Join(parent, "secret.txt"), "secret")
	if err := os.Symlink(filepath.Join(parent, "secret.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	if err := os.Mkdir(filepath.Join(parent, "outdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(parent, "outdir"), filepath.Join(root, "Docs")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "a.txt"), "A")

	res := postApply(t, applyServer(), root, []sorter.SortMove{
		{File: "link.txt", SuggestedFolder: "Other"},
		{File: "a.txt", SuggestedFolder: "Docs"}, // Docs is a symlink to outside
	})

	if res.applied != 0 || len(res.errors) != 2 {
		t.Fatalf("symlink escapes were not rejected: %+v", res)
	}
	if got := fileContent(t, filepath.Join(parent, "secret.txt")); got != "secret" {
		t.Fatal("file behind the symlink was moved")
	}
	if entries, _ := os.ReadDir(filepath.Join(parent, "outdir")); len(entries) != 0 {
		t.Fatal("a file was written outside the folder through a symlink")
	}
}

func TestSortApply_CollisionKeepsBothFiles(t *testing.T) {
	_, root := applyDirs(t)
	writeFile(t, filepath.Join(root, "x", "report.pdf"), "one")
	writeFile(t, filepath.Join(root, "y", "report.pdf"), "two")

	res := postApply(t, applyServer(), root, []sorter.SortMove{
		{File: "x/report.pdf", SuggestedFolder: "Reports"},
		{File: "y/report.pdf", SuggestedFolder: "Reports"},
	})

	if res.applied != 2 || res.hasErrs {
		t.Fatalf("unexpected result: %+v", res)
	}
	if got := fileContent(t, filepath.Join(root, "Reports", "report.pdf")); got != "one" {
		t.Fatalf("report.pdf = %q", got)
	}
	if got := fileContent(t, filepath.Join(root, "Reports", "report (1).pdf")); got != "two" {
		t.Fatalf("report (1).pdf = %q", got)
	}
}

func TestSortApply_RelativeFolderIsRejected(t *testing.T) {
	res := postApply(t, applyServer(), "relative/folder", []sorter.SortMove{{File: "a.txt", SuggestedFolder: "Docs"}})
	if res.status != http.StatusBadRequest || res.rawErr == nil {
		t.Fatalf("status = %d, want 400 with an error", res.status)
	}
}

func TestSortApply_WithoutExecutorIsUnavailable(t *testing.T) {
	_, root := applyDirs(t)
	writeFile(t, filepath.Join(root, "a.txt"), "A")
	res := postApply(t, &server{}, root, []sorter.SortMove{{File: "a.txt", SuggestedFolder: "Docs"}})
	if res.status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", res.status)
	}
	if got := fileContent(t, filepath.Join(root, "a.txt")); got != "A" {
		t.Fatal("file moved without an executor")
	}
}
