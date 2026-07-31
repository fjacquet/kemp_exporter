# 0004. Dual transport, single model, runtime detection

- **Status:** accepted
- **Date:** 2026-07-31
- **Deciders:** Frederic Jacquet

## Context and problem statement

LoadMaster appliances speak two wire encodings for the same underlying commands:
a classic XML API (`GET /access/<cmd>?apikey=...`) available on essentially every
firmware, and a newer JSON API (firmware 7.2.50+, session-token auth) that some
appliances have enabled and some do not. An exporter monitoring a fleet of
LoadMasters at different firmware levels needs to speak whichever encoding each
appliance actually supports, without the operator having to declare it per target.

## Considered options

- One collection pipeline per encoding (mirroring `pflex_exporter`'s two-pipeline
  split for its bulk-CSV vs. per-entity REST paths), each deriving its own metrics
  independently.
- A single `transport` interface (`Do(ctx, cmd, params, out)`) behind which both
  encodings decode into the same `internal/models` types, with one derivation layer
  above it; the concrete transport is chosen once per system by runtime detection.

## Decision outcome

Chosen option: **"single model, runtime detection"**.

`pflex_exporter`'s two-pipeline split exists because its bulk and per-entity paths
return genuinely different data shapes and, in places, different fields entirely —
splitting the pipeline was the only way to keep each path's derivation honest about
what it actually has. That does not hold here: the LoadMaster XML and JSON APIs
issue the **same commands** (`stats`, `listvs`) and return the **same fields**,
merely encoded differently (XML elements vs. JSON keys with matching tag names — see
`internal/models/statistics.go`'s parallel `xml:`/`json:` struct tags). Splitting the
pipeline would duplicate the derivation logic in `derivations.go` for zero benefit,
and — worse — would let the two paths drift apart silently. Instead, both transports
decode into identical `internal/models` types, and exactly one derivation layer
(`derivations.go`) runs on top of either. `internal/kemp/transport_parity_test.go` is
the invariant guard: it feeds byte-different XML and JSON fixtures representing the
same logical data through both transports and asserts the decoded `Statistics` are
identical.

**Why detection caches.** Detecting which transport a system speaks requires a live
probe (attempt one, fall back to the other on `errUnsupported`). Doing that probe on
every collection cycle would double outbound requests to every appliance for no
gained information — a LoadMaster does not change its supported API surface between
one collection cycle and the next. Detection therefore runs once per system at
startup (or on a config-triggered client rebuild — see
[ADR 0008](0008-config-hot-reload.md)) and the resulting `transport` is held for the
life of that client.

**Why a static API key deviates from bearer+refresh.** The JSON transport's session
token *does* refresh (see `internal/kemp/auth.go`'s `session` type: a token is
obtained lazily, cached, and re-obtained once on a 401 — bounded to avoid retry-driven
account lockout). The XML transport's `apikey`, by contrast, is a long-lived,
operator-provisioned static credential with no session or expiry concept in the
classic API at all — LoadMaster API keys are designed to be generated once,
distributed to a monitoring tool, and rotated manually, not refreshed
programmatically. Building a refresh cycle for a credential type that never expires
would be unneeded complexity with no corresponding appliance-side behavior to react
to.

### Consequences

- Good — one derivation layer, one set of label builders, one metric catalog; adding
  a metric means touching `derivations.go` once, not twice.
- Good — the transport parity test is a standing, automated guard against the two
  wire paths silently diverging as either is touched.
- Good — a fleet at mixed firmware levels needs zero per-target configuration beyond
  credentials; detection picks the right encoding per system automatically.
- Neutral — detection assumes a system's supported transport is stable for the life
  of the process; an appliance whose firmware is upgraded (enabling/disabling the
  JSON API) mid-run keeps using the transport detected at startup until the process
  or its client is rebuilt (config reload triggers a client rebuild — see
  [ADR 0008](0008-config-hot-reload.md)).
- Bad — the XML apikey has no programmatic rotation path; an operator must rotate it
  out-of-band and update the config (which hot-reloads without a restart).
