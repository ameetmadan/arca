Build a macOS app called Arca with the following architecture. The project has two top-level components: a SwiftUI app (thin shell) and a Go backend that runs as an embedded subprocess on localhost:44742.
Go backend (/backend)
Use Go modules. Structure the backend as the following packages:

pkg/llm — LLMProvider interface with Generate(ctx, prompt string) (io.Reader, error) for streaming and GenerateSync(ctx, prompt string) (string, error)
pkg/embed — EmbeddingProvider interface with Embed(ctx, text string) ([]float32, error)
pkg/vector — VectorStore interface with Upsert, Search(query []float32, topK int), Delete
pkg/parser — DocumentParser interface with Parse(path string) ([]Chunk, error). Implement for .pdf, .docx, .xlsx, .md, .txt
pkg/sqlengine — SQLEngine interface with QueryNL(ctx, question, schemaHint string) (QueryResult, error). Implement with DuckDB via CGo
pkg/llm/llamacpp — concrete LlamaCppProvider using go-llama.cpp CGo bindings, loading a .gguf from a configurable path
pkg/embed/nomic — concrete NomicEmbedder using the same llama.cpp bindings with the nomic-embed-text-v1.5.Q4_K_M.gguf model
pkg/vector/sqlitevec — concrete SQLiteVecStore using sqlite-vec extension
internal/watcher — FSEvents-based folder watcher that posts to the index route on change
internal/modelmanager — downloads .gguf files (Phi-4 mini or Gemma 3 4B, user's choice) with SHA256 verification, saves to ~/Library/Application Support/Arca/models/, streams download progress as SSE
internal/config — YAML config loader that wires all provider implementations via dependency injection at startup. No provider is hardcoded outside this file.
cmd/server — chi router exposing: POST /api/index, POST /api/sort, GET /api/chat (SSE), POST /api/query, GET /api/models, GET /api/models/download (SSE progress)

SwiftUI app (/app)

Launches the Go binary as a subprocess on app start, terminates it on quit
Communicates exclusively via HTTP to localhost:44742
Four main views: File Sorter, Document Chat, Table Chat, Settings
Settings view: model picker (Phi-4 mini / Gemma 3 4B), watched folder selection
First-launch onboarding: detects no model present, shows download progress bar streaming from /api/models/download
All API calls go through a single APIClient.swift — no view makes HTTP calls directly

Pluggability rule: Every provider (LLM, embedder, vector store, SQL engine) must be wired exclusively in internal/config. Adding a new provider means adding one new struct file and one new case in config — nothing else should change.
Start by scaffolding the full directory structure, Go module, and Swift package, then implement the interfaces and dependency injection before any UI.