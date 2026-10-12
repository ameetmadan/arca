package semantic

import (
	"fmt"
	"math"
)

// Strategy selects how a file is scored against a folder.
type Strategy string

const (
	// Centroid scores a folder by the cosine to its centroid.
	Centroid Strategy = "centroid"
	// KNN scores a folder by the mean cosine to its K nearest examples.
	KNN Strategy = "knn"
)

// Tunables holds every adjustable number of the semantic engine in one place.
// The package documentation explains how they combine. Use DefaultTunables as
// the starting point; the zero value is not valid.
type Tunables struct {
	// SampleSize is the maximum number of files embedded per folder.
	SampleSize int
	// Seed makes the sample deterministic: the same seed and the same folder
	// contents always select the same files.
	Seed uint64
	// Strategy is Centroid or KNN.
	Strategy Strategy
	// K is the number of neighbours averaged by the KNN strategy.
	K int
	// SimilarityFloor is the best cosine similarity below which the engine
	// declines to decide. It also anchors the absolute-quality term of the
	// confidence. Must be in [0, 1).
	SimilarityFloor float64
	// MarginForFull is the relative margin ((s1-s2)/s1) at which the margin
	// term reaches 1. Must be > 0.
	MarginForFull float64
	// MarginWeight is the share of the confidence taken from the margin; the
	// rest comes from the absolute similarity. Must be in [0, 1].
	MarginWeight float64
	// MaxConfidence is the highest confidence the engine reports, in (0, 1].
	MaxConfidence float64
}

// DefaultTunables returns the recommended settings.
func DefaultTunables() Tunables {
	return Tunables{
		SampleSize:      50,
		Seed:            1,
		Strategy:        Centroid,
		K:               3,
		SimilarityFloor: 0.30,
		MarginForFull:   0.25,
		MarginWeight:    0.70,
		MaxConfidence:   0.95,
	}
}

// Validate reports the first setting that is out of range.
func (t Tunables) Validate() error {
	switch {
	case t.SampleSize < 1:
		return fmt.Errorf("semantic: SampleSize %d must be at least 1", t.SampleSize)
	case t.Strategy != Centroid && t.Strategy != KNN:
		return fmt.Errorf("semantic: unknown Strategy %q", t.Strategy)
	case t.K < 1:
		return fmt.Errorf("semantic: K %d must be at least 1", t.K)
	case !inRange(t.SimilarityFloor, 0, 1) || t.SimilarityFloor == 1:
		return fmt.Errorf("semantic: SimilarityFloor %v must be in [0, 1)", t.SimilarityFloor)
	case math.IsNaN(t.MarginForFull) || t.MarginForFull <= 0:
		return fmt.Errorf("semantic: MarginForFull %v must be > 0", t.MarginForFull)
	case !inRange(t.MarginWeight, 0, 1):
		return fmt.Errorf("semantic: MarginWeight %v must be in [0, 1]", t.MarginWeight)
	case !inRange(t.MaxConfidence, 0, 1) || t.MaxConfidence == 0:
		return fmt.Errorf("semantic: MaxConfidence %v must be in (0, 1]", t.MaxConfidence)
	}
	return nil
}

// Confidence maps the best (s1) and second-best (s2) folder similarity to a
// confidence in [0, MaxConfidence]. See the package documentation for the
// formula. It assumes s1 >= SimilarityFloor, which Engine.Decide guarantees.
func (t Tunables) Confidence(s1, s2 float64) float64 {
	if math.IsNaN(s1) || math.IsNaN(s2) || s1 <= 0 {
		return 0
	}
	margin := clamp01(((s1 - s2) / s1) / t.MarginForFull)
	quality := clamp01((s1 - t.SimilarityFloor) / (1 - t.SimilarityFloor))
	return t.MaxConfidence * clamp01(t.MarginWeight*margin+(1-t.MarginWeight)*quality)
}

func inRange(v, lo, hi float64) bool { return v >= lo && v <= hi } // false for NaN

func clamp01(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	}
	return v
}
