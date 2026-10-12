# ADR-0001: Record architecture decisions

- **Status:** Accepted
- **Date:** 2026-10-12
- **Deciders:** Ameet Madan

## Context

Until now, Arca's architecture has lived in `PLAN.md` (the original build prompt), one design
spec under `docs/superpowers/`, and the code. That shows *what* exists but not *why*, and the
code has already drifted from `PLAN.md`. For example, the plan names Phi-4 mini / Gemma 3 4B,
while `modelmanager` ships Llama 3.2 3B / Qwen 2.5 3B. With a decision engine coming, we need
a durable record of the reasoning that still makes sense two years from now.

## Decision

Record significant architectural decisions as ADRs in `docs/adr/`, using the Nygard format
(see `template.md`). Keep the diagrams in `docs/architecture/README.md` up to date with them.
Decisions that already exist in the code are backfilled as ADRs marked "backfilled".

## Alternatives considered

- **Keep extending `PLAN.md`:** it's a prompt, not a decision log, and doesn't record alternatives or trade-offs.
- **Wiki / Notion:** goes stale away from the code and isn't reviewed in PRs.

## Consequences

- **Positive:** reviewers can see the reasoning and trade-offs in the same PR as the code.
- **Negative:** a bit of extra writing for each significant change.
- **Follow-ups:** PRs that change architecture should link to or add an ADR.
