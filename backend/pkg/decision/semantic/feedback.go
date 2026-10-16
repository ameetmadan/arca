package semantic

import (
	"context"
	"path/filepath"
	"sort"
)

type LearnedExample struct {
	Root, File, Target string
	Vector             []float32
}
type ExampleSource interface {
	LearnedExamples(context.Context) ([]LearnedExample, error)
}

// LearningStore overlays durable confirmations on the index. Both scoring modes
// see the same learned examples; reindexing cannot erase explicit corrections.
type LearningStore struct {
	FolderStore
	Feedback ExampleSource
}

func (s *LearningStore) List(ctx context.Context) ([]FolderIndex, error) {
	folders, err := s.FolderStore.List(ctx)
	if err != nil {
		return nil, err
	}
	examples, err := s.Feedback.LearnedExamples(ctx)
	if err != nil {
		return nil, err
	}
	for _, e := range examples {
		v, ok := normalize(e.Vector)
		if !ok {
			continue
		}
		found := false
		for _, f := range folders {
			if f.Root == e.Root && f.Rel == e.Target {
				found = true
			}
		}
		if !found {
			folders = append(folders, FolderIndex{Root: e.Root, Rel: e.Target, Centroid: make([]float32, len(v))})
		}
		for i := range folders {
			f := &folders[i]
			if f.Root != e.Root {
				continue
			}
			// Remove any old example of this file, including an already-indexed
			// copy at its corrected destination, before adding the confirmation.
			copied := make([]Example, 0, len(f.Examples)+1)
			removed := 0
			for _, ex := range f.Examples {
				oldPath := filepath.ToSlash(filepath.Join(f.Rel, ex.Name))
				if oldPath == filepath.ToSlash(e.File) || (f.Rel == e.Target && ex.Name == filepath.Base(e.File)) {
					removed++
					continue
				}
				copied = append(copied, ex)
			}
			changed := removed > 0
			f.FileCount -= removed
			if f.Rel == e.Target && len(f.Centroid) == len(v) {
				copied = append(copied, Example{Name: filepath.Base(e.File), Vector: v})
				f.FileCount++
				changed = true
			}
			if !changed {
				continue
			}
			f.Examples = copied
			sum := make([]float32, len(f.Centroid))
			for _, ex := range copied {
				if len(ex.Vector) == len(sum) {
					for j, x := range ex.Vector {
						sum[j] += x
					}
				}
			}
			f.Centroid = nil
			if center, ok := normalize(sum); ok {
				f.Centroid = center
			}
		}
	}
	sort.Slice(folders, func(i, j int) bool {
		if folders[i].Root == folders[j].Root {
			return folders[i].Rel < folders[j].Rel
		}
		return folders[i].Root < folders[j].Root
	})
	return folders, nil
}
