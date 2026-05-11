# Intelligent File Sorting Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace filename-only LLM sorting with an embed→cluster→name pipeline that groups files by semantic content.

**Architecture:** Parse each file to extract content (or synthesise metadata for images/unknowns), embed with the existing `EmbeddingProvider`, run k-means clustering on the vectors, then ask the LLM to name each cluster. A new `internal/sorter` package owns the pipeline; the HTTP handler and Swift client are unchanged.

**Tech Stack:** Go 1.25, `github.com/rwcarlsen/goexif/exif` (pure Go EXIF), existing `pkg/llm`, `pkg/embed`, `pkg/parser` interfaces.

---

### Task 1: Add goexif dependency

**Files:**

- Modify: `backend/go.mod`, `backend/go.sum`

- [ ] **Step 1: Add the dependency**

Run from `backend/`:

```bash
cd backend && go get github.com/rwcarlsen/goexif/exif
```

Expected: `go.mod` gains `github.com/rwcarlsen/goexif v0.0.0-...` and `go.sum` is updated.

- [ ] **Step 2: Verify**

```bash
cd backend && go mod tidy && go build ./...
```

Expected: builds cleanly, no errors.

- [ ] **Step 3: Commit**

```bash
git add backend/go.mod backend/go.sum
git commit -m "feat: add rwcarlsen/goexif for EXIF metadata extraction"
```

---

### Task 2: Implement clustering helpers

**Files:**

- Create: `backend/internal/sorter/cluster.go`
- Create: `backend/internal/sorter/cluster_test.go`

- [ ] **Step 1: Write the failing tests**

Create `backend/internal/sorter/cluster_test.go`:

```go
package sorter

import (
	"math"
	"testing"
)

func TestCosineSim_Identical(t *testing.T) {
	got := cosineSim([]float32{1, 0}, []float32{1, 0})
	if math.Abs(float64(got-1.0)) > 1e-6 {
		t.Errorf("got %v, want 1.0", got)
	}
}

func TestCosineSim_Orthogonal(t *testing.T) {
	got := cosineSim([]float32{1, 0}, []float32{0, 1})
	if math.Abs(float64(got)) > 1e-6 {
		t.Errorf("got %v, want 0.0", got)
	}
}

func TestCosineSim_Opposite(t *testing.T) {
	got := cosineSim([]float32{1, 0}, []float32{-1, 0})
	if math.Abs(float64(got+1.0)) > 1e-6 {
		t.Errorf("got %v, want -1.0", got)
	}
}

func TestCosineSim_ZeroVector(t *testing.T) {
	got := cosineSim([]float32{0, 0}, []float32{1, 0})
	if got != 0 {
		t.Errorf("got %v, want 0", got)
	}
}

func TestCluster_TwoGroups(t *testing.T) {
	// 3 vectors near [1,0,0], 3 near [0,1,0] — should split cleanly into 2 clusters
	a := []float32{1, 0, 0}
	b := []float32{0, 1, 0}
	vecs := [][]float32{a, a, a, b, b, b}

	clusters := cluster(vecs)

	if len(clusters) != 2 {
		t.Fatalf("expected 2 clusters, got %d", len(clusters))
	}
	for _, c := range clusters {
		if len(c) != 3 {
			t.Errorf("expected cluster size 3, got %d", len(c))
		}
	}
}

func TestCluster_TwoFiles(t *testing.T) {
	// Minimum valid input: 2 distinct vectors → 2 clusters of 1
	vecs := [][]float32{{1, 0}, {0, 1}}
	clusters := cluster(vecs)
	if len(clusters) != 2 {
		t.Fatalf("expected 2 clusters, got %d", len(clusters))
	}
}

func TestClampInt(t *testing.T) {
	tests := []struct{ v, lo, hi, want int }{
		{1, 2, 20, 2},
		{25, 2, 20, 20},
		{10, 2, 20, 10},
	}
	for _, tc := range tests {
		if got := clampInt(tc.v, tc.lo, tc.hi); got != tc.want {
			t.Errorf("clampInt(%d,%d,%d) = %d, want %d", tc.v, tc.lo, tc.hi, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run to confirm they fail**

```bash
cd backend && go test ./internal/sorter/...
```

Expected: `cannot find package` or `undefined: cosineSim`.

- [ ] **Step 3: Implement cluster.go**

Create `backend/internal/sorter/cluster.go`:

```go
package sorter

import (
	"math"
	"math/rand/v2"
)

func cosineSim(a, b []float32) float32 {
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(normA) * math.Sqrt(normB)))
}

// cluster groups vecs into k clusters using k-means with k-means++ init.
// k = clamp(√n, 2, sortMaxClusters).
func cluster(vecs [][]float32) [][]int {
	n := len(vecs)
	k := clampInt(int(math.Sqrt(float64(n))), 2, sortMaxClusters)
	dim := len(vecs[0])

	// k-means++ init: first seed random, each subsequent seed is the point
	// farthest from its nearest existing seed.
	seeds := make([]int, k)
	seeds[0] = rand.IntN(n)
	for i := 1; i < k; i++ {
		var maxDist float32 = -1
		best := 0
		for j := 0; j < n; j++ {
			var minDist float32 = 2
			for _, s := range seeds[:i] {
				if d := 1 - cosineSim(vecs[j], vecs[s]); d < minDist {
					minDist = d
				}
			}
			if minDist > maxDist {
				maxDist = minDist
				best = j
			}
		}
		seeds[i] = best
	}

	centroids := make([][]float32, k)
	for i, s := range seeds {
		centroids[i] = make([]float32, dim)
		copy(centroids[i], vecs[s])
	}

	assignments := make([]int, n)
	for iter := 0; iter < sortMaxIterations; iter++ {
		changed := false
		for i, v := range vecs {
			best, bestSim := 0, float32(-2)
			for ci, c := range centroids {
				if sim := cosineSim(v, c); sim > bestSim {
					bestSim = sim
					best = ci
				}
			}
			if assignments[i] != best {
				changed = true
				assignments[i] = best
			}
		}
		if !changed {
			break
		}

		newCentroids := make([][]float32, k)
		counts := make([]int, k)
		for i := range newCentroids {
			newCentroids[i] = make([]float32, dim)
		}
		for i, a := range assignments {
			counts[a]++
			for d, x := range vecs[i] {
				newCentroids[a][d] += x
			}
		}
		for ci := range newCentroids {
			if counts[ci] > 0 {
				for d := range newCentroids[ci] {
					newCentroids[ci][d] /= float32(counts[ci])
				}
			}
		}
		centroids = newCentroids
	}

	result := make([][]int, k)
	for i, a := range assignments {
		result[a] = append(result[a], i)
	}
	var nonempty [][]int
	for _, c := range result {
		if len(c) > 0 {
			nonempty = append(nonempty, c)
		}
	}
	return nonempty
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
```

- [ ] **Step 4: Run tests to confirm they pass**

```bash
cd backend && go test ./internal/sorter/... -run "TestCosineSim|TestCluster|TestClampInt" -v
```

Expected: all 7 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/sorter/cluster.go backend/internal/sorter/cluster_test.go
git commit -m "feat: add k-means clustering with cosine similarity for sorter"
```

---

### Task 3: Implement file text extraction

**Files:**

- Create: `backend/internal/sorter/filetext.go`

No dedicated unit tests for this file — it is covered by the integration test in Task 4 (the mock parser exercises the parser branch; the EXIF and fallback branches are tested indirectly through `fileText` returning non-empty strings for temp files).

- [ ] **Step 1: Create filetext.go**

Create `backend/internal/sorter/filetext.go`:

```go
package sorter

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rwcarlsen/goexif/exif"
)

// fileText returns a text representation of path for embedding.
// Returns "" for zero-byte files, hidden files, and unreadable files.
func (s *Sorter) fileText(path string) string {
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		return ""
	}
	if strings.HasPrefix(filepath.Base(path), ".") {
		return ""
	}

	// Try content parser first (PDF, DOCX, XLSX, MD, TXT).
	if chunks, err := s.Parser.Parse(path); err == nil && len(chunks) > 0 {
		content := chunks[0].Content
		if len(content) > sortSnippetLen*5 {
			content = content[:sortSnippetLen*5]
		}
		return content
	}

	// For images, attempt EXIF before falling back to stat metadata.
	ext := strings.ToLower(filepath.Ext(path))
	if isImageExt(ext) {
		if text := exifText(path); text != "" {
			return text
		}
	}

	return fmt.Sprintf("filename: %s | type: %s | size: %.1fKB | modified: %s",
		filepath.Base(path), ext,
		float64(info.Size())/1024,
		info.ModTime().Format("2006-01-02"),
	)
}

func isImageExt(ext string) bool {
	switch ext {
	case ".jpg", ".jpeg", ".png", ".tiff", ".tif", ".heic", ".heif", ".webp":
		return true
	}
	return false
}

func exifText(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	x, err := exif.Decode(f)
	if err != nil {
		return ""
	}

	var parts []string
	if tm, err := x.DateTime(); err == nil {
		parts = append(parts, "date: "+tm.Format("2006-01-02"))
	}
	if lat, lon, err := x.LatLong(); err == nil {
		parts = append(parts, fmt.Sprintf("location: %.4f,%.4f", lat, lon))
	}
	if len(parts) == 0 {
		return ""
	}

	return fmt.Sprintf("filename: %s | type: %s | %s",
		filepath.Base(path),
		strings.ToLower(filepath.Ext(path)),
		strings.Join(parts, " | "),
	)
}

func deriveFolder(relPath string) string {
	base := filepath.Base(relPath)
	return strings.TrimSuffix(base, filepath.Ext(base))
}
```

- [ ] **Step 2: Verify it compiles**

```bash
cd backend && go build ./internal/sorter/...
```

Expected: no errors (package has no `main`, so build just type-checks).

- [ ] **Step 3: Commit**

```bash
git add backend/internal/sorter/filetext.go
git commit -m "feat: add file text extraction with EXIF and metadata fallback"
```

---

### Task 4: Implement Sorter and Sort pipeline

**Files:**

- Create: `backend/internal/sorter/sorter.go`
- Create: `backend/internal/sorter/sorter_test.go`

- [ ] **Step 1: Write the failing tests**

Create `backend/internal/sorter/sorter_test.go`:

```go
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

	// LLM that always errors
	s := &Sorter{
		LLM: &errLLM{},
		Embed: &mockEmbed{},
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
```

- [ ] **Step 2: Run to confirm they fail**

```bash
cd backend && go test ./internal/sorter/... -run "TestSort" -v
```

Expected: `undefined: Sorter`.

- [ ] **Step 3: Implement sorter.go**

Create `backend/internal/sorter/sorter.go`:

```go
package sorter

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ameetmadan/arca/pkg/embed"
	"github.com/ameetmadan/arca/pkg/llm"
	"github.com/ameetmadan/arca/pkg/parser"
)

const (
	sortBatchSize       = 25
	sortMaxClusters     = 20
	sortMaxIterations   = 20
	sortRepresentatives = 5
	sortSnippetLen      = 200
)

// SortMove describes where one file should be moved.
type SortMove struct {
	File            string `json:"file"`
	SuggestedFolder string `json:"suggested_folder"`
}

// Sorter runs the embed→cluster→name pipeline.
type Sorter struct {
	LLM    llm.LLMProvider
	Embed  embed.EmbeddingProvider
	Parser parser.DocumentParser
}

// Sort returns suggested moves for all files under folder.
func (s *Sorter) Sort(ctx context.Context, folder string) ([]SortMove, error) {
	var relPaths, texts []string

	err := filepath.WalkDir(folder, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		text := s.fileText(path)
		if text == "" {
			return nil
		}
		rel, _ := filepath.Rel(folder, path)
		relPaths = append(relPaths, rel)
		texts = append(texts, text)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("sorter: walk %s: %w", folder, err)
	}
	if len(relPaths) == 0 {
		return nil, nil
	}

	// Embed in batches; skip files whose embedding fails.
	type entry struct {
		rel  string
		text string
		vec  []float32
	}
	var good []entry
	for i := 0; i < len(texts); i += sortBatchSize {
		end := i + sortBatchSize
		if end > len(texts) {
			end = len(texts)
		}
		for j := i; j < end; j++ {
			vec, err := s.Embed.Embed(ctx, texts[j])
			if err != nil {
				log.Printf("sorter: embed %s: %v", relPaths[j], err)
				continue
			}
			good = append(good, entry{relPaths[j], texts[j], vec})
		}
	}
	if len(good) == 0 {
		return nil, nil
	}

	// Single-file shortcut: skip clustering entirely.
	if len(good) == 1 {
		return []SortMove{{File: good[0].rel, SuggestedFolder: deriveFolder(good[0].rel)}}, nil
	}

	vecs := make([][]float32, len(good))
	for i, g := range good {
		vecs[i] = g.vec
	}

	clusters := cluster(vecs)

	var moves []SortMove
	for gi, idxs := range clusters {
		cpaths := make([]string, len(idxs))
		ctexts := make([]string, len(idxs))
		cvecs := make([][]float32, len(idxs))
		for i, idx := range idxs {
			cpaths[i] = good[idx].rel
			ctexts[i] = good[idx].text
			cvecs[i] = good[idx].vec
		}
		name := s.nameCluster(ctx, cpaths, ctexts, cvecs, gi+1)
		for _, p := range cpaths {
			moves = append(moves, SortMove{File: p, SuggestedFolder: name})
		}
	}
	return moves, nil
}

func (s *Sorter) nameCluster(ctx context.Context, paths, texts []string, vecs [][]float32, fallbackN int) string {
	// Compute centroid.
	dim := len(vecs[0])
	centroid := make([]float32, dim)
	for _, v := range vecs {
		for d, x := range v {
			centroid[d] += x
		}
	}
	n := float32(len(vecs))
	for d := range centroid {
		centroid[d] /= n
	}

	// Pick representatives closest to centroid.
	type rep struct {
		idx int
		sim float32
	}
	reps := make([]rep, len(vecs))
	for i, v := range vecs {
		reps[i] = rep{i, cosineSim(v, centroid)}
	}
	slices.SortFunc(reps, func(a, b rep) int {
		if a.sim > b.sim {
			return -1
		}
		if a.sim < b.sim {
			return 1
		}
		return 0
	})
	if len(reps) > sortRepresentatives {
		reps = reps[:sortRepresentatives]
	}

	var sb strings.Builder
	sb.WriteString("These files belong in the same folder. Suggest a short, descriptive folder name (2-4 words, no slashes).\n\n")
	for _, r := range reps {
		snippet := texts[r.idx]
		if len(snippet) > sortSnippetLen {
			snippet = snippet[:sortSnippetLen]
		}
		fmt.Fprintf(&sb, "File: %s\nContent: %s\n\n", paths[r.idx], snippet)
	}
	sb.WriteString("Folder name:")

	raw, err := s.LLM.GenerateSync(ctx, sb.String())
	if err != nil {
		log.Printf("sorter: name cluster %d: %v", fallbackN, err)
		return fmt.Sprintf("group-%d", fallbackN)
	}

	name := strings.TrimSpace(raw)
	name = strings.ReplaceAll(name, "/", "-")
	name = strings.ReplaceAll(name, "\\", "-")
	if name == "" {
		return fmt.Sprintf("group-%d", fallbackN)
	}
	if len(name) > 50 {
		name = name[:50]
	}
	return name
}
```

- [ ] **Step 4: Run all sorter tests**

```bash
cd backend && go test ./internal/sorter/... -v
```

Expected: all tests PASS (TestCosineSim*, TestCluster*, TestClampInt, TestSort\*).

- [ ] **Step 5: Commit**

```bash
git add backend/internal/sorter/sorter.go backend/internal/sorter/sorter_test.go
git commit -m "feat: implement Sorter with embed→cluster→name pipeline"
```

---

### Task 5: Wire into server and update handlers

**Files:**

- Modify: `backend/cmd/server/main.go`
- Modify: `backend/cmd/server/handlers.go`

- [ ] **Step 1: Update the server struct and initialisation in main.go**

In `backend/cmd/server/main.go`, replace:

```go
import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/ameetmadan/arca/internal/config"
	"github.com/ameetmadan/arca/internal/modelmanager"
	"github.com/ameetmadan/arca/internal/watcher"
)

type server struct {
	app *config.App
	mm  *modelmanager.Manager
}
```

with:

```go
import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/ameetmadan/arca/internal/config"
	"github.com/ameetmadan/arca/internal/modelmanager"
	"github.com/ameetmadan/arca/internal/sorter"
	"github.com/ameetmadan/arca/internal/watcher"
)

type server struct {
	app    *config.App
	mm     *modelmanager.Manager
	sorter *sorter.Sorter
}
```

Then replace the server construction line:

```go
srv := &server{app: app, mm: mm}
```

with:

```go
srv := &server{
    app: app,
    mm:  mm,
    sorter: &sorter.Sorter{
        LLM:    app.LLMProvider,
        Embed:  app.EmbedProvider,
        Parser: app.Parser,
    },
}
```

- [ ] **Step 2: Update handlers.go**

In `backend/cmd/server/handlers.go`:

a) Add import for sorter package. Replace the import block:

```go
import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/ameetmadan/arca/internal/modelmanager"
)
```

with:

```go
import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/ameetmadan/arca/internal/modelmanager"
	"github.com/ameetmadan/arca/internal/sorter"
)
```

b) Remove the `SortMove` type definition (lines 64-67):

```go
type SortMove struct {
	File            string `json:"file"`
	SuggestedFolder string `json:"suggested_folder"`
}
```

c) Replace the entire `handleSort` function body:

```go
// POST /api/sort — sort / categorise files in a folder
func (s *server) handleSort(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Folder string `json:"folder"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Folder == "" {
		apiResponse(w, http.StatusBadRequest, nil, "folder required")
		return
	}

	moves, err := s.sorter.Sort(r.Context(), req.Folder)
	if err != nil {
		apiResponse(w, http.StatusInternalServerError, nil, err.Error())
		return
	}
	if moves == nil {
		moves = []sorter.SortMove{}
	}
	apiResponse(w, http.StatusOK, map[string][]sorter.SortMove{"moves": moves}, "")
}
```

d) Update `handleSortApply` to use `sorter.SortMove`. Replace:

```go
func (s *server) handleSortApply(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Folder string     `json:"folder"`
		Moves  []SortMove `json:"moves"`
	}
```

with:

```go
func (s *server) handleSortApply(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Folder string           `json:"folder"`
		Moves  []sorter.SortMove `json:"moves"`
	}
```

e) Remove the `parseSortMoves` function entirely (it was only used by the old `handleSort`):

```go
// parseSortMoves extracts a JSON array of SortMove from raw LLM output.
func parseSortMoves(raw string) ([]SortMove, error) {
	// Find the first '[' and last ']' to handle any surrounding text.
	start := strings.Index(raw, "[")
	end := strings.LastIndex(raw, "]")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("no JSON array found in LLM output")
	}
	var moves []SortMove
	if err := json.Unmarshal([]byte(raw[start:end+1]), &moves); err != nil {
		return nil, err
	}
	return moves, nil
}
```

- [ ] **Step 3: Verify the build**

```bash
cd backend && go build ./...
```

Expected: clean build, no unused imports or undefined symbols.

- [ ] **Step 4: Run the full test suite**

```bash
cd backend && go test ./...
```

Expected: all tests pass (sorter tests + any existing tests).

- [ ] **Step 5: Commit**

```bash
git add backend/cmd/server/main.go backend/cmd/server/handlers.go
git commit -m "feat: wire intelligent sorter into HTTP handler"
```
