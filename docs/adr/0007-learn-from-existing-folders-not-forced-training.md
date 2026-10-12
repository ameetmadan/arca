# ADR-0007: Learn from existing folders and corrections instead of forced up-front training

- **Status:** Proposed
- **Date:** 2026-10-12
- **Deciders:** Ameet Madan

## Context

The semantic engine (ADR-0006) needs to know where *this* user puts *this kind* of file. One
option was to make users sort 50–100 files by hand during onboarding to train a personal model.

Problems with that:

- **Friction:** it's a lot of manual work before Arca does anything useful, and many users will drop out.
- **Thin data:** 100 labels over 10–15 folders is about 7 examples per class, too few for a reliable classifier.
- **Wasted labels:** randomly chosen files are mostly obvious and teach the model little.

Most users have *already* sorted thousands of files into folders, and that structure is free
training data.

## Decision

1. **Learn from existing folders:** embed the files already in each subfolder of a watched root,
   and store a centroid plus example vectors per folder in sqlite-vec. Place new files by
   nearest centroid / kNN, with confidence taken from the similarity margin between the best
   and second-best folders.
2. **Optional calibration, not required:** offer to show the user **10–20 of the most uncertain
   files** (smallest margin, or sitting between two folders) to confirm. Present this as
   "help Arca learn your system", and let them skip it.
3. **Learn from every decision:** accepting or rejecting a suggestion updates the folder centroids
   straight away. If the same correction happens N times (default 3), suggest turning it into
   a rule (ADR-0006, engine ①).
4. **Earn autonomy:** a folder becomes eligible for `auto` mode only after its acceptance rate is ≥ 90% over at least 20 decisions.
5. **Cold start:** folders with no structure (e.g. Downloads) use the existing cluster-and-name
   sorter (ADR-0005). The user's first accepts and rejects of those clusters then serve as the
   initial training data.

## Alternatives considered

- **Mandatory training on 50–100 files:** rejected for the reasons above.
- **A trained classifier (logistic regression / small MLP) on embeddings:** possible later, once a user has hundreds of corrections. kNN/centroids need no training step, update immediately, and are easy to explain ("closest to the 40 files in `Taxes/2025`").
- **A general-purpose decision model (e.g. Jev) as the primary engine:** see ADR-0008.

## Consequences

- **Positive:** useful from the first launch with no work from the user. Fits the user's own system. Fully local. Easy to explain.
- **Negative / trade-offs:** less clever than a general model on unusual files. Inherits a messy existing structure. Initial embedding of large folders takes time, so it must run in the background with progress shown.
- **Follow-ups:** decide how to treat very large folders (sample N files per folder). Define the margin → confidence mapping. Add metrics for acceptance rate per folder.
