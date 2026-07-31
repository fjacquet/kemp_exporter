# 0006. Label-key union invariant

- **Status:** accepted
- **Date:** 2026-07-31
- **Deciders:** Frederic Jacquet

## Context and problem statement

The Prometheus client library panics (or, depending on the collector implementation,
silently drops series) when the same metric name is registered with two different
label-key sets within one scrape. A collector deriving samples from loosely
structured, partially-joined data — a virtual service whose name failed to resolve,
a real server with no matching parent — is at risk of accidentally emitting a
divergent label-key set for an edge case that the happy path never exercises.

## Considered options

- Let each derivation site build its own `[]Label` slice ad hoc, including only the
  labels it happens to have resolved at that point.
- Enforce one label-key set per metric name, built exclusively through shared label
  constructor functions, with unresolved values represented as empty *values* never
  missing *keys*; have the collector defensively drop any series that still manages
  to diverge, rather than let it crash or corrupt the exposition.

## Decision outcome

Chosen option: **"one label-key set per metric name, empty value not missing key,
defensive drop at the collector"**.

`internal/kemp/metrics.go` centralizes every label-key set into one constructor per
metric family — `systemLabels`, `cpuLabels`, `interfaceLabels`, `vsLabels`,
`rsLabels`, and `withLabel` for the `_status` families' extra key. No call site
outside `metrics.go` constructs a `[]Label` literal; every sample carries a label set
built by exactly one of these functions, so the key set for a given metric name is
mechanically fixed by which constructor its derivation calls, not left to reviewer
discipline.

- **Unresolved names are empty values, not missing keys.** When `listvs` fails to
  resolve a virtual service's name for a given `address:port`, the sample still
  carries a `name` label — with an empty string value — rather than omitting the
  `name` key entirely. An omitted key would change the *label-key set* for that
  series, which is exactly the divergence this invariant exists to prevent; an empty
  value changes only what that series looks like when queried, which PromQL handles
  natively.
- **The `_status` metrics are their own families with a sixth key.**
  `kemp_virtual_service_status` and `kemp_real_server_status` are not the same
  metric *name* as `kemp_virtual_service_up`/`kemp_real_server_up` — they are
  separate families that happen to share the base five (or four) keys plus one more,
  `status`, built via `withLabel(base, "status", value)`. Because they are distinct
  metric names, carrying an extra key on them does not violate the one-set-per-name
  rule; it would only be a violation if two samples of the *same* metric name carried
  different key sets.
- **The collector drops divergent series rather than failing the scrape.** As a
  defense against a bug in a derivation site (not a designed code path — see
  `PromCollector.Collect`'s doc comment), if a duplicate label set is ever observed
  for one metric name with different values, the collector keeps the first sample
  encountered and logs a `Warn` for the rest, rather than panicking the whole scrape
  or silently double-registering. The OTLP export path applies the identical
  first-wins rule over the identical iteration order, so the two export paths never
  disagree about which sample survives (see `internal/kemp/otlp.go`'s doc comment on
  `OTLPExporter`).

### Consequences

- Good — a scrape can never be taken down by a label-key mismatch panic from the
  underlying `client_golang` library, because the mismatch is prevented upstream by
  construction (shared constructors) and defended against downstream (drop + log) if
  it ever occurs anyway.
- Good — Prometheus and OTLP consumers see mechanically identical label sets per
  metric name; a dashboard or alert rule written against one export path transfers
  directly to the other.
- Neutral — an unresolved `name`/`status` value surfaces as an empty-string label
  value in the exposition, which a dashboard author must handle explicitly (e.g.
  filtering `name != ""`) rather than the series simply not existing.
- Bad — the defensive drop-and-log path means a genuine derivation bug could reduce
  scrape completeness silently (the dropped series just doesn't appear) rather than
  failing loudly; mitigated by the `Warn`-level log line naming the offending metric
  and labels, and by this being a last-resort backstop behind constructors that make
  the bug hard to introduce in the first place.
