package sorter

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ameetmadan/arca/pkg/parser"
)

// --- mock implementations ---

type mockParser struct {
	chunks map[string][]parser.Chunk
}

func (m *mockParser) Parse(path string) ([]parser.Chunk, error) {
	if c, ok := m.chunks[path]; ok {
		return c, nil
	}
	return nil, os.ErrNotExist
}

type mockEmbed struct{}

// Embed returns [0.99,0.14] for text containing "report", [0.14,0.99] otherwise.
func (m *mockEmbed) Embed(_ context.Context, text string) ([]float32, error) {
	if strings.Contains(strings.ToLower(text), "report") {
		return []float32{0.99, 0.14}, nil
	}
	return []float32{0.14, 0.99}, nil
}

type mockLLM struct{}

// GenerateSync returns "Reports" if the prompt mentions "report", "Invoices" otherwise.
func (m *mockLLM) GenerateSync(_ context.Context, prompt string) (string, error) {
	if strings.Contains(strings.ToLower(prompt), "report") {
		return "Reports", nil
	}
	return "Invoices", nil
}

func (m *mockLLM) Generate(_ context.Context, _ string) (io.Reader, error) {
	return strings.NewReader(""), nil
}

// --- tests ---

func newSorter(mp *mockParser) *Sorter {
	return &Sorter{LLM: &mockLLM{}, Embed: &mockEmbed{}, Parser: mp}
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSort_EmptyFolder(t *testing.T) {
	s := newSorter(&mockParser{chunks: map[string][]parser.Chunk{}})
	moves, err := s.Sort(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(moves) != 0 {
		t.Errorf("expected 0 moves, got %d", len(moves))
	}
}

func TestSort_SingleFile(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "report.txt", "quarterly sales data")

	mp := &mockParser{chunks: map[string][]parser.Chunk{
		path: {{Content: "quarterly sales data"}},
	}}
	moves, err := newSorter(mp).Sort(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(moves) != 1 {
		t.Fatalf("expected 1 move, got %d", len(moves))
	}
	// Single file gets deriveFolder name (filename without extension).
	if moves[0].SuggestedFolder != "report" {
		t.Errorf("expected folder 'report', got %q", moves[0].SuggestedFolder)
	}
}

func TestSort_GroupsRelatedFiles(t *testing.T) {
	dir := t.TempDir()

	files := map[string]string{
		"q1-report.txt":   "quarterly sales report revenue",
		"q2-report.txt":   "report quarterly earnings summary",
		"invoice-jan.txt": "invoice payment due amount",
		"invoice-feb.txt": "invoice billing statement total",
	}
	mp := &mockParser{chunks: make(map[string][]parser.Chunk)}
	for name, content := range files {
		path := writeFile(t, dir, name, content)
		mp.chunks[path] = []parser.Chunk{{Content: content}}
	}

	moves, err := newSorter(mp).Sort(context.Background(), dir)
	if err != nil {
		t.Fatalf("Sort: %v", err)
	}
	if len(moves) != 4 {
		t.Fatalf("expected 4 moves, got %d", len(moves))
	}

	folders := make(map[string]string)
	for _, mv := range moves {
		folders[mv.File] = mv.SuggestedFolder
	}

	if folders["q1-report.txt"] != folders["q2-report.txt"] {
		t.Error("report files should share a folder")
	}
	if folders["invoice-jan.txt"] != folders["invoice-feb.txt"] {
		t.Error("invoice files should share a folder")
	}
	if folders["q1-report.txt"] == folders["invoice-jan.txt"] {
		t.Error("reports and invoices should be in different folders")
	}
}

func TestSort_LLMFailureFallsBackToGroupN(t *testing.T) {
	dir := t.TempDir()

	files := map[string]string{
		"a.txt": "alpha content",
		"b.txt": "beta content",
	}
	mp := &mockParser{chunks: make(map[string][]parser.Chunk)}
	for name, content := range files {
		path := writeFile(t, dir, name, content)
		mp.chunks[path] = []parser.Chunk{{Content: content}}
	}

	s := &Sorter{
		LLM:    &errLLM{},
		Embed:  &mockEmbed{},
		Parser: mp,
	}
	moves, err := s.Sort(context.Background(), dir)
	if err != nil {
		t.Fatalf("Sort should not error when LLM fails: %v", err)
	}
	for _, mv := range moves {
		if !strings.HasPrefix(mv.SuggestedFolder, "group-") {
			t.Errorf("expected group-N fallback, got %q", mv.SuggestedFolder)
		}
	}
}

type errLLM struct{}

func (e *errLLM) GenerateSync(_ context.Context, _ string) (string, error) {
	return "", os.ErrInvalid
}
func (e *errLLM) Generate(_ context.Context, _ string) (io.Reader, error) {
	return nil, os.ErrInvalid
}

func TestSort_EmbedFailureSkipsFile(t *testing.T) {
	dir := t.TempDir()

	// Three files: two succeed embedding, one fails.
	pathA := writeFile(t, dir, "a.txt", "report content")
	pathB := writeFile(t, dir, "b.txt", "report content two")
	pathC := writeFile(t, dir, "c.txt", "should fail embed")

	mp := &mockParser{chunks: map[string][]parser.Chunk{
		pathA: {{Content: "report content"}},
		pathB: {{Content: "report content two"}},
		pathC: {{Content: "should fail embed"}},
	}}

	s := &Sorter{
		LLM:    &mockLLM{},
		Embed:  &partialErrEmbed{failOn: "should fail embed"},
		Parser: mp,
	}

	moves, err := s.Sort(context.Background(), dir)
	if err != nil {
		t.Fatalf("Sort should not error on embed failure: %v", err)
	}
	// Only 2 files should have moves (c.txt was skipped).
	if len(moves) != 2 {
		t.Errorf("expected 2 moves, got %d", len(moves))
	}
	for _, mv := range moves {
		if mv.File == "c.txt" {
			t.Error("c.txt should have been skipped due to embed failure")
		}
	}
}

// partialErrEmbed fails for a specific text value, succeeds for others.
type partialErrEmbed struct {
	failOn string
}

func (p *partialErrEmbed) Embed(_ context.Context, text string) ([]float32, error) {
	if strings.Contains(text, p.failOn) {
		return nil, os.ErrInvalid
	}
	return []float32{0.99, 0.14}, nil
}
