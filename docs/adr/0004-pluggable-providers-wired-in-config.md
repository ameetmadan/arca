# ADR-0004: Pluggable providers, wired only in `internal/config`

- **Status:** Accepted (backfilled)
- **Date:** 2026-10-12
- **Deciders:** Ameet Madan

## Context

Models, vector stores, and inference runtimes change quickly. We want to swap them without
touching handlers or business logic, and to test logic without CGo dependencies.

## Decision

- Each capability is an interface in `pkg/`: `llm.LLMProvider`, `embed.EmbeddingProvider`,
  `vector.VectorStore`, `parser.DocumentParser`, `sqlengine.SQLEngine`.
- Each concrete implementation lives in a subpackage, with a `_cgo.go` build-tagged real
  implementation and a stub for builds without CGo.
- **`internal/config` is the only place implementations are chosen**, using a `switch` on the
  provider name from YAML. Adding a provider means one new struct file and one new `case`.

## Consequences

- **Positive:** providers can be swapped and logic can be unit-tested with fakes (e.g. `sorter_test.go`).
- **Negative / trade-offs:** build tags must stay in sync across stubs. Errors can be vague when a stub is wired by mistake.
- **Follow-ups:** the decision engine (ADR-0006) and any hosted decision provider (ADR-0008) follow this same rule.
