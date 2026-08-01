# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Standing practice: on each release, move `[Unreleased]` to a dated `[X.Y.Z]`
section rather than letting it accumulate indefinitely.

## [Unreleased]

## [0.2.0] - 2026-08-01

### Breaking

- The default metrics port moves from `9447` to `9448`. `kemp_exporter` and
  `nsr_exporter` both defaulted to `9447` and could not run on the same host;
  `nsr_exporter` is the older repo and keeps it. Update your Prometheus scrape
  configuration and any published container port mapping, or pin
  `server.port: "9447"` in your own `config.yaml` to keep the old behaviour. See
  [ADR 0009](docs/adr/0009-always-200-probes-and-port-9448.md).

### Changed

- `/health` now always answers `200`. It previously returned `503` before the
  first collection cycle and whenever the snapshot went stale, which made it
  unsafe as an orchestrator probe target — a liveness probe would restart an
  exporter that was merely starting up. The starting/stale verdict is unchanged;
  it is now reported in the response body (`starting: …`, `stale: last collection
  Ns ago`, or `ok`) instead of the status code.
- The container base image drops its `alpine:3.22` pin for `alpine:latest`,
  matching the rest of the exporter family.

### Added

- `/livez` and `/readyz` endpoints, both always `200`, both reading no exporter
  state at all. These are the endpoints orchestrator probes should target;
  `/health` remains the diagnostic endpoint. Neither probes `/metrics`, which
  would render the full exposition on every tick and can block behind a slow
  collection cycle.
- `HEALTHCHECK` in both `Dockerfile` and `Dockerfile.goreleaser`, and a matching
  `healthcheck:` in `docker-compose.yml` and `docker-compose.ghcr.yml`, all
  probing `http://127.0.0.1:9448/livez` on a 30s interval with a 5s timeout.

## [0.1.0] - 2026-07-31

### Added

- Initial implementation: dual-transport (XML/JSON) collection with runtime
  detection and a single derivation layer over both.
- Snapshot-based collection loop decoupling backend API load from scrape/export
  frequency; Prometheus `/metrics` and OTLP export paths render from the same
  immutable snapshot.
- 33-metric catalog covering target reachability, appliance-wide CPU/memory/TPS/
  traffic totals, per-interface counters, and per-virtual-service/per-real-server
  status, connection, and traffic metrics.
- `kemp_exporter_build_info` constant-1 metric for build/version identification.
- Config hot reload via `SIGHUP` and a directory-level file watch, fail-safe on a
  malformed config file.
- GoReleaser-driven release pipeline: cross-compiled binaries, checksums, a
  CycloneDX SBOM per release, a multi-arch GHCR image, and an optional Homebrew
  cask.
- Docker Compose demo stack (build-from-source and GHCR variants) with a
  provisioned Grafana overview dashboard and Prometheus alert rules.
- systemd unit with a hardened sandbox profile and `SIGHUP`-mapped
  `systemctl reload`.
- Full documentation site (MkDocs Material): metric catalog, dashboards guide,
  Docker/systemd deployment guides, and eight architecture decision records.

### Known limitations

- No LoadMaster appliance was available during development; every wire-level
  field path not directly documented by Kemp's own API reference is marked
  **unconfirmed** in `docs/metrics.md` pending live validation (see that
  document's live-validation checklist).
- No disk-utilization metric: the LoadMaster REST surface collected here has no
  identified equivalent to Grafana dashboard 12160's `dskAvail` panel.
- Extended surface (SubVS statistics, HA/cluster state, WAF counters, TLS
  certificate expiry) is deferred; certificate expiry is the highest-value
  addition for a load balancer and the leading candidate for the next release.
