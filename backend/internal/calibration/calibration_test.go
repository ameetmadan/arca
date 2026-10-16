package calibration

import (
	"github.com/ameetmadan/arca/pkg/decision/semantic"
	"testing"
)

func TestUncertaintyAndDuplicates(t *testing.T) {
	folders := []semantic.FolderIndex{{Root: "/r", Rel: "A", Centroid: []float32{1, 0}}, {Root: "/r", Rel: "B", Centroid: []float32{0, 1}}, {Root: "/r", Rel: "C", Centroid: []float32{-1, 0}}}
	files := []File{{Root: "/r", Path: "sure", Vector: []float32{1, 0}}, {Root: "/r", Path: "tie", Vector: []float32{1, 1}}, {Root: "/r", Path: "copy", Vector: []float32{1, 1}}}
	got := Rank(files, folders, 15)
	if len(got) != 2 || got[0].Margin != 0 || len(got[0].Candidates) != 3 {
		t.Fatalf("unexpected ranking: %+v", got)
	}
	if len(Rank(files, folders, 1)) != 1 {
		t.Fatal("limit ignored")
	}
	if len(Rank(nil, folders, 15)) != 0 {
		t.Fatal("empty input")
	}
}
