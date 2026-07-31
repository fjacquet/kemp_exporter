# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Standing practice: on each release, move `[Unreleased]` to a dated `[X.Y.Z]`
section rather than letting it accumulate indefinitely.

## [Unreleased]

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
