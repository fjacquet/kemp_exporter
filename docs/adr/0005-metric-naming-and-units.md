# 0005. Metric naming and units

- **Status:** accepted
- **Date:** 2026-07-31
- **Deciders:** Frederic Jacquet

## Context and problem statement

A metric's name is a promise about how it may be queried. Getting the gauge/counter
distinction, the unit, and the naming suffix wrong invites a dashboard author or
alert-rule writer to apply `rate()` to something that is already a rate (double
derivative, meaningless output), or to assume kilobytes where the appliance reports
bytes. LoadMaster's own field naming makes this easy to get wrong: several
per-second fields are literally named `Total*` in the payload (`TPS.Total`,
`Totals.ConnsPerSec` is *not* named `Total`, but the raw response element for TPS
uses `Total`), inviting a naive mapping straight to a `_total` counter suffix.

## Considered options

- Name every metric after its source field verbatim, including the `_total` suffix
  wherever the payload field is literally named `Total*`.
- Name every metric by its actual semantics (rate vs. cumulative count), independent
  of the payload field's own naming, and document the mismatch explicitly where the
  two disagree.

## Decision outcome

Chosen option: **"name by semantics, not by source field spelling"**.

- **Per-second values are gauges.** `kemp_connections_per_second`,
  `kemp_bytes_per_second`, `kemp_packets_per_second`, `kemp_tps`, `kemp_tps_ssl`, and
  every `*_connections_per_second` metric are derived with `sum`/`avg` aggregations
  in PromQL, never `rate()` — `rate()` over an already-instantaneous-rate gauge
  computes a rate of a rate, which is not a meaningful quantity.
- **Cumulative values carry `_total`**, and `rate()`/`increase()` are the correct
  PromQL functions there: `kemp_virtual_service_bytes_total`,
  `kemp_real_server_connections_total`, `kemp_interface_bytes_read_total`, etc.
- **`kemp_tps` and `kemp_tps_ssl` are gauges despite their source field being named
  `Total`.** The LoadMaster `TPS` payload element is named `Total` (see
  `internal/models/statistics.go`'s `TPS` struct: `Total Num`), but its value is an
  instantaneous transactions-per-second rate, not a cumulative count — the comment on
  that struct states this explicitly. Naming the metric `kemp_tps_total` to match the
  field would have been wrong twice: it would invite `rate()`, and it would collide
  with the naming convention's promise that `_total` means "monotonic count since
  process start." `internal/kemp/health_test.go`'s
  `TestDeriveHealthMemoryAndTPS` asserts the negative directly: `kemp_tps_total` must
  never be emitted.
- **Bytes, not kilobytes.** LoadMaster's memory fields (`memused`, `memfree`) report
  raw bytes; `kemp_memory_used_bytes`/`kemp_memory_free_bytes` pass them through
  unconverted rather than dividing by 1024, matching the field's actual unit rather
  than a guessed one.
- **Percentages are 0–100**, matching the appliance's own convention
  (`percentmemused`, CPU idle/user/system percentages), not renormalized to 0–1.

**Every sample renders as `prometheus.GaugeValue`, including the `_total` ones —
this is a family convention, not an oversight.** The snapshot collection model
(see [ADR 0002](0002-snapshot-collection-model.md)) has no notion of a persistent
counter that survives a process restart: on restart, the in-process value for any
cumulative metric starts over from whatever the LoadMaster currently reports, not
from zero. A genuine `prometheus.CounterValue` carries an implicit contract that the
value only increases for the life of the *series*, and Prometheus's own `rate()`
implementation already handles counter resets gracefully (detecting a drop and
correcting for it) — so encoding these as `CounterValue` would gain nothing over
`GaugeValue` for a value that is, from this exporter's perspective, always freshly
read from the appliance's own running total rather than accumulated locally. Using
`GaugeValue` uniformly for every sample keeps `PromCollector.Collect` (Task 12) a
single, un-branching code path with no per-metric type table to keep in sync with
the naming convention above.

### Consequences

- Good — the naming convention answers "should I `rate()` this?" unambiguously from
  the metric name alone, without consulting source code.
- Good — `kemp_tps`/`kemp_tps_ssl` avoid the double-rate footgun their source field
  name would otherwise invite; a regression test (`TestDeriveHealthMemoryAndTPS`)
  pins this.
- Neutral — the uniform `GaugeValue` wire encoding means a client library that
  distinguishes gauge vs. counter type strictly (e.g. for automatic `rate()`
  suggestions in some UIs) will treat `_total` metrics as gauges at the protocol
  level; the `_total` naming suffix remains the authoritative signal for PromQL
  authors, per this ADR and `docs/metrics.md`.
- Bad — a metric consumer that infers "is this a counter" purely from the wire-level
  Prometheus type (rather than the name) will get the wrong answer for every
  `_total` metric; this is called out explicitly in `docs/metrics.md`'s Type column
  documentation to prevent that misreading.
