package semantic

import "math"

// normalize returns a unit-length copy of v. It reports false, and returns
// nil, if v is empty, all zeros, or contains NaN or infinite values: such a
// vector has no direction to compare.
func normalize(v []float32) ([]float32, bool) {
	if len(v) == 0 {
		return nil, false
	}
	var sum float64
	for _, x := range v {
		f := float64(x)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, false
		}
		sum += f * f
	}
	norm := math.Sqrt(sum)
	if norm == 0 || math.IsInf(norm, 0) {
		return nil, false
	}
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(float64(x) / norm)
	}
	return out, true
}

// dot returns the dot product of a and b, which must have the same length.
// For unit vectors it is the cosine similarity.
func dot(a, b []float32) float64 {
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

// centroidOf returns the unit-length mean direction of equal-length unit
// vectors. It reports false if there are none or they cancel out.
func centroidOf(vs [][]float32) ([]float32, bool) {
	if len(vs) == 0 {
		return nil, false
	}
	sum := make([]float32, len(vs[0]))
	for _, v := range vs {
		for i, x := range v {
			sum[i] += x
		}
	}
	return normalize(sum)
}
