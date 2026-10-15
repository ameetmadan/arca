# ADR-0010: The app restarts the server to apply watched-folder and model changes

- **Status:** Proposed
- **Date:** 2026-10-15
- **Deciders:** Ameet Madan
- **Supersedes / Superseded by:** —

## Context

Watched folders were stored only in the app's UserDefaults and never reached the server, which
reads `watcher.folders` from `config.yaml`. Several server components take the folder list at
start-up: the watcher, the decider's roots, the semantic folder indexer, the LLM engine's
candidate folders and the file scanner. Likewise, the semantic indexer and the scanner run at
start and simply fail while the embedding model is missing.

## Decision

The server owns the settings. `PUT /api/v1/settings/folders` validates and saves the folders to
`config.yaml` and answers `restart_required: true`; the app then restarts the server (waiting for
the old process to exit so DuckDB's file lock is released). The app also restarts it after
activating a model, so indexing starts with the embedding model in place.

## Alternatives considered

- **Make every component reconfigurable at runtime:** each would need locking and a way to add
  and drop roots (and the semantic index to rebuild), across five packages. More code and more
  ways to get it subtly wrong, for a change users make rarely.
- **Keep folders in the app and pass them as flags:** splits configuration between two places
  and breaks running the server on its own.

## Consequences

- **Positive:** one source of truth (`config.yaml`); start-up is the only code path that wires
  folders, and it is already tested.
- **Negative / trade-offs:** a folder change interrupts in-flight requests (a chat answer being
  streamed) and takes a second or two; the UI shows "Restarting…".
- **Follow-ups:** if folder changes become frequent, make the watcher and scanner live first;
  they are the parts users notice.
