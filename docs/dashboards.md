# Dashboards

## Overview dashboard

`grafana/kemp-overview.json` — provisioned automatically by the demo compose stack
(`docker compose up --build`, Grafana at <http://localhost:3000>), or importable
manually into any Grafana instance with a Prometheus datasource scraping this
exporter.

It is organized into three rows:

- **Status row** — target reachability (`kemp_up`), virtual-service up/down counts,
  active connections, a CPU-busy gauge, and free memory.
- **Traffic row** — outbound bits/s per virtual service, and inbound/outbound bits/s
  per network interface, as timeseries panels.
- **Transactions & virtual services row** — TPS over time, a bar gauge of the top 5
  virtual services by 24h traffic, and a table of currently degraded
  (`kemp_virtual_service_status{status="Sick"}`) services.

### `system` template variable

Every panel is filtered by a dashboard-level template variable named `system`,
populated with `label_values(kemp_up, system)`. This is the same `system` label
every metric in this exporter carries (see
[ADR 0006](adr/0006-label-key-union-invariant.md)), so switching the variable at the
top of the dashboard re-points every panel at a different LoadMaster in the fleet
without editing a single query.

### Metric-name safety net

Every query in this dashboard is checked in CI against the exporter's own known-metric
set (`internal/dashboards/dashboards_test.go`'s `knownMetrics` map, which mirrors
`docs/metrics.md`): a panel referencing a metric name the exporter does not actually
emit fails the build. A second test in the same file asserts that no per-second gauge
(`kemp_connections_per_second`, `kemp_tps`, etc.) is ever wrapped in `rate()` — see
[ADR 0005](adr/0005-metric-naming-and-units.md) for why that would be wrong.

## Grafana dashboard 12160 is **not** compatible

If you search Grafana's public dashboard library for "Kemp LoadMaster", you will find
[dashboard 12160](https://grafana.com/grafana/dashboards/12160/). **Do not import it
expecting it to work against this exporter's metrics.** It is sourced from
`snmp_exporter` polling a LoadMaster over SNMP, and queries metric names this
exporter never emits: `vSstate`, `ifHCInOctets`, `ssCpuIdle`, `dskAvail`, all keyed
on a `device` label rather than this exporter's `system` label. There is zero
metric-name overlap between 12160 and this exporter's `kemp_*` catalog.

`grafana/kemp-overview.json` reuses 12160's general panel *layout* (a status row, a
traffic row, a transactions row) as a familiar reference point, but every query in it
is written fresh against this exporter's own metrics. One panel from 12160 has no
equivalent here at all: its disk-utilization panel (`dskAvail`) has no corresponding
field in the LoadMaster `stats`/`listvs` REST responses this exporter collects, so it
is simply not present — see [ADR 0007](adr/0007-own-dashboard-not-grafana-12160.md)
for the full reasoning and the disk-metrics follow-up.
