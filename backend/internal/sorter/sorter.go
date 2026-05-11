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
