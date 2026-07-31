# CLAUDE.md

Guidance for Claude Code (and any other agent) working in this repository.

## Overview

`kemp_exporter` is a Go Prometheus + OTLP exporter for Progress Kemp LoadMaster
appliances. One process monitors many LoadMasters, polls each on a shared interval,
derives a common set of metrics from whichever wire encoding (XML or JSON) each
appliance speaks, and serves them at `/metrics` and/or pushes them via OTLP. Full
documentation — metric catalog, dashboards, deployment guides, ADRs — is at
`docs/` and published to <https://fjacquet.github.io/kemp_exporter/>.

Module: `github.com/fjacquet/kemp_exporter`. Binary: `kemp_exporter`. Metrics port:
`9447`. OTLP gRPC: `4317`.

## Commands

```bash
make cli              # build ./bin/kemp_exporter
make test              # go test ./...
make test-race          # race detector + coverage
make lint              # golangci-lint run
make vuln              # govulncheck
make ci                # lint + test + build + vuln + semgrep — the CI gate, run this before committing
make sure              # fmt-check + vet + test + build — local convenience gate
make docker            # build the container image
make demo              # docker compose up --build (exporter + Prometheus + Grafana)
make docs              # mkdocs build --strict
make release-snapshot  # goreleaser release --snapshot --clean (local dry-run)
```

Run a single collection cycle and dump samples without starting the HTTP server:

```bash
./bin/kemp_exporter --config config.yaml --once --debug
```

`--trace` additionally logs full API response bodies (never headers; auth
responses are skipped) — useful for live validation against a real appliance, but
treat the output as sensitive (see `docs/metrics.md`'s live-validation checklist).

## Architecture

```
main.go                     cobra CLI (--config --debug --once --trace); starts the
                             HTTP server FIRST, then the collection loop
internal/config             YAML + ${ENV} + passwordFile + .env; SIGHUP + fsnotify
                             hot reload (ADR 0008)
internal/logging            logrus setup
internal/telemetry          OTLP manager
internal/kemp
  transport.go/_xml/_json    two wire encodings behind one `transport` interface,
                             runtime-detected per system (ADR 0004)
  auth.go                    JSON-transport session token: lazy login, single
                              bounded refresh on 401 (no 4xx retry storm)
  derivations.go              stats + listvs -> []Sample; the single derivation
                              layer above both transports
  metrics.go                  label-key constructors — the only place a []Label is
                               built (ADR 0006)
  snapshot.go / collector.go   background collection loop -> immutable Snapshot
                               behind an RWMutex-guarded pointer swap (ADR 0002)
  prometheus.go                PromCollector: Snapshot -> Prometheus registry gather
  otlp.go                      OTLPExporter: Snapshot -> OTLP observable gauges via
                               a PeriodicReader (production) / ManualReader (tests)
  buildinfo.go                 kemp_exporter_build_info constant-1 gauge
internal/models              decoded payload types shared by both transports
                              (Num tracks parsed-or-absent; avoids an import cycle
                              with internal/kemp)
internal/dashboards           dashboard-vs-metric-catalog consistency tests
```

## Load-bearing constraints

- **Absent, never zero.** A missing or unparseable numeric field produces no
  sample — never a fabricated `0`. Enforced at the single choke point
  `addSample` in `derivations.go`, backed by `models.Num`'s parsed/absent tracking.
- **Label-key invariant.** One label-key set per metric name, built only through
  the shared constructors in `metrics.go`. An unresolved name/status is an empty
  label *value*, never a missing label *key*. See
  `docs/adr/0006-label-key-union-invariant.md`.
- **No 4xx retry.** `newRestyClient`'s retry condition excludes 4xx responses:
  retrying a rejected credential against a LoadMaster with account lockout enabled
  locks the account. The JSON transport's single bounded session-refresh-on-401 is
  a deliberate, structural exception (exactly one retried call, no loop).
- **HTTP server starts before collection.** `run()` in `main.go` starts the HTTP
  listener first, then the collection loop — `/metrics` and `/health` are reachable
  (health reporting "starting") even before the first collection cycle completes.
- **No inline suppressions.** No `//nolint`, no `// nosemgrep` anywhere in the tree.
  A false-positive lint/semgrep finding is fixed at its root cause or addressed in
  the project-level tool config — never silenced inline. The Definition of Done
  greps for both patterns.
- **`Num` lives in `internal/models`, not `internal/kemp`, to avoid an import
  cycle.** `internal/models` is imported by `internal/kemp`; if `Num`'s
  parsed/absent tracking lived in `internal/kemp` instead, `internal/models`
  would need to import back into `internal/kemp` to use it, which Go disallows.
- **Every sample renders as `prometheus.GaugeValue`**, including `_total` counters
  — a deliberate family convention (ADR 0005), not an oversight.
- **TLS minimum 1.2; `insecureSkipVerify` is per-target, operator-controlled,
  defaults to `false`.** Never hardcode `InsecureSkipVerify: true` — that is
  precisely the defect this project avoided by not depending on
  `giantswarm/kemp-client` (ADR 0003).
- **No secrets in logs.** Never `resty.SetDebug`; resty's own default logger is
  replaced with a no-op (see `newRestyClient`'s comment) because it logs full
  request URLs — apikey query parameter included — unconditionally.

## Testing

TDD is the default workflow: a failing test first, then the minimum code to pass,
then refactor. Two standing invariant tests gate every change:

- `internal/kemp/transport_parity_test.go` — XML and JSON transports must decode
  identical fixtures to an identical `Statistics` (ADR 0004's guard).
- `internal/dashboards/dashboards_test.go` — every metric referenced by
  `grafana/kemp-overview.json` must exist in the exporter's own known-metric set,
  and no per-second gauge may ever be wrapped in `rate()`.

Run `make test-race` before opening a PR. New metrics need: a label-key constructor
in `metrics.go` (or reuse of an existing one), a derivation in `derivations.go`
using `addSample`, an entry in `docs/metrics.md`, and — if dashboard-visible — an
entry in `internal/dashboards/dashboards_test.go`'s `knownMetrics` map.

## CI/CD

`.github/workflows/{ci,docs,release,security}.yml` are thin callers into the
shared `fjacquet/ci` reusable workflows, pinned to the `@v1` tag (a deliberate
first-party trade-off — see
`docs/adr/0001-supply-chain-and-release-hardening.md`). The Makefile is the
portable contract: `make ci` (lint + test-race + build + govulncheck + semgrep) is what CI
actually runs; `make sbom`/`make security`/`make docs` back the SBOM, Semgrep, and
docs-site jobs respectively. Releases are GoReleaser-driven (`.goreleaser.yaml`):
cross-compiled binaries, checksums, a CycloneDX SBOM per release, a multi-arch GHCR
image, and an optional Homebrew cask gated on a cross-repo token.
