# ADR-0002: Local-first, on-device inference

- **Status:** Accepted (backfilled)
- **Date:** 2026-10-12
- **Deciders:** Ameet Madan

## Context

Arca reads the contents of personal files: invoices, contracts, photos with GPS data,
spreadsheets. The product only works if users trust it with those files. Apple Silicon Macs can
run 3–4B parameter quantized models and small embedding models fast enough to use.

## Decision

All parsing, embedding, vector search, SQL, and LLM inference run **on the user's machine**:

- LLM: llama.cpp (`pkg/llm/llamacpp`) with GGUF models (currently Llama 3.2 3B / Qwen 2.5 3B, Q4_K_M)
- Embeddings: nomic-embed-text v1.5 via llama.cpp (`pkg/embed/nomic`)
- Vectors: SQLite + sqlite-vec (`pkg/vector/sqlitevec`)
- Tabular queries: DuckDB (`pkg/sqlengine/duckdb`)

The only network access is downloading models from Hugging Face, checked against a SHA256 hash.
**No file content leaves the device by default.**

## Alternatives considered

- **Hosted LLM APIs:** better quality and less to ship, but sends file contents to third parties and needs accounts and API keys.
- **Hybrid by default:** more complex, and the privacy promise gets murky.

## Consequences

- **Positive:** private, works offline, no per-request cost, no accounts.
- **Negative / trade-offs:** small models produce lower-quality answers. CGo + Metal builds tie the backend to macOS/arm64. Models are large downloads.
- **Follow-ups:** any feature that sends data off the device must be opt-in and clearly labelled (see ADR-0008).
