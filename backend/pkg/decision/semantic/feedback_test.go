package semantic

import (
	"context"
	"testing"
)

type learned []LearnedExample

func (l learned) LearnedExamples(context.Context) ([]LearnedExample, error) { return l, nil }
func TestLearningMovesCentroidWithoutMutatingBase(t *testing.T) {
	ctx := context.Background()
	base := NewMemoryStore()
	base.Put(ctx, FolderIndex{Root: "/r", Rel: "A", FileCount: 1, Centroid: []float32{1, 0}, Examples: []Example{{Name: "old.txt", Vector: []float32{1, 0}}}})
	s := LearningStore{FolderStore: base, Feedback: learned{{Root: "/r", File: "new.txt", Target: "A", Vector: []float32{0, 1}}}}
	got, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Centroid[1] < 0.7 || got[0].Centroid[1] > 0.71 {
		t.Fatalf("centroid did not learn: %v", got[0].Centroid)
	}
	original, _, _ := base.Get(ctx, "/r", "A")
	if original.Centroid[1] != 0 || len(original.Examples) != 1 {
		t.Fatal("mutated base")
	}
	again, _ := s.List(ctx)
	if len(again[0].Examples) != 2 {
		t.Fatal("feedback counted twice")
	}
}
