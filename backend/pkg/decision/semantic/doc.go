// Package semantic is decision engine (2): it places a file by similarity to
// the folders the user already has. There is no training step; the structure
// the user built over the years is the training data
// (docs/adr/0007-learn-from-existing-folders-not-forced-training.md).
//
// # How it works
//
// An [Indexer] walks the sub-folders of a watched root (not the root itself,
// and no hidden folders), embeds a deterministic sample of the files directly
// inside each one, and stores a unit-length centroid plus the example vectors
// in a [FolderStore]. An [Engine] then compares Facts.Vector against those
// folders at decision time and returns a Move to the best one, or
// decision.ErrNoDecision when it cannot do so honestly. Indexing runs in the
// background ([Indexer.Start]) and each folder is swapped in atomically when
// it is done; [Engine.Decide] only reads what is already stored, so it never
// waits for indexing. Call [Indexer.IndexFolder] when a folder changes: it
// re-embeds only that folder, and only if its file names, sizes or
// modification times differ from the last run.
//
// # Scoring
//
// Each folder gets a similarity s in [-1, 1] to the file's vector (cosine):
//
//   - [Centroid]: cosine(file, folder centroid). The default.
//   - [KNN]: the mean cosine of the file to its K nearest example vectors in
//     the folder. More robust for folders that hold several unrelated kinds
//     of file, where one centroid would sit between the clusters.
//
// Let s1 be the best score and s2 the second best.
//
// # Margin to confidence
//
// The confidence says how sure the engine is that s1 is the right folder, not
// merely a plausible one. It combines how far ahead the winner is with how
// good the match is in absolute terms:
//
//	m  = clamp01( ((s1 - s2) / s1) / MarginForFull )          scaled relative margin
//	q  = clamp01( (s1 - SimilarityFloor) / (1 - SimilarityFloor) )   absolute quality
//	confidence = MaxConfidence * ( MarginWeight*m + (1 - MarginWeight)*q )
//
// With the defaults (MarginForFull 0.25, MarginWeight 0.70, SimilarityFloor
// 0.30, MaxConfidence 0.95):
//
//	s1=0.90 s2=0.30  ->  0.91   clear winner
//	s1=0.80 s2=0.60  ->  0.87   comfortable lead
//	s1=0.80 s2=0.78  ->  0.27   two folders look alike: let a later engine decide
//	s1=0.50 s2=0.10  ->  0.75   clear lead but only a moderate match
//
// The margin is relative (divided by s1) so that a 0.05 gap means more between
// 0.30 and 0.25 than between 0.95 and 0.90. A tie gives m = 0, so confidence
// is at most (1-MarginWeight)*MaxConfidence, about 0.29 with the defaults,
// which is below any sensible chain threshold. MaxConfidence stays under 1
// because 1.0 is reserved for explicit user rules. [Tunables.Confidence] is
// the single implementation of this formula.
//
// # When there is no decision
//
// [Engine.Decide] returns decision.ErrNoDecision, so the chain moves on, when
//
//   - Facts.Vector is empty, all zeros, or not finite,
//   - fewer than two indexed folders can be compared with the vector (a
//     margin needs a runner-up; empty folders and folders indexed with a
//     different embedding size do not count), or
//   - the best similarity s1 is below SimilarityFloor.
//
// A file is only compared with the folders of the root that contains
// Facts.Path. The root itself is never a target. The engine does not check
// whether the file already sits in the winning folder; the caller decides
// what a Move to the current location means.
//
// # Tunables
//
// Everything adjustable lives in [Tunables]; see [DefaultTunables] for the
// values and [Tunables.Validate] for the allowed ranges.
//
//	SampleSize       50    files embedded per folder (sampled when it has more)
//	Seed             1     same seed and same folder contents: same sample
//	Strategy         centroid or knn
//	K                3     neighbours averaged by the knn strategy
//	SimilarityFloor  0.30  below this best similarity there is no decision
//	MarginForFull    0.25  relative margin at which the margin term saturates
//	MarginWeight     0.70  share of the confidence that comes from the margin
//	MaxConfidence    0.95  ceiling on the confidence this engine reports
//
// The chain's own threshold for the "semantic" step (decision.Step
// .MinConfidence) is a separate knob owned by the wiring code. The right
// SimilarityFloor depends on the embedding model, so revisit it if the model
// changes.
//
// # Wiring
//
// This package chooses no provider. [NewEngine] and [NewIndexer] take the
// store, the embedding provider and the [Source] as arguments; they are meant
// to be called from internal/config (ADR-0004). [MemoryStore] keeps the index
// in memory, so it is rebuilt in the background after a restart; a persistent
// [FolderStore] can be swapped in without touching the engine. The text the
// indexer embeds for a file ([Source.Text]) must be produced the same way as
// the text behind Facts.Vector, or file and folder vectors live in slightly
// different spaces. No file contents or names are ever logged by this package.
package semantic
