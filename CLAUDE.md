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
`9448` (moved from 9447 in v0.2.0; 9447 collided with `nsr_exporter`). OTLP gRPC: `4317`.

## Commands

```bash
make cli              # build ./bin/kemp_exporter
make test              # go test -race with an atomic coverage profile (NOT a plain go test)
make test-race          # go test -race -cover — lighter than `make test` despite the name
make lint              # golangci-lint run
make vuln              # govulncheck
make security          # semgrep — blocking, and part of `make ci`
make ci                # lint + test + build + vuln + security — the CI gate, run this before committing
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

The alert rules carry their own gate, not wired into `make ci`:

```bash
promtool check rules deploy/prometheus/kemp.rules.yml deploy/prometheus/kemp.rules.unconfirmed.yml
promtool test rules deploy/prometheus/tests/kemp.rules_test.yml
```

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
                               built, and the only place a label VALUE is
                               sanitised (ADR 0006)
  dropwarn.go                 bounds the "dropping sample" Warn lines both readers
                               emit: one per reason/metric/system per process
  tlsconfig.go                per-target *tls.Config; MinVersion 1.2
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

- **Absent, never zero.** A missing, unparseable, or non-finite numeric field
  produces no sample — never a fabricated `0`. Enforced at the single choke point
  `addSample` in `derivations.go`, backed by `models.Num`, which rejects NaN and
  ±Inf as well as text that does not parse.
- **Label-key invariant.** One label-key set per metric name, built only through
  the shared constructors in `metrics.go`. An unresolved name/status is an empty
  label *value*, never a missing label *key*. See
  `docs/adr/0006-label-key-union-invariant.md`.
- **Label values are sanitised in `metrics.go`, never in a reader.** Every
  appliance-supplied string passes through `cleanValue` (invalid UTF-8 → U+FFFD)
  in the label constructors. Sanitising in one reader and not the other is how the
  two readers once diverged, and invalid UTF-8 fails `proto.Marshal`, which drops
  the entire OTLP batch — not just the offending series.
- **A system name is validated at load, not just defaulted.** `config.Load`
  requires every `systems[].name` to be non-empty, unique, and valid UTF-8. The
  name becomes the `system` label on every metric as `cleanValue(name)`, so
  comparing raw names alone is not enough: two names differing only in invalid
  bytes collapse to one label value, and both readers' first-wins dedup then drops
  an entire appliance while `/metrics` and `/health` stay green.
- **No 4xx retry.** `newRestyClient`'s retry condition excludes 4xx responses:
  retrying a rejected credential against a LoadMaster with account lockout enabled
  locks the account. The JSON transport's single bounded session-refresh-on-401 is
  a deliberate, structural exception (exactly one retried call, no loop).
- **HTTP server starts before collection.** `run()` in `main.go` starts the HTTP
  listener first, then the collection loop — `/metrics` and `/health` are reachable
  (health reporting "starting") even before the first collection cycle completes.
- **Drained backends are excluded in the alert layer, never in the metric.**
  `statusToUp` maps "Disabled" to `0` on purpose: `kemp_*_up` means "is this
  serving traffic", which is what dashboards and SLOs want. The two Down alerts in
  `deploy/prometheus/kemp.rules.yml` exclude disabled objects themselves, matching
  case-insensitively and tolerating surrounding whitespace because the exporter
  trims before mapping. Do not "fix" a paging complaint by changing `statusToUp`.
  Equally, an unrecognised status yields no `_up` sample at all — the per-object
  `*StatusUnrecognised` alerts exist to surface that blind spot, and they must join
  on the full identity label set, since `on(system)` is satisfied by any sibling.
- **No inline suppressions.** No `//nolint`, no `// nosemgrep`, no `//#nosec` anywhere in the tree.
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
portable contract: `make ci` (lint + test + build + vuln + security) is what CI
actually runs — `test` is already race-enabled, and `security` runs semgrep as a
blocking step. `make sbom`/`make docs` back the SBOM and docs-site jobs. Semgrep's
default ignores skip `*_test.go`, so that gate covers production code only. Releases are GoReleaser-driven (`.goreleaser.yaml`):
cross-compiled binaries, checksums, a CycloneDX SBOM per release, a multi-arch GHCR
image, and an optional Homebrew cask gated on a cross-repo token.
