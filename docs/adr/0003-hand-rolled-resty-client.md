# 0003. Hand-rolled resty client

- **Status:** accepted
- **Date:** 2026-07-31
- **Deciders:** Frederic Jacquet

## Context and problem statement

Progress Kemp publishes no official Go SDK for the LoadMaster API — only PowerShell,
Java, and Python clients. A community Go client exists,
[`giantswarm/kemp-client`](https://github.com/giantswarm/kemp-client), and reusing it
would have saved writing a transport layer from scratch. It needed to be evaluated
against this project's baseline criteria for a third-party API client dependency:
(1) support for a modern, non-Basic auth mechanism; (2) sane retry/timeout defaults;
(3) an actively maintained release cadence; (4) no hardcoded insecure defaults.

## Considered options

- Adopt `giantswarm/kemp-client` as the transport layer.
- Fork `giantswarm/kemp-client` and patch its auth and TLS defaults.
- Write a small, hand-rolled `resty`-based client scoped to exactly the commands this
  exporter needs (`stats`, `listvs`, login/session).

## Decision outcome

Chosen option: **"hand-rolled resty client"**, because `giantswarm/kemp-client` fails
two of the four baseline criteria outright, and the exporter's actual wire-protocol
surface is small enough (a handful of GET/POST commands across two encodings) that a
purpose-built client is less code than adapting someone else's.

`giantswarm/kemp-client` fails:

- **Criterion 1 (modern auth).** It supports HTTP Basic authentication only. The
  LoadMaster JSON API's session-token flow (`POST /access/login`, then
  `X-API-Key: <token>` on subsequent requests) has no path through that client at
  all — supporting the JSON transport would have meant bypassing the library's HTTP
  layer entirely, defeating the point of depending on it.
- **Criterion 4 (no hardcoded insecure defaults).** Its `kemp.go` hardcodes
  `InsecureSkipVerify: true` in the `tls.Config` it builds, with **no opt-out**. Every
  request that library makes disables TLS certificate verification unconditionally.
  That is the opposite of this exporter's posture (`insecureSkipVerify` is a
  per-target, operator-controlled config field defaulting to `false` — see
  `internal/kemp/tlsconfig.go`), and there is no supported way to override it short of
  a fork.

Given those two failures, forking would mean rewriting the auth layer and the TLS
config path — most of the library's surface area — while keeping only its XML struct
shapes, which this exporter needed to define anyway to support the JSON transport
identically (see [ADR 0004](0004-dual-transport-single-model.md)). A hand-rolled
`resty` client scoped to the exact command set in use (`internal/kemp/transport_xml.go`,
`transport_json.go`, `auth.go`) was less total work and carries no inherited defaults
to audit.

### Consequences

- Good — TLS verification defaults to on everywhere, per-target opt-out only, with no
  vendored code path that silently disables it.
- Good — the JSON session-token flow is fully supported; the client surface is scoped
  to exactly the four commands the exporter issues, so there is no unused API surface
  to maintain or audit.
- Good — `newRestyClient`'s retry policy is purpose-built for this API's failure modes
  (no retry on 4xx, to avoid LoadMaster account lockout — see
  [ADR 0004](0004-dual-transport-single-model.md)), which a generic client's defaults
  would not have matched without patching anyway.
- Neutral — `giantswarm/kemp-client`'s XML struct tags were still consulted as a
  reference for element names during development (see `docs/metrics.md`'s
  unconfirmed-field notes); the dependency itself was not used.
- Bad — this project owns the transport layer's maintenance burden (wire format
  changes across LoadMaster firmware versions) rather than sharing it with an upstream
  library; mitigated by the transport parity test
  (`internal/kemp/transport_parity_test.go`) which keeps both encodings honest against
  the same fixtures as firmware assumptions evolve.
