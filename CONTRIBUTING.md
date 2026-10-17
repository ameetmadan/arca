# Contributing to Arca

Thanks for your interest. Bug reports, fixes and focused improvements are welcome.

## Before you start

- For anything larger than a small fix, open an issue first so we can agree on the approach.
- Read the [architecture overview](docs/architecture/README.md) and the
  [ADRs](docs/adr/README.md). Arca is local-first: a change that sends file content off the
  device must be opt-in and clearly labelled ([ADR-0002](docs/adr/0002-local-first-on-device-inference.md)).
- Report security problems privately; see [SECURITY.md](SECURITY.md).

## Setup

Building the whole app needs macOS on Apple Silicon; see the
[README](README.md#prerequisites). The Go backend builds and tests on any platform:

```sh
cd backend
go vet ./...
go test -race ./...
go test -tags sqlitevec,duckdb ./...   # SQLite and DuckDB providers (needs SQLite headers)
```

The Swift package is tested on macOS with `cd app && swift test`.

## Making a change

1. Branch from `main`: `feature/<name>` or `bugfix/<name>`.
2. Write a test first for a bug fix, and add tests for new behaviour. Keep tests fast and
   deterministic.
3. Run the commands above before pushing.
4. Use [conventional commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`, `docs:`,
   `refactor:`) and explain *why* in the message, not just what.
5. Open a pull request against `main`. Explain the why and any trade-offs, so the reasoning still
   makes sense in two years. Rebase on `main` first.

Wire new providers (LLM, embedder, vector store, SQL engine) only in `internal/config`; adding a
provider should not change anything else ([ADR-0004](docs/adr/0004-pluggable-providers-wired-in-config.md)).
Record significant design decisions as an ADR.

## License

Arca is licensed under the [AGPL-3.0](LICENSE). By submitting a contribution you agree that it is
licensed under the same terms.
