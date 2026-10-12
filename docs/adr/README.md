# Architecture Decision Records

This folder holds the significant architectural decisions for Arca, using the
[Michael Nygard format](https://cognitect.com/blog/2011/11/15/documenting-architecture-decisions):
Context, Decision, Consequences, plus the alternatives that were considered.

- Copy [`template.md`](template.md) to `NNNN-short-title.md`, using the next free number.
- Status is one of: `Proposed` · `Accepted` · `Deprecated` · `Superseded by ADR-NNNN`.
- Never rewrite an accepted ADR. To change a decision, write a new ADR that supersedes it.
- ADRs 0002–0005 were written on 2026-10-12 to record decisions already in the code.

| # | Title | Status | Date |
|---|---|---|---|
| [0001](0001-record-architecture-decisions.md) | Record architecture decisions | Accepted | 2026-10-12 |
| [0002](0002-local-first-on-device-inference.md) | Local-first, on-device inference | Accepted | 2026-10-12 (backfilled) |
| [0003](0003-swiftui-shell-with-embedded-go-backend.md) | SwiftUI shell with embedded Go backend over localhost HTTP | Accepted | 2026-10-12 (backfilled) |
| [0004](0004-pluggable-providers-wired-in-config.md) | Pluggable providers, wired only in `internal/config` | Accepted | 2026-10-12 (backfilled) |
| [0005](0005-content-based-sorting-embed-cluster-name.md) | Content-based sorting: embed → cluster → name | Accepted | 2026-05-11 (backfilled) |
| [0006](0006-decision-engine-chain-with-policy-and-executor.md) | Decision engine: rules → semantic → LLM chain, with separate Policy and Executor | Proposed | 2026-10-12 |
| [0007](0007-learn-from-existing-folders-not-forced-training.md) | Learn from existing folders and corrections instead of forced up-front training | Proposed | 2026-10-12 |
| [0008](0008-hosted-decision-models-optional-only.md) | Hosted decision models (e.g. Jev) are optional, opt-in providers only | Proposed | 2026-10-12 |

See [`../architecture/README.md`](../architecture/README.md) for the diagrams.
