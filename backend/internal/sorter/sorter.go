package sorter

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"math"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ameetmadan/arca/pkg/decision"
	"github.com/ameetmadan/arca/pkg/embed"
	"github.com/ameetmadan/arca/pkg/llm"
	"github.com/ameetmadan/arca/pkg/parser"
)

const (
	sortBatchSize       = 25
	sortRepresentatives = 5
	sortSnippetLen      = 200

	// ClusterMaxConfidence caps the confidence of a cluster decision. The
	// cluster name is an LLM guess about files the user has not placed yet,
	// so it stays below the default auto-apply threshold (0.9): batch
	// suggestions are for the user to review (ADR-0007, cold start).
	ClusterMaxConfidence = 0.8
	// clusterMinConfidence is the floor for a cluster of two or more files.
	clusterMinConfidence = 0.1
	// clusterSingleConfidence is the confidence of a one-file cluster, whose
	// folder is just derived from the file name.
	clusterSingleConfidence = 0.3

	// ClusterSource is the Decision.Source of every decision the sorter makes.
	ClusterSource = "cluster"
)

// SortMove describes where one file should be moved.
type SortMove struct {
	File            string `json:"file"`
	SuggestedFolder string `json:"suggested_folder"`
}

// Suggestion is one file's batch-sort result as a decision. File is relative
// to the sorted folder, and Decision.Target is a folder relative to it too, as
// the Executor requires.
type Suggestion struct {
	File     string
	Decision decision.Decision
}

// clusterMove is the internal result of the embed -> cluster -> name pipeline.
type clusterMove struct {
	file     string
	folder   string
	size     int     // files in the cluster
	cohesion float64 // mean cosine similarity of the members to their centroid
}

func (cm clusterMove) decision() decision.Decision {
	d := decision.Decision{Action: decision.Move, Target: cm.folder, Source: ClusterSource}
	if cm.size <= 1 {
		d.Confidence = clusterSingleConfidence
		d.Reason = fmt.Sprintf("cluster of one file: nothing similar to group it with, so the folder %q is named after the file", cm.folder)
		return d
	}
	d.Confidence = math.Min(math.Max(cm.cohesion, clusterMinConfidence), ClusterMaxConfidence)
	d.Reason = fmt.Sprintf("cluster %q: %d files with similar content", cm.folder, cm.size)
	return d
}

// Sorter runs the embed→cluster→name pipeline.
type Sorter struct {
	LLM    llm.LLMProvider
	Embed  embed.EmbeddingProvider
	Parser parser.DocumentParser
}

// Sort returns suggested moves for all files under folder. It is the response
// shape of POST /api/v1/sort that the Swift client reads.
func (s *Sorter) Sort(ctx context.Context, folder string) ([]SortMove, error) {
	cms, err := s.sortClusters(ctx, folder)
	if err != nil {
		return nil, err
	}
	var moves []SortMove
	for _, cm := range cms {
		moves = append(moves, SortMove{File: cm.file, SuggestedFolder: cm.folder})
	}
	return moves, nil
}

// Suggest runs the same pipeline as Sort and returns each move as a
// decision.Decision (Source "cluster"), ready to be submitted to the decision
// queue like any other engine's answer (ADR-0006).
func (s *Sorter) Suggest(ctx context.Context, folder string) ([]Suggestion, error) {
	cms, err := s.sortClusters(ctx, folder)
	if err != nil {
		return nil, err
	}
	var out []Suggestion
	for _, cm := range cms {
		out = append(out, Suggestion{File: cm.file, Decision: cm.decision()})
	}
	return out, nil
}

// sortClusters embeds, clusters and names the files directly in folder.
// Files already in sub-folders count as sorted and are left alone, as are
// hidden files.
func (s *Sorter) sortClusters(ctx context.Context, folder string) ([]clusterMove, error) {
	var relPaths, texts []string

	err := filepath.WalkDir(folder, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != folder {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
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
			if err := ctx.Err(); err != nil {
				return nil, fmt.Errorf("sorter: %w", err)
			}
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
		return []clusterMove{{file: good[0].rel, folder: deriveFolder(good[0].rel), size: 1}}, nil
	}

	vecs := make([][]float32, len(good))
	for i, g := range good {
		vecs[i] = g.vec
	}

	clusters := cluster(vecs)

	var moves []clusterMove
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
		coh := cohesion(cvecs)
		for _, p := range cpaths {
			moves = append(moves, clusterMove{file: p, folder: name, size: len(cpaths), cohesion: coh})
		}
	}
	return moves, nil
}

// centroidOf returns the mean of vecs, which must not be empty.
func centroidOf(vecs [][]float32) []float32 {
	centroid := make([]float32, len(vecs[0]))
	for _, v := range vecs {
		for d, x := range v {
			centroid[d] += x
		}
	}
	n := float32(len(vecs))
	for d := range centroid {
		centroid[d] /= n
	}
	return centroid
}

// cohesion is the mean cosine similarity of vecs to their centroid: close to
// 1 for a tight group, lower for a loose one.
func cohesion(vecs [][]float32) float64 {
	c := centroidOf(vecs)
	var sum float64
	for _, v := range vecs {
		sum += float64(cosineSim(v, c))
	}
	return sum / float64(len(vecs))
}

func (s *Sorter) nameCluster(ctx context.Context, paths, texts []string, vecs [][]float32, fallbackN int) string {
	centroid := centroidOf(vecs)

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
	if len([]rune(name)) > 50 {
		name = string([]rune(name)[:50])
	}
	return name
}
