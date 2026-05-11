# Intelligent File Sorting — Design Spec

**Date:** 2026-05-11
**Status:** Approved

## Problem

The current `/api/sort` endpoint passes only filenames to the LLM. With no content signal, the model pattern-matches on names and produces folder suggestions that mirror the filenames rather than reflecting what files actually contain.

## Goal

Sort files by semantic meaning. Documents about the same topic cluster together; photos from the same trip land in the same folder. The LLM names categories it did not have to guess.

## Scope

- Supported for content: PDF, DOCX, XLSX, MD, TXT (via existing `Parser`)
- Supported via metadata: images (EXIF date/GPS), video, and any file the parser cannot handle
- Not in scope: visual encoding for images/video (future)

---

## Architecture

A new `internal/sorter` package owns the three-stage pipeline. The HTTP handler becomes a thin wrapper; the `[]SortMove` response format and the Swift client are unchanged.

```
handleSort(folder)
  └─ sorter.Sort(ctx, folder)
       ├─ Stage 1: Parse + Embed
       ├─ Stage 2: Cluster
       └─ Stage 3: Name
```

### Sorter type

```go
type Sorter struct {
    LLM    llm.LLMProvider
    Embed  embed.EmbeddingProvider
    Parser parser.DocumentParser
}

func (s *Sorter) Sort(ctx context.Context, folder string) ([]SortMove, error)
```

Dependencies are injected at construction; wired in `internal/config` alongside other providers.

---

## Stage 1: Parse + Embed

Walk the folder recursively. Process files in batches of `sortBatchSize = 25` (constant, single line to change).

For each file:

1. Attempt `Parser.Parse(path)` → take the first content chunk
2. If parsing fails or the file type is unsupported, synthesise a metadata string:
   ```
   "filename: sunset-rome.jpg | type: image | size: 3.2MB | modified: 2023-07-14 | location: Rome, Italy"
   ```
   For image files, attempt EXIF extraction (date taken, GPS) using `rwcarlsen/goexif` (pure Go). Fall back to `os.Stat` if EXIF is absent or fails.
3. Embed the text with `EmbedProvider.Embed()` → `[]float32`

Files with no meaningful signal (`.DS_Store`, zero-byte files) are skipped and excluded from output.

Embedding batches run sequentially — the local model is single-threaded; parallelism would queue on the same resource.

---

## Stage 2: Cluster

Group files by semantic similarity using k-means on the embedding vectors. No external library required; vectors are `[]float32`.

If n ≤ 1, skip clustering entirely and return a single move using a filename-derived folder name.

```
k = clamp(int(√n), 2, 20)

Init:  pick k random seeds from the file embeddings
Loop (max 20 iterations):
  Assign each file to nearest centroid (cosine similarity)
  Recompute centroids (mean of assigned vectors)
  Early-stop when assignments are stable
```

Output: `[][]int` — a list of clusters, each containing indices into the file list.

---

## Stage 3: Name

For each cluster:

1. Compute centroid, select up to 5 files closest to it as representatives
2. Build a prompt with each representative's filename + first 200 chars of content
3. Call `LLM.GenerateSync()` asking for a single concise folder name
4. Assign that name to every file in the cluster → `SortMove{File: ..., SuggestedFolder: ...}`

---

## Error Handling

| Failure                        | Behaviour                                       |
| ------------------------------ | ----------------------------------------------- |
| Parse fails                    | Fall back to metadata text; file still included |
| Embed fails                    | Log error; skip file (no move returned)         |
| LLM naming fails for a cluster | Use `group-1`, `group-2`, … as folder name      |
| Context cancelled              | Propagates through all stages cleanly           |

---

## Constants

| Constant              | Value | Location                    |
| --------------------- | ----- | --------------------------- |
| `sortBatchSize`       | 25    | `internal/sorter/sorter.go` |
| `sortMaxClusters`     | 20    | `internal/sorter/sorter.go` |
| `sortMaxIterations`   | 20    | `internal/sorter/sorter.go` |
| `sortRepresentatives` | 5     | `internal/sorter/sorter.go` |
| `sortSnippetLen`      | 200   | `internal/sorter/sorter.go` |

---

## Files Changed

| File                        | Change                                                                          |
| --------------------------- | ------------------------------------------------------------------------------- |
| `internal/sorter/sorter.go` | New — Sorter type, SortMove type, Sort(), cluster(), cosineSim(), EXIF fallback |
| `internal/config/config.go` | Wire Sorter with injected providers                                             |
| `cmd/server/main.go`        | Initialise Sorter; add `sorter` field to `server` struct                        |
| `cmd/server/handlers.go`    | Remove SortMove definition; replace sort body with `s.sorter.Sort()` call       |
| `go.mod` / `go.sum`         | Add `rwcarlsen/goexif`                                                          |

No changes to the Swift client or API contract.
