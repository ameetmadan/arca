package semantic

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ameetmadan/arca/pkg/decision"
)

// Name is the value of Decision.Source for decisions made by this engine.
const Name = "semantic"

// Engine implements decision.Engine by comparing a file's embedding with the
// folders recorded in a FolderStore. It never reads the filesystem and never
// calls the embedding provider, so Decide is fast and does not wait for the
// background Indexer. It is safe for concurrent use.
type Engine struct {
	store FolderStore
	t     Tunables
}

var _ decision.Engine = (*Engine)(nil)

// NewEngine returns an Engine reading from store. It returns an error if store
// is nil or the tunables are invalid.
func NewEngine(store FolderStore, t Tunables) (*Engine, error) {
	if store == nil {
		return nil, errors.New("semantic: nil FolderStore")
	}
	if err := t.Validate(); err != nil {
		return nil, err
	}
	return &Engine{store: store, t: t}, nil
}

type scored struct {
	f     FolderIndex
	score float64
}

// Decide returns a Move to the existing folder most similar to f.Vector.
//
// It returns decision.ErrNoDecision when f.Vector is unusable, when fewer than
// two indexed folders can be compared with it, or when the best similarity is
// below Tunables.SimilarityFloor. Folders are only considered if they belong
// to the root that contains f.Path; if f.Path is empty every root counts.
// Other errors come from the store or from ctx.
func (e *Engine) Decide(ctx context.Context, f decision.Facts) (decision.Decision, error) {
	if err := ctx.Err(); err != nil {
		return decision.Decision{}, err
	}
	q, ok := normalize(f.Vector)
	if !ok {
		return decision.Decision{}, fmt.Errorf("%w: no usable vector", decision.ErrNoDecision)
	}

	folders, err := e.store.List(ctx)
	if err != nil {
		return decision.Decision{}, fmt.Errorf("semantic: list folders: %w", err)
	}

	var cands []scored
	for _, fi := range folders {
		if len(fi.Centroid) != len(q) || !underRoot(fi.Root, f.Path) {
			continue
		}
		cands = append(cands, scored{fi, e.similarity(q, fi)})
	}
	if len(cands) < 2 {
		return decision.Decision{}, fmt.Errorf("%w: need at least 2 indexed folders, have %d", decision.ErrNoDecision, len(cands))
	}

	// Highest score first; ties go to the lexicographically smaller path so
	// the answer does not depend on store ordering.
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		if cands[i].f.Root != cands[j].f.Root {
			return cands[i].f.Root < cands[j].f.Root
		}
		return cands[i].f.Rel < cands[j].f.Rel
	})
	best, second := cands[0], cands[1]
	if best.score < e.t.SimilarityFloor {
		return decision.Decision{}, fmt.Errorf("%w: best similarity %.2f is below the floor %.2f",
			decision.ErrNoDecision, best.score, e.t.SimilarityFloor)
	}

	return decision.Decision{
		Action:     decision.Move,
		Target:     filepath.Join(best.f.Root, filepath.FromSlash(best.f.Rel)),
		Confidence: e.t.Confidence(best.score, second.score),
		Reason:     fmt.Sprintf("closest to the %d files in %s", best.f.FileCount, best.f.Rel),
		Source:     Name,
	}, nil
}

// similarity scores unit vector q against one folder using the configured strategy.
func (e *Engine) similarity(q []float32, fi FolderIndex) float64 {
	if e.t.Strategy == KNN {
		if s, ok := knnScore(q, fi.Examples, e.t.K); ok {
			return s
		}
	}
	return dot(q, fi.Centroid)
}

// knnScore is the mean similarity of q to its k nearest examples. It reports
// false if the folder has no example with a matching dimension.
func knnScore(q []float32, examples []Example, k int) (float64, bool) {
	sims := make([]float64, 0, len(examples))
	for _, ex := range examples {
		if len(ex.Vector) == len(q) {
			sims = append(sims, dot(q, ex.Vector))
		}
	}
	if len(sims) == 0 {
		return 0, false
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(sims)))
	if k > len(sims) {
		k = len(sims)
	}
	var sum float64
	for _, s := range sims[:k] {
		sum += s
	}
	return sum / float64(k), true
}

// underRoot reports whether path lies inside root. An empty path matches every root.
func underRoot(root, path string) bool {
	if path == "" {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
