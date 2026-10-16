# ADR-0009: Releases are built in CI on macOS and ad-hoc signed

- **Status:** Proposed
- **Date:** 2026-10-15
- **Deciders:** Ameet Madan
- **Supersedes / Superseded by:** —

## Context

Arca needs a downloadable build for testers. The app links llama.cpp (Metal), sqlite-vec and
DuckDB through CGo and is a SwiftUI app, so it can only be built on macOS. Development also
happens on Linux machines, which cannot produce the app. There is no Apple Developer ID
certificate in the project yet.

## Decision

A GitHub Actions workflow builds `Arca.app` on a `macos-15` (Apple Silicon) runner on every pull
request and push to `main`, smoke-tests the bundled server (it must start on an empty home
directory and answer `/health`, `/api/v1/settings` and `/api/v1/models`), and uploads a zip. A
`v*` tag publishes that zip as a GitHub Release. llama.cpp is pinned (`LLAMA_CPP_REF`) and its
build cached. The app and the nested server are ad-hoc signed (`codesign -s -`), not notarized.

## Alternatives considered

- **Build releases by hand on a developer Mac:** not reproducible, and nothing checks that the
  Swift code compiles when work is done on Linux.
- **Developer ID signing and notarization now:** the right end state, but it needs a paid Apple
  account and secrets in CI. The workflow has a single signing step where this slots in.
- **Universal (arm64 + x86_64) build:** llama.cpp with Metal targets Apple Silicon; Intel Macs
  would run inference on the CPU and be too slow to be useful. Not worth doubling CI time yet.

## Consequences

- **Positive:** every PR proves the app builds and the server starts on a fresh machine; a
  release is one `git push` of a tag.
- **Negative / trade-offs:** testers must clear Gatekeeper's quarantine once (documented in the
  release notes); macOS minutes on a private repository cost more than Linux minutes, which is
  why the macOS job does not run on every branch push.
- **Follow-ups:** Developer ID signing and notarization; bump `LLAMA_CPP_REF` deliberately,
  since the CGo code calls `llama.h` directly.
