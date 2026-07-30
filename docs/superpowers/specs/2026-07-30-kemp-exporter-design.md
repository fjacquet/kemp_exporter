# kemp_exporter — Design

**Date:** 2026-07-30
**Status:** Approved
**Module:** `github.com/fjacquet/kemp_exporter`

## 1. Purpose & scope

A Go Prometheus + OTLP exporter for Progress Kemp LoadMaster load balancers, built to the
`exporter-standards` family conventions (see the `exporter-standards` skill: `stack.md`,
`architecture.md`, `cicd.md`, `decisions.md`, `new-exporter-checklist.md`).

This is a **rewrite**, not a port. The archived
[`giantswarm/prometheus-kemp-exporter`](https://github.com/giantswarm/prometheus-kemp-exporter)
(~250 LOC, archived 2023-10-27) contributes two things and nothing else: its metric list, and
the observation that the Kemp `stats` command returns virtual-service address and port but not
name, requiring a join against `listvs`.

Everything else in that repo conflicts with the standard: credentials passed as `argv`
(visible in `ps` output), no config file, no multi-target support, no snapshot model, no OTLP,
no tests, the removed `prometheus.Handler()` API, and — in its `giantswarm/kemp-client`
dependency — `InsecureSkipVerify: true` hardcoded in `kemp.go`, which unconditionally disables
TLS certificate validation for every user with no opt-out.

### Not in scope

Grafana dashboard [12160 "Kemp Loadmaster"](https://grafana.com/grafana/dashboards/12160-kemp-loadmaster/)
cannot be driven by this exporter and will not be shipped. It is sourced from `snmp_exporter`,
and its queries reference SNMP MIB object names with a `device` label:

```
vSstate{device="$device"} == 2                    # KEMP MIB
vSActivConns{device="$device"} > 0
rate(vSOutBytes{device="$device"}[5m]) * 8
irate(ifHCInOctets{device="$device"}[2m]) * 8     # IF-MIB
irate(ifHCOutOctets{device="$device"}[2m]) * 8
100 - ssCpuIdle{device="$device"}                 # UCD-SNMP-MIB
memTotalFree{device="$device"}
dskAvail{device="$device"}
```

There is zero overlap with the `kemp_*` namespace, and 12160 additionally requires two scrape
jobs (an snmp_exporter `common` module plus a custom KEMP module). Renaming our metrics to SNMP
object names to satisfy it would break the metric-prefix rule, unit-explicit naming, and the
identity-label rule simultaneously.

We ship our own dashboard, using 12160's **panel layout** as the reference. See ADR 0007.

### Validation posture

No LoadMaster appliance is available for this build. The exporter is developed against
fixtures derived from the Kemp RESTful API documentation and the `giantswarm/kemp-client`
struct tags. Every metric in `docs/metrics.md` is marked **confirmed** or **unconfirmed**, and a
live-validation pass is a tracked follow-up — not a silent gap. See §8.

## 2. Family slot

| Property | Value |
|---|---|
| Repo / binary | `kemp_exporter` |
| Metric prefix | `kemp_` |
| Metrics port | `9447` |
| OTLP port | `4317` |
| Identity label | `system` |
| Go | `1.26.5` (patch-pinned in `go.mod`) |
| Client | hand-rolled `go-resty/resty/v2` — ADR 0003 |

Port 9447 continues the family's `9438`–`9446` block. It collides with "Resque Exporter" in the
Prometheus default-port-allocations wiki, as does every other port in the family block; this is
existing, accepted family behaviour, not new drift.

### Client decision (checklist step 0)

No official Kemp Go SDK exists. Progress ships a PowerShell module, a Java API, and a Python SDK
(`python-kemptech-api`). The only Go option, `giantswarm/kemp-client`, is third-party, XML-only,
HTTP-Basic-only, unmaintained since 2023, and hardcodes TLS certificate-validation bypass.

Applying the rule in `decisions.md`: the SDK criterion fails at **(1) modern auth** and
**(4) forces a regression**. Outcome: hand-roll a lean `resty/v2` client. Recorded as ADR 0003.

## 3. Architecture

Standard family shape — one collection loop, an immutable snapshot, two readers.

```
main.go (cobra: --config --debug --once --trace)
   │  starts HTTP server FIRST, then the collection loop
   ├── internal/config      yaml + ${ENV} + passwordFile + .env; SIGHUP + fsnotify reload
   ├── internal/logging     logrus
   ├── internal/telemetry   OTLP manager (pflex pattern)
   ├── internal/models      Statistics, VirtualService, RealServer, Interface, CPU, Memory, TPS
   └── internal/kemp
         client.go          Client interface: GetStatistics, ListVirtualServices
         transport.go       transport interface + detection & caching
         transport_xml.go   GET /access/<cmd>?apikey=  → xml.Decoder
         transport_json.go  POST /access/<cmd> JSON    → json.Decoder + session login
         num.go             tolerant numeric type (parsed-or-absent)
         auth.go            api key vs session token; retry excludes 4xx
         tracing.go         OnAfterResponse hook, body-only, skips auth responses
         collector.go       loop: errgroup over targets, SetLimit
         snapshot.go        immutable Snapshot + SnapshotStore (RWMutex pointer swap)
         metrics.go         Sample{Name, []Label, Value} + shared label builders
         derivations.go     stats+listvs join; VS/RS/health → samples
         state.go           kemp_up, per-target health
         buildinfo.go       kemp_exporter_build_info{version, goversion}
         prometheus.go      unchecked collector (Describe sends nothing)
         otlp.go            observable gauges, periodic reader
```

```
loop ─ per target ─ ensure transport (cached, probed on first contact)
                  ├─ GetStatistics()       ──┐
                  └─ ListVirtualServices() ──┴─→ join → Snapshot ─ Store.Swap()
                                                            ├── PromCollector  /metrics
                                                            └── OTLPExporter   push
```

Reference siblings: **`pflex_exporter`** for dual export, OTLP wiring and tracing;
**`ppdd_exporter`** for the resty client and the `config`/`dotenv`/`watcher` layout. Note that
`ppdd` has **no** OTLP support — that is known family drift and must not be copied.

### Load-bearing choices

- **HTTP server starts before the first collection.** Login plus the first poll can exceed the
  collection timeout; blocking startup on it stalls `/metrics`. The endpoint answers immediately
  with `kemp_up=0`.
- **Identity label `system` on every metric.** One process serves many LoadMasters.
- **Per-target degradation.** An unreachable LoadMaster sets its own `kemp_up=0` and never fails
  the cycle for other targets.
- **VS name join keyed on `address:port`.** The upstream exporter keyed its lookup on address
  alone, which collides when one VIP hosts multiple ports — two services silently share a name.
  An unresolved name yields an empty label value, never a dropped series.
- **Absent, never zero.** An unparseable field yields no sample. A fabricated `0` on
  `kemp_virtual_service_active_connections` is indistinguishable from a healthy idle service.

## 4. Components

### 4.1 `models` — one decoded shape for both transports

Both transports decode into the same structs. Struct tags carry `xml:` and `json:` side by side,
so there is no adapter layer and no second set of types.

```go
type Statistics struct {
    Totals          Totals           `xml:"VStotals"    json:"VStotals"`
    VirtualServices []VirtualService `xml:"Vs"          json:"Vs"`
    RealServers     []RealServer     `xml:"Rs"          json:"Rs"`
    CPUs            []CPU            `xml:"CPU>CPU"     json:"CPU"`
    Memory          Memory           `xml:"Memory"      json:"Memory"`
    Interfaces      []Interface      `xml:"Network>Nic" json:"Network"`
    TPS             TPS              `xml:"TPS"         json:"TPS"`
}
```

Numeric fields use a tolerant `Num` type defined in `num.go`, not `int`. Kemp returns numbers as
XML chardata; older firmware pads with whitespace, and fields come back `""` or `N/A` when a
subsystem is disabled. `Num` records whether a value parsed, and derivations skip unparsed
fields rather than emitting zero.

The exact element paths above are provisional — `Network` in particular is unconfirmed and must
be reconciled against a live `stats` response during validation (§8).

### 4.2 `transport` — the detection seam

```go
type transport interface {
    Name() string  // "json" | "xml"
    Do(ctx context.Context, cmd string, params map[string]string, out any) error
}
```

Detection runs **once per target**, on first contact:

1. Try the JSON path: `POST /access/get` with a cheap parameter.
2. On 4xx, or on a decode failure, fall back to the XML path: `GET /access/get?apikey=`.
3. Cache the winning transport on the target's client. Detection is not re-run per cycle.

On a hard failure of the cached transport — a transport error or a decode error, but **not** an
auth 401 — re-probe once, then stick with the outcome.

**Detection 4xx and runtime 4xx are different things.** During detection, a 4xx is the expected
negative signal that a firmware does not support the JSON path, and it drives the fallback. At
runtime, on an already-detected transport, a 4xx is a hard failure with no retry (§7). The
distinction lives in `transport.go`: the probe path handles its own status codes and never routes
through the retry policy.

`Client` does not branch on transport. `collector.go` never sees one.

**Why one model rather than two pipelines.** `pflex` splits Gen1/Gen2 into separate pipelines
because those generations expose different metrics in different units
(`_bandwidth_kb_per_second` vs `_bandwidth_bytes_per_second`). Kemp's two paths expose the same
`stats` command with the same fields in a different encoding — the difference genuinely is
transport-level. Keeping one model means one sample-building path, which satisfies the label-key
union invariant by construction. Recorded as ADR 0004.

### 4.3 `auth.go`

- **JSON path:** session login; token held in the client; refreshed on 401 exactly once per cycle.
- **XML path:** static API key (`apikey` parameter or `X-API-Key` header). No refresh — a static
  key that fails will keep failing.
- Both paths set TLS minimum version 1.2.
- `insecureSkipVerify` is a per-target config field, **default false**, accepting a native bool
  or a `${VAR}` reference. Never hardcoded. This is the concrete regression being corrected
  relative to `giantswarm/kemp-client`.

The static API key has no refresh cycle, which deviates from the family's bearer+refresh norm.
The deviation is deliberate — LoadMaster API keys are long-lived by design — and is recorded in
ADR 0004.

**Retry policy:** resty with backoff, **4xx excluded**. Retrying a 401 against a LoadMaster with
account lockout enabled locks the account.

### 4.4 `tracing.go`

An `OnAfterResponse` hook logging method, path, status and **body only**. It skips the JSON
session-login response, which carries the token in the body. Never use `resty.SetDebug` — it
dumps request headers and leaks the API key into logs.

### 4.5 `derivations.go`

Pure functions: `Statistics + []VirtualServiceInfo → []Sample`. No I/O, no client dependency.
The join, the unit conversions and the absent-not-zero policy all live here, and this file
carries the bulk of the unit tests.

## 5. Data flow

One cycle per `collection.interval`, default `60s`. LoadMaster stats are cheap, and the
dashboard wants finer resolution than the family's 5m storage default.

```
for each target (errgroup, SetLimit = maxConcurrent):
   1. ensure transport                                   (cached; probe on first contact)
   2. GetStatistics()        → models.Statistics         ┐ concurrent
   3. ListVirtualServices()  → []VirtualServiceInfo      ┘
   4. derive: index VS info by "address:port"; walk stats; build []Sample
   5. per-target result → aggregate → Snapshot{Time, map[system][]Sample}
Store.Swap(snapshot)
```

Steps 2 and 3 run concurrently per target. Failure of either marks that target down and keeps
its previous samples **out** of the snapshot: a down LoadMaster reports `kemp_up=0` with no
stale series, rather than frozen values that look live.

## 6. Metric catalog

Scope for v0.1.0: parity with the upstream exporter, reshaped to standard naming and types, plus
the appliance health metrics the dashboard needs.

```
kemp_up{system}
kemp_exporter_build_info{version, goversion}

# totals — already per-second, therefore gauges
kemp_connections_per_second{system}
kemp_bytes_per_second{system}
kemp_packets_per_second{system}

# appliance health
kemp_cpu_idle_percent{system, cpu}
kemp_cpu_user_percent{system, cpu}
kemp_cpu_system_percent{system, cpu}
kemp_memory_free_bytes{system}
kemp_memory_used_bytes{system}
kemp_memory_used_percent{system}
kemp_tps{system}
kemp_tps_ssl{system}
kemp_interface_bytes_read_total{system, interface}
kemp_interface_bytes_written_total{system, interface}

# virtual service — {system, name, address, port, protocol}
kemp_virtual_service_up
kemp_virtual_service_status                      # + status label, always 1 (see §6.1)
kemp_virtual_service_active_connections
kemp_virtual_service_connections_per_second
kemp_virtual_service_connections_total
kemp_virtual_service_packets_total
kemp_virtual_service_bytes_total
kemp_virtual_service_bytes_read_total
kemp_virtual_service_bytes_written_total

# real server — {system, address, port, vs_address, vs_port}
kemp_real_server_up
kemp_real_server_status                          # + status label, always 1 (see §6.1)
kemp_real_server_active_connections
kemp_real_server_connections_per_second
kemp_real_server_connections_total
kemp_real_server_packets_total
kemp_real_server_bytes_total
kemp_real_server_bytes_read_total
kemp_real_server_bytes_written_total
```

`kemp_tps` and `kemp_tps_ssl` are **gauges** — LoadMaster reports transactions per second, not a
cumulative count. They deliberately carry no `_total` suffix, so the suffix remains an unambiguous
marker of counter type across the whole catalog (§6.1).

### 6.1 Types and units

| Source field | Metric | Type | Correct PromQL |
|---|---|---|---|
| `ConnsPerSec`, `BytesPerSec`, `PktsPerSec` | `kemp_connections_per_second`, … | **gauge** | `sum` / `avg` — **never `rate()`** |
| `TotalConns`, `TotalPkts`, `TotalBytes`, `BytesRead`, `BytesWritten` | `kemp_*_total` | **counter** | `rate()` / `increase()` |
| `ActiveConns` | `kemp_*_active_connections` | gauge | `sum` |
| VS / RS status | `kemp_*_up` | gauge, 0 or 1 | `count(… == 1)` |

`kemp_virtual_service_up` and `kemp_real_server_up` collapse LoadMaster's multi-valued status
into a binary. The mapping is explicit and total:

| LoadMaster status | Value | Rationale |
|---|---|---|
| `Up` | `1` | serving |
| `Sick` | `1` | degraded but still serving traffic |
| `Down` | `0` | not serving |
| `Disabled` | `0` | administratively out of rotation |
| `Redirect` | `1` | serving a redirect is serving |
| unrecognised / unparseable | **absent** | absent-not-zero — an unknown status is not evidence of failure |

Collapsing `Sick` to `1` loses information, so the raw status is preserved as a separate
info-style gauge `kemp_virtual_service_status{system, name, address, port, protocol, status} 1`
(one series per service, `status` carrying the verbatim LoadMaster string). Alerting on
degradation uses that; `_up` stays a clean binary for counting panels.

The standard's "never `rate()`" rule governs the already-per-second **gauges**. The cumulative
**counters** are the one place `rate()` is correct. `docs/metrics.md` states which is which per
metric, and the `_total` suffix marks the counters.

Cumulative counters reset on LoadMaster reboot or a stats clear. `rate()` and `increase()` handle
resets natively — the exporter implements no reset detection.

**Unit explicitness.** Bytes stay bytes (`_bytes`, `_bytes_total`), never kilobytes. Percentages
are `_percent` on a 0–100 scale, as Kemp reports them, with no /100 conversion. Memory is
`_bytes`, converted once in derivations from whatever unit the payload uses, with the conversion
named in `docs/metrics.md`.

### 6.2 Label sets — fixed canonical order

```
totals / health   {system}
cpu               {system, cpu}            cpu="total" plus one series per core ("0", "1", …)
memory, tps       {system}
interface         {system, interface}
virtual_service   {system, name, address, port, protocol}
real_server       {system, address, port, vs_address, vs_port}

kemp_virtual_service_status  {system, name, address, port, protocol, status}
kemp_real_server_status      {system, address, port, vs_address, vs_port, status}
```

The invariant is scoped to a **metric name**, not to a conceptual group: the two `_status`
metrics carry one extra key and are their own families. No other metric name mixes key sets.

`vs_address` and `vs_port` on real servers come from the `VSIndex` → virtual-service mapping, so
a dashboard can group real servers under their virtual service.

Every metric in a family carries all of its keys on every series; an unresolved name is
`name=""`, not a missing key. The single derivation path makes the union invariant hold by
construction, but a table-driven test asserts it regardless.

## 7. Error handling

Principle: an exporter that lies is worse than one that is down. Every failure resolves to an
honest absent series or an explicit `0` on a health gauge — never a plausible-looking data value.

| Failure | Handling | Observable |
|---|---|---|
| Target unreachable / TLS failure | log; mark target down; drop its samples from the snapshot | `kemp_up{system}=0`, no VS/RS series |
| 401 on JSON path | refresh session token, retry once this cycle; still 401 → target down | `kemp_up=0` |
| 401 on XML path | no retry — a failing static key keeps failing, and retrying risks account lockout | `kemp_up=0` |
| Other 4xx | no retry (family rule) | `kemp_up=0` |
| 5xx / network | resty backoff retry, capped by `collection.timeout` | `kemp_up=0` if exhausted |
| Transport decode failure | re-probe the other transport once, then stick | transport name logged; `kemp_up=0` if both fail |
| `stats` ok, `listvs` fails | keep the stats; emit VS/RS series with `name=""` | full metrics, empty name label |
| Single field unparseable | that sample is absent | metric missing for that object only |
| Whole subsystem absent (e.g. no `TPS`) | that metric family absent for that system | no `kemp_tps_*` series |
| Config reload with a bad file | keep running on the old config; log the error | no interruption |

Two consequences worth naming explicitly:

**Absent-not-zero applies to health metrics too.** If the memory section fails to parse,
`kemp_memory_free_bytes` disappears. An alert on `kemp_memory_free_bytes < X` then goes stale
rather than firing. That is the correct trade — and `deploy/prometheus/kemp.rules.yml` therefore
pairs every value alert with an `absent()` companion, so a vanished metric is itself alertable.

**`kemp_up` is per-target and per-cycle.** It reflects the last completed collection, not the
liveness of the HTTP handler. `/health` is separate and reads the snapshot age, so a wedged
collection loop is detectable even while every `kemp_up` is a stale 1.

**Secrets never reach logs.** Config redaction via a `SafeConfig` type (pflex pattern); the trace
hook skips the session-login response body; `--debug` prints samples only, never request or
response headers.

## 8. Testing

Test-driven throughout. Fixtures-only validation means the fixture set *is* the contract, so it
is built deliberately rather than accreted.

- **`testdata/`** — paired `stats.xml` / `stats.json` and `listvs.xml` / `listvs.json`, derived
  from the Kemp API documentation and the `giantswarm/kemp-client` struct tags. Plus hostile
  variants: whitespace-padded numbers, `N/A` values, a missing `TPS` section, a VS present in
  `stats` but absent from `listvs`, and two virtual services sharing one address on different
  ports.
- **Transport parity test** — the XML and JSON fixtures decode to an identical
  `models.Statistics`. This is the test that makes the single-model design safe; drift between
  the two paths fails here rather than in production.
- **Derivation tests** — table-driven and pure. Where the absent-not-zero policy is enforced:
  assert the *absence* of a sample, not a zero value.
- **Collector tests assert via both readers** — the Prometheus registry gather **and** an OTLP
  `ManualReader`. Family requirement; catches a metric wired into only one export path.
- **Label-key union test** — every series of a family carries the same key set in canonical order.
- **`httptest` mock LoadMaster** serving the fixtures, driving the full loop including transport
  detection and the fallback path.
- **Semgrep clean.** No inline `// nosemgrep` or `//nolint` suppressions — restructure instead.

### Live validation (tracked follow-up)

Every metric in `docs/metrics.md` is marked **confirmed** (documented in the Kemp API reference)
or **unconfirmed** (inferred from struct tags or reasoning). The `Network`, `CPU`, `Memory` and
`TPS` sections of the `stats` response are all unconfirmed, as is the JSON path's field naming.

Validation recipe, once an appliance is reachable:

```
kemp_exporter --config real.yaml --once --debug --trace 2>trace.log | sort > samples.txt
```

Diff `samples.txt` against `docs/metrics.md` and reconcile. Until then, the unconfirmed markers
stay in the docs.

## 9. Deliverables

Beyond the binary, all required by the family checklist:

- **`grafana/kemp-overview.json`** — panels mirroring dashboard 12160's layout, driven by
  `kemp_*` metrics and a `$system` template variable. The disk panel is dropped (no REST
  equivalent identified); interface throughput comes from `kemp_interface_bytes_*_total` rather
  than `ifHC*`.
- **`docker-compose.yml`** (builds from `./Dockerfile`) and **`docker-compose.ghcr.yml`** (pulls
  `ghcr.io/fjacquet/kemp_exporter:latest`), both with Prometheus and Grafana auto-provisioned.
  `.env.example` lists the `KEMP1_*` variables and the Grafana admin password; `.env` is
  gitignored.
- **`deploy/prometheus/kemp.rules.yml`** — target down, virtual service down, real server down,
  memory and CPU thresholds; every value alert paired with an `absent()` companion.
- **`deploy/kemp_exporter.service`** and **`deploy/kemp_exporter.env.example`** — full systemd
  sandbox hardening block, `ExecReload=/bin/kill -HUP $MAINPID`, journal logging.
- **`Dockerfile`** — multi-stage, Alpine, non-root `USER`, CA certificates **copied from the
  builder stage**, never `apk add ca-certificates` (that fails behind a corporate MITM proxy).
  **`Dockerfile.goreleaser`** with `ARG TARGETPLATFORM` + `COPY ${TARGETPLATFORM}/kemp_exporter`.
- **Makefile** with the full target contract: `tools fmt-check fmt vet lint test test-race
  test-coverage vuln ci sure cli sbom release release-snapshot docker run-cli clean`.
- **Four thin `fjacquet/ci@v1` callers** in `.github/workflows/` — `ci.yml`, `security.yml`,
  `release.yml`, `docs.yml` — copied from `fjacquet/ci/templates/workflows/`. Do not re-inline.
- **`.goreleaser.yaml`** version 2, with a `dockers_v2` block (`sbom: true`, multi-arch, tags
  `{{.Version}}` / `{{.Major}}.{{.Minor}}` / `latest`), `cyclonedx-gomod` SBOM, and a
  self-skipping Homebrew cask. GoReleaser pinned to `v2.16.0` in the Makefile.
- **`.github/dependabot.yml`** with `gomod` + `docker` ecosystems only — no `github-actions`.
- **MkDocs Material site**, `docs/metrics.md`, `docs/dashboards.md`,
  `docs/deployment/docker.md`, `docs/deployment/systemd.md`.
- **`CLAUDE.md`**, **`CHANGELOG.md`** (Keep a Changelog), **`README.md`** with the canonical
  six-badge header, `config.yaml` sample, canonical `.gitignore`.

## 10. ADRs

| # | Decision |
|---|---|
| 0001 | Supply-chain and release hardening |
| 0002 | Snapshot collection model |
| 0003 | Hand-rolled resty client — no official Kemp Go SDK; `giantswarm/kemp-client` fails modern auth and forces `InsecureSkipVerify: true` |
| 0004 | Dual transport, single model, runtime detection — and why a static API key deviates from bearer+refresh |
| 0005 | Metric naming and units — gauges vs `_total` counters, and when `rate()` is correct |
| 0006 | Label-key union invariant |
| 0007 | Own dashboard instead of Grafana 12160 — 12160 is SNMP-sourced, with zero metric overlap |
| 0008 | Config hot reload |

## 11. References

- [giantswarm/prometheus-kemp-exporter](https://github.com/giantswarm/prometheus-kemp-exporter) (archived 2023-10-27)
- [giantswarm/kemp-client](https://github.com/giantswarm/kemp-client)
- [Grafana dashboard 12160 — Kemp Loadmaster](https://grafana.com/grafana/dashboards/12160-kemp-loadmaster/)
- [LoadMaster RESTful API documentation](https://docs.progress.com/category/loadmaster-restful-api)
- [LoadMaster RESTful APIv2 documentation](https://loadmasterapiv2.docs.progress.com/)
- [How to Enable Kemp LoadMaster RESTful API interface](https://support.kemptechnologies.com/hc/en-us/articles/201640799-How-to-Enable-Kemp-LoadMaster-RESTful-API-interface)
- [Prometheus default port allocations](https://github.com/prometheus/prometheus/wiki/Default-port-allocations)
- `exporter-standards` skill — `stack.md`, `architecture.md`, `cicd.md`, `decisions.md`, `new-exporter-checklist.md`
