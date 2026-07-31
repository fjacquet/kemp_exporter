# Architecture decision records

This directory records the significant architectural decisions for `kemp_exporter` —
the *why* behind the design, in the form of dated [MADR](https://adr.github.io/madr/)-style
records. Decisions are immutable once accepted: rather than editing a past record,
add a new one that supersedes it.

| ADR | Decision | Status |
|---|---|---|
| [0001](0001-supply-chain-and-release-hardening.md) | Supply-chain and release hardening — what this repo enforces directly vs. what it inherits from `fjacquet/ci` | accepted |
| [0002](0002-snapshot-collection-model.md) | Decouple backend API load from scrape/export frequency with a background snapshot collector | accepted |
| [0003](0003-hand-rolled-resty-client.md) | Hand-rolled resty client instead of `giantswarm/kemp-client` | accepted |
| [0004](0004-dual-transport-single-model.md) | Dual transport (XML/JSON), single data model, runtime detection | accepted |
| [0005](0005-metric-naming-and-units.md) | Metric naming by semantics (rate vs. cumulative), not by source field spelling | accepted |
| [0006](0006-label-key-union-invariant.md) | One label-key set per metric name; empty value, never missing key | accepted |
| [0007](0007-own-dashboard-not-grafana-12160.md) | Own Grafana dashboard instead of the incompatible community dashboard 12160 | accepted |
| [0008](0008-config-hot-reload.md) | SIGHUP plus directory-watch config hot reload, fail-safe on a bad file | accepted |
