# ADR-0006: Decision engine: rules → semantic → LLM chain, with separate Policy and Executor

- **Status:** Proposed
- **Date:** 2026-10-12
- **Deciders:** Ameet Madan

## Context

Deciding what to do with a file is currently split up and ad hoc:

- The watcher always re-indexes, and it does so by calling its own HTTP API with hand-built JSON.
- The sorter only produces batch suggestions.
- `/api/sort/apply` moves files using paths from the request without checking that the
  result stays inside the target folder (`../` can escape it).

We want ongoing, explainable decisions (move / tag / index / ignore / ask) that can run
automatically once the user trusts them, and that the user can always undo.

## Decision

1. **Domain (`pkg/decision`):** `Engine.Decide(ctx, Facts) (Decision, error)`, where
   `Decision = {Action, Target, Confidence, Reason, Source}`. This package never touches the filesystem.
2. **Chain of engines** that stops at the first answer above its confidence threshold:
   1. `rules`: user rules in YAML, evaluated with [`expr-lang/expr`](https://github.com/expr-lang/expr). Confidence 1.0.
   2. `semantic`: the nearest folder centroid / kNN over the user's existing folders (ADR-0007).
   3. `llm`: the local LLM picks from a fixed list of folders, and its JSON output is checked against a schema.
   4. Optionally, a hosted provider (ADR-0008), off by default.
3. **Policy (application layer):** each folder has a mode (`off` / `suggest` / `auto`) and an
   auto-apply threshold. The default is `suggest`.
4. **Executor (application layer):** **the only code allowed to move or modify user files.**
   It checks that the target stays inside the root folder (using `filepath.Rel`), records each
   action in a SQLite journal, and supports undo.
5. **In-process pipeline:** the watcher, sorter, and API all publish events to a Go channel. This
   replaces the watcher's call to its own `/api/index`.
6. **API (versioned):** `/api/v1/decisions`, `/api/v1/decisions/{id}/accept|reject`,
   `/api/v1/rules`, `/api/v1/journal/{id}/undo`.
7. All engines are wired in `internal/config` (ADR-0004).

## Alternatives considered

- **A single LLM classifier for every file:** slow on every file event, can't be explained, and hard to trust with autonomous moves.
- **A full rule engine with forward-chaining inference (Drools / RETE-style, e.g. Grule):** too much for "if conditions match, do this". Hard for end users to write rules for.
- **CEL (`google/cel-go`) instead of `expr`:** more formally specified, but heavier, and its syntax is less friendly for non-developers. Worth revisiting if rules ever need to be shared between products.
- **Keeping decisions inside handlers:** mixes policy with HTTP and can't be tested.

## Consequences

- **Positive:** every action can be explained (`Reason`, `Source`) and undone. The path-escape bug is fixed structurally, not with a one-off patch. Cheap engines handle most files, and the LLM only sees the hard ones. The domain logic can be tested without infrastructure.
- **Negative / trade-offs:** more moving parts (queue, journal, policy). There are confidence thresholds to tune. Rule syntax becomes something users depend on, so it must stay stable.
- **Follow-ups:** phase 1 = types + Executor + journal and moving `/api/sort/apply` onto them. Phase 2 = rules. Phase 3 = semantic + LLM + watcher pipeline + Inbox UI.
