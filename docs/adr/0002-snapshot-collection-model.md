# 0002. Snapshot collection model

- **Status:** accepted
- **Date:** 2026-07-31
- **Deciders:** Frederic Jacquet

## Context and problem statement

An exporter serves two independent clients — a Prometheus scraper hitting `/metrics`
and an OTLP `PeriodicReader` pushing on its own interval — and can be scraped by more
than one Prometheus instance at once, or scraped while an OTLP export is in flight.
If each read triggered a fresh call to every configured LoadMaster, backend API load
would scale with *scraper count*, not with the collection interval: two Prometheus
instances plus OTLP would mean three times the appliance load for the same data,
and a slow or unreachable LoadMaster would make a scrape block on network I/O,
turning a backend outage into a `/metrics` timeout.

## Considered options

- Collect synchronously inside the scrape/export handler (one collection per read).
- Collect once per interval on a background loop; readers block on a mutex-guarded
  cache until the first collection completes.
- Collect once per interval on a background loop into an immutable snapshot; readers
  atomically load the latest snapshot and never block on it.

## Decision outcome

Chosen option: **"background loop, immutable snapshot, non-blocking reads"**, because
it decouples backend load from scrape frequency and guarantees a read is never
slowed down by a LoadMaster round-trip.

`CollectionLoop.Run` ticks on `cfg.Collection.Interval`, queries every configured
system (bounded by `maxConcurrent`), derives samples, and atomically stores the
result in a `SnapshotStore` (an `atomic.Pointer` to an immutable `Snapshot`). Both
the Prometheus `PromCollector.Collect` and each OTLP observable-gauge callback call
`store.Load()` and render from whatever snapshot is currently there — never
triggering a collection themselves, never blocking on one in progress.

### Consequences

- Good — backend API load is a function of `cfg.Collection.Interval` alone, regardless
  of how many Prometheus instances scrape or how tight the OTLP export interval is.
- Good — a slow or unreachable LoadMaster degrades that one collection cycle, never a
  scrape: a reader during a stalled collection still gets the previous snapshot,
  stale but present, rather than hanging.
- Good — both export paths render from the identical snapshot, so they cannot
  disagree with each other even under an anomalous input (see the duplicate-label
  handling documented on `PromCollector.Collect` and `OTLPExporter`'s callbacks).
- Neutral — a scrape can observe data up to one collection interval old; this is the
  standard and accepted trade-off for any polling exporter and is not unique to this
  design.
- Bad — the snapshot is only as fresh as the last successful cycle: if the exporter
  loses connectivity to a LoadMaster, its `kemp_up` reading correctly reflects that,
  but any per-metric family absent that cycle simply carries no new samples rather
  than an obviously stale one (mitigated operationally by the missing-series
  companion alerts in `deploy/prometheus/kemp.rules.yml`).
