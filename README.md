# kemp_exporter

[![CI](https://github.com/fjacquet/kemp_exporter/actions/workflows/ci.yml/badge.svg)](https://github.com/fjacquet/kemp_exporter/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/fjacquet/kemp_exporter?include_prereleases&sort=semver)](https://github.com/fjacquet/kemp_exporter/releases/latest)
[![Go Report Card](https://goreportcard.com/badge/github.com/fjacquet/kemp_exporter)](https://goreportcard.com/report/github.com/fjacquet/kemp_exporter)
[![Go Version](https://img.shields.io/github/go-mod/go-version/fjacquet/kemp_exporter)](go.mod)
[![License](https://img.shields.io/github/license/fjacquet/kemp_exporter)](LICENSE)
[![Docs](https://img.shields.io/badge/docs-mkdocs--material-blue)](https://fjacquet.github.io/kemp_exporter/)

A Go Prometheus + OTLP exporter for **Progress Kemp LoadMaster** appliances. One process
monitors many LoadMasters, polls each on an interval, and serves metrics at `/metrics`
(Prometheus) and/or pushes them via OTLP. Full documentation, including the metric
catalog and the architecture decision records, is published at
<https://fjacquet.github.io/kemp_exporter/>.

## Quick start

```bash
make cli
export KEMP1_HOSTNAME='lm-prod-01.example.com'
export KEMP1_APIKEY='your-read-only-api-key'
./bin/kemp_exporter --config config.yaml
# metrics: http://localhost:9448/metrics
```

## Container image

```bash
make docker
docker run -p 9448:9448 \
  -e KEMP1_HOSTNAME='lm-prod-01.example.com' \
  -e KEMP1_APIKEY='your-read-only-api-key' \
  kemp_exporter:dev
```

## Configuration

See `config.yaml` for the full schema: server, collection interval/timeout/concurrency,
optional OTLP export, and one entry per LoadMaster under `systems`. Secrets
(`apiKey`, `password`) accept `${ENV_VAR}` references and are resolved at load time; the
config also hot-reloads on `SIGHUP` or file change.

## Metrics

33 metrics under the `kemp_` prefix (plus `kemp_exporter_build_info`), covering
target reachability, appliance-wide CPU/memory/TPS/traffic totals, per-interface
counters, and per-virtual-service and per-real-server status/connection/traffic
metrics. See [`docs/metrics.md`](docs/metrics.md) for the full catalog — every row
is marked **confirmed** (matches Kemp's own documented API fields) or
**unconfirmed** (inferred, pending validation against a real appliance; see the
live-validation checklist at the end of that document).

Every metric carries a `system` label identifying which LoadMaster it came from,
except `kemp_exporter_build_info`. A missing or unparseable source field produces no
sample — never a fabricated `0`.

## Relationship to `giantswarm/prometheus-kemp-exporter`

This project is a **rewrite**, not a port, of the archived
[`giantswarm/prometheus-kemp-exporter`](https://github.com/giantswarm/prometheus-kemp-exporter)
(~250 LOC, archived 2023-10-27). That project contributes two things to this one and
nothing else: its metric list, and the observation that the Kemp `stats` command
returns a virtual service's address and port but not its name, requiring a join
against `listvs`.

Everything else about it conflicted with this project's baseline: credentials passed
as `argv` (visible in `ps` output on a shared host), no config file, no multi-target
support, no snapshot model decoupling backend load from scrape frequency, no OTLP
export, no tests, and a dependency on the removed `prometheus.Handler()` API. Its
`giantswarm/kemp-client` dependency also hardcodes `InsecureSkipVerify: true` in
`kemp.go`, unconditionally disabling TLS certificate validation for every user with
no opt-out — see [ADR 0003](docs/adr/0003-hand-rolled-resty-client.md) for why this
project hand-rolls its own transport instead.

## License

Apache-2.0.
