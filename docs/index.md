# kemp_exporter

A Go Prometheus + OTLP exporter for **Progress Kemp LoadMaster** appliances. One
process monitors many LoadMasters, polls each on an interval, and serves metrics at
`/metrics` (Prometheus) and/or pushes them via OTLP — both export paths render from
the same immutable, periodically-refreshed snapshot (see
[ADR 0002](adr/0002-snapshot-collection-model.md)).

Current release: [v0.1.0](https://github.com/fjacquet/kemp_exporter/releases/tag/v0.1.0) — see the
[changelog](https://github.com/fjacquet/kemp_exporter/blob/main/CHANGELOG.md).

## Quick start

```bash
make cli
export KEMP1_HOSTNAME='lm-prod-01.example.com'
export KEMP1_APIKEY='your-read-only-api-key'
./bin/kemp_exporter --config config.yaml
# metrics: http://localhost:9447/metrics
```

## Where to go next

- [Metrics reference](metrics.md) — every metric this exporter can emit, its type,
  its labels, and whether its wire-level field name is confirmed against Kemp's own
  documentation or inferred and awaiting live validation.
- [Dashboards](dashboards.md) — the bundled Grafana overview dashboard, and why
  Grafana's public dashboard 12160 is not a drop-in replacement.
- [Docker deployment](deployment/docker.md) — the compose quickstart and the GHCR
  variant.
- [systemd deployment](deployment/systemd.md) — install, operate, harden (with a
  macOS `launchd` note).
- [Architecture decision records](adr/index.md) — the *why* behind the design.

## Configuration

See `config.yaml` in the repository root for the full schema: server, collection
interval/timeout/concurrency, optional OTLP export, and one entry per LoadMaster
under `systems`. Secrets (`apiKey`, `password`) accept `${ENV_VAR}` references and
are resolved at load time; the config also hot-reloads on `SIGHUP` or file change
(see [ADR 0008](adr/0008-config-hot-reload.md)).

## Two wire transports, one data model

LoadMaster appliances speak either a classic XML API or a newer JSON API depending
on firmware. This exporter detects which one each configured appliance speaks and
decodes either into the same internal model, so the metric catalog and label sets
are identical regardless of which encoding a given LoadMaster happens to use — see
[ADR 0004](adr/0004-dual-transport-single-model.md).
