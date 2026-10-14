package sorter

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ameetmadan/arca/pkg/decision"
	"github.com/ameetmadan/arca/pkg/parser"
)

func twoClusterSorter(t *testing.T) (*Sorter, string) {
	t.Helper()
	dir := t.TempDir()
	mp := &mockParser{chunks: make(map[string][]parser.Chunk)}
	for name, content := range map[string]string{
		"q1-report.txt":   "quarterly sales report revenue",
		"q2-report.txt":   "report quarterly earnings summary",
		"invoice-jan.txt": "invoice payment due amount",
		"invoice-feb.txt": "invoice billing statement total",
	} {
		mp.chunks[writeFile(t, dir, name, content)] = []parser.Chunk{{Content: content}}
	}
	return newSorter(mp), dir
}

func TestSuggest_ProducesClusterDecisions(t *testing.T) {
	s, dir := twoClusterSorter(t)

	got, err := s.Suggest(context.Background(), dir)
	if err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("expected 4 suggestions, got %d", len(got))
	}
	byFile := map[string]Suggestion{}
	for _, sg := range got {
		byFile[sg.File] = sg
		d := sg.Decision
		if d.Action != decision.Move || d.Source != "cluster" {
			t.Errorf("%s: action=%q source=%q, want move/cluster", sg.File, d.Action, d.Source)
		}
		if d.Target == "" || filepath.IsAbs(d.Target) || strings.Contains(d.Target, "..") {
			t.Errorf("%s: target %q must be a plain root-relative folder", sg.File, d.Target)
		}
		if !strings.Contains(strings.ToLower(d.Reason), "cluster") || !strings.Contains(d.Reason, d.Target) {
			t.Errorf("%s: reason %q should mention the cluster and its name", sg.File, d.Reason)
		}
		if d.Confidence <= 0 || d.Confidence > ClusterMaxConfidence {
			t.Errorf("%s: confidence %v outside (0, %v]", sg.File, d.Confidence, ClusterMaxConfidence)
		}
	}
	if byFile["q1-report.txt"].Decision.Target != byFile["q2-report.txt"].Decision.Target {
		t.Error("report files should share a target")
	}
	if byFile["q1-report.txt"].Decision.Target == byFile["invoice-jan.txt"].Decision.Target {
		t.Error("reports and invoices should have different targets")
	}
}

func TestSuggest_SingleFileHasLowConfidence(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "report.txt", "quarterly sales data")
	mp := &mockParser{chunks: map[string][]parser.Chunk{path: {{Content: "quarterly sales data"}}}}

	got, err := newSorter(mp).Suggest(context.Background(), dir)
	if err != nil || len(got) != 1 {
		t.Fatalf("Suggest = %+v, %v", got, err)
	}
	d := got[0].Decision
	if d.Target != "report" || d.Source != "cluster" || d.Confidence >= 0.5 {
		t.Errorf("decision = %+v; a name-derived folder is a weak guess", d)
	}
}

func TestSuggest_EmptyFolder(t *testing.T) {
	got, err := newSorter(&mockParser{chunks: map[string][]parser.Chunk{}}).Suggest(context.Background(), t.TempDir())
	if err != nil || len(got) != 0 {
		t.Fatalf("Suggest = %+v, %v; want nothing", got, err)
	}
}

func TestSort_StillReturnsRawMoves(t *testing.T) {
	// Sort is what the current Swift client sees; it must keep its shape.
	s, dir := twoClusterSorter(t)
	moves, err := s.Sort(context.Background(), dir)
	if err != nil || len(moves) != 4 {
		t.Fatalf("Sort = %+v, %v", moves, err)
	}
	for _, m := range moves {
		if m.File == "" || m.SuggestedFolder == "" {
			t.Errorf("incomplete move %+v", m)
		}
	}
}

// Files already in a sub-folder count as sorted, and hidden files are never
// moved: only loose files directly in the folder are suggested.
func TestSuggest_OnlyLooseTopLevelFiles(t *testing.T) {
	s, dir := twoClusterSorter(t)
	mp := s.Parser.(*mockParser)
	if err := os.Mkdir(filepath.Join(dir, "Archive"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{filepath.Join("Archive", "old-invoice.txt"), ".hidden-invoice.txt"} {
		p := writeFile(t, dir, name, "invoice payment due amount")
		mp.chunks[p] = []parser.Chunk{{Content: "invoice payment due amount"}}
	}
	got, err := s.Suggest(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, sg := range got {
		if strings.Contains(sg.File, "Archive") || strings.HasPrefix(sg.File, ".") {
			t.Fatalf("suggested moving %s", sg.File)
		}
	}
	if len(got) != 4 {
		t.Fatalf("got %d suggestions, want 4", len(got))
	}
}
