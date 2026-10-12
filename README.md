# Arca

Arca is a local-first file assistant for macOS. It reads the files in the folders you choose and
helps you sort them into folders, chat with your documents, and ask questions of your
spreadsheets.

It is a thin SwiftUI app (`app/`) that launches a Go backend (`backend/`) as a subprocess. The two
talk over HTTP on `localhost:44742`.

## The local-first promise

Parsing, embeddings, vector search, SQL, and LLM inference all run **on your Mac**. No file
content leaves the device by default. The only network access is downloading model files from
Hugging Face. Any future feature that sends data off the device must be opt-in and clearly
labelled. See [ADR-0002](docs/adr/0002-local-first-on-device-inference.md) for the reasoning and
trade-offs.

The models the backend can download (see `backend/internal/modelmanager/modelmanager.go`):

| ID | Model | Approx. size |
|---|---|---|
| `llama32-3b` | Llama 3.2 3B Instruct (Q4_K_M) | 2.0 GB |
| `qwen25-3b` | Qwen 2.5 3B Instruct (Q4_K_M) | 1.9 GB |
| `nomic-embed` | Nomic Embed Text v1.5 (Q4_K_M), used for embeddings | 0.08 GB |

> `modelmanager` supports SHA256 verification, but the hashes in the model list are currently
> empty, so downloads are not yet checked against a hash.

## Documentation

- [Architecture overview and diagrams](docs/architecture/README.md)
- [Architecture decision records (ADRs)](docs/adr/README.md)

## Prerequisites

Building the full app needs **macOS on Apple Silicon** with:

- macOS 14 or later (the Swift package targets `.macOS(.v14)`)
- Xcode with the Swift toolchain (the app is built with `swift build`)
- Go 1.25 or later (`backend/go.mod`)
- CMake (to build llama.cpp)
- A llama.cpp checkout at `~/llama.cpp`

Building and testing only the Go backend without CGo needs just Go 1.25+ and works on any
platform.

## Build and run

All commands run from the repository root.

| Command | What it does | Platform |
|---|---|---|
| `make llama-build` | Builds the llama.cpp static libraries (Metal enabled, arm64) | macOS only |
| `make backend-stub` | Builds `backend/arca-server` without CGo, using stub providers | Any |
| `make backend` | Builds `backend/arca-server` with llama.cpp, sqlite-vec, and DuckDB linked in | macOS only |
| `make bundle` | Runs `make backend`, then copies the binary to `app/Sources/Arca/Resources/arca-server` | macOS only |
| `make dist` | Runs `make bundle`, builds the Swift app in release mode, and assembles an ad-hoc signed `dist/Arca.app` | macOS only |
| `make clean` | Removes the built backend binaries and `dist/` | Any |

### 1. Build llama.cpp (macOS only, once)

The Makefile expects llama.cpp at `~/llama.cpp`. Override the location with `LLAMA_DIR`.

```sh
git clone https://github.com/ggml-org/llama.cpp ~/llama.cpp
make llama-build
```

### 2. Build the backend

Without CGo. This works anywhere, but the LLM, embedding, vector, and SQL providers are stubs
that return errors when used. It is meant for working on the server and API layer:

```sh
make backend-stub
```

With real inference (macOS only, needs step 1):

```sh
make backend
```

Both write `backend/arca-server`.

> Heads up: `backend/arca-server`, `app/Sources/Arca/Resources/arca-server` and `dist/Arca.app`
> are currently tracked in git, so rebuilding changes tracked files. Don't commit rebuilt binaries
> with unrelated changes.

### 3. Build the app (macOS only)

```sh
make dist
open dist/Arca.app
```

The app starts the bundled `arca-server` as a subprocess when it launches. You still need a
`config.yaml` (see below) before the backend can start.

### Running the backend by hand

The backend reads its config from the default path below. Use `-config` to point it somewhere
else.

```sh
backend/arca-server                      # uses the default config path
backend/arca-server -config ./my.yaml    # uses a custom config file
```

If the config file does not exist the server exits with a "load config" error, so create one first
(see below). On start it also creates `~/Library/Application Support/Arca/` and its `models/`
subfolder.

## Configuration

The default config path is:

```
~/Library/Application Support/Arca/config.yaml
```

Nothing creates this file for you yet, so write it yourself. A complete example, with the values
shown being the ones the backend uses when a field is omitted or the one provider that is
implemented for each slot:

```yaml
# Local HTTP port. The Swift app expects 44742 (the default if omitted).
server:
  port: 44742

# LLM provider. Only "llamacpp" exists today (also the default if omitted).
llm:
  provider: llamacpp
  # A .gguf file. Updated automatically when you activate a model in the app.
  model_path: ~/Library/Application Support/Arca/models/Llama-3.2-3B-Instruct-Q4_K_M.gguf

# Embedding provider. Only "nomic" exists today (also the default).
embed:
  provider: nomic
  model_path: ~/Library/Application Support/Arca/models/nomic-embed-text-v1.5.Q4_K_M.gguf

# Vector store. Only "sqlitevec" exists today (also the default).
vector:
  provider: sqlitevec
  db_path: ~/Library/Application Support/Arca/vectors.db

# SQL engine for spreadsheet questions. Only "duckdb" exists today (also the default).
sql:
  provider: duckdb
  db_path: ~/Library/Application Support/Arca/tables.duckdb

# Folders watched for changes. Changed files are re-indexed.
watcher:
  folders:
    - ~/Documents

# Where downloaded models are stored.
# Defaults to ~/Library/Application Support/Arca/models if omitted.
models:
  dir: ~/Library/Application Support/Arca/models
```

Notes:

- A leading `~/` is expanded to your home directory in all path fields.
- Providers are chosen only in `backend/internal/config/config.go`
  ([ADR-0004](docs/adr/0004-pluggable-providers-wired-in-config.md)). An unknown provider name
  makes startup fail.
- When you activate a model through the app, the backend rewrites `config.yaml` with the new model
  paths. Comments in the file are not preserved when that happens.
- The decision engine proposed in [ADR-0006](docs/adr/0006-decision-engine-chain-with-policy-and-executor.md)
  will add a `decision:` section. It does not exist yet, so it is not documented here. This README
  will be updated when it lands.

## Tests

The Go tests run without CGo. The CGo-backed providers (`llamacpp`, `nomic`, `sqlitevec`,
`duckdb`) sit behind build tags (`-tags llamacpp,sqlitevec,duckdb`); a default build uses the stub
versions, so no llama.cpp, Metal, or macOS is needed:

```sh
cd backend
go test ./...
```

Tests use fake providers and temporary directories, so they need no real models. Don't rely on
the CGo/Metal build in tests.

## Project layout

```
app/        SwiftUI shell (File Sorter, Document Chat, Table Chat, Settings, onboarding)
backend/    Go backend
  cmd/server/   HTTP server (chi router)
  internal/     config (provider wiring), sorter, watcher, modelmanager
  pkg/          provider interfaces and implementations: llm, embed, vector, sqlengine, parser
docs/       architecture overview and ADRs
dist/       output of `make dist`
```
