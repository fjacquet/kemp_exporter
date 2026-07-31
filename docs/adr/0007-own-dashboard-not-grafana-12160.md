# 0007. Own dashboard instead of Grafana 12160

- **Status:** accepted
- **Date:** 2026-07-31
- **Deciders:** Frederic Jacquet

## Context and problem statement

Grafana's public dashboard library has an existing entry for Kemp LoadMaster,
[dashboard 12160](https://grafana.com/grafana/dashboards/12160/). Reusing an existing
community dashboard rather than building one from scratch is normally the cheaper
path. It needed evaluating before being adopted or rejected.

## Considered options

- Import dashboard 12160 as-is and point its queries at this exporter's metrics.
- Import 12160 and rename its query fields to match `kemp_*` naming.
- Build a purpose-built dashboard (`grafana/kemp-overview.json`) that reuses 12160's
  panel *layout* but writes fresh queries against this exporter's actual metric names.

## Decision outcome

Chosen option: **"own dashboard, reused layout, fresh queries"**, because 12160's
queries have **zero metric-name overlap** with this exporter and adopting it would
mean rewriting every query anyway.

12160 is sourced from `snmp_exporter` scraping a LoadMaster over SNMP, not from this
project's REST-based collection. Its panels query `vSstate`, `ifHCInOctets`,
`ssCpuIdle`, `dskAvail`, all keyed on a `device` label — an entirely different metric
vocabulary from this exporter's `kemp_*` names and `system` label. There is no query
in 12160 that would resolve against this exporter's data without being rewritten from
scratch.

**Renaming to match would break three naming rules at once.** Forcing this
exporter's metrics to match 12160's SNMP-derived names (`vSstate`, `ifHCInOctets`,
etc.) would violate: the `kemp_` namespace prefix every metric in this project
carries; the gauge/counter naming convention in
[ADR 0005](0005-metric-naming-and-units.md) (`ifHCInOctets` implies a 64-bit SNMP
counter convention foreign to this exporter's `_total` suffix rule); and the
`system` label-key invariant in [ADR 0006](0006-label-key-union-invariant.md)
(12160's `device` label does not carry the same semantics as `system`, and mixing
label vocabularies across a fleet would defeat cross-appliance dashboards).

**We reuse 12160's layout, not its queries.** `grafana/kemp-overview.json` mirrors
12160's overall shape — a status row, a traffic row, a transactions/services row —
because that layout is a reasonable, familiar way to present this domain's data, but
every panel query in it is written fresh against `kemp_*` metric names, with a
`system` template variable (`label_values(kemp_up, system)`) driving per-appliance
filtering.

**The disk panel has no REST equivalent and is dropped.** 12160's `dskAvail` panel
has no corresponding field anywhere in the LoadMaster `stats`/`listvs` REST responses
this exporter collects — disk metrics are not exposed there at all. Rather than
fabricate a disk panel with no backing data, it is omitted; this is tracked as a
known follow-up (see the project's "Known follow-ups" list) pending access to a real
appliance's full `stats` payload to check for any disk-adjacent field this rewrite
missed.

### Consequences

- Good — every panel in `grafana/kemp-overview.json` resolves against a real,
  emitted `kemp_*` metric; `internal/dashboards/dashboards_test.go` asserts this
  automatically against the exporter's own known-metrics set on every `make ci` run.
- Good — the naming, unit, and label-key conventions established elsewhere in this
  project stay intact; the dashboard does not become a special case that needs its
  own vocabulary.
- Neutral — an operator who searches Grafana's dashboard library for "Kemp" and finds
  12160 first will not be able to drop it in unmodified; this ADR (and
  `docs/dashboards.md`) states that explicitly so the incompatibility is discovered
  before, not after, an import attempt.
- Bad — no disk-utilization panel exists yet, since the REST surface this exporter
  collects has no known disk field to back one; revisit once a real appliance's full
  `stats` payload is available to check (see "Known follow-ups").
