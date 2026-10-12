# ADR-0008: Hosted decision models (e.g. Jev) are optional, opt-in providers only

- **Status:** Proposed
- **Date:** 2026-10-12
- **Deciders:** Ameet Madan

## Context

Jev, from TypeSafe, is a probabilistic decision model released in early access on
2026-09-15. Its "Choice" question type picks an option from a fixed set and returns a
probability per option and a confidence score. That closely matches our `decision.Engine`
contract, and it would likely be better calibrated than a 3B local model.

However:

- Everything we found describes it as a hosted API. Using it would send file names and
  content snippets off the device, which conflicts with ADR-0002.
- It is very new. The available reviews are documentation and architecture reviews, not
  production reports or independent benchmarks.
- It adds a network dependency, latency, cost per call, and an API key to manage.

Unverified: whether Jev can run locally or on-device. If it can, this ADR should be revisited.

## Decision

- Hosted decision models are **not** part of the default engine chain.
- They may be added later as an **optional provider** behind `decision.Engine` (one struct file
  plus one `case` in `internal/config`, per ADR-0004). They would run only as the final fallback
  for files nothing else could place.
- Turning one on requires explicit opt-in **per watched folder**, with a clear
  "sends data off this Mac" label. The API key is stored in the macOS Keychain (not in
  `config.yaml`), and only the minimum is sent (snippet + candidate folder names, never the whole file).

## Alternatives considered

- **Jev as the primary engine:** best accuracy on unusual files, but gives up the local-first promise and depends on an early-access vendor.
- **Never integrate hosted models:** simplest, but closes off a useful option for users who choose it.

## Consequences

- **Positive:** the privacy default is kept. The pluggable design leaves the door open. Evaluation can happen without committing to it.
- **Negative / trade-offs:** accuracy on unusual files is capped by local models for most users. A second path (network, keys, errors) has to be maintained once added.
- **Follow-ups:** read TypeSafe's official docs on deployment options and data retention. If we move ahead, benchmark Jev against the local chain on a labelled set of real folders.

## References

- https://www.cometapi.com/en/models/typesafe-ai/jev
- https://www.blockchain-council.org/ai/jev-as-a-decision-layer/
- https://wavect.io/blog/jev-ai-decision-model-review/
