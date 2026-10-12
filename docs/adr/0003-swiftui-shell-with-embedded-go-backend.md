# ADR-0003: SwiftUI shell with embedded Go backend over localhost HTTP

- **Status:** Accepted (backfilled)
- **Date:** 2026-10-12
- **Deciders:** Ameet Madan

## Context

Arca needs a native macOS UI as well as a backend with access to llama.cpp, sqlite-vec, DuckDB,
and document parsers. Go has a mature library ecosystem for these and simple CGo integration.
SwiftUI is the natural choice for the UI.

## Decision

- `app/`: a thin SwiftUI shell. `BackendProcess.swift` starts `arca-server` from the app
  bundle's Resources when the app launches and stops it when the app quits.
- `backend/`: a Go binary serving a chi router on `localhost:44742`.
- The UI talks to the backend **only** over HTTP (JSON, and SSE for streaming), and every
  call goes through `APIClient.swift`.

## Alternatives considered

- **All-Swift:** would mean re-implementing or wrapping every native library in Swift.
- **Go as a C library linked into Swift:** no process isolation, and harder to test the API on its own.
- **XPC service:** more native, but locks the backend to Apple IPC and makes testing with curl harder.

## Consequences

- **Positive:** the backend can be tested and developed without the UI (`make backend-stub`). A crash in inference doesn't take down the UI. There is a clear API contract.
- **Negative / trade-offs:** port collisions are possible. Any local process can call the API, since CORS doesn't restrict non-browser clients. The two processes' lifecycles must be kept in sync.
- **Follow-ups:** consider a per-launch auth token passed from the app to the backend. Add graceful shutdown and `/health`.
