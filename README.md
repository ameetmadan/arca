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

> Model URLs are pinned to a Hugging Face commit and every download is verified against a SHA256;
> a mismatch (or a model with no hash) fails the download and removes the partial file.

Arca does not bundle or redistribute these models; it downloads them from Hugging Face on your
request. Each model has its own license (for example the Llama 3.2 Community License and the
Qwen license), which is separate from Arca's license and governs how you may use the model. Read
the terms on each model's Hugging Face page before use, especially for commercial purposes.

## Install

Download the latest zip from [Releases](https://github.com/ameetmadan/arca/releases), unzip it,
and move **Arca.app** to Applications. Builds are ad-hoc signed, not notarized, so clear the
quarantine flag once (`xattr -dr com.apple.quarantine /Applications/Arca.app`) or use **System
Settings → Privacy & Security → Open Anyway**. On first launch Arca walks you through choosing
folders and downloading a model. [docs/TESTING.md](docs/TESTING.md) is a guided tour.

## Releasing

CI (`.github/workflows/build.yml`) builds and smoke-tests `Arca.app` on every pull request and
push to `main`. To publish a release, push a tag: `git tag v0.1.0 && git push origin v0.1.0`
(a tag with a suffix such as `v0.1.0-beta.1` becomes a pre-release). The workflow attaches the
zip and uses [docs/release-notes.md](docs/release-notes.md) as the notes.

## Documentation

- [Architecture overview and diagrams](docs/architecture/README.md)
- [Architecture decision records (ADRs)](docs/adr/README.md)
- [Contributing](CONTRIBUTING.md)
- [Security policy](SECURITY.md)

## Prerequisites

Building the full app needs **macOS on Apple Silicon** with:

- macOS 14 or later (the Swift package targets `.macOS(.v14)`)
- Xcode with the Swift toolchain (the app is built with `swift build`)
- Go 1.26 or later (`backend/go.mod`)
- CMake (to build llama.cpp)
- A llama.cpp checkout at `~/llama.cpp`

Building and testing only the Go backend without CGo needs just Go 1.26+ and works on any
platform.

## Build and run

All commands run from the repository root.

| Command | What it does | Platform |
|---|---|---|
| `make llama-build` | Builds the llama.cpp static libraries (Metal enabled, arm64) | macOS only |
| `make backend-stub` | Builds `backend/arca-server` without CGo, using stub providers | Any |
| `make backend` | Builds `backend/arca-server` with llama.cpp, sqlite-vec, and DuckDB linked in | macOS only |
| `make bundle` | Runs `make backend`, then copies the binary to `app/arca-server` so `swift run` from `app/` finds it | macOS only |
| `make dist` | Runs `make backend`, builds the Swift app in release mode, and assembles an ad-hoc signed `dist/Arca.app`. `VERSION=1.2.3` stamps the version | macOS only |
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

> Note: `backend/arca-server`, `app/arca-server` and `dist/` are build
> outputs and are git-ignored. `make clean` removes them.

### 3. Build the app (macOS only)

```sh
make dist
open dist/Arca.app
```

The app starts the bundled `arca-server` as a subprocess (with `-exit-with-parent`, so it never
outlives the app) and writes its output to `~/Library/Logs/Arca/arca-server.log`.

### Running the backend by hand

The backend reads its config from the default path below. Use `-config` to point it somewhere
else.

```sh
backend/arca-server --insecure-dev       # uses the default config path
backend/arca-server --insecure-dev -config ./my.yaml    # uses a custom config file
```

If the config file does not exist the server writes a default one (no models, no watched
folders). On start it also creates `~/Library/Application Support/Arca/` and its `models/`
subfolder.

## Configuration

The default config path is:

```
~/Library/Application Support/Arca/config.yaml
```

The server creates it on first start, and the app updates the model paths and watched folders
(through `PUT /api/v1/settings/folders` and `POST /api/v1/models/activate`). A complete example,
with the values shown being the ones the backend uses when a field is omitted or the one provider
that is implemented for each slot:

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

### Decision engines (optional)

The `decision:` section ([ADR-0006](docs/adr/0006-decision-engine-chain-with-policy-and-executor.md))
lists the engines that decide what to do with a file, in order. The first engine whose answer
reaches its `min_confidence` wins; if none does, the file is left for the user to decide. Leave the
section out and you get the default below.

```yaml
decision:
  engines:
    - {type: rules,    min_confidence: 1.0}   # your own rules; rules_path defaults to <app dir>/rules.yaml
    - {type: semantic, min_confidence: 0.7}   # the folder it most resembles, learned from your existing folders
    - {type: llm,      min_confidence: 0.5}   # the local LLM picks from your existing folders
```

- `type` is `rules`, `semantic` or `llm`, each at most once. An unknown type, a `min_confidence`
  outside 0 to 1, or an engine whose provider is missing makes startup fail with a message naming
  the entry.
- `min_confidence` may be omitted; the values shown are the defaults.
- `rules_path` (rules only) points at the rules file. A missing file just means no rules.
- The semantic engine learns from the sub-folders of `watcher.folders` in the background after
  each start, so it has nothing to go on until that has finished.

### Indexing

The watcher only sees changes, so the server also scans the watched folders at start and on
`POST /api/v1/index/rescan`: supported files (`.pdf`, `.docx`, `.xlsx`, `.md`, `.txt`, `.csv`)
whose size or modification time changed are indexed, and files that disappeared are removed from
the index. Progress is at `GET /api/v1/index/status`; what was indexed is remembered in
`index-state.json` next to the config.

### Table Chat

`POST /api/v1/tables/import` loads a `.csv`, `.tsv`, `.xlsx` (one table per sheet), `.parquet`
or `.json` file into DuckDB; `GET /api/v1/tables` lists them. The SQL the LLM writes must be a
single read-only `SELECT`; anything that writes, touches files or URLs, or loads extensions is
refused. DuckDB's extension auto-install is off.

## Tests

The Go tests run without CGo. The CGo-backed providers (`llamacpp`, `nomic`, `sqlitevec`,
`duckdb`) sit behind build tags (`-tags llamacpp,sqlitevec,duckdb`); a default build uses the stub
versions, so no llama.cpp, Metal, or macOS is needed:

```sh
cd backend
go test ./...
```

Tests use fake providers and temporary directories, so they need no real models. The sqlite-vec
and DuckDB providers have integration tests behind their tags; they need a C compiler (and
`sqlite3.h`, e.g. `libsqlite3-dev` on Linux) but no models:

```sh
go test -tags sqlitevec,duckdb ./...
```

Swift model/decoding tests: `cd app && swift test` (macOS).

## Project layout

```
app/        SwiftUI shell (File Sorter, Document Chat, Table Chat, Settings, onboarding)
backend/    Go backend
  cmd/server/   HTTP server (chi router)
  internal/     config (provider wiring), scan, sorter, watcher, modelmanager, decisions, executor
  pkg/          provider interfaces and implementations: llm, embed, vector, sqlengine, parser
docs/       architecture overview and ADRs
dist/       output of `make dist`
```

### Session security and learning

The app generates a fresh in-memory bearer token on each backend launch. Manual development
requires `--insecure-dev`, or `ARCA_API_TOKEN` in the environment and a matching Authorization
header. Only `/health` is public; browser origins are rejected.

Inbox supports bulk moves above a chosen confidence level. Automatic folder mode requires an
explicit warning confirmation. Settings → Improve Suggestions opens optional calibration;
skipping and opening it do not change files or feedback. Number keys choose candidates, S skips,
and Escape closes the flow.

Confirmed vectors and correction outcomes are committed together in `decisions.db`. The semantic
engine overlays those examples on the folder index, preserving them across reindexing. Auto-mode
eligibility uses the last 100 resolved decisions; changing the target counts as a miss. Repeated
extension-to-folder corrections produce disabled rule suggestions after three corrections
(configurable with `decision.suggestion_threshold`). Review suggestions before enabling them.
See [learning and calibration](docs/api/learning.md) for details and limits.

## License

Arca is licensed under the [GNU Affero General Public License v3.0](LICENSE) (AGPL-3.0-only).
If you distribute a modified version, or let others interact with one over a network, you must
make the corresponding source available under the same license. Third-party components keep
their own (permissive) licenses.
