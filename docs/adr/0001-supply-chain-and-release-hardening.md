# 0001. Supply-chain and release hardening

- **Status:** accepted
- **Date:** 2026-07-31
- **Deciders:** Frederic Jacquet

## Context and problem statement

`kemp_exporter` ships four GitHub Actions workflows: `ci.yml`, `docs.yml`,
`release.yml`, `security.yml`. All four are **thin callers** — each contains no
direct action steps, only a single `uses: fjacquet/ci/.github/workflows/<name>.yml@v1`
call to a reusable workflow in a separate, shared `fjacquet/ci` repository. This is a
deliberate split: the Makefile contract (`make ci`, `make sbom`, `make security`,
`make docs`, `make release`) lives in this repo and is portable to any CI system; the
Action-level plumbing that runs those targets — SHA-pinned actions, credential
scoping, caching policy — is centralized once in `fjacquet/ci` and shared across every
sibling exporter (`pflex_exporter`, `pstore_exporter`, `ppdd_exporter`, etc.) rather
than copy-pasted and drifting per repo.

That split has a consequence worth stating plainly: **this repository cannot verify,
from its own tree, that the shared workflows SHA-pin their actions, drop credential
persistence, or disable caching on release.** Those properties live in `fjacquet/ci`,
a different repository with its own history and its own review process. What *is*
verifiable here is what the caller files themselves do, and they pin the shared
workflow reference itself to the **mutable tag `@v1`**, not a commit SHA.

## Considered options

- Say nothing about this split and let ADR-0001 assert guarantees (SHA-pinning,
  `persist-credentials: false`, `cache: false`) as if they were enforced by this repo.
- Inline the full Action-level hardening into each of the four workflow files here,
  duplicating what `fjacquet/ci` already does, so every guarantee is locally verifiable.
- Document the split honestly: state which guarantees are inherited from `fjacquet/ci`
  and are out of scope for this repo's own audit trail, which guarantees this repo does
  enforce directly, and treat the caller-level `@v1` pin as a named, deliberate
  trade-off rather than an oversight.

## Decision outcome

Chosen option: **"document the split honestly"**, because asserting a guarantee this
repository cannot back up is worse than not asserting it — it stops the next reader
from checking, and a security review that trusts this ADR's word for SHA-pinning
would be trusting the wrong repository's commit history.

### What is enforced here, verifiably, in this repo

- `permissions: contents: read` is the default at the top of every workflow file,
  narrowed further per job only where a job needs more (`release.yml` needs
  `contents: write` + `packages: write` + `id-token: write` to publish; `docs.yml`
  needs `pages: write` + `id-token: write` to deploy).
- `concurrency` groups on `ci.yml`, `docs.yml`, and `security.yml` prevent overlapping
  runs from racing each other.
- `.goreleaser.yaml` genuinely produces a CycloneDX SBOM per release, locally
  reproducible: the `sboms` stanza runs `cyclonedx-gomod mod -licenses -json` and
  `make release-snapshot` exercises the identical path without a real tag. This is the
  one supply-chain guarantee from the original scope that this repo can and does prove
  to itself — `goreleaser check` and a snapshot release are both run from this tree.
- The caller-level pin on all four workflows is `fjacquet/ci/.github/workflows/<n>.yml@v1`
  — a **mutable major-version tag**, not a SHA. This is a deliberate first-party
  trade-off: `fjacquet/ci` is a trusted, first-party repository shared across every
  sibling exporter, and pinning its consumers to `@v1` lets a fix or hardening
  improvement in `fjacquet/ci` roll out to every consumer without a PR in each one.
  The cost is that a compromise of `fjacquet/ci` at the `v1` tag would affect every
  consumer simultaneously; that risk is accepted here in exchange for centralized
  maintenance, and is the same trade-off every sibling exporter makes.

### What is inherited from `fjacquet/ci` and not verifiable from this tree

- SHA-pinning of the individual actions used *inside* the reusable workflows
  (`actions/checkout`, `actions/setup-go`, the Semgrep/CodeQL/osv steps, etc.).
- `persist-credentials: false` on any checkout step inside those reusable workflows.
- `cache: false` on the release job's `setup-go` step (or equivalent), if present.
- Any Dependabot configuration that keeps those pins current.

Auditing those requires cloning and reading `fjacquet/ci` directly; this repo's own
CI logs and workflow files are not sufficient evidence for them.

### Consequences

- Good — the ADR states only what this repo can prove about itself, so a reader who
  checks it against the actual `.github/workflows/*.yml` files will find it accurate.
- Good — the CycloneDX SBOM claim is real, local, and independently reproducible via
  `make release-snapshot`.
- Neutral — auditing the action-level hardening (SHA-pins, credential scoping inside
  the reusable workflows) requires a second repository (`fjacquet/ci`); that repository
  is out of scope for this ADR and for this project's own security review.
- Bad — the caller-level `@v1` pin means a compromised or misbehaving `fjacquet/ci`
  release affects this repo (and every sibling) without an intervening review step
  here; mitigated only by `fjacquet/ci` being first-party and by the low blast radius
  of a `contents: read`-scoped CI job.

## Pros and cons of the options (optional)

### Assert guarantees this repo cannot verify
- Good: reads well, matches the aspirational hardening story.
- Bad: false confidence; a security reviewer following this ADR's claims into
  `.github/workflows/` would find none of the SHA-pins or `persist-credentials` flags
  it describes, and would reasonably distrust the rest of the document.

### Inline full hardening locally, duplicating `fjacquet/ci`
- Good: every guarantee becomes locally auditable.
- Bad: reintroduces the exact per-repo drift `fjacquet/ci` was created to eliminate;
  a fix to one exporter's pinned SHA would not propagate to its siblings.
