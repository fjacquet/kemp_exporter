# 0009. Static liveness/readiness probes, an always-200 /health, and port 9448

- **Status:** accepted
- **Date:** 2026-08-01
- **Deciders:** Frederic Jacquet

## Context and problem statement

Three problems, all discovered together when the exporter family's standard was
audited against the repos it had actually been applied to.

First, `/health` returned `503` in two cases: before the first collection cycle
completed, and whenever the newest snapshot was older than two collection
intervals plus one timeout. That is honest reporting, but it makes `/health`
actively dangerous as an orchestrator probe target — and it is the only
health-shaped endpoint the exporter had, so it is the one operators wire up. A
Kubernetes liveness probe pointed at it restarts an exporter that is merely
waiting on its first cycle; a readiness probe depools one whose LoadMaster is
briefly unreachable. Neither condition is one the process can fix by dying, and a
restart resets the snapshot, making the starting case self-perpetuating under a
tight `initialDelaySeconds`.

Second, the images carried no `HEALTHCHECK` and the compose files no
`healthcheck:`, so Docker and Compose had no signal at all beyond "the process has
not exited".

Third, `kemp_exporter` and `nsr_exporter` both defaulted to port `9447`. The two
cannot run on the same host without one of them being reconfigured. The family's
`9438`–`9446` port block exists precisely to prevent this; neither repo was in the
family table when it was allocated, which is how the collision survived a release
of each.

## Considered options

- **Keep `/health` as the only endpoint, and document that probes must tolerate
  503.** Costs nothing to implement and pushes the problem onto every operator,
  who will get it wrong once each.
- **Add `/livez` and `/readyz` but leave `/health` returning 503.** Fixes the probe
  story for anyone who reads the docs, leaves a loaded gun for anyone who does not.
- **Add `/livez` and `/readyz` as always-200 static handlers, and make `/health`
  always answer 200 with the verdict in its body.** Probes get an endpoint that
  cannot fail; diagnostics keep every bit of information they had.
- For the port: **move `nsr_exporter` to 9448** (it is the older, more widely
  deployed repo) versus **move `kemp_exporter` to 9448** (it is at v0.1.0 with a
  far smaller install base).

## Decision outcome

Chosen: **static `/livez` + `/readyz`, an always-200 `/health`, a container
`HEALTHCHECK` on `/livez`, and `kemp_exporter` moves to port 9448.**

`/livez` and `/readyz` are both wired to one handler that reads no state
whatsoever — not the snapshot, not the collection loop, not `kemp_up`. It writes
`200 ok` and nothing else. A probe pointed at it can never be the reason a healthy
process is restarted or pulled from rotation, which is the only property a probe
endpoint actually needs.

`/health` keeps the snapshot-age logic verbatim and reports `starting: …`,
`stale: last collection Ns ago`, or `ok` in the body, always under a `200`. It
remains the endpoint for a human or a dashboard asking whether collection is
current. An endpoint that answers that question by refusing to answer is useless
exactly when it matters.

Neither probe touches `/metrics`. Rendering the full exposition every thirty
seconds to answer a healthcheck is needless load, and it can block behind a slow
collection cycle — the failure mode these endpoints exist to be immune to.

The `HEALTHCHECK` uses `http://127.0.0.1:9448/livez`, never `localhost`. Alpine's
busybox `wget` resolves `localhost` via `::1` first and this exporter binds IPv4
only, so a `localhost`-based check fails at runtime with connection refused while
passing both `hadolint` and `docker compose config`. The timeout is `5s` in the
image and in every compose file alike.

On the port: `nsr_exporter` is at v0.12.4 against this repo's v0.1.0, so
`kemp_exporter` is the one that moves. `9448` is the next free port after the
family's `9438`–`9446` block. This is a **breaking change** for anyone relying on
the default; operators who pin `server.port` in their own `config.yaml` are
unaffected.

The base image also drops its `alpine:3.22` pin for `alpine:latest`, matching all
fifteen repos in the family. This cuts against ADR 0001's supply-chain posture —
the Go toolchain, the linters and every GitHub Action are pinned, so
`alpine:latest` becomes the one input whose contents can change between two builds
of the same commit, which is exactly what the SBOM and provenance attestations
exist to pin down. Uniformity across fifteen repos was chosen over reproducibility
on three; revisiting it is a family-wide decision, not a per-repo one.

### Consequences

- Probes are safe to wire by default; `/health` is safe to wire by accident.
- Nothing in the exporter can now report unhealthy via HTTP status. Genuine
  backend failure is visible where it always was: `kemp_up`, the `/health` body,
  and the bundled Prometheus alert rules.
- Users upgrading must update their scrape configuration and any published port
  mapping from `9447` to `9448`, or pin `server.port: "9447"` in their config.
- The published image's contents can differ between two builds of the same commit,
  by the amount `alpine:latest` moved.
