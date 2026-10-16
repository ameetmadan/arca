// Package calibration ranks uncertain files without changing any state.
package calibration

import (
	"github.com/ameetmadan/arca/pkg/decision/semantic"
	"math"
	"sort"
)

type File struct {
	Root   string    `json:"root"`
	Path   string    `json:"file"`
	Vector []float32 `json:"-"`
}
type Candidate struct {
	Target string  `json:"target"`
	Score  float64 `json:"score"`
}
type Item struct {
	File
	Candidates []Candidate `json:"candidates"`
	Margin     float64     `json:"margin"`
}

func cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return math.NaN()
	}
	var dot, aa, bb float64
	for i, x := range a {
		dot += float64(x) * float64(b[i])
		aa += float64(x) * float64(x)
		bb += float64(b[i]) * float64(b[i])
	}
	return dot / math.Sqrt(aa*bb)
}
func Rank(files []File, folders []semantic.FolderIndex, limit int) []Item {
	if limit <= 0 {
		return []Item{}
	}
	items := []Item{}
	for _, f := range files {
		c := []Candidate{}
		for _, folder := range folders {
			if folder.Root != f.Root {
				continue
			}
			s := cosine(f.Vector, folder.Centroid)
			if math.IsNaN(s) || math.IsInf(s, 0) {
				continue
			}
			c = append(c, Candidate{folder.Rel, s})
		}
		sort.Slice(c, func(i, j int) bool {
			if c[i].Score == c[j].Score {
				return c[i].Target < c[j].Target
			}
			return c[i].Score > c[j].Score
		})
		if len(c) < 2 {
			continue
		}
		margin := c[0].Score - c[1].Score
		if len(c) > 3 {
			c = c[:3]
		}
		items = append(items, Item{f, c, margin})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Margin == items[j].Margin {
			if items[i].Root == items[j].Root {
				return items[i].Path < items[j].Path
			}
			return items[i].Root < items[j].Root
		}
		return items[i].Margin < items[j].Margin
	})
	out := []Item{}
	for _, item := range items {
		duplicate := false
		for _, old := range out {
			if old.Root == item.Root && cosine(old.Vector, item.Vector) >= 0.98 {
				duplicate = true
				break
			}
		}
		if !duplicate {
			out = append(out, item)
		}
		if len(out) >= limit {
			break
		}
	}
	return out
}
