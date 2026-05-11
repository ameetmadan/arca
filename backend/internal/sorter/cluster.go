package sorter

import (
	"math"
	"math/rand/v2"
)

const (
	sortMaxClusters   = 20
	sortMaxIterations = 20
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
	if len(vecs) == 0 {
		return nil
	}
	if len(vecs) == 1 {
		return [][]int{{0}}
	}
	n := len(vecs)
	k := clampInt(int(math.Sqrt(float64(n))), 2, sortMaxClusters)
	dim := len(vecs[0])

	// k-means++ init: first seed random, each subsequent seed is the point
	// farthest from its nearest existing seed.
	// Note: if all vectors are identical, all seeds will be the same index.
	// The algorithm still converges to one non-empty cluster without panicking.
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
