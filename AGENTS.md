# AGENTS.md

Entry point for coding agents. Humans: see [CONTRIBUTING.md](CONTRIBUTING.md).

Arca is a local-first macOS app: a SwiftUI shell (`app/`) that embeds a Go backend (`backend/`).
The shared coding standards in [ameetmadan/standards](https://github.com/ameetmadan/standards)
apply unless this file overrides one. Rules are in its `rules/` directory, referenced by ID
(for example GIT-001).

## Read first

- [README.md](README.md) for prerequisites and what the app does.
- [docs/architecture/README.md](docs/architecture/README.md) and the [ADRs](docs/adr/README.md).
  Arca is local-first: anything that sends file content off the device must be opt-in and
  clearly labelled ([ADR-0002](docs/adr/0002-local-first-on-device-inference.md)).
- [CONTRIBUTING.md](CONTRIBUTING.md) for the change workflow.

## Toolchain and lockfiles

- Go backend: module `backend/go.mod`, lockfile `backend/go.sum`. The Go version and toolchain
  come from `go.mod`; CI reads the same file. Do not edit versions by hand; Renovate does it.
- Swift app: `app/Package.swift`, macOS 14+ on Apple Silicon. It has no third-party dependencies.

## Verify a change

Run the commands for the workspace you touched. Backend, on any platform (from `backend/`):

```sh
go vet ./...
go test -race ./...
go test -tags sqlitevec,duckdb ./...   # SQLite and DuckDB providers; needs SQLite headers
```

Swift app, macOS only (from `app/`):

```sh
swift test
```

`ARCA_ACCEPTANCE_TESTS=1 swift test` also runs the native UI acceptance checks. CI sets it and
it needs the `arca-server` binary next to the package (`go build -o ../app/arca-server ./cmd/server`
from `backend/`). Do not run it as routine verification.

Dependency advisories are checked by `govulncheck` in `.github/workflows/audit.yml`; to run it
locally: `cd backend && go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...`.

## What needs macOS or CGo

- The full app (`make dist`, `make backend`, `swift test`, `swift build`) needs macOS on Apple
  Silicon, Xcode tools and a llama.cpp build (`make llama-build`). It cannot be verified on Linux.
- The `llamacpp` build tag links llama.cpp through CGo (`backend/pkg/{llm,embed}`). Without the tag
  those providers are stubs, which is what the plain `go test ./...` above exercises.
- The `sqlitevec` and `duckdb` tags also use CGo (SQLite headers needed).
- If you cannot run a macOS-only check, say so; do not claim it passed.

## What does not exist

- There is no lint script or formatter config beyond `go vet`. Do not claim lint passed.
- There is no Swift type-check step apart from `swift test`.

## Architecture rules

- Wire providers (LLM, embedder, vector store, SQL engine) only in `backend/internal/config`.
  Adding a provider must not change anything else
  ([ADR-0004](docs/adr/0004-pluggable-providers-wired-in-config.md)).
- Record significant design decisions as an ADR in `docs/adr/` and add a row to its README.

## Never routine verification

- `make dist`, `make backend` and `make llama-build` build release artifacts or large native
  dependencies. `make clean` deletes `dist/` and the built binaries.
- Pushing a `v*` tag publishes a GitHub Release from CI. Never tag as part of a change.
- Do not run the app against real user folders; use a scratch folder
  ([docs/TESTING.md](docs/TESTING.md)).

## Git

Branch from `main`, open a PR, squash-merge. Commit messages and PR titles use Conventional
Commits (`feat:`, `fix:`, `docs:`, `chore:`, ...) and explain why. A `PR title` check enforces this.
