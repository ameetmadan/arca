# Security policy

Arca reads the files in the folders you choose and runs a local HTTP API, so we take reports
seriously.

## Reporting a vulnerability

Please **do not open a public issue** for a security problem. Use GitHub's private reporting
instead: on the repository page go to **Security → Report a vulnerability**. Include what you
found, how to reproduce it, and the Arca version (`arca-server --version` or the app's About
box).

This is a small project maintained by one person, so expect an acknowledgement within a week
and a fix or a plan within a few weeks, depending on severity. Please give us a reasonable
window to ship a fix before publishing details.

## Supported versions

Only the latest release receives security fixes. Releases are pre-1.0.

## What is in scope

- The Go backend (`backend/`) and its HTTP API on `localhost:44742`.
- The macOS app (`app/`) and how it launches and talks to the backend.
- The release pipeline (`.github/workflows/build.yml`) and the published `Arca.app`.
- Anything that causes files to be moved, deleted or overwritten outside what the user approved,
  or that sends file content off the device.

## Security model, in short

- **Local-first.** Parsing, embeddings, vector search, SQL and LLM inference run on the device.
  The only network access is downloading model files from Hugging Face, pinned to a commit and
  verified against a SHA256 ([ADR-0002](docs/adr/0002-local-first-on-device-inference.md)).
- **Loopback only, authenticated.** The backend listens on `localhost`. The app generates a
  random bearer token per launch and passes it to the backend through the environment. Requests
  without it are rejected, as are requests carrying an `Origin` header (browser access is
  disabled). `--insecure-dev` turns authentication off and is for development only.
- **Moves are journaled and reversible**, and the executor refuses to act outside the watched
  roots.

## Known limitations

- Release builds are ad-hoc signed, not notarized
  ([ADR-0009](docs/adr/0009-ci-built-ad-hoc-signed-releases.md)). Verify the published
  `.sha256` file before running a download.
- Any process running as the same macOS user can read the backend's environment and files; Arca
  does not defend against a compromised user account.
- Document parsers process untrusted files. Parser crashes or resource exhaustion from a
  malformed file are in scope for reports.
