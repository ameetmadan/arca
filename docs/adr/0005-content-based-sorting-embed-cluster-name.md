# ADR-0005: Content-based sorting: embed → cluster → name

- **Status:** Accepted (backfilled)
- **Date:** 2026-05-11
- **Deciders:** Ameet Madan
- **Detail:** [`docs/superpowers/specs/2026-05-11-intelligent-sort-design.md`](../superpowers/specs/2026-05-11-intelligent-sort-design.md)

## Context

The first `/api/sort` gave the LLM only filenames. It matched on the names themselves and
suggested folders that just echoed them, not what the files were about.

## Decision

`internal/sorter` runs a three-stage pipeline:

1. **Parse + embed:** use the first parsed chunk of each file. Images, video, and file types
   the parser can't read fall back to a metadata string (EXIF date/GPS, type, size, modified time).
2. **Cluster:** k-means with cosine similarity over the embeddings.
3. **Name:** the LLM names each cluster using a few example files from it.

The HTTP response format (`[]SortMove`) and the Swift client stay the same.

## Consequences

- **Positive:** files are grouped by what they're about, and the LLM only names groups instead of placing each file.
- **Negative / trade-offs:** it's a batch job. It ignores the folders the user already has, doesn't learn from corrections, and folder names can vary between runs.
- **Follow-ups:** in the decision engine (ADR-0006) this becomes the **cold-start / batch strategy** for folders with no structure yet. It is not removed.
