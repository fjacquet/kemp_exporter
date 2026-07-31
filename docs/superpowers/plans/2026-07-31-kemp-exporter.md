# kemp_exporter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Prometheus + OTLP exporter for Progress Kemp LoadMaster conforming to the `exporter-standards` family.

**Architecture:** A single background collection loop polls every configured LoadMaster on `collection.interval`, builds an immutable `Snapshot`, and pointer-swaps it into a `SnapshotStore`. Two readers consume that snapshot: an unchecked Prometheus collector serving `/metrics`, and an OTLP observable-gauge exporter pushing over gRPC. The API client hides two wire transports (XML and JSON) behind one interface, detected once per target and decoding into a single shared model.

**Tech Stack:** Go 1.26.5, `go-resty/resty/v2`, `prometheus/client_golang`, `go.opentelemetry.io/otel`, `spf13/cobra`, `sirupsen/logrus`, `gopkg.in/yaml.v2`, `joho/godotenv`, `fsnotify/fsnotify`, `golang.org/x/sync/errgroup`.

**Spec:** `docs/superpowers/specs/2026-07-30-kemp-exporter-design.md`. Read it before starting. Where this plan and the spec disagree, the plan wins and the discrepancy is called out inline.

## Global Constraints

Every task's requirements implicitly include this section.

- **Module path:** `github.com/fjacquet/kemp_exporter`. Binary name `kemp_exporter`.
- **Go version:** `go 1.26.5` in `go.mod` — patch-pinned, never bare `go 1.26`.
- **Metric prefix:** `kemp_`. Metrics port `9447`. OTLP gRPC port `4317`.
- **Identity label:** `system`, present on every metric except `kemp_exporter_build_info`.
- **Absent, never zero.** An unparseable or missing field produces no sample. Never substitute `0`.
- **Per-second values are gauges** (`sum`/`avg` in PromQL, never `rate()`). Cumulative values are counters named with a `_total` suffix (`rate()`/`increase()` correct). No metric may carry `_total` unless it is cumulative.
- **Retry excludes 4xx.** Never retry a 4xx at runtime. Transport *detection* is exempt — see Task 6.
- **TLS minimum version 1.2.** `insecureSkipVerify` is per-target config, default `false`, never hardcoded.
- **No secrets in logs.** Never `resty.SetDebug`. The trace hook logs body only and skips auth responses.
- **No inline suppressions.** No `//nolint`, no `// nosemgrep`. Restructure the code instead.
- **TDD.** Every task writes a failing test first, watches it fail, then implements. Commit at the end of every task.
- **Every metric documented** in `docs/metrics.md`, marked **confirmed** (in the Kemp API reference) or **unconfirmed** (inferred from fixtures/struct tags).

### Spec corrections locked in here

1. **`Num` lives in `internal/models`, not `internal/kemp`.** The spec's file tree (§3) places `num.go` under `internal/kemp`, but `models` structs have `Num`-typed fields. `models` importing `kemp` while `kemp` imports `models` is an import cycle. `Num` is therefore defined in `internal/models/num.go`.
2. **`Label` uses `Key`/`Value` field names** (the `ppdd` convention), not `Name`/`Value` (the `pflex` convention). Pick one and hold it — `pflex`'s `attrsFor` helper uses `l.Name` and must be transcribed as `l.Key` here.

### Unconfirmed-payload policy

No LoadMaster appliance is available. XML and JSON element paths are inferred from the Kemp API docs and `giantswarm/kemp-client` struct tags. Fixtures are therefore **the contract under test**, and the tests verify the exporter's behaviour given those fixtures — not that the fixtures match a real appliance.

Every task that hardcodes a wire path must add the metric or field to `docs/metrics.md` marked **unconfirmed**. Do not silently present inferred paths as verified. Task 18 collects these into a live-validation checklist.

---

## File Structure

| Path | Responsibility |
|---|---|
| `main.go` | cobra CLI, HTTP server (started before first collect), signal handling, wiring |
| `internal/models/num.go` | `Num` — tolerant numeric type, parsed-or-absent |
| `internal/models/statistics.go` | `Statistics`, `Totals`, `VirtualService`, `RealServer`, `CPU`, `Memory`, `Interface`, `TPS` |
| `internal/models/vsinfo.go` | `VirtualServiceInfo` — the `listvs` payload |
| `internal/config/config.go` | `Config`/`System`/`Server`/`Collection`, `Load`, `${ENV}` interpolation |
| `internal/config/env_bool.go` | `EnvBool` — bool-or-`${VAR}` YAML type |
| `internal/config/dotenv.go` | `.env` loading before interpolation |
| `internal/config/watcher.go` | SIGHUP + fsnotify reload |
| `internal/config/safe_config.go` | `SafeConfig` — redacted stringification |
| `internal/logging/logging.go` | logrus setup |
| `internal/telemetry/manager.go` | OTLP provider lifecycle |
| `internal/kemp/transport.go` | `transport` interface, detection, caching |
| `internal/kemp/transport_xml.go` | XML wire path |
| `internal/kemp/transport_json.go` | JSON wire path + session login |
| `internal/kemp/auth.go` | API key vs session token |
| `internal/kemp/tracing.go` | `OnAfterResponse` hook |
| `internal/kemp/client.go` | `Client` interface + live implementation |
| `internal/kemp/metrics.go` | `Sample`, `Label`, shared label builders |
| `internal/kemp/snapshot.go` | `Snapshot`, `SystemSnapshot`, `SnapshotStore` |
| `internal/kemp/derivations.go` | `Statistics` + `[]VirtualServiceInfo` → `[]Sample` |
| `internal/kemp/health.go` | CPU/memory/TPS/interface samples |
| `internal/kemp/state.go` | `kemp_up` and per-target state samples |
| `internal/kemp/buildinfo.go` | `kemp_exporter_build_info` collector |
| `internal/kemp/prometheus.go` | unchecked Prometheus collector |
| `internal/kemp/otlp.go` | OTLP observable gauges |
| `internal/kemp/collector.go` | the collection loop |
| `internal/kemp/testdata/` | XML + JSON fixture pairs, including hostile variants |

---

## Task 1: Tolerant numeric type

**Files:**
- Create: `go.mod`, `internal/models/num.go`
- Test: `internal/models/num_test.go`

**Interfaces:**
- Consumes: nothing (first task).
- Produces: `models.Num` with fields `Val float64` and `OK bool`; methods `UnmarshalXML(*xml.Decoder, xml.StartElement) error`, `UnmarshalJSON([]byte) error`, and `Get() (float64, bool)`.

`Num` is the mechanism that makes "absent, never zero" enforceable. A plain `int` field cannot distinguish "the LoadMaster reported 0" from "the field was `N/A`" — both decode to `0`, and the second becomes a lie in a dashboard. `Num` carries that distinction in `OK`, and every derivation checks it.

- [ ] **Step 1: Initialize the module**

```bash
cd /Users/fjacquet/Projects/kemp_exporter
go mod init github.com/fjacquet/kemp_exporter
```

Then edit `go.mod` so the `go` line is exactly `go 1.26.5`.

- [ ] **Step 2: Write the failing test**

Create `internal/models/num_test.go`:

```go
package models

import (
	"encoding/json"
	"encoding/xml"
	"testing"
)

type numHolder struct {
	V Num `xml:"V" json:"V"`
}

func TestNumXML(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantVal float64
		wantOK  bool
	}{
		{"plain", `<numHolder><V>42</V></numHolder>`, 42, true},
		{"zero", `<numHolder><V>0</V></numHolder>`, 0, true},
		{"padded", `<numHolder><V>  17  </V></numHolder>`, 17, true},
		{"float", `<numHolder><V>3.5</V></numHolder>`, 3.5, true},
		{"na", `<numHolder><V>N/A</V></numHolder>`, 0, false},
		{"empty", `<numHolder><V></V></numHolder>`, 0, false},
		{"garbage", `<numHolder><V>abc</V></numHolder>`, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var h numHolder
			if err := xml.Unmarshal([]byte(tt.in), &h); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			gotVal, gotOK := h.V.Get()
			if gotOK != tt.wantOK {
				t.Fatalf("OK = %v, want %v", gotOK, tt.wantOK)
			}
			if gotOK && gotVal != tt.wantVal {
				t.Fatalf("Val = %v, want %v", gotVal, tt.wantVal)
			}
		})
	}
}

func TestNumJSON(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantVal float64
		wantOK  bool
	}{
		{"number", `{"V":42}`, 42, true},
		{"zero", `{"V":0}`, 0, true},
		{"string number", `{"V":"17"}`, 17, true},
		{"padded string", `{"V":"  17  "}`, 17, true},
		{"na string", `{"V":"N/A"}`, 0, false},
		{"empty string", `{"V":""}`, 0, false},
		{"null", `{"V":null}`, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var h numHolder
			if err := json.Unmarshal([]byte(tt.in), &h); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			gotVal, gotOK := h.V.Get()
			if gotOK != tt.wantOK {
				t.Fatalf("OK = %v, want %v", gotOK, tt.wantOK)
			}
			if gotOK && gotVal != tt.wantVal {
				t.Fatalf("Val = %v, want %v", gotVal, tt.wantVal)
			}
		})
	}
}

// A field the payload omits entirely must be absent, not zero.
func TestNumAbsentField(t *testing.T) {
	var h numHolder
	if err := json.Unmarshal([]byte(`{}`), &h); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := h.V.Get(); ok {
		t.Fatal("omitted field reported OK; want absent")
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/models/ -run TestNum -v`
Expected: FAIL — `undefined: Num`.

- [ ] **Step 4: Implement `Num`**

Create `internal/models/num.go`:

```go
// Package models holds the decoded LoadMaster payload types shared by both
// wire transports (XML and JSON).
package models

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"strconv"
	"strings"
)

// Num is a numeric payload field that records whether it actually parsed.
//
// LoadMaster returns numbers as XML chardata; older firmware pads them with
// whitespace, and a disabled subsystem yields "" or "N/A". Decoding those into a
// plain float64 would silently produce 0, which is indistinguishable from a real
// zero reading. Num keeps that distinction so derivations can omit the sample
// entirely rather than publish a fabricated value.
type Num struct {
	Val float64
	OK  bool
}

// Get returns the value and whether it parsed. Callers must check the bool and
// skip emitting a sample when it is false.
func (n Num) Get() (float64, bool) { return n.Val, n.OK }

// parse fills n from a raw payload string. Unparseable input leaves n absent
// rather than returning an error: one bad field must not fail the whole decode.
func (n *Num) parse(raw string) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return
	}
	n.Val, n.OK = v, true
}

// UnmarshalXML decodes chardata into the tolerant representation.
func (n *Num) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var raw string
	if err := d.DecodeElement(&raw, &start); err != nil {
		return err
	}
	n.parse(raw)
	return nil
}

// UnmarshalJSON accepts a JSON number, a stringified number, or null. Kemp's JSON
// mode is not consistent about which it uses for a given field.
func (n *Num) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		n.parse(s)
		return nil
	}
	n.parse(string(b))
	return nil
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/models/ -v`
Expected: PASS, all subtests.

- [ ] **Step 6: Commit**

```bash
git add go.mod internal/models/num.go internal/models/num_test.go
git commit -m "feat(models): add tolerant Num type for parsed-or-absent fields"
```

---

## Task 2: Payload models

**Files:**
- Create: `internal/models/statistics.go`, `internal/models/vsinfo.go`
- Test: `internal/models/statistics_test.go`
- Create: `internal/kemp/testdata/stats.xml`, `internal/kemp/testdata/stats.json`, `internal/kemp/testdata/listvs.xml`, `internal/kemp/testdata/listvs.json`

**Interfaces:**
- Consumes: `models.Num` from Task 1.
- Produces: `models.Statistics` (fields `Totals`, `VirtualServices []VirtualService`, `RealServers []RealServer`, `CPUs []CPU`, `Memory`, `Interfaces []Interface`, `TPS`), `models.VirtualServiceInfo` (fields `Name`, `Address`, `Port`, `Protocol`, `Status` — all `string` except `Port int`).

The fixtures created here are consumed by Tasks 4, 5, 6, 9, 10 and 14. They live under `internal/kemp/testdata/` (not `internal/models/`) because the transports and the collector are their primary consumers; the models test reads them via a relative path.

**Wire paths in this task are unconfirmed.** Record every one in a running list for `docs/metrics.md` (Task 18).

- [ ] **Step 1: Write the fixtures**

Create `internal/kemp/testdata/stats.xml`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<Response stat="200" code="ok">
  <Success>
    <Data>
      <VStotals>
        <ConnsPerSec>150</ConnsPerSec>
        <BytesPerSec>2048000</BytesPerSec>
        <PktsPerSec>3200</PktsPerSec>
      </VStotals>
      <CPU>
        <total>
          <User>12</User>
          <System>8</System>
          <Idle>80</Idle>
        </total>
        <cpu0>
          <User>14</User>
          <System>9</System>
          <Idle>77</Idle>
        </cpu0>
      </CPU>
      <Memory>
        <memused>2147483648</memused>
        <percentmemused>50</percentmemused>
        <memfree>2147483648</memfree>
      </Memory>
      <TPS>
        <Total>420</Total>
        <SSL>75</SSL>
      </TPS>
      <Network>
        <eth0>
          <ifaceID>0</ifaceID>
          <bytesread>987654321</bytesread>
          <byteswritten>123456789</byteswritten>
        </eth0>
      </Network>
      <Vs>
        <Index>1</Index>
        <VSAddress>10.0.0.10</VSAddress>
        <VSPort>443</VSPort>
        <VSProt>tcp</VSProt>
        <TotalConns>50000</TotalConns>
        <TotalPkts>900000</TotalPkts>
        <TotalBytes>7000000000</TotalBytes>
        <ActiveConns>42</ActiveConns>
        <ConnsPerSec>90</ConnsPerSec>
        <BytesRead>4000000000</BytesRead>
        <BytesWritten>3000000000</BytesWritten>
        <Enable>1</Enable>
      </Vs>
      <Vs>
        <Index>2</Index>
        <VSAddress>10.0.0.10</VSAddress>
        <VSPort>80</VSPort>
        <VSProt>tcp</VSProt>
        <TotalConns>1200</TotalConns>
        <TotalPkts>24000</TotalPkts>
        <TotalBytes>90000000</TotalBytes>
        <ActiveConns>3</ActiveConns>
        <ConnsPerSec>5</ConnsPerSec>
        <BytesRead>50000000</BytesRead>
        <BytesWritten>40000000</BytesWritten>
        <Enable>1</Enable>
      </Vs>
      <Rs>
        <VSIndex>1</VSIndex>
        <RSIndex>1</RSIndex>
        <Addr>192.168.1.20</Addr>
        <Port>8443</Port>
        <Conns>25000</Conns>
        <Pkts>450000</Pkts>
        <Bytes>3500000000</Bytes>
        <ActiveConns>21</ActiveConns>
        <ConnsPerSec>45</ConnsPerSec>
        <BytesRead>2000000000</BytesRead>
        <BytesWritten>1500000000</BytesWritten>
      </Rs>
    </Data>
  </Success>
</Response>
```

Create `internal/kemp/testdata/stats.json` — the same data through the JSON path:

```json
{
  "status": "ok",
  "code": 200,
  "Success": {
    "Data": {
      "VStotals": { "ConnsPerSec": 150, "BytesPerSec": 2048000, "PktsPerSec": 3200 },
      "CPU": [
        { "id": "total", "User": 12, "System": 8, "Idle": 80 },
        { "id": "cpu0", "User": 14, "System": 9, "Idle": 77 }
      ],
      "Memory": { "memused": 2147483648, "percentmemused": 50, "memfree": 2147483648 },
      "TPS": { "Total": 420, "SSL": 75 },
      "Network": [
        { "id": "eth0", "ifaceID": 0, "bytesread": 987654321, "byteswritten": 123456789 }
      ],
      "Vs": [
        { "Index": 1, "VSAddress": "10.0.0.10", "VSPort": 443, "VSProt": "tcp",
          "TotalConns": 50000, "TotalPkts": 900000, "TotalBytes": 7000000000,
          "ActiveConns": 42, "ConnsPerSec": 90,
          "BytesRead": 4000000000, "BytesWritten": 3000000000, "Enable": 1 },
        { "Index": 2, "VSAddress": "10.0.0.10", "VSPort": 80, "VSProt": "tcp",
          "TotalConns": 1200, "TotalPkts": 24000, "TotalBytes": 90000000,
          "ActiveConns": 3, "ConnsPerSec": 5,
          "BytesRead": 50000000, "BytesWritten": 40000000, "Enable": 1 }
      ],
      "Rs": [
        { "VSIndex": 1, "RSIndex": 1, "Addr": "192.168.1.20", "Port": 8443,
          "Conns": 25000, "Pkts": 450000, "Bytes": 3500000000,
          "ActiveConns": 21, "ConnsPerSec": 45,
          "BytesRead": 2000000000, "BytesWritten": 1500000000 }
      ]
    }
  }
}
```

Create `internal/kemp/testdata/listvs.xml`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<Response stat="200" code="ok">
  <Success>
    <Data>
      <VS>
        <Index>1</Index>
        <NickName>web-https</NickName>
        <VSAddress>10.0.0.10</VSAddress>
        <VSPort>443</VSPort>
        <Protocol>tcp</Protocol>
        <Status>Up</Status>
      </VS>
      <VS>
        <Index>2</Index>
        <NickName>web-http</NickName>
        <VSAddress>10.0.0.10</VSAddress>
        <VSPort>80</VSPort>
        <Protocol>tcp</Protocol>
        <Status>Sick</Status>
      </VS>
    </Data>
  </Success>
</Response>
```

Create `internal/kemp/testdata/listvs.json`:

```json
{
  "status": "ok",
  "code": 200,
  "Success": {
    "Data": {
      "VS": [
        { "Index": 1, "NickName": "web-https", "VSAddress": "10.0.0.10",
          "VSPort": 443, "Protocol": "tcp", "Status": "Up" },
        { "Index": 2, "NickName": "web-http", "VSAddress": "10.0.0.10",
          "VSPort": 80, "Protocol": "tcp", "Status": "Sick" }
      ]
    }
  }
}
```

Note the two virtual services deliberately share address `10.0.0.10` on different ports. That is the collision case the upstream exporter got wrong, and Task 9 tests the join against it.

- [ ] **Step 2: Write the failing test**

Create `internal/models/statistics_test.go`:

```go
package models

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"testing"
)

// xmlEnvelope mirrors the transport-layer envelope so models can be tested
// against the real fixture shape without importing the kemp package.
type xmlEnvelope struct {
	XMLName xml.Name   `xml:"Response"`
	Stat    string     `xml:"stat,attr"`
	Data    Statistics `xml:"Success>Data"`
}

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "kemp", "testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

func TestStatisticsXMLDecode(t *testing.T) {
	var env xmlEnvelope
	if err := xml.Unmarshal(loadFixture(t, "stats.xml"), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	st := env.Data

	if v, ok := st.Totals.ConnsPerSec.Get(); !ok || v != 150 {
		t.Errorf("Totals.ConnsPerSec = %v/%v, want 150/true", v, ok)
	}
	if got := len(st.VirtualServices); got != 2 {
		t.Fatalf("len(VirtualServices) = %d, want 2", got)
	}
	vs := st.VirtualServices[0]
	if vs.Address != "10.0.0.10" || vs.Port != 443 || vs.Protocol != "tcp" {
		t.Errorf("VS[0] identity = %s:%d/%s, want 10.0.0.10:443/tcp", vs.Address, vs.Port, vs.Protocol)
	}
	if v, ok := vs.ActiveConns.Get(); !ok || v != 42 {
		t.Errorf("VS[0].ActiveConns = %v/%v, want 42/true", v, ok)
	}
	if got := len(st.RealServers); got != 1 {
		t.Fatalf("len(RealServers) = %d, want 1", got)
	}
	if st.RealServers[0].VSIndex != 1 {
		t.Errorf("RS[0].VSIndex = %d, want 1", st.RealServers[0].VSIndex)
	}
	if got := len(st.CPUs); got != 2 {
		t.Fatalf("len(CPUs) = %d, want 2 (total + cpu0)", got)
	}
	if st.CPUs[0].ID != "total" {
		t.Errorf("CPUs[0].ID = %q, want \"total\"", st.CPUs[0].ID)
	}
	if v, ok := st.Memory.FreeBytes.Get(); !ok || v != 2147483648 {
		t.Errorf("Memory.FreeBytes = %v/%v, want 2147483648/true", v, ok)
	}
	if got := len(st.Interfaces); got != 1 || st.Interfaces[0].ID != "eth0" {
		t.Errorf("Interfaces = %+v, want one entry with ID eth0", st.Interfaces)
	}
	if v, ok := st.TPS.SSL.Get(); !ok || v != 75 {
		t.Errorf("TPS.SSL = %v/%v, want 75/true", v, ok)
	}
}

func TestVirtualServiceInfoXMLDecode(t *testing.T) {
	var env struct {
		XMLName xml.Name             `xml:"Response"`
		VS      []VirtualServiceInfo `xml:"Success>Data>VS"`
	}
	if err := xml.Unmarshal(loadFixture(t, "listvs.xml"), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := len(env.VS); got != 2 {
		t.Fatalf("len(VS) = %d, want 2", got)
	}
	if env.VS[0].Name != "web-https" || env.VS[0].Port != 443 || env.VS[0].Status != "Up" {
		t.Errorf("VS[0] = %+v, want web-https:443 Up", env.VS[0])
	}
	if env.VS[1].Status != "Sick" {
		t.Errorf("VS[1].Status = %q, want \"Sick\"", env.VS[1].Status)
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/models/ -run 'TestStatistics|TestVirtualServiceInfo' -v`
Expected: FAIL — `undefined: Statistics`.

- [ ] **Step 4: Implement the models**

Create `internal/models/statistics.go`:

```go
package models

import "encoding/xml"

// Totals is the appliance-wide VStotals block. Every field is already a
// per-second rate, so these become gauges, never counters.
type Totals struct {
	ConnsPerSec Num `xml:"ConnsPerSec" json:"ConnsPerSec"`
	BytesPerSec Num `xml:"BytesPerSec" json:"BytesPerSec"`
	PktsPerSec  Num `xml:"PktsPerSec"  json:"PktsPerSec"`
}

// VirtualService is one Vs element of the stats payload. Note it carries no
// service name — that comes from listvs and is joined on address:port.
type VirtualService struct {
	Index        int    `xml:"Index"        json:"Index"`
	Address      string `xml:"VSAddress"    json:"VSAddress"`
	Port         int    `xml:"VSPort"       json:"VSPort"`
	Protocol     string `xml:"VSProt"       json:"VSProt"`
	TotalConns   Num    `xml:"TotalConns"   json:"TotalConns"`
	TotalPkts    Num    `xml:"TotalPkts"    json:"TotalPkts"`
	TotalBytes   Num    `xml:"TotalBytes"   json:"TotalBytes"`
	ActiveConns  Num    `xml:"ActiveConns"  json:"ActiveConns"`
	ConnsPerSec  Num    `xml:"ConnsPerSec"  json:"ConnsPerSec"`
	BytesRead    Num    `xml:"BytesRead"    json:"BytesRead"`
	BytesWritten Num    `xml:"BytesWritten" json:"BytesWritten"`
	Enable       Num    `xml:"Enable"       json:"Enable"`
}

// RealServer is one Rs element. VSIndex ties it back to its virtual service.
type RealServer struct {
	VSIndex      int    `xml:"VSIndex"      json:"VSIndex"`
	RSIndex      int    `xml:"RSIndex"      json:"RSIndex"`
	Address      string `xml:"Addr"         json:"Addr"`
	Port         int    `xml:"Port"         json:"Port"`
	TotalConns   Num    `xml:"Conns"        json:"Conns"`
	TotalPkts    Num    `xml:"Pkts"         json:"Pkts"`
	TotalBytes   Num    `xml:"Bytes"        json:"Bytes"`
	ActiveConns  Num    `xml:"ActiveConns"  json:"ActiveConns"`
	ConnsPerSec  Num    `xml:"ConnsPerSec"  json:"ConnsPerSec"`
	BytesRead    Num    `xml:"BytesRead"    json:"BytesRead"`
	BytesWritten Num    `xml:"BytesWritten" json:"BytesWritten"`
	Status       string `xml:"Status"       json:"Status"`
}

// CPU is one processor row. ID is "total" for the aggregate and "cpuN" per core.
type CPU struct {
	ID     string `xml:"-" json:"id"`
	User   Num    `xml:"User"   json:"User"`
	System Num    `xml:"System" json:"System"`
	Idle   Num    `xml:"Idle"   json:"Idle"`
}

// Memory is the appliance memory block. Kemp reports bytes here, not kilobytes.
type Memory struct {
	UsedBytes   Num `xml:"memused"        json:"memused"`
	UsedPercent Num `xml:"percentmemused" json:"percentmemused"`
	FreeBytes   Num `xml:"memfree"        json:"memfree"`
}

// TPS holds transactions per second. Despite the name these are instantaneous
// rates, so they map to gauges without a _total suffix.
type TPS struct {
	Total Num `xml:"Total" json:"Total"`
	SSL   Num `xml:"SSL"   json:"SSL"`
}

// Interface is one network interface row.
type Interface struct {
	ID           string `xml:"-" json:"id"`
	BytesRead    Num    `xml:"bytesread"    json:"bytesread"`
	BytesWritten Num    `xml:"byteswritten" json:"byteswritten"`
}

// Statistics is the decoded stats payload.
//
// The CPU and Network sections are keyed by element NAME in XML (<total>, <cpu0>,
// <eth0>) but are arrays with an "id" field in JSON. Custom UnmarshalXML methods on
// cpuSection and networkSection reconcile the two into the same slice shape, so both
// transports produce an identical Statistics.
type Statistics struct {
	Totals          Totals           `xml:"VStotals" json:"VStotals"`
	VirtualServices []VirtualService `xml:"Vs"       json:"Vs"`
	RealServers     []RealServer     `xml:"Rs"       json:"Rs"`
	CPUs            cpuSection       `xml:"CPU"      json:"CPU"`
	Memory          Memory           `xml:"Memory"   json:"Memory"`
	Interfaces      netSection       `xml:"Network"  json:"Network"`
	TPS             TPS              `xml:"TPS"      json:"TPS"`
}

// cpuSection decodes the name-keyed XML <CPU> block into a flat slice.
type cpuSection []CPU

// UnmarshalXML walks the child elements of <CPU>, using each element's own name
// as the cpu ID.
func (c *cpuSection) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			var cpu CPU
			if err := d.DecodeElement(&cpu, &t); err != nil {
				return err
			}
			cpu.ID = t.Name.Local
			*c = append(*c, cpu)
		case xml.EndElement:
			if t.Name == start.Name {
				return nil
			}
		}
	}
}

// netSection decodes the name-keyed XML <Network> block into a flat slice.
func (n *netSection) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			var iface Interface
			if err := d.DecodeElement(&iface, &t); err != nil {
				return err
			}
			iface.ID = t.Name.Local
			*n = append(*n, iface)
		case xml.EndElement:
			if t.Name == start.Name {
				return nil
			}
		}
	}
}

// netSection is a slice of Interface with XML-shape reconciliation.
type netSection []Interface
```

Create `internal/models/vsinfo.go`:

```go
package models

// VirtualServiceInfo is one entry from the listvs command. The stats payload
// omits service names, so this supplies them; the join key is address:port,
// never address alone — one VIP commonly hosts several ports.
type VirtualServiceInfo struct {
	Index    int    `xml:"Index"     json:"Index"`
	Name     string `xml:"NickName"  json:"NickName"`
	Address  string `xml:"VSAddress" json:"VSAddress"`
	Port     int    `xml:"VSPort"    json:"VSPort"`
	Protocol string `xml:"Protocol"  json:"Protocol"`
	Status   string `xml:"Status"    json:"Status"`
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/models/ -v`
Expected: PASS. If the `netSection` type declaration ordering causes a compile error, move the `type netSection []Interface` declaration above its methods — Go allows either, but keep the file readable.

- [ ] **Step 6: Commit**

```bash
git add internal/models/ internal/kemp/testdata/
git commit -m "feat(models): add LoadMaster payload types and wire fixtures"
```

---

## Task 3: Configuration

**Files:**
- Create: `internal/config/config.go`, `internal/config/env_bool.go`, `internal/config/dotenv.go`, `internal/config/safe_config.go`, `config.yaml`, `.env.example`
- Test: `internal/config/config_test.go`, `internal/config/env_bool_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces:
  - `config.Config{Server Server, Collection Collection, OTel OTelConfig, Systems []System}`
  - `config.System{Name, Host string, Port int, APIKey, Username, Password, PasswordFile string, InsecureSkipVerify EnvBool}` with method `BaseURL() string`
  - `config.Server{Host, Port, URI, LogName string}`
  - `config.Collection{Interval, Timeout time.Duration, MaxConcurrent int}`
  - `config.OTelConfig{Enabled bool, Endpoint string, Insecure bool, Interval time.Duration}`
  - `config.Load(path string) (*Config, error)`
  - `config.LoadDotEnv(cfgPath string)`
  - `config.EnvBool` with `Resolve(func(string) (string, error)) error` and `Value() bool`
  - `config.SafeConfig(c *Config) string`

Model this on `/Users/fjacquet/Projects/ppdd_exporter/internal/config/config.go`, which is the family reference. The differences from that file, all deliberate:

- LoadMaster listens on **443**, not 3009 — `BaseURL` defaults `Port` to 443.
- An **`apiKey` field** exists alongside username/password, because the XML transport authenticates with a static key. `apiKey` interpolates and honours `passwordFile` semantics via its own `apiKeyFile`.
- `Collection` gains **`MaxConcurrent`** (default 4) to cap the per-cycle errgroup.
- Default `Collection.Interval` is **`60s`**, not 5m (spec §5).
- Server port default is **`9447`**.

- [ ] **Step 1: Write the failing test**

Create `internal/config/config_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadAppliesDefaults(t *testing.T) {
	path := writeConfig(t, `
systems:
  - name: lm-01
    host: 10.0.0.1
    apiKey: secret
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Port != "9447" {
		t.Errorf("Server.Port = %q, want \"9447\"", cfg.Server.Port)
	}
	if cfg.Server.URI != "/metrics" {
		t.Errorf("Server.URI = %q, want \"/metrics\"", cfg.Server.URI)
	}
	if cfg.Collection.Interval != 60*time.Second {
		t.Errorf("Collection.Interval = %v, want 60s", cfg.Collection.Interval)
	}
	if cfg.Collection.Timeout != 60*time.Second {
		t.Errorf("Collection.Timeout = %v, want 60s", cfg.Collection.Timeout)
	}
	if cfg.Collection.MaxConcurrent != 4 {
		t.Errorf("Collection.MaxConcurrent = %d, want 4", cfg.Collection.MaxConcurrent)
	}
	if got := cfg.Systems[0].BaseURL(); got != "https://10.0.0.1:443" {
		t.Errorf("BaseURL = %q, want https://10.0.0.1:443", got)
	}
	if cfg.Systems[0].InsecureSkipVerify.Value() {
		t.Error("InsecureSkipVerify defaulted true; must default false")
	}
}

func TestLoadInterpolatesEnv(t *testing.T) {
	t.Setenv("KEMP1_HOSTNAME", "lm.example.com")
	t.Setenv("KEMP1_APIKEY", "abc123")
	path := writeConfig(t, `
systems:
  - name: lm-01
    host: ${KEMP1_HOSTNAME}
    apiKey: ${KEMP1_APIKEY}
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Systems[0].Host != "lm.example.com" {
		t.Errorf("Host = %q, want lm.example.com", cfg.Systems[0].Host)
	}
	if cfg.Systems[0].APIKey != "abc123" {
		t.Errorf("APIKey = %q, want abc123", cfg.Systems[0].APIKey)
	}
}

// An unset ${VAR} must fail at load, not silently produce an empty credential
// that turns into repeated runtime auth failures.
func TestLoadFailsFastOnUnsetEnv(t *testing.T) {
	path := writeConfig(t, `
systems:
  - name: lm-01
    host: 10.0.0.1
    apiKey: ${KEMP_DEFINITELY_UNSET_VAR}
`)
	if _, err := Load(path); err == nil {
		t.Fatal("Load succeeded with an unset env reference; want error")
	}
}

func TestLoadReadsAPIKeyFile(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "apikey")
	if err := os.WriteFile(keyPath, []byte("  filekey\n"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	path := writeConfig(t, `
systems:
  - name: lm-01
    host: 10.0.0.1
    apiKeyFile: `+keyPath+`
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Systems[0].APIKey != "filekey" {
		t.Errorf("APIKey = %q, want \"filekey\" (trimmed)", cfg.Systems[0].APIKey)
	}
}

func TestLoadRejectsEmptySystems(t *testing.T) {
	path := writeConfig(t, "systems: []\n")
	if _, err := Load(path); err == nil {
		t.Fatal("Load succeeded with no systems; want error")
	}
}

// SafeConfig is what gets logged. A credential appearing in its output is a
// security defect, so this test guards it directly.
func TestSafeConfigRedactsCredentials(t *testing.T) {
	path := writeConfig(t, `
systems:
  - name: lm-01
    host: 10.0.0.1
    apiKey: SUPERSECRETKEY
    password: ALSOSECRET
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	out := SafeConfig(cfg)
	for _, secret := range []string{"SUPERSECRETKEY", "ALSOSECRET"} {
		if contains(out, secret) {
			t.Errorf("SafeConfig output leaked %q:\n%s", secret, out)
		}
	}
	if !contains(out, "lm-01") {
		t.Errorf("SafeConfig dropped the system name; output:\n%s", out)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		(func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		})()
}
```

Create `internal/config/env_bool_test.go`:

```go
package config

import (
	"testing"

	"gopkg.in/yaml.v2"
)

func TestEnvBoolNativeBool(t *testing.T) {
	var holder struct {
		V EnvBool `yaml:"v"`
	}
	if err := yaml.Unmarshal([]byte("v: true\n"), &holder); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := holder.V.Resolve(func(s string) (string, error) { return s, nil }); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !holder.V.Value() {
		t.Error("Value() = false, want true")
	}
}

func TestEnvBoolEnvReference(t *testing.T) {
	var holder struct {
		V EnvBool `yaml:"v"`
	}
	if err := yaml.Unmarshal([]byte("v: ${KEMP1_SKIP_VERIFY}\n"), &holder); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := holder.V.Resolve(func(string) (string, error) { return "true", nil }); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !holder.V.Value() {
		t.Error("Value() = false, want true after resolving ${VAR} to \"true\"")
	}
}

func TestEnvBoolAbsentDefaultsFalse(t *testing.T) {
	var holder struct {
		V EnvBool `yaml:"v"`
	}
	if err := yaml.Unmarshal([]byte("{}\n"), &holder); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := holder.V.Resolve(func(s string) (string, error) { return s, nil }); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if holder.V.Value() {
		t.Error("absent insecureSkipVerify resolved true; must default false")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/config/ -v`
Expected: FAIL — `undefined: Load`, `undefined: EnvBool`.

- [ ] **Step 3: Add the dependencies**

```bash
go get gopkg.in/yaml.v2@v2.4.0
go get github.com/joho/godotenv@v1.5.1
go get github.com/fsnotify/fsnotify@v1.10.1
```

- [ ] **Step 4: Implement `EnvBool`**

Create `internal/config/env_bool.go`:

```go
package config

import (
	"fmt"
	"strconv"
	"strings"
)

// EnvBool is a YAML boolean that also accepts a "${VAR}" reference, resolved after
// load. It exists so insecureSkipVerify can be driven from the environment in a
// compose or systemd deployment without templating the config file.
//
// The zero value is false, which is the required default: TLS verification stays on
// unless something explicitly turns it off.
type EnvBool struct {
	raw      string
	resolved bool
}

// UnmarshalYAML accepts either a native bool or a string (possibly a ${VAR} ref).
func (e *EnvBool) UnmarshalYAML(unmarshal func(any) error) error {
	var b bool
	if err := unmarshal(&b); err == nil {
		e.resolved = b
		return nil
	}
	var s string
	if err := unmarshal(&s); err != nil {
		return fmt.Errorf("insecureSkipVerify must be a boolean or a ${VAR} reference: %w", err)
	}
	e.raw = s
	return nil
}

// Resolve expands any ${VAR} reference using interp and parses the result.
// Called once during Load; a no-op when the YAML held a native bool.
func (e *EnvBool) Resolve(interp func(string) (string, error)) error {
	if e.raw == "" {
		return nil
	}
	expanded, err := interp(e.raw)
	if err != nil {
		return err
	}
	expanded = strings.TrimSpace(expanded)
	if expanded == "" {
		return nil
	}
	b, err := strconv.ParseBool(expanded)
	if err != nil {
		return fmt.Errorf("value %q is not a boolean", expanded)
	}
	e.resolved = b
	return nil
}

// Value reports the resolved boolean.
func (e EnvBool) Value() bool { return e.resolved }
```

- [ ] **Step 5: Implement `Load`**

Create `internal/config/config.go`:

```go
// Package config loads and validates the exporter configuration.
package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v2"
)

// System is one LoadMaster to monitor.
//
// Two credential shapes are supported because the two wire transports authenticate
// differently: the XML path uses a static API key, the JSON path a username/password
// session login. A system may configure either or both; the transport picked at
// detection time decides which is used.
type System struct {
	Name               string  `yaml:"name"`
	Host               string  `yaml:"host"`
	Port               int     `yaml:"port"` // defaults to 443
	APIKey             string  `yaml:"apiKey"`
	APIKeyFile         string  `yaml:"apiKeyFile"`
	Username           string  `yaml:"username"`
	Password           string  `yaml:"password"`
	PasswordFile       string  `yaml:"passwordFile"`
	InsecureSkipVerify EnvBool `yaml:"insecureSkipVerify"`
}

// BaseURL returns the https://host:port root for the LoadMaster REST API.
func (s System) BaseURL() string {
	port := s.Port
	if port == 0 {
		port = 443
	}
	return fmt.Sprintf("https://%s:%d", s.Host, port)
}

// Server holds HTTP-server settings.
type Server struct {
	Host    string `yaml:"host"`
	Port    string `yaml:"port"`
	URI     string `yaml:"uri"`
	LogName string `yaml:"logName"` // "" -> stdout
}

// Collection holds loop timing and concurrency.
type Collection struct {
	Interval      time.Duration `yaml:"interval"`
	Timeout       time.Duration `yaml:"timeout"`
	MaxConcurrent int           `yaml:"maxConcurrent"`
}

// OTelConfig configures the OTLP push exporter.
type OTelConfig struct {
	Enabled  bool          `yaml:"enabled"`
	Endpoint string        `yaml:"endpoint"`
	Insecure bool          `yaml:"insecure"`
	Interval time.Duration `yaml:"interval"`
}

// Config is the whole file.
type Config struct {
	Server     Server     `yaml:"server"`
	Collection Collection `yaml:"collection"`
	OTel       OTelConfig `yaml:"otel"`
	Systems    []System   `yaml:"systems"`
}

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// interpolate replaces every ${VAR} in s with its environment value, returning an
// error if any referenced variable is unset. Failing fast turns a typo'd secret
// name into a config-load error instead of repeated runtime auth failures.
func interpolate(s string) (string, error) {
	var missing []string
	out := envRef.ReplaceAllStringFunc(s, func(m string) string {
		name := envRef.FindStringSubmatch(m)[1]
		v, ok := os.LookupEnv(name)
		if !ok {
			missing = append(missing, name)
		}
		return v
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("unset environment variable(s): %s", strings.Join(missing, ", "))
	}
	return out, nil
}

// readSecretFile returns the trimmed contents of path.
func readSecretFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// Load reads, interpolates ${ENV} references, applies defaults, and validates.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	for i := range cfg.Systems {
		s := &cfg.Systems[i]
		// Interpolate name first so later errors can quote the resolved name.
		for _, f := range []struct {
			label string
			ptr   *string
		}{
			{"name", &s.Name},
			{"host", &s.Host},
			{"apiKey", &s.APIKey},
			{"username", &s.Username},
			{"password", &s.Password},
		} {
			v, err := interpolate(*f.ptr)
			if err != nil {
				return nil, fmt.Errorf("system %s %s: %w", s.Name, f.label, err)
			}
			*f.ptr = v
		}
		if s.APIKeyFile != "" && s.APIKey == "" {
			v, err := readSecretFile(s.APIKeyFile)
			if err != nil {
				return nil, fmt.Errorf("system %s apiKeyFile: %w", s.Name, err)
			}
			s.APIKey = v
		}
		if s.PasswordFile != "" && s.Password == "" {
			v, err := readSecretFile(s.PasswordFile)
			if err != nil {
				return nil, fmt.Errorf("system %s passwordFile: %w", s.Name, err)
			}
			s.Password = v
		}
		if err := s.InsecureSkipVerify.Resolve(interpolate); err != nil {
			return nil, fmt.Errorf("system %s insecureSkipVerify: %w", s.Name, err)
		}
		if s.Host == "" {
			return nil, fmt.Errorf("system %s: host is required", s.Name)
		}
		if s.APIKey == "" && (s.Username == "" || s.Password == "") {
			return nil, fmt.Errorf("system %s: needs apiKey (XML path) or username+password (JSON path)", s.Name)
		}
	}

	if cfg.Server.Port == "" {
		cfg.Server.Port = "9447"
	}
	if cfg.Server.URI == "" {
		cfg.Server.URI = "/metrics"
	}
	if cfg.Collection.Interval == 0 {
		cfg.Collection.Interval = 60 * time.Second
	}
	if cfg.Collection.Timeout == 0 {
		cfg.Collection.Timeout = 60 * time.Second
	}
	if cfg.Collection.MaxConcurrent == 0 {
		cfg.Collection.MaxConcurrent = 4
	}
	if cfg.OTel.Endpoint == "" {
		cfg.OTel.Endpoint = "localhost:4317"
	}
	if cfg.OTel.Interval == 0 {
		cfg.OTel.Interval = 10 * time.Second
	}
	if len(cfg.Systems) == 0 {
		return nil, fmt.Errorf("no systems configured")
	}
	return &cfg, nil
}
```

- [ ] **Step 6: Implement `SafeConfig` and `LoadDotEnv`**

Create `internal/config/safe_config.go`:

```go
package config

import (
	"fmt"
	"strings"
)

// SafeConfig renders the configuration for logging with every credential removed.
// Use it anywhere the config would otherwise be printed; the raw Config must never
// reach a log line.
func SafeConfig(c *Config) string {
	if c == nil {
		return "<nil config>"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "server: %s:%s%s\n", c.Server.Host, c.Server.Port, c.Server.URI)
	fmt.Fprintf(&b, "collection: interval=%s timeout=%s maxConcurrent=%d\n",
		c.Collection.Interval, c.Collection.Timeout, c.Collection.MaxConcurrent)
	fmt.Fprintf(&b, "otel: enabled=%t endpoint=%s insecure=%t interval=%s\n",
		c.OTel.Enabled, c.OTel.Endpoint, c.OTel.Insecure, c.OTel.Interval)
	for _, s := range c.Systems {
		fmt.Fprintf(&b, "system %s: url=%s auth=%s insecureSkipVerify=%t\n",
			s.Name, s.BaseURL(), authMode(s), s.InsecureSkipVerify.Value())
	}
	return b.String()
}

// authMode describes which credentials are present without revealing them.
func authMode(s System) string {
	switch {
	case s.APIKey != "" && s.Username != "":
		return "apikey+session"
	case s.APIKey != "":
		return "apikey"
	case s.Username != "":
		return "session"
	default:
		return "none"
	}
}
```

Create `internal/config/dotenv.go`:

```go
package config

import (
	"path/filepath"

	"github.com/joho/godotenv"
)

// LoadDotEnv loads a .env file from the working directory and then from the config
// file's directory, before any ${VAR} interpolation runs.
//
// godotenv never overrides an already-set environment variable, so real secret
// injection (systemd EnvironmentFile, compose environment:, a Kubernetes secret)
// always wins over a checked-out .env. Missing files are not an error — .env is a
// developer convenience, and config.yaml remains the source of truth.
func LoadDotEnv(cfgPath string) {
	_ = godotenv.Load(".env")
	if cfgPath != "" {
		_ = godotenv.Load(filepath.Join(filepath.Dir(cfgPath), ".env"))
	}
}
```

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go test ./internal/config/ -v`
Expected: PASS.

- [ ] **Step 8: Write the sample config and env example**

Create `config.yaml`:

```yaml
---
server:
  host: "0.0.0.0"
  port: "9447"
  uri: "/metrics"
  logName: ""            # "" -> stdout
collection:
  interval: "60s"        # tune freely; no metric depends on the cycle length
  timeout: "60s"
  maxConcurrent: 4
otel:
  enabled: false
  endpoint: "localhost:4317"
  insecure: true
  interval: "10s"
systems:
  # Single-target quickstart: set KEMP1_HOSTNAME and KEMP1_APIKEY in the
  # environment (or a .env file). config.yaml is the source of truth — for
  # multiple LoadMasters add one entry per appliance.
  - name: lm-prod-01
    host: "${KEMP1_HOSTNAME}"
    # XML transport: a static API key, ideally scoped read-only.
    apiKey: "${KEMP1_APIKEY}"
    # JSON transport: session login. Optional; supply if the appliance runs
    # firmware 7.2.50+ with session management enabled.
    # username: "${KEMP1_USERNAME}"
    # password: "${KEMP1_PASSWORD}"
    # insecureSkipVerify disables TLS certificate verification (man-in-the-middle
    # risk). Leave it off in production; enable only for dev against a LoadMaster
    # with a self-signed certificate. Accepts a native boolean or a ${VAR} ref.
    # insecureSkipVerify: false
```

Create `.env.example`:

```bash
# Copy to .env for the docker-compose quickstart. .env is gitignored.
KEMP1_HOSTNAME=10.0.0.1
KEMP1_APIKEY=replace-me
# Optional, for the JSON/session transport:
# KEMP1_USERNAME=bal
# KEMP1_PASSWORD=replace-me
# KEMP1_SKIP_CERTIFICATE=false

GRAFANA_ADMIN_PASSWORD=admin
```

- [ ] **Step 9: Commit**

```bash
git add internal/config/ config.yaml .env.example go.mod go.sum
git commit -m "feat(config): add YAML config with env interpolation and secret redaction"
```

---

## Task 4: Config hot reload and logging

**Files:**
- Create: `internal/config/watcher.go`, `internal/logging/logging.go`
- Test: `internal/config/watcher_test.go`

**Interfaces:**
- Consumes: `config.Load` from Task 3.
- Produces:
  - `config.Watcher` with `NewWatcher(path string, onReload func(*Config)) (*Watcher, error)`, `Start(ctx context.Context)`, `Close() error`
  - `logging.Setup(logName string, debug bool) error`

The reload contract: a bad config file **never** takes the exporter down. `onReload` is called only with a successfully parsed and validated config; a parse failure logs and leaves the running config in place.

- [ ] **Step 1: Write the failing test**

Create `internal/config/watcher_test.go`:

```go
package config

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestWatcherReloadsOnChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	write := func(host string) {
		body := "systems:\n  - name: lm-01\n    host: " + host + "\n    apiKey: k\n"
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	write("10.0.0.1")

	var mu sync.Mutex
	var got []string
	w, err := NewWatcher(path, func(c *Config) {
		mu.Lock()
		got = append(got, c.Systems[0].Host)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer func() { _ = w.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	write("10.0.0.2")

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) == 0 {
		t.Fatal("no reload callback fired within 5s")
	}
	if got[len(got)-1] != "10.0.0.2" {
		t.Errorf("reloaded host = %q, want 10.0.0.2", got[len(got)-1])
	}
}

// A broken config must not fire the callback: the process keeps running on the
// last good configuration rather than losing its targets to a typo.
func TestWatcherIgnoresInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("systems:\n  - name: lm-01\n    host: 10.0.0.1\n    apiKey: k\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	var mu sync.Mutex
	fired := 0
	w, err := NewWatcher(path, func(*Config) {
		mu.Lock()
		fired++
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer func() { _ = w.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	if err := os.WriteFile(path, []byte("systems: [\n"), 0o600); err != nil {
		t.Fatalf("write bad: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if fired != 0 {
		t.Errorf("callback fired %d times for an invalid config; want 0", fired)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/config/ -run TestWatcher -v`
Expected: FAIL — `undefined: NewWatcher`.

- [ ] **Step 3: Implement the watcher**

Create `internal/config/watcher.go`:

```go
package config

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/sirupsen/logrus"
)

// Watcher reloads the configuration on SIGHUP and on file change.
//
// It watches the config file's DIRECTORY rather than the file itself: editors and
// config-management tools replace files by writing a temporary file and renaming it
// over the target, which detaches an inode-level watch after the first change.
type Watcher struct {
	path     string
	onReload func(*Config)
	fsw      *fsnotify.Watcher
}

// NewWatcher creates a watcher for path. onReload runs only for a config that
// parses and validates.
func NewWatcher(path string, onReload func(*Config)) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		_ = fsw.Close()
		return nil, err
	}
	if err := fsw.Add(filepath.Dir(abs)); err != nil {
		_ = fsw.Close()
		return nil, err
	}
	return &Watcher{path: abs, onReload: onReload, fsw: fsw}, nil
}

// Start runs the watch loop until ctx is cancelled.
func (w *Watcher) Start(ctx context.Context) {
	sighup := make(chan os.Signal, 1)
	signal.Notify(sighup, syscall.SIGHUP)

	go func() {
		defer signal.Stop(sighup)
		// Coalesce bursts: a single save often emits several fsnotify events.
		var timer *time.Timer
		debounce := func() {
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(100*time.Millisecond, w.reload)
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-sighup:
				w.reload()
			case ev, ok := <-w.fsw.Events:
				if !ok {
					return
				}
				if filepath.Clean(ev.Name) == w.path {
					debounce()
				}
			case err, ok := <-w.fsw.Errors:
				if !ok {
					return
				}
				logrus.WithError(err).Warn("config watcher error")
			}
		}
	}()
}

// reload re-reads the config, invoking the callback only on success.
func (w *Watcher) reload() {
	cfg, err := Load(w.path)
	if err != nil {
		logrus.WithError(err).Error("config reload failed; keeping previous configuration")
		return
	}
	logrus.Info("configuration reloaded")
	w.onReload(cfg)
}

// Close releases the filesystem watch.
func (w *Watcher) Close() error { return w.fsw.Close() }
```

- [ ] **Step 4: Implement logging**

```bash
go get github.com/sirupsen/logrus@v1.9.4
```

Create `internal/logging/logging.go`:

```go
// Package logging configures the process-wide logrus logger.
package logging

import (
	"os"

	"github.com/sirupsen/logrus"
)

// Setup directs logs to stdout when logName is empty, or appends to the named file.
// Writing to stdout is the default so a systemd unit or container captures logs
// through the journal or the container runtime rather than a file the operator has
// to rotate.
func Setup(logName string, debug bool) error {
	logrus.SetFormatter(&logrus.TextFormatter{FullTimestamp: true})
	if debug {
		logrus.SetLevel(logrus.DebugLevel)
	} else {
		logrus.SetLevel(logrus.InfoLevel)
	}
	if logName == "" {
		logrus.SetOutput(os.Stdout)
		return nil
	}
	f, err := os.OpenFile(logName, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	logrus.SetOutput(f)
	return nil
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/config/ ./internal/logging/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/config/watcher.go internal/config/watcher_test.go internal/logging/ go.mod go.sum
git commit -m "feat(config): add SIGHUP and file-watch hot reload"
```

---

## Task 5: XML transport

**Files:**
- Create: `internal/kemp/transport.go`, `internal/kemp/transport_xml.go`, `internal/kemp/tracing.go`
- Test: `internal/kemp/transport_xml_test.go`

**Interfaces:**
- Consumes: `models.Statistics`, `models.VirtualServiceInfo` (Task 2); `config.System` (Task 3).
- Produces:
  - `type transport interface { Name() string; Do(ctx context.Context, cmd string, params map[string]string, out any) error }`
  - `newXMLTransport(sys config.System, trace bool) (*xmlTransport, error)`
  - `newRestyClient(sys config.System, trace bool) (*resty.Client, error)` — shared by Task 6
  - `errAuth` and `errUnsupported` sentinel errors

The XML wire shape: `GET {base}/access/{cmd}?apikey={key}&...` returning
`<Response stat="200"><Success><Data>…</Data></Success></Response>`. `Do` decodes the `Success>Data` subtree into `out`.

- [ ] **Step 1: Write the failing test**

Create `internal/kemp/transport_xml_test.go`:

```go
package kemp

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fjacquet/kemp_exporter/internal/config"
	"github.com/fjacquet/kemp_exporter/internal/models"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// systemFor points a config.System at a test server. The test servers use
// self-signed certificates, so verification is disabled here only.
func systemFor(t *testing.T, srv *httptest.Server) config.System {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	port := 0
	if _, err := fmtSscan(u.Port(), &port); err != nil {
		t.Fatalf("parse port %q: %v", u.Port(), err)
	}
	var sys config.System
	sys.Name = "lm-test"
	sys.Host = u.Hostname()
	sys.Port = port
	sys.APIKey = "testkey"
	sys.InsecureSkipVerify = insecureTrue(t)
	return sys
}

func TestXMLTransportDecodesStats(t *testing.T) {
	var gotPath, gotKey string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.URL.Query().Get("apikey")
		w.Header().Set("Content-Type", "application/xml")
		writeBytes(w, fixture(t, "stats.xml"))
	}))
	defer srv.Close()

	tr, err := newXMLTransport(systemFor(t, srv), false)
	if err != nil {
		t.Fatalf("newXMLTransport: %v", err)
	}
	var st models.Statistics
	if err := tr.Do(context.Background(), "stats", nil, &st); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if gotPath != "/access/stats" {
		t.Errorf("path = %q, want /access/stats", gotPath)
	}
	if gotKey != "testkey" {
		t.Errorf("apikey = %q, want testkey", gotKey)
	}
	if len(st.VirtualServices) != 2 {
		t.Fatalf("len(VirtualServices) = %d, want 2", len(st.VirtualServices))
	}
	if v, ok := st.Totals.ConnsPerSec.Get(); !ok || v != 150 {
		t.Errorf("Totals.ConnsPerSec = %v/%v, want 150/true", v, ok)
	}
	if tr.Name() != "xml" {
		t.Errorf("Name() = %q, want xml", tr.Name())
	}
}

func TestXMLTransportAuthFailureIsNotRetried(t *testing.T) {
	calls := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		writeBytes(w, []byte(`<Response stat="401"><Error>Invalid API key</Error></Response>`))
	}))
	defer srv.Close()

	tr, err := newXMLTransport(systemFor(t, srv), false)
	if err != nil {
		t.Fatalf("newXMLTransport: %v", err)
	}
	var st models.Statistics
	err = tr.Do(context.Background(), "stats", nil, &st)
	if err == nil {
		t.Fatal("Do succeeded on 401; want error")
	}
	if calls != 1 {
		t.Errorf("server received %d requests; 4xx must not be retried (want 1)", calls)
	}
}

func TestXMLTransportDecodesListVS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeBytes(w, fixture(t, "listvs.xml"))
	}))
	defer srv.Close()

	tr, err := newXMLTransport(systemFor(t, srv), false)
	if err != nil {
		t.Fatalf("newXMLTransport: %v", err)
	}
	var out struct {
		VS []models.VirtualServiceInfo `xml:"VS"`
	}
	if err := tr.Do(context.Background(), "listvs", nil, &out); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if len(out.VS) != 2 || out.VS[0].Name != "web-https" {
		t.Fatalf("VS = %+v, want two entries starting with web-https", out.VS)
	}
}

// TLS 1.2 is the family floor. This asserts the client refuses to negotiate below it.
func TestXMLTransportMinTLS12(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeBytes(w, fixture(t, "stats.xml"))
	}))
	srv.TLS = &tls.Config{MaxVersion: tls.VersionTLS11}
	srv.StartTLS()
	defer srv.Close()

	tr, err := newXMLTransport(systemFor(t, srv), false)
	if err != nil {
		t.Fatalf("newXMLTransport: %v", err)
	}
	var st models.Statistics
	if err := tr.Do(context.Background(), "stats", nil, &st); err == nil {
		t.Fatal("handshake succeeded against a TLS 1.1 server; want failure")
	} else if !strings.Contains(strings.ToLower(err.Error()), "tls") &&
		!strings.Contains(strings.ToLower(err.Error()), "protocol version") {
		t.Logf("got error %v (accepted: any handshake failure)", err)
	}
}
```

Create `internal/kemp/testhelpers_test.go` — small shared helpers. `writeBytes` takes an `io.Writer` rather than the `http.ResponseWriter` directly: semgrep flags unchecked writes to a `ResponseWriter`, and the family rule forbids inline suppressions, so the seam is restructured instead.

```go
package kemp

import (
	"fmt"
	"io"
	"testing"

	"github.com/fjacquet/kemp_exporter/internal/config"
	"gopkg.in/yaml.v2"
)

// writeBytes writes to an io.Writer. Taking the interface rather than the concrete
// http.ResponseWriter keeps the write off the rule that flags unchecked
// ResponseWriter writes, without an inline suppression.
func writeBytes(w io.Writer, b []byte) {
	if _, err := w.Write(b); err != nil {
		panic(err)
	}
}

// fmtSscan wraps fmt.Sscan so callers get a (n, err) pair for a single value.
func fmtSscan(s string, out *int) (int, error) { return fmt.Sscan(s, out) }

// insecureTrue builds an EnvBool set to true, for talking to httptest's
// self-signed TLS servers. Never used outside tests.
func insecureTrue(t *testing.T) config.EnvBool {
	t.Helper()
	var holder struct {
		V config.EnvBool `yaml:"v"`
	}
	if err := yaml.Unmarshal([]byte("v: true\n"), &holder); err != nil {
		t.Fatalf("build EnvBool: %v", err)
	}
	if err := holder.V.Resolve(func(s string) (string, error) { return s, nil }); err != nil {
		t.Fatalf("resolve EnvBool: %v", err)
	}
	return holder.V
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/kemp/ -v`
Expected: FAIL — `undefined: newXMLTransport`.

- [ ] **Step 3: Add resty**

```bash
go get github.com/go-resty/resty/v2@v2.17.2
```

- [ ] **Step 4: Implement the transport interface and shared resty setup**

Create `internal/kemp/transport.go`:

```go
// Package kemp implements the LoadMaster API client, the collection loop, and the
// Prometheus and OTLP export paths.
package kemp

import (
	"context"
	"crypto/tls"
	"errors"
	"time"

	"github.com/fjacquet/kemp_exporter/internal/config"
	"github.com/go-resty/resty/v2"
)

// transport is one wire encoding of the LoadMaster API. Both implementations decode
// into the same models types, so nothing above this interface branches on encoding.
type transport interface {
	// Name reports the wire encoding: "xml" or "json".
	Name() string
	// Do issues an API command and decodes the response payload into out.
	Do(ctx context.Context, cmd string, params map[string]string, out any) error
}

// errAuth marks a credential rejection (4xx). It is never retried: retrying a 401
// against a LoadMaster with account lockout enabled locks the account.
var errAuth = errors.New("authentication rejected")

// errUnsupported marks a transport the appliance does not speak. Detection treats it
// as the signal to fall back; it is not a runtime failure.
var errUnsupported = errors.New("transport not supported by this appliance")

// newRestyClient builds the shared HTTP client for either transport.
//
// Retry deliberately excludes 4xx: a rejected credential or an unsupported command
// will fail identically on every attempt, and retrying costs an account lockout.
func newRestyClient(sys config.System, trace bool) (*resty.Client, error) {
	c := resty.New().
		SetBaseURL(sys.BaseURL()).
		SetTimeout(30*time.Second).
		SetTLSClientConfig(&tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: sys.InsecureSkipVerify.Value(), //#nosec G402 -- operator-controlled, defaults false, documented
		}).
		SetRetryCount(3).
		SetRetryWaitTime(500 * time.Millisecond).
		SetRetryMaxWaitTime(5 * time.Second).
		AddRetryCondition(func(r *resty.Response, err error) bool {
			if r == nil {
				return err != nil // transport error: retry
			}
			return r.StatusCode() >= 500
		})
	if trace {
		installTracing(c)
	}
	return c, nil
}
```

**Note on the `#nosec` comment above:** the family rule forbids inline suppressions. Before implementing, try removing that comment and running `make lint` plus the semgrep hook. If a rule fires, restructure by moving TLS construction into a small `tlsConfigFor(sys config.System) *tls.Config` helper in its own file with a package-level doc comment explaining the operator-controlled flag — do **not** keep the suppression. Record whichever resolution you land on in the task's commit message.

- [ ] **Step 5: Implement the XML transport and tracing**

Create `internal/kemp/transport_xml.go`:

```go
package kemp

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"

	"github.com/fjacquet/kemp_exporter/internal/config"
	"github.com/go-resty/resty/v2"
)

// xmlTransport speaks the classic LoadMaster RESTful API: a GET to
// /access/<cmd> with an apikey query parameter, answered with XML.
type xmlTransport struct {
	client *resty.Client
	apiKey string
}

// newXMLTransport builds the XML wire path for one system.
func newXMLTransport(sys config.System, trace bool) (*xmlTransport, error) {
	c, err := newRestyClient(sys, trace)
	if err != nil {
		return nil, err
	}
	return &xmlTransport{client: c, apiKey: sys.APIKey}, nil
}

// Name reports the wire encoding.
func (t *xmlTransport) Name() string { return "xml" }

// xmlEnvelope is the response wrapper every command shares. The payload is decoded
// straight out of Success>Data into the caller's type.
type xmlEnvelope struct {
	XMLName xml.Name `xml:"Response"`
	Stat    string   `xml:"stat,attr"`
	Error   string   `xml:"Error"`
}

// Do issues cmd and decodes Success>Data into out.
func (t *xmlTransport) Do(ctx context.Context, cmd string, params map[string]string, out any) error {
	req := t.client.R().SetContext(ctx)
	if t.apiKey != "" {
		req.SetQueryParam("apikey", t.apiKey)
	}
	for k, v := range params {
		req.SetQueryParam(k, v)
	}
	resp, err := req.Get("/access/" + cmd)
	if err != nil {
		return fmt.Errorf("xml %s: %w", cmd, err)
	}
	switch {
	case resp.StatusCode() == http.StatusUnauthorized, resp.StatusCode() == http.StatusForbidden:
		return fmt.Errorf("xml %s: %w (status %d)", cmd, errAuth, resp.StatusCode())
	case resp.StatusCode() >= 400:
		return fmt.Errorf("xml %s: status %d", cmd, resp.StatusCode())
	}

	body := resp.Body()

	// Decode the envelope first so an API-level error is reported as such rather
	// than surfacing as an empty payload.
	var env xmlEnvelope
	if err := xml.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("xml %s: decode envelope: %w", cmd, err)
	}
	if env.Error != "" {
		return fmt.Errorf("xml %s: appliance error: %s", cmd, env.Error)
	}

	// Then decode the payload into the caller's type via a generic wrapper.
	wrapper := struct {
		XMLName xml.Name `xml:"Response"`
		Data    any      `xml:"Success>Data"`
	}{Data: out}
	if err := xml.Unmarshal(body, &wrapper); err != nil {
		return fmt.Errorf("xml %s: decode payload: %w", cmd, err)
	}
	return nil
}
```

**Implementation warning:** `encoding/xml` will not decode into an `any` field holding a pointer the way the wrapper above assumes. Verify this with the test at Step 6. If it fails, replace the wrapper with a direct two-step decode: locate the `Success>Data` element with an `xml.Decoder` token loop, then call `d.DecodeElement(out, &start)` on it. That approach is known to work and is the fallback to implement — do not leave the code in a state where the payload silently decodes to nothing.

Create `internal/kemp/tracing.go`:

```go
package kemp

import (
	"strings"

	"github.com/go-resty/resty/v2"
	"github.com/sirupsen/logrus"
)

// installTracing logs each API response's method, path, status and BODY.
//
// Never use resty.SetDebug for this: it dumps request headers, which carry the API
// key and any session token straight into the log. Responses from authentication
// endpoints are skipped entirely, because the JSON login returns its token in the
// response body — body-only logging is not sufficient there.
func installTracing(c *resty.Client) {
	c.OnAfterResponse(func(_ *resty.Client, r *resty.Response) error {
		path := r.Request.URL
		if isAuthPath(path) {
			logrus.WithFields(logrus.Fields{
				"method": r.Request.Method,
				"path":   path,
				"status": r.StatusCode(),
			}).Debug("api response (body suppressed: authentication endpoint)")
			return nil
		}
		logrus.WithFields(logrus.Fields{
			"method": r.Request.Method,
			"path":   path,
			"status": r.StatusCode(),
			"body":   string(r.Body()),
		}).Debug("api response")
		return nil
	})
}

// isAuthPath reports whether a response from this path may carry credentials.
func isAuthPath(path string) bool {
	p := strings.ToLower(path)
	for _, frag := range []string{"login", "logon", "session", "token"} {
		if strings.Contains(p, frag) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/kemp/ -v`
Expected: PASS. If `TestXMLTransportDecodesStats` reports zero virtual services, apply the token-loop fallback described in Step 5.

- [ ] **Step 7: Commit**

```bash
git add internal/kemp/ go.mod go.sum
git commit -m "feat(kemp): add XML transport with TLS 1.2 floor and no-4xx-retry policy"
```

---

## Task 6: JSON transport and session auth

**Files:**
- Create: `internal/kemp/transport_json.go`, `internal/kemp/auth.go`
- Test: `internal/kemp/transport_json_test.go`

**Interfaces:**
- Consumes: `transport`, `newRestyClient`, `errAuth`, `errUnsupported` (Task 5).
- Produces: `newJSONTransport(sys config.System, trace bool) (*jsonTransport, error)`.

The JSON wire shape: `POST {base}/access/{cmd}` with a JSON body, answered with
`{"status":"ok","code":200,"Success":{"Data":{…}}}`. Session login happens lazily on the first call and re-runs at most once per `Do` on a 401.

**Unconfirmed:** the login path, the token header name, and the envelope key casing are all inferred. Record them in the `docs/metrics.md` unconfirmed list (Task 18).

- [ ] **Step 1: Write the failing test**

Create `internal/kemp/transport_json_test.go`:

```go
package kemp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/fjacquet/kemp_exporter/internal/models"
)

// jsonServer serves the login endpoint plus one command, counting calls to each.
func jsonServer(t *testing.T, payload []byte, failFirstAuth bool) (*httptest.Server, *int32, *int32) {
	t.Helper()
	var logins, cmds int32
	var authFailed atomic.Bool
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/access/login":
			atomic.AddInt32(&logins, 1)
			w.Header().Set("Content-Type", "application/json")
			writeBytes(w, []byte(`{"status":"ok","code":200,"Success":{"Data":{"token":"tok-123"}}}`))
		default:
			atomic.AddInt32(&cmds, 1)
			if failFirstAuth && !authFailed.Load() {
				authFailed.Store(true)
				w.WriteHeader(http.StatusUnauthorized)
				writeBytes(w, []byte(`{"status":"fail","code":401}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			writeBytes(w, payload)
		}
	}))
	return srv, &logins, &cmds
}

func jsonSystem(t *testing.T, srv *httptest.Server) (sys configSystem) {
	t.Helper()
	s := systemFor(t, srv)
	s.APIKey = ""
	s.Username = "bal"
	s.Password = "secret"
	return s
}

func TestJSONTransportDecodesStats(t *testing.T) {
	srv, logins, _ := jsonServer(t, fixture(t, "stats.json"), false)
	defer srv.Close()

	tr, err := newJSONTransport(jsonSystem(t, srv), false)
	if err != nil {
		t.Fatalf("newJSONTransport: %v", err)
	}
	var st models.Statistics
	if err := tr.Do(context.Background(), "stats", nil, &st); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if tr.Name() != "json" {
		t.Errorf("Name() = %q, want json", tr.Name())
	}
	if len(st.VirtualServices) != 2 {
		t.Fatalf("len(VirtualServices) = %d, want 2", len(st.VirtualServices))
	}
	if *logins != 1 {
		t.Errorf("login called %d times, want 1 (lazy, cached)", *logins)
	}

	// A second command must reuse the cached session rather than logging in again.
	var st2 models.Statistics
	if err := tr.Do(context.Background(), "stats", nil, &st2); err != nil {
		t.Fatalf("second Do: %v", err)
	}
	if *logins != 1 {
		t.Errorf("login called %d times after two commands, want 1", *logins)
	}
}

// A 401 mid-session means the token expired: re-login once and retry the command.
func TestJSONTransportRefreshesOnceOn401(t *testing.T) {
	srv, logins, cmds := jsonServer(t, fixture(t, "stats.json"), true)
	defer srv.Close()

	tr, err := newJSONTransport(jsonSystem(t, srv), false)
	if err != nil {
		t.Fatalf("newJSONTransport: %v", err)
	}
	var st models.Statistics
	if err := tr.Do(context.Background(), "stats", nil, &st); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if *logins != 2 {
		t.Errorf("login called %d times, want 2 (initial + one refresh)", *logins)
	}
	if *cmds != 2 {
		t.Errorf("command called %d times, want 2 (401 then success)", *cmds)
	}
	if len(st.VirtualServices) != 2 {
		t.Errorf("payload not decoded after refresh: %d virtual services", len(st.VirtualServices))
	}
}

// A 404 on login means this firmware has no JSON path — the signal for detection
// to fall back to XML, not a hard failure.
func TestJSONTransportUnsupportedFirmware(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	tr, err := newJSONTransport(jsonSystem(t, srv), false)
	if err != nil {
		t.Fatalf("newJSONTransport: %v", err)
	}
	var st models.Statistics
	err = tr.Do(context.Background(), "stats", nil, &st)
	if err == nil {
		t.Fatal("Do succeeded against a firmware with no JSON path; want error")
	}
	if !isUnsupported(err) {
		t.Errorf("error = %v, want one classified as unsupported", err)
	}
}

func TestJSONEnvelopeRejectsAPIError(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/access/login" {
			writeBytes(w, []byte(`{"status":"ok","code":200,"Success":{"Data":{"token":"t"}}}`))
			return
		}
		writeBytes(w, []byte(`{"status":"fail","code":422,"Error":"bad parameter"}`))
	}))
	defer srv.Close()

	tr, err := newJSONTransport(jsonSystem(t, srv), false)
	if err != nil {
		t.Fatalf("newJSONTransport: %v", err)
	}
	var st models.Statistics
	if err := tr.Do(context.Background(), "stats", nil, &st); err == nil {
		t.Fatal("Do succeeded on an appliance-level error payload; want error")
	}
}

var _ = json.Marshal // keep the json import meaningful if tests are trimmed
```

Add to `internal/kemp/testhelpers_test.go`:

```go
// configSystem aliases config.System so test helpers read less noisily.
type configSystem = config.System

// isUnsupported reports whether err was classified as an unsupported transport.
func isUnsupported(err error) bool { return errors.Is(err, errUnsupported) }
```

and add `"errors"` to that file's imports.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/kemp/ -run TestJSON -v`
Expected: FAIL — `undefined: newJSONTransport`.

- [ ] **Step 3: Implement session auth**

Create `internal/kemp/auth.go`:

```go
package kemp

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/go-resty/resty/v2"
)

// session holds a LoadMaster JSON-API session token.
//
// The XML path has no equivalent: it authenticates with a static, long-lived API
// key that is never refreshed. That asymmetry is deliberate and recorded in ADR 0004.
type session struct {
	mu    sync.Mutex
	token string
}

// loginResponse is the token-bearing login payload. Responses from this endpoint are
// never logged by the trace hook.
type loginResponse struct {
	Success struct {
		Data struct {
			Token string `json:"token"`
		} `json:"Data"`
	} `json:"Success"`
}

// ensure returns a valid token, logging in if none is cached.
func (s *session) ensure(ctx context.Context, c *resty.Client, user, pass string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" {
		return s.token, nil
	}
	return s.loginLocked(ctx, c, user, pass)
}

// refresh discards the cached token and logs in again. Called at most once per Do.
func (s *session) refresh(ctx context.Context, c *resty.Client, user, pass string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = ""
	return s.loginLocked(ctx, c, user, pass)
}

// loginLocked performs the login. The caller must hold s.mu.
func (s *session) loginLocked(ctx context.Context, c *resty.Client, user, pass string) (string, error) {
	var out loginResponse
	resp, err := c.R().
		SetContext(ctx).
		SetHeader("Content-Type", "application/json").
		SetBody(map[string]string{"user": user, "password": pass}).
		SetResult(&out).
		Post("/access/login")
	if err != nil {
		return "", fmt.Errorf("login: %w", err)
	}
	if resp.StatusCode() == http.StatusNotFound {
		return "", fmt.Errorf("login: %w (status 404)", errUnsupported)
	}
	if resp.StatusCode() == http.StatusUnauthorized || resp.StatusCode() == http.StatusForbidden {
		return "", fmt.Errorf("login: %w (status %d)", errAuth, resp.StatusCode())
	}
	if resp.StatusCode() >= 400 {
		return "", fmt.Errorf("login: status %d", resp.StatusCode())
	}
	if out.Success.Data.Token == "" {
		return "", fmt.Errorf("login: %w (no token in response)", errUnsupported)
	}
	s.token = out.Success.Data.Token
	return s.token, nil
}
```

- [ ] **Step 4: Implement the JSON transport**

Create `internal/kemp/transport_json.go`:

```go
package kemp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/fjacquet/kemp_exporter/internal/config"
	"github.com/go-resty/resty/v2"
)

// jsonTransport speaks the LoadMaster JSON API (firmware 7.2.50+), which requires
// session management enabled and Basic authentication disabled on the appliance.
type jsonTransport struct {
	client *resty.Client
	user   string
	pass   string
	sess   session
}

// newJSONTransport builds the JSON wire path for one system.
func newJSONTransport(sys config.System, trace bool) (*jsonTransport, error) {
	c, err := newRestyClient(sys, trace)
	if err != nil {
		return nil, err
	}
	return &jsonTransport{client: c, user: sys.Username, pass: sys.Password}, nil
}

// Name reports the wire encoding.
func (t *jsonTransport) Name() string { return "json" }

// jsonEnvelope is the response wrapper. Data is decoded into the caller's type via
// json.RawMessage so one envelope definition serves every command.
type jsonEnvelope struct {
	Status  string `json:"status"`
	Code    int    `json:"code"`
	Error   string `json:"Error"`
	Success struct {
		Data json.RawMessage `json:"Data"`
	} `json:"Success"`
}

// Do issues cmd and decodes Success.Data into out, refreshing the session at most
// once if the appliance rejects the cached token.
func (t *jsonTransport) Do(ctx context.Context, cmd string, params map[string]string, out any) error {
	token, err := t.sess.ensure(ctx, t.client, t.user, t.pass)
	if err != nil {
		return err
	}

	body, status, err := t.post(ctx, cmd, params, token)
	if err != nil {
		return err
	}
	if status == http.StatusUnauthorized {
		// Expired token: log in again and retry exactly once.
		token, err = t.sess.refresh(ctx, t.client, t.user, t.pass)
		if err != nil {
			return err
		}
		body, status, err = t.post(ctx, cmd, params, token)
		if err != nil {
			return err
		}
		if status == http.StatusUnauthorized {
			return fmt.Errorf("json %s: %w (after refresh)", cmd, errAuth)
		}
	}
	if status == http.StatusNotFound {
		return fmt.Errorf("json %s: %w (status 404)", cmd, errUnsupported)
	}
	if status >= 400 {
		return fmt.Errorf("json %s: status %d", cmd, status)
	}

	var env jsonEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("json %s: decode envelope: %w", cmd, err)
	}
	if env.Error != "" {
		return fmt.Errorf("json %s: appliance error: %s", cmd, env.Error)
	}
	if len(env.Success.Data) == 0 {
		return fmt.Errorf("json %s: empty payload", cmd)
	}
	if err := json.Unmarshal(env.Success.Data, out); err != nil {
		return fmt.Errorf("json %s: decode payload: %w", cmd, err)
	}
	return nil
}

// post issues one command request and returns the raw body and status.
func (t *jsonTransport) post(ctx context.Context, cmd string, params map[string]string, token string) ([]byte, int, error) {
	payload := map[string]string{}
	for k, v := range params {
		payload[k] = v
	}
	resp, err := t.client.R().
		SetContext(ctx).
		SetHeader("Content-Type", "application/json").
		SetHeader("X-API-Key", token).
		SetBody(payload).
		Post("/access/" + cmd)
	if err != nil {
		return nil, 0, fmt.Errorf("json %s: %w", cmd, err)
	}
	return resp.Body(), resp.StatusCode(), nil
}

// compile-time assertions that both transports satisfy the interface.
var (
	_ transport = (*jsonTransport)(nil)
	_ transport = (*xmlTransport)(nil)
)
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/kemp/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/kemp/
git commit -m "feat(kemp): add JSON transport with session login and single-refresh retry"
```

---

## Task 7: Transport detection and Client

**Files:**
- Create: `internal/kemp/client.go`
- Test: `internal/kemp/client_test.go`, `internal/kemp/transport_parity_test.go`

**Interfaces:**
- Consumes: both transports (Tasks 5, 6); `models` (Task 2); `config.System` (Task 3).
- Produces:
  - `type Client interface { Name() string; GetStatistics(ctx) (*models.Statistics, error); ListVirtualServices(ctx) ([]models.VirtualServiceInfo, error); TransportName() string }`
  - `NewSystemClient(sys config.System, trace bool) (*SystemClient, error)`

The **transport parity test** lives here. It is the test that makes the single-model design safe: if the XML and JSON paths ever decode to different `Statistics`, it fails here rather than in a dashboard.

- [ ] **Step 1: Write the failing tests**

Create `internal/kemp/transport_parity_test.go`:

```go
package kemp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/fjacquet/kemp_exporter/internal/models"
)

// The two wire paths must decode to an identical Statistics. This is the invariant
// that lets every layer above the transport ignore which encoding was used; without
// it the single-model design silently produces different metrics per firmware.
func TestTransportParityStatistics(t *testing.T) {
	xmlSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeBytes(w, fixture(t, "stats.xml"))
	}))
	defer xmlSrv.Close()

	jsonSrv, _, _ := jsonServer(t, fixture(t, "stats.json"), false)
	defer jsonSrv.Close()

	xt, err := newXMLTransport(systemFor(t, xmlSrv), false)
	if err != nil {
		t.Fatalf("newXMLTransport: %v", err)
	}
	jt, err := newJSONTransport(jsonSystem(t, jsonSrv), false)
	if err != nil {
		t.Fatalf("newJSONTransport: %v", err)
	}

	var fromXML, fromJSON models.Statistics
	if err := xt.Do(context.Background(), "stats", nil, &fromXML); err != nil {
		t.Fatalf("xml Do: %v", err)
	}
	if err := jt.Do(context.Background(), "stats", nil, &fromJSON); err != nil {
		t.Fatalf("json Do: %v", err)
	}

	if !reflect.DeepEqual(fromXML, fromJSON) {
		t.Errorf("transports decoded to different Statistics.\nxml:  %+v\njson: %+v", fromXML, fromJSON)
	}
}
```

Create `internal/kemp/client_test.go`:

```go
package kemp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// Detection prefers JSON, so an appliance offering both must land on json.
func TestClientPrefersJSON(t *testing.T) {
	srv, _, _ := jsonServer(t, fixture(t, "stats.json"), false)
	defer srv.Close()

	sys := jsonSystem(t, srv)
	sys.APIKey = "alsoset"
	c, err := NewSystemClient(sys, false)
	if err != nil {
		t.Fatalf("NewSystemClient: %v", err)
	}
	if _, err := c.GetStatistics(context.Background()); err != nil {
		t.Fatalf("GetStatistics: %v", err)
	}
	if got := c.TransportName(); got != "json" {
		t.Errorf("TransportName() = %q, want json", got)
	}
}

// An appliance with no JSON path must fall back to XML transparently.
func TestClientFallsBackToXML(t *testing.T) {
	var loginHits int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/access/login" {
			atomic.AddInt32(&loginHits, 1)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeBytes(w, fixture(t, "stats.xml"))
	}))
	defer srv.Close()

	sys := systemFor(t, srv)
	sys.Username, sys.Password = "bal", "secret"
	c, err := NewSystemClient(sys, false)
	if err != nil {
		t.Fatalf("NewSystemClient: %v", err)
	}
	st, err := c.GetStatistics(context.Background())
	if err != nil {
		t.Fatalf("GetStatistics: %v", err)
	}
	if len(st.VirtualServices) != 2 {
		t.Fatalf("len(VirtualServices) = %d, want 2", len(st.VirtualServices))
	}
	if got := c.TransportName(); got != "xml" {
		t.Errorf("TransportName() = %q, want xml", got)
	}
}

// Detection runs once per client, not once per cycle.
func TestClientCachesTransport(t *testing.T) {
	var loginHits int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/access/login" {
			atomic.AddInt32(&loginHits, 1)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeBytes(w, fixture(t, "stats.xml"))
	}))
	defer srv.Close()

	sys := systemFor(t, srv)
	sys.Username, sys.Password = "bal", "secret"
	c, err := NewSystemClient(sys, false)
	if err != nil {
		t.Fatalf("NewSystemClient: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := c.GetStatistics(context.Background()); err != nil {
			t.Fatalf("GetStatistics #%d: %v", i, err)
		}
	}
	if n := atomic.LoadInt32(&loginHits); n != 1 {
		t.Errorf("JSON login probed %d times across 3 cycles; want 1 (cached detection)", n)
	}
}

// A system configured with only an API key must not attempt a JSON login at all.
func TestClientAPIKeyOnlySkipsJSONProbe(t *testing.T) {
	var loginHits int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/access/login" {
			atomic.AddInt32(&loginHits, 1)
		}
		writeBytes(w, fixture(t, "stats.xml"))
	}))
	defer srv.Close()

	c, err := NewSystemClient(systemFor(t, srv), false) // APIKey set, no username
	if err != nil {
		t.Fatalf("NewSystemClient: %v", err)
	}
	if _, err := c.GetStatistics(context.Background()); err != nil {
		t.Fatalf("GetStatistics: %v", err)
	}
	if n := atomic.LoadInt32(&loginHits); n != 0 {
		t.Errorf("JSON login probed %d times with no session credentials; want 0", n)
	}
}

func TestClientListVirtualServices(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeBytes(w, fixture(t, "listvs.xml"))
	}))
	defer srv.Close()

	c, err := NewSystemClient(systemFor(t, srv), false)
	if err != nil {
		t.Fatalf("NewSystemClient: %v", err)
	}
	vs, err := c.ListVirtualServices(context.Background())
	if err != nil {
		t.Fatalf("ListVirtualServices: %v", err)
	}
	if len(vs) != 2 || vs[0].Name != "web-https" || vs[1].Port != 80 {
		t.Fatalf("ListVirtualServices = %+v, want web-https:443 and web-http:80", vs)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/kemp/ -run 'TestClient|TestTransportParity' -v`
Expected: FAIL — `undefined: NewSystemClient`.

- [ ] **Step 3: Implement the client**

Create `internal/kemp/client.go`:

```go
package kemp

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/fjacquet/kemp_exporter/internal/config"
	"github.com/fjacquet/kemp_exporter/internal/models"
	"github.com/sirupsen/logrus"
)

// Client is the per-LoadMaster API abstraction, satisfied by SystemClient and by
// test doubles. Nothing above this interface knows which wire encoding is in use.
type Client interface {
	// Name returns the configured system name, used as the `system` label.
	Name() string
	// GetStatistics fetches the stats payload.
	GetStatistics(ctx context.Context) (*models.Statistics, error)
	// ListVirtualServices fetches virtual-service metadata, which supplies the
	// service names the stats payload omits.
	ListVirtualServices(ctx context.Context) ([]models.VirtualServiceInfo, error)
	// TransportName reports the detected wire encoding ("xml", "json", or "" before
	// the first call).
	TransportName() string
}

// SystemClient talks to one LoadMaster over whichever transport it supports.
type SystemClient struct {
	name string
	sys  config.System

	mu       sync.Mutex
	active   transport
	xml      *xmlTransport
	json     *jsonTransport
	reprobed bool
}

// NewSystemClient builds both transports; detection happens on first use.
func NewSystemClient(sys config.System, trace bool) (*SystemClient, error) {
	c := &SystemClient{name: sys.Name, sys: sys}
	if sys.APIKey != "" {
		xt, err := newXMLTransport(sys, trace)
		if err != nil {
			return nil, err
		}
		c.xml = xt
	}
	if sys.Username != "" && sys.Password != "" {
		jt, err := newJSONTransport(sys, trace)
		if err != nil {
			return nil, err
		}
		c.json = jt
	}
	if c.xml == nil && c.json == nil {
		return nil, fmt.Errorf("system %s: no usable credentials", sys.Name)
	}
	return c, nil
}

// Name returns the configured system name.
func (c *SystemClient) Name() string { return c.name }

// TransportName reports the detected encoding, or "" before the first call.
func (c *SystemClient) TransportName() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil {
		return ""
	}
	return c.active.Name()
}

// do runs cmd on the detected transport, probing on first use and re-probing once
// after a hard failure.
//
// Detection treats a 4xx as the expected negative signal that a firmware lacks the
// JSON path; that is distinct from the runtime rule in newRestyClient, which never
// retries a 4xx on an already-detected transport.
func (c *SystemClient) do(ctx context.Context, cmd string, params map[string]string, out any) error {
	tr, err := c.ensureTransport(ctx, cmd, params, out)
	if err != nil {
		return err
	}
	if tr == nil {
		return nil // ensureTransport already satisfied the request during probing
	}
	err = tr.Do(ctx, cmd, params, out)
	if err == nil {
		return nil
	}
	// An auth rejection is final: do not re-probe, do not retry.
	if errors.Is(err, errAuth) {
		return err
	}
	return c.reprobe(ctx, cmd, params, out, err)
}

// ensureTransport selects a transport, preferring JSON. It returns a nil transport
// when the probe itself already produced the caller's result.
func (c *SystemClient) ensureTransport(ctx context.Context, cmd string, params map[string]string, out any) (transport, error) {
	c.mu.Lock()
	if c.active != nil {
		tr := c.active
		c.mu.Unlock()
		return tr, nil
	}
	c.mu.Unlock()

	if c.json != nil {
		if err := c.json.Do(ctx, cmd, params, out); err == nil {
			c.mu.Lock()
			c.active = c.json
			c.mu.Unlock()
			logrus.WithFields(logrus.Fields{"system": c.name, "transport": "json"}).
				Info("detected LoadMaster API transport")
			return nil, nil
		} else if !errors.Is(err, errUnsupported) && !errors.Is(err, errAuth) {
			// A transport-level failure is not evidence about firmware support.
			return nil, err
		}
	}
	if c.xml != nil {
		c.mu.Lock()
		c.active = c.xml
		c.mu.Unlock()
		logrus.WithFields(logrus.Fields{"system": c.name, "transport": "xml"}).
			Info("detected LoadMaster API transport")
		return c.xml, nil
	}
	return nil, fmt.Errorf("system %s: no transport accepted by appliance", c.name)
}

// reprobe switches to the other transport once after a hard failure, then gives up.
func (c *SystemClient) reprobe(ctx context.Context, cmd string, params map[string]string, out any, cause error) error {
	c.mu.Lock()
	if c.reprobed {
		c.mu.Unlock()
		return cause
	}
	c.reprobed = true
	var alt transport
	if c.active == c.json && c.xml != nil {
		alt = c.xml
	} else if c.active == c.xml && c.json != nil {
		alt = c.json
	}
	c.mu.Unlock()

	if alt == nil {
		return cause
	}
	logrus.WithFields(logrus.Fields{"system": c.name, "transport": alt.Name()}).
		WithError(cause).Warn("transport failed; re-probing the alternate path once")
	if err := alt.Do(ctx, cmd, params, out); err != nil {
		return cause // report the original failure, not the fallback's
	}
	c.mu.Lock()
	c.active = alt
	c.mu.Unlock()
	return nil
}

// GetStatistics fetches the stats payload.
func (c *SystemClient) GetStatistics(ctx context.Context) (*models.Statistics, error) {
	var st models.Statistics
	if err := c.do(ctx, "stats", nil, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// listVSPayload wraps the listvs response, whose Data holds repeated VS elements.
type listVSPayload struct {
	VS []models.VirtualServiceInfo `xml:"VS" json:"VS"`
}

// ListVirtualServices fetches virtual-service metadata for the name join.
func (c *SystemClient) ListVirtualServices(ctx context.Context) ([]models.VirtualServiceInfo, error) {
	var out listVSPayload
	if err := c.do(ctx, "listvs", nil, &out); err != nil {
		return nil, err
	}
	return out.VS, nil
}

var _ Client = (*SystemClient)(nil)
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/kemp/ -v`
Expected: PASS.

If `TestTransportParityStatistics` fails, the fixtures disagree — fix the **fixtures or the struct tags**, never the assertion. That test failing is the design working.

- [ ] **Step 5: Commit**

```bash
git add internal/kemp/
git commit -m "feat(kemp): add transport detection with cached probe and single re-probe"
```

---

## Task 8: Sample model and snapshot store

**Files:**
- Create: `internal/kemp/metrics.go`, `internal/kemp/snapshot.go`
- Test: `internal/kemp/snapshot_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces:
  - `type Label struct { Key, Value string }`
  - `type Sample struct { Name string; Labels []Label; Value float64 }`
  - `systemLabels(system string) []Label`
  - `cpuLabels(system, id string) []Label`
  - `interfaceLabels(system, iface string) []Label`
  - `vsLabels(system, name, address string, port int, protocol string) []Label`
  - `rsLabels(system, address string, port int, vsAddress string, vsPort int) []Label`
  - `withLabel(base []Label, key, value string) []Label` — appends one key, for the `_status` info metrics
  - `type SystemSnapshot struct { System string; LastScrape time.Time; OK bool; Err string; TransportName string; Samples []Sample }`
  - `type Snapshot struct { BuiltAt time.Time; Systems []*SystemSnapshot }` with `MetricNames() []string` and `SamplesByName(name string) []Sample`
  - `type SnapshotStore` with `NewSnapshotStore()`, `Store(*Snapshot)`, `Load() *Snapshot`

The label builders are the **only** place label order is decided. Every derivation calls them; none constructs a `[]Label` literal. That is what makes the canonical-order invariant mechanical rather than a review checklist item.

- [ ] **Step 1: Write the failing test**

Create `internal/kemp/snapshot_test.go`:

```go
package kemp

import (
	"testing"
	"time"
)

func TestLabelBuildersCanonicalOrder(t *testing.T) {
	got := vsLabels("lm-01", "web", "10.0.0.10", 443, "tcp")
	want := []string{"system", "name", "address", "port", "protocol"}
	if len(got) != len(want) {
		t.Fatalf("vsLabels returned %d labels, want %d", len(got), len(want))
	}
	for i, key := range want {
		if got[i].Key != key {
			t.Errorf("vsLabels[%d].Key = %q, want %q", i, got[i].Key, key)
		}
	}
	if got[3].Value != "443" {
		t.Errorf("port label = %q, want \"443\"", got[3].Value)
	}

	rs := rsLabels("lm-01", "192.168.1.20", 8443, "10.0.0.10", 443)
	wantRS := []string{"system", "address", "port", "vs_address", "vs_port"}
	for i, key := range wantRS {
		if rs[i].Key != key {
			t.Errorf("rsLabels[%d].Key = %q, want %q", i, rs[i].Key, key)
		}
	}
}

// An unresolved virtual-service name must produce an empty VALUE, never a dropped
// key: a metric name must carry one label-key set across all of its series.
func TestVSLabelsKeepsKeyWhenNameEmpty(t *testing.T) {
	got := vsLabels("lm-01", "", "10.0.0.10", 443, "tcp")
	if len(got) != 5 {
		t.Fatalf("len = %d, want 5 even with an empty name", len(got))
	}
	if got[1].Key != "name" || got[1].Value != "" {
		t.Errorf("labels[1] = %+v, want key \"name\" with an empty value", got[1])
	}
}

func TestSnapshotStoreNeverReturnsNil(t *testing.T) {
	s := NewSnapshotStore()
	if s.Load() == nil {
		t.Fatal("Load() returned nil before the first cycle")
	}
	snap := &Snapshot{BuiltAt: time.Now()}
	s.Store(snap)
	if s.Load() != snap {
		t.Fatal("Load() did not return the stored snapshot")
	}
}

func TestSnapshotMetricNamesAndSamplesByName(t *testing.T) {
	snap := &Snapshot{Systems: []*SystemSnapshot{
		{System: "a", Samples: []Sample{
			{Name: "kemp_up", Labels: systemLabels("a"), Value: 1},
			{Name: "kemp_tps", Labels: systemLabels("a"), Value: 10},
		}},
		{System: "b", Samples: []Sample{
			{Name: "kemp_up", Labels: systemLabels("b"), Value: 0},
		}},
	}}

	names := snap.MetricNames()
	if len(names) != 2 {
		t.Fatalf("MetricNames() = %v, want 2 unique names", names)
	}
	// Sorted output keeps OTLP instrument registration deterministic.
	if names[0] != "kemp_tps" || names[1] != "kemp_up" {
		t.Errorf("MetricNames() = %v, want sorted [kemp_tps kemp_up]", names)
	}

	ups := snap.SamplesByName("kemp_up")
	if len(ups) != 2 {
		t.Fatalf("SamplesByName(kemp_up) returned %d, want 2", len(ups))
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/kemp/ -run 'TestLabel|TestVSLabels|TestSnapshot' -v`
Expected: FAIL — `undefined: vsLabels`.

- [ ] **Step 3: Implement the sample model**

Create `internal/kemp/metrics.go`:

```go
package kemp

import "strconv"

// Label is one metric label. Key/Value ordering within a []Label is significant:
// it defines the canonical order for that metric family.
type Label struct {
	Key   string
	Value string
}

// Sample is one exported time-series point, transport- and protocol-agnostic.
// Both the Prometheus collector and the OTLP exporter render from these.
type Sample struct {
	Name   string
	Labels []Label
	Value  float64
}

// systemLabels is the base label set every metric carries.
func systemLabels(system string) []Label {
	return []Label{{Key: "system", Value: system}}
}

// cpuLabels identifies one processor row; id is "total" or "cpuN".
func cpuLabels(system, id string) []Label {
	return []Label{
		{Key: "system", Value: system},
		{Key: "cpu", Value: id},
	}
}

// interfaceLabels identifies one network interface.
func interfaceLabels(system, iface string) []Label {
	return []Label{
		{Key: "system", Value: system},
		{Key: "interface", Value: iface},
	}
}

// vsLabels is the canonical virtual-service label set.
//
// All five keys are always present. An unresolved name yields an empty VALUE, never
// a missing key: a metric name must carry one label-key set across every series, or
// the Prometheus collector drops the divergent ones.
func vsLabels(system, name, address string, port int, protocol string) []Label {
	return []Label{
		{Key: "system", Value: system},
		{Key: "name", Value: name},
		{Key: "address", Value: address},
		{Key: "port", Value: strconv.Itoa(port)},
		{Key: "protocol", Value: protocol},
	}
}

// rsLabels is the canonical real-server label set. vs_address and vs_port let a
// dashboard group real servers under their virtual service.
func rsLabels(system, address string, port int, vsAddress string, vsPort int) []Label {
	return []Label{
		{Key: "system", Value: system},
		{Key: "address", Value: address},
		{Key: "port", Value: strconv.Itoa(port)},
		{Key: "vs_address", Value: vsAddress},
		{Key: "vs_port", Value: strconv.Itoa(vsPort)},
	}
}

// withLabel appends one key to a label set, for the *_status info metrics whose
// family carries an extra `status` key.
func withLabel(base []Label, key, value string) []Label {
	out := make([]Label, len(base), len(base)+1)
	copy(out, base)
	return append(out, Label{Key: key, Value: value})
}
```

- [ ] **Step 4: Implement the snapshot store**

Create `internal/kemp/snapshot.go`:

```go
package kemp

import (
	"sort"
	"sync"
	"time"
)

// SystemSnapshot is one LoadMaster's result for a single collection cycle.
type SystemSnapshot struct {
	System        string
	LastScrape    time.Time
	OK            bool   // the appliance was reachable and authenticated
	Err           string // top-level failure; empty when OK
	TransportName string // "xml" or "json"
	Samples       []Sample
}

// Snapshot is an immutable, point-in-time view across every configured LoadMaster.
// Readers never mutate it; the collection loop publishes a new one each cycle.
type Snapshot struct {
	BuiltAt time.Time
	Systems []*SystemSnapshot
}

// MetricNames returns every distinct metric name in the snapshot, sorted.
// Sorting keeps OTLP instrument registration deterministic across cycles.
func (s *Snapshot) MetricNames() []string {
	seen := map[string]struct{}{}
	for _, sys := range s.Systems {
		for _, sample := range sys.Samples {
			seen[sample.Name] = struct{}{}
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// SamplesByName returns every sample carrying the given metric name.
func (s *Snapshot) SamplesByName(name string) []Sample {
	var out []Sample
	for _, sys := range s.Systems {
		for _, sample := range sys.Samples {
			if sample.Name == name {
				out = append(out, sample)
			}
		}
	}
	return out
}

// SnapshotStore holds the latest Snapshot behind an RWMutex pointer swap. Readers
// take a read lock only long enough to copy the pointer, so a slow scrape never
// blocks the collection loop.
type SnapshotStore struct {
	mu   sync.RWMutex
	snap *Snapshot
}

// NewSnapshotStore returns a store pre-populated with an empty snapshot so readers
// never see nil before the first collection cycle completes.
func NewSnapshotStore() *SnapshotStore {
	return &SnapshotStore{snap: &Snapshot{}}
}

// Store atomically swaps in a new snapshot.
func (s *SnapshotStore) Store(snap *Snapshot) {
	s.mu.Lock()
	s.snap = snap
	s.mu.Unlock()
}

// Load returns the current snapshot (never nil).
func (s *SnapshotStore) Load() *Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snap
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/kemp/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/kemp/metrics.go internal/kemp/snapshot.go internal/kemp/snapshot_test.go
git commit -m "feat(kemp): add sample model, canonical label builders, and snapshot store"
```

---

## Task 9: Virtual-service and real-server derivations

**Files:**
- Create: `internal/kemp/derivations.go`
- Test: `internal/kemp/derivations_test.go`
- Create: `internal/kemp/testdata/stats_hostile.xml`

**Interfaces:**
- Consumes: `models.Statistics`, `models.VirtualServiceInfo` (Task 2); `Sample`, label builders (Task 8).
- Produces:
  - `deriveVirtualServices(system string, st *models.Statistics, info []models.VirtualServiceInfo) []Sample`
  - `deriveRealServers(system string, st *models.Statistics, info []models.VirtualServiceInfo) []Sample`
  - `statusToUp(status string) (float64, bool)`
  - `addSample(out []Sample, name string, labels []Label, n models.Num) []Sample` — the single choke point for absent-never-zero; **Task 10 depends on it**
  - Test helpers reused by Tasks 10 and 14: `decodeStats`, `decodeVSInfo`, `findSample`, `hasSample`

This is the task where "absent, never zero" is enforced and where the address:port join collision is fixed. Pure functions — no I/O, no client.

- [ ] **Step 1: Write the hostile fixture**

Create `internal/kemp/testdata/stats_hostile.xml` — every field the exporter must survive:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<Response stat="200" code="ok">
  <Success>
    <Data>
      <VStotals>
        <ConnsPerSec>  150  </ConnsPerSec>
        <BytesPerSec>N/A</BytesPerSec>
        <PktsPerSec></PktsPerSec>
      </VStotals>
      <Memory>
        <memused>2147483648</memused>
        <percentmemused>N/A</percentmemused>
        <memfree></memfree>
      </Memory>
      <Vs>
        <Index>9</Index>
        <VSAddress>10.0.0.99</VSAddress>
        <VSPort>8080</VSPort>
        <VSProt>tcp</VSProt>
        <TotalConns>7</TotalConns>
        <TotalPkts>N/A</TotalPkts>
        <TotalBytes></TotalBytes>
        <ActiveConns>0</ActiveConns>
        <ConnsPerSec>N/A</ConnsPerSec>
        <BytesRead>1</BytesRead>
        <BytesWritten>2</BytesWritten>
      </Vs>
    </Data>
  </Success>
</Response>
```

Note the virtual service at index 9 appears in `stats` but has no `listvs` entry — the unresolved-name case.

- [ ] **Step 2: Write the failing test**

Create `internal/kemp/derivations_test.go`:

```go
package kemp

import (
	"encoding/xml"
	"testing"

	"github.com/fjacquet/kemp_exporter/internal/models"
)

func decodeStats(t *testing.T, name string) *models.Statistics {
	t.Helper()
	var wrapper struct {
		XMLName xml.Name         `xml:"Response"`
		Data    models.Statistics `xml:"Success>Data"`
	}
	if err := xml.Unmarshal(fixture(t, name), &wrapper); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return &wrapper.Data
}

func decodeVSInfo(t *testing.T, name string) []models.VirtualServiceInfo {
	t.Helper()
	var wrapper struct {
		XMLName xml.Name                    `xml:"Response"`
		VS      []models.VirtualServiceInfo `xml:"Success>Data>VS"`
	}
	if err := xml.Unmarshal(fixture(t, name), &wrapper); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return wrapper.VS
}

// findSample returns the first sample with the given name and label values.
func findSample(samples []Sample, name string, labelValues ...string) (Sample, bool) {
	for _, s := range samples {
		if s.Name != name {
			continue
		}
		if len(labelValues) == 0 {
			return s, true
		}
		match := true
		for i, want := range labelValues {
			if i >= len(s.Labels) || s.Labels[i].Value != want {
				match = false
				break
			}
		}
		if match {
			return s, true
		}
	}
	return Sample{}, false
}

func hasSample(samples []Sample, name string) bool {
	_, ok := findSample(samples, name)
	return ok
}

// Two virtual services on one address must each get their OWN name. The upstream
// exporter keyed its lookup on address alone and silently mislabelled one of them.
func TestDeriveVSJoinsOnAddressAndPort(t *testing.T) {
	st := decodeStats(t, "stats.xml")
	info := decodeVSInfo(t, "listvs.xml")
	samples := deriveVirtualServices("lm-01", st, info)

	s443, ok := findSample(samples, "kemp_virtual_service_active_connections", "lm-01", "web-https", "10.0.0.10", "443", "tcp")
	if !ok {
		t.Fatalf("no active_connections sample for web-https:443; got %+v", samples)
	}
	if s443.Value != 42 {
		t.Errorf("web-https active connections = %v, want 42", s443.Value)
	}
	if _, ok := findSample(samples, "kemp_virtual_service_active_connections", "lm-01", "web-http", "10.0.0.10", "80", "tcp"); !ok {
		t.Error("no active_connections sample for web-http:80 — the join collapsed two ports onto one name")
	}
}

// Status maps to the binary _up gauge and to the verbatim _status info metric.
func TestDeriveVSStatusMapping(t *testing.T) {
	st := decodeStats(t, "stats.xml")
	info := decodeVSInfo(t, "listvs.xml")
	samples := deriveVirtualServices("lm-01", st, info)

	up443, ok := findSample(samples, "kemp_virtual_service_up", "lm-01", "web-https", "10.0.0.10", "443", "tcp")
	if !ok || up443.Value != 1 {
		t.Errorf("web-https up = %+v, want value 1 (status Up)", up443)
	}
	// Sick is degraded but still serving, so it counts as up.
	up80, ok := findSample(samples, "kemp_virtual_service_up", "lm-01", "web-http", "10.0.0.10", "80", "tcp")
	if !ok || up80.Value != 1 {
		t.Errorf("web-http up = %+v, want value 1 (status Sick still serves)", up80)
	}
	// The verbatim status survives as an info metric with a sixth label.
	stat, ok := findSample(samples, "kemp_virtual_service_status", "lm-01", "web-http", "10.0.0.10", "80", "tcp", "Sick")
	if !ok {
		t.Fatalf("no kemp_virtual_service_status sample carrying status=Sick; got %+v", samples)
	}
	if stat.Value != 1 {
		t.Errorf("status info metric value = %v, want 1", stat.Value)
	}
	if len(stat.Labels) != 6 || stat.Labels[5].Key != "status" {
		t.Errorf("status labels = %+v, want six keys ending in \"status\"", stat.Labels)
	}
}

func TestStatusToUp(t *testing.T) {
	tests := []struct {
		status string
		want   float64
		ok     bool
	}{
		{"Up", 1, true},
		{"up", 1, true},
		{"Sick", 1, true},
		{"Redirect", 1, true},
		{"Down", 0, true},
		{"Disabled", 0, true},
		{"", 0, false},
		{"Bananas", 0, false},
	}
	for _, tt := range tests {
		got, ok := statusToUp(tt.status)
		if ok != tt.ok {
			t.Errorf("statusToUp(%q) ok = %v, want %v", tt.status, ok, tt.ok)
			continue
		}
		if ok && got != tt.want {
			t.Errorf("statusToUp(%q) = %v, want %v", tt.status, got, tt.want)
		}
	}
}

// The core invariant: an unparseable field yields NO sample. A fabricated 0 on a
// connection count is indistinguishable from a healthy idle service.
func TestDeriveVSOmitsUnparseableFields(t *testing.T) {
	st := decodeStats(t, "stats_hostile.xml")
	samples := deriveVirtualServices("lm-01", st, nil)

	// TotalConns=7 parsed, so its counter is present.
	if s, ok := findSample(samples, "kemp_virtual_service_connections_total"); !ok || s.Value != 7 {
		t.Errorf("connections_total = %+v, want value 7", s)
	}
	// ActiveConns=0 is a REAL zero and must be emitted.
	if s, ok := findSample(samples, "kemp_virtual_service_active_connections"); !ok || s.Value != 0 {
		t.Errorf("active_connections = %+v, want a real 0 to be emitted", s)
	}
	// TotalPkts=N/A, TotalBytes="", ConnsPerSec=N/A must all be absent.
	for _, name := range []string{
		"kemp_virtual_service_packets_total",
		"kemp_virtual_service_bytes_total",
		"kemp_virtual_service_connections_per_second",
	} {
		if hasSample(samples, name) {
			t.Errorf("%s was emitted for an unparseable field; want absent", name)
		}
	}
}

// A virtual service present in stats but absent from listvs keeps every label KEY
// with an empty name value, so the metric family holds one label-key set.
func TestDeriveVSUnresolvedNameKeepsLabelKeys(t *testing.T) {
	st := decodeStats(t, "stats_hostile.xml")
	samples := deriveVirtualServices("lm-01", st, nil)

	s, ok := findSample(samples, "kemp_virtual_service_connections_total")
	if !ok {
		t.Fatal("no connections_total sample")
	}
	if len(s.Labels) != 5 {
		t.Fatalf("labels = %+v, want 5 keys even with no listvs entry", s.Labels)
	}
	if s.Labels[1].Key != "name" || s.Labels[1].Value != "" {
		t.Errorf("labels[1] = %+v, want key \"name\" with empty value", s.Labels[1])
	}
}

// With no listvs entry there is no status, so neither status metric may appear.
func TestDeriveVSNoStatusWithoutListVS(t *testing.T) {
	st := decodeStats(t, "stats_hostile.xml")
	samples := deriveVirtualServices("lm-01", st, nil)
	for _, name := range []string{"kemp_virtual_service_up", "kemp_virtual_service_status"} {
		if hasSample(samples, name) {
			t.Errorf("%s emitted with no listvs data; want absent", name)
		}
	}
}

func TestDeriveRealServersLinksToVirtualService(t *testing.T) {
	st := decodeStats(t, "stats.xml")
	info := decodeVSInfo(t, "listvs.xml")
	samples := deriveRealServers("lm-01", st, info)

	s, ok := findSample(samples, "kemp_real_server_active_connections", "lm-01", "192.168.1.20", "8443", "10.0.0.10", "443")
	if !ok {
		t.Fatalf("no real-server sample linked to its virtual service; got %+v", samples)
	}
	if s.Value != 21 {
		t.Errorf("active connections = %v, want 21", s.Value)
	}
	if len(s.Labels) != 5 {
		t.Errorf("labels = %+v, want 5 keys", s.Labels)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/kemp/ -run TestDerive -v`
Expected: FAIL — `undefined: deriveVirtualServices`.

- [ ] **Step 4: Implement the derivations**

Create `internal/kemp/derivations.go`:

```go
package kemp

import (
	"strconv"
	"strings"

	"github.com/fjacquet/kemp_exporter/internal/models"
)

// vsKey is the join key between the stats and listvs payloads.
//
// Keying on address alone is wrong: one virtual IP commonly hosts several ports
// (80 and 443 on the same VIP is the default web pattern), and an address-only
// lookup silently gives both services the same name.
func vsKey(address string, port int) string {
	return address + ":" + strconv.Itoa(port)
}

// indexVSInfo builds the address:port lookup used to resolve service names.
func indexVSInfo(info []models.VirtualServiceInfo) map[string]models.VirtualServiceInfo {
	idx := make(map[string]models.VirtualServiceInfo, len(info))
	for _, v := range info {
		idx[vsKey(v.Address, v.Port)] = v
	}
	return idx
}

// statusToUp maps a LoadMaster status string onto the binary _up gauge.
//
// The mapping is total and deliberate. "Sick" and "Redirect" both still serve
// traffic, so they count as up; "Disabled" is administratively out of rotation and
// counts as down. An unrecognised status returns ok=false so the caller omits the
// sample entirely — an unknown status is not evidence of failure, and reporting it
// as 0 would fire a false outage alert.
func statusToUp(status string) (float64, bool) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "up", "sick", "redirect":
		return 1, true
	case "down", "disabled":
		return 0, true
	default:
		return 0, false
	}
}

// addSample appends a sample only when the source field parsed. This is the single
// choke point for the absent-never-zero policy.
func addSample(out []Sample, name string, labels []Label, n models.Num) []Sample {
	v, ok := n.Get()
	if !ok {
		return out
	}
	return append(out, Sample{Name: name, Labels: labels, Value: v})
}

// deriveVirtualServices turns the stats payload's Vs entries into samples, joining
// each against listvs for its name, protocol and status.
func deriveVirtualServices(system string, st *models.Statistics, info []models.VirtualServiceInfo) []Sample {
	if st == nil {
		return nil
	}
	idx := indexVSInfo(info)
	var out []Sample

	for _, vs := range st.VirtualServices {
		meta, resolved := idx[vsKey(vs.Address, vs.Port)]

		// Protocol comes from stats and is corrected by listvs when available.
		protocol := vs.Protocol
		if resolved && meta.Protocol != "" {
			protocol = meta.Protocol
		}
		labels := vsLabels(system, meta.Name, vs.Address, vs.Port, protocol)

		out = addSample(out, "kemp_virtual_service_active_connections", labels, vs.ActiveConns)
		out = addSample(out, "kemp_virtual_service_connections_per_second", labels, vs.ConnsPerSec)
		out = addSample(out, "kemp_virtual_service_connections_total", labels, vs.TotalConns)
		out = addSample(out, "kemp_virtual_service_packets_total", labels, vs.TotalPkts)
		out = addSample(out, "kemp_virtual_service_bytes_total", labels, vs.TotalBytes)
		out = addSample(out, "kemp_virtual_service_bytes_read_total", labels, vs.BytesRead)
		out = addSample(out, "kemp_virtual_service_bytes_written_total", labels, vs.BytesWritten)

		// Status metrics require listvs; without it there is nothing to report and
		// guessing from Enable would conflate "administratively enabled" with "up".
		if !resolved || meta.Status == "" {
			continue
		}
		if v, ok := statusToUp(meta.Status); ok {
			out = append(out, Sample{Name: "kemp_virtual_service_up", Labels: labels, Value: v})
		}
		out = append(out, Sample{
			Name:   "kemp_virtual_service_status",
			Labels: withLabel(labels, "status", meta.Status),
			Value:  1,
		})
	}
	return out
}

// deriveRealServers turns the stats payload's Rs entries into samples, linking each
// back to its virtual service through VSIndex.
func deriveRealServers(system string, st *models.Statistics, info []models.VirtualServiceInfo) []Sample {
	if st == nil {
		return nil
	}
	// Map VSIndex -> virtual service identity, preferring the stats payload's own
	// Vs entries and falling back to listvs.
	vsByIndex := map[int]struct {
		address string
		port    int
	}{}
	for _, vs := range st.VirtualServices {
		vsByIndex[vs.Index] = struct {
			address string
			port    int
		}{vs.Address, vs.Port}
	}
	for _, v := range info {
		if _, ok := vsByIndex[v.Index]; !ok {
			vsByIndex[v.Index] = struct {
				address string
				port    int
			}{v.Address, v.Port}
		}
	}

	var out []Sample
	for _, rs := range st.RealServers {
		parent := vsByIndex[rs.VSIndex] // zero value gives empty address, port 0
		labels := rsLabels(system, rs.Address, rs.Port, parent.address, parent.port)

		out = addSample(out, "kemp_real_server_active_connections", labels, rs.ActiveConns)
		out = addSample(out, "kemp_real_server_connections_per_second", labels, rs.ConnsPerSec)
		out = addSample(out, "kemp_real_server_connections_total", labels, rs.TotalConns)
		out = addSample(out, "kemp_real_server_packets_total", labels, rs.TotalPkts)
		out = addSample(out, "kemp_real_server_bytes_total", labels, rs.TotalBytes)
		out = addSample(out, "kemp_real_server_bytes_read_total", labels, rs.BytesRead)
		out = addSample(out, "kemp_real_server_bytes_written_total", labels, rs.BytesWritten)

		if rs.Status == "" {
			continue
		}
		if v, ok := statusToUp(rs.Status); ok {
			out = append(out, Sample{Name: "kemp_real_server_up", Labels: labels, Value: v})
		}
		out = append(out, Sample{
			Name:   "kemp_real_server_status",
			Labels: withLabel(labels, "status", rs.Status),
			Value:  1,
		})
	}
	return out
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/kemp/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/kemp/derivations.go internal/kemp/derivations_test.go internal/kemp/testdata/stats_hostile.xml
git commit -m "feat(kemp): derive virtual-service and real-server samples with address:port join"
```

---

## Task 10: Appliance health derivations

**Files:**
- Create: `internal/kemp/health.go`
- Test: `internal/kemp/health_test.go`

**Interfaces:**
- Consumes: `models.Statistics` (Task 2); `Sample`, `cpuLabels`, `interfaceLabels`, `systemLabels`, `addSample` (Tasks 8, 9).
- Produces: `deriveHealth(system string, st *models.Statistics) []Sample`.

Covers the totals block plus CPU, memory, TPS and interfaces — the metrics the dashboard needs beyond upstream parity.

- [ ] **Step 1: Write the failing test**

Create `internal/kemp/health_test.go`:

```go
package kemp

import "testing"

func TestDeriveHealthTotalsAreGauges(t *testing.T) {
	st := decodeStats(t, "stats.xml")
	samples := deriveHealth("lm-01", st)

	for _, tc := range []struct {
		name string
		want float64
	}{
		{"kemp_connections_per_second", 150},
		{"kemp_bytes_per_second", 2048000},
		{"kemp_packets_per_second", 3200},
	} {
		s, ok := findSample(samples, tc.name, "lm-01")
		if !ok {
			t.Errorf("%s missing", tc.name)
			continue
		}
		if s.Value != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, s.Value, tc.want)
		}
		if len(s.Labels) != 1 || s.Labels[0].Key != "system" {
			t.Errorf("%s labels = %+v, want only {system}", tc.name, s.Labels)
		}
	}
}

func TestDeriveHealthCPUPerRow(t *testing.T) {
	st := decodeStats(t, "stats.xml")
	samples := deriveHealth("lm-01", st)

	total, ok := findSample(samples, "kemp_cpu_idle_percent", "lm-01", "total")
	if !ok || total.Value != 80 {
		t.Errorf("cpu total idle = %+v, want value 80", total)
	}
	core, ok := findSample(samples, "kemp_cpu_idle_percent", "lm-01", "cpu0")
	if !ok || core.Value != 77 {
		t.Errorf("cpu0 idle = %+v, want value 77", core)
	}
	if _, ok := findSample(samples, "kemp_cpu_user_percent", "lm-01", "total"); !ok {
		t.Error("kemp_cpu_user_percent missing for cpu=total")
	}
	if _, ok := findSample(samples, "kemp_cpu_system_percent", "lm-01", "total"); !ok {
		t.Error("kemp_cpu_system_percent missing for cpu=total")
	}
}

func TestDeriveHealthMemoryAndTPS(t *testing.T) {
	st := decodeStats(t, "stats.xml")
	samples := deriveHealth("lm-01", st)

	for _, tc := range []struct {
		name string
		want float64
	}{
		{"kemp_memory_free_bytes", 2147483648},
		{"kemp_memory_used_bytes", 2147483648},
		{"kemp_memory_used_percent", 50},
		{"kemp_tps", 420},
		{"kemp_tps_ssl", 75},
	} {
		s, ok := findSample(samples, tc.name, "lm-01")
		if !ok {
			t.Errorf("%s missing", tc.name)
			continue
		}
		if s.Value != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, s.Value, tc.want)
		}
	}
	// TPS is a rate, so it must NOT carry the counter suffix.
	if hasSample(samples, "kemp_tps_total") {
		t.Error("kemp_tps_total emitted; TPS is a gauge and must not carry _total")
	}
}

func TestDeriveHealthInterfaces(t *testing.T) {
	st := decodeStats(t, "stats.xml")
	samples := deriveHealth("lm-01", st)

	s, ok := findSample(samples, "kemp_interface_bytes_read_total", "lm-01", "eth0")
	if !ok || s.Value != 987654321 {
		t.Errorf("eth0 bytes read = %+v, want 987654321", s)
	}
	if _, ok := findSample(samples, "kemp_interface_bytes_written_total", "lm-01", "eth0"); !ok {
		t.Error("kemp_interface_bytes_written_total missing for eth0")
	}
}

// An unparseable health field is absent, not zero — a fake 0 on free memory would
// fire a false capacity alert.
func TestDeriveHealthOmitsUnparseableFields(t *testing.T) {
	st := decodeStats(t, "stats_hostile.xml")
	samples := deriveHealth("lm-01", st)

	if s, ok := findSample(samples, "kemp_connections_per_second", "lm-01"); !ok || s.Value != 150 {
		t.Errorf("connections_per_second = %+v, want 150 (whitespace-padded value)", s)
	}
	for _, name := range []string{
		"kemp_bytes_per_second",    // N/A
		"kemp_packets_per_second",  // empty
		"kemp_memory_used_percent", // N/A
		"kemp_memory_free_bytes",   // empty
	} {
		if hasSample(samples, name) {
			t.Errorf("%s emitted for an unparseable field; want absent", name)
		}
	}
	// The section that DID parse is still present.
	if !hasSample(samples, "kemp_memory_used_bytes") {
		t.Error("kemp_memory_used_bytes missing; a sibling field failing must not drop it")
	}
	// The fixture has no TPS or Network section at all.
	for _, name := range []string{"kemp_tps", "kemp_tps_ssl", "kemp_interface_bytes_read_total"} {
		if hasSample(samples, name) {
			t.Errorf("%s emitted with no source section; want absent", name)
		}
	}
}

func TestDeriveHealthNilStatistics(t *testing.T) {
	if got := deriveHealth("lm-01", nil); got != nil {
		t.Errorf("deriveHealth(nil) = %+v, want nil", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/kemp/ -run TestDeriveHealth -v`
Expected: FAIL — `undefined: deriveHealth`.

- [ ] **Step 3: Implement**

Create `internal/kemp/health.go`:

```go
package kemp

import "github.com/fjacquet/kemp_exporter/internal/models"

// deriveHealth turns the appliance-wide sections of the stats payload — totals,
// CPU, memory, TPS and network interfaces — into samples.
//
// Every value here is either an instantaneous gauge or a cumulative byte counter,
// so nothing depends on the collection interval.
func deriveHealth(system string, st *models.Statistics) []Sample {
	if st == nil {
		return nil
	}
	base := systemLabels(system)
	var out []Sample

	// Totals are already per-second rates: gauges, aggregated with sum/avg in
	// PromQL, never rate().
	out = addSample(out, "kemp_connections_per_second", base, st.Totals.ConnsPerSec)
	out = addSample(out, "kemp_bytes_per_second", base, st.Totals.BytesPerSec)
	out = addSample(out, "kemp_packets_per_second", base, st.Totals.PktsPerSec)

	for _, cpu := range st.CPUs {
		labels := cpuLabels(system, cpu.ID)
		out = addSample(out, "kemp_cpu_idle_percent", labels, cpu.Idle)
		out = addSample(out, "kemp_cpu_user_percent", labels, cpu.User)
		out = addSample(out, "kemp_cpu_system_percent", labels, cpu.System)
	}

	// Percentages stay on the 0-100 scale Kemp reports; no /100 conversion.
	out = addSample(out, "kemp_memory_free_bytes", base, st.Memory.FreeBytes)
	out = addSample(out, "kemp_memory_used_bytes", base, st.Memory.UsedBytes)
	out = addSample(out, "kemp_memory_used_percent", base, st.Memory.UsedPercent)

	// Transactions per second are rates despite the field name, so no _total suffix.
	out = addSample(out, "kemp_tps", base, st.TPS.Total)
	out = addSample(out, "kemp_tps_ssl", base, st.TPS.SSL)

	for _, iface := range st.Interfaces {
		labels := interfaceLabels(system, iface.ID)
		out = addSample(out, "kemp_interface_bytes_read_total", labels, iface.BytesRead)
		out = addSample(out, "kemp_interface_bytes_written_total", labels, iface.BytesWritten)
	}

	return out
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/kemp/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/kemp/health.go internal/kemp/health_test.go
git commit -m "feat(kemp): derive appliance health samples for CPU, memory, TPS and interfaces"
```

---

## Task 11: Target state and build info

**Files:**
- Create: `internal/kemp/state.go`, `internal/kemp/buildinfo.go`
- Test: `internal/kemp/state_test.go`, `internal/kemp/buildinfo_test.go`

**Interfaces:**
- Consumes: `Sample`, `systemLabels` (Task 8).
- Produces:
  - `upSample(system string, ok bool) Sample`
  - `NewBuildInfoCollector(version, goversion string) prometheus.Collector`

- [ ] **Step 1: Write the failing tests**

Create `internal/kemp/state_test.go`:

```go
package kemp

import "testing"

func TestUpSample(t *testing.T) {
	up := upSample("lm-01", true)
	if up.Name != "kemp_up" || up.Value != 1 {
		t.Errorf("upSample(true) = %+v, want kemp_up = 1", up)
	}
	if len(up.Labels) != 1 || up.Labels[0].Key != "system" || up.Labels[0].Value != "lm-01" {
		t.Errorf("labels = %+v, want {system=lm-01}", up.Labels)
	}
	down := upSample("lm-01", false)
	if down.Value != 0 {
		t.Errorf("upSample(false) value = %v, want 0", down.Value)
	}
}
```

Create `internal/kemp/buildinfo_test.go`:

```go
package kemp

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestBuildInfoCollector(t *testing.T) {
	c := NewBuildInfoCollector("v1.2.3", "go1.26.5")
	want := `
# HELP kemp_exporter_build_info Exporter build information; constant 1, with the running version and Go version in the ` + "`version`" + ` and ` + "`goversion`" + ` labels.
# TYPE kemp_exporter_build_info gauge
kemp_exporter_build_info{goversion="go1.26.5",version="v1.2.3"} 1
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want)); err != nil {
		t.Fatalf("unexpected metric: %v", err)
	}
	if got := testutil.CollectAndCount(c); got != 1 {
		t.Errorf("collected %d metrics, want 1", got)
	}
	// It must register cleanly into a real registry.
	reg := prometheus.NewRegistry()
	if err := reg.Register(c); err != nil {
		t.Fatalf("Register: %v", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/kemp/ -run 'TestUpSample|TestBuildInfo' -v`
Expected: FAIL — `undefined: upSample`.

- [ ] **Step 3: Add the Prometheus dependency**

```bash
go get github.com/prometheus/client_golang@v1.23.2
```

`testutil` pulls in the indirect dependency `github.com/kylelemons/godebug` — expect one added line in `go.mod`. That is expected family-wide, not a problem.

- [ ] **Step 4: Implement**

Create `internal/kemp/state.go`:

```go
package kemp

// upSample reports whether the last collection cycle reached and authenticated
// against a LoadMaster.
//
// This is per-target and per-cycle: it describes the backend, not the liveness of
// the exporter's own HTTP handler. A wedged collection loop leaves every kemp_up at
// a stale 1, which is why /health reports snapshot age separately.
func upSample(system string, ok bool) Sample {
	v := 0.0
	if ok {
		v = 1
	}
	return Sample{Name: "kemp_up", Labels: systemLabels(system), Value: v}
}
```

Create `internal/kemp/buildinfo.go`:

```go
package kemp

import "github.com/prometheus/client_golang/prometheus"

// NewBuildInfoCollector returns a collector exposing a single constant metric,
// `kemp_exporter_build_info{version="...",goversion="..."} 1`, so one scrape reveals
// exactly which build is running — the check that catches a stale container that was
// never re-pulled after a release.
//
// The name is the BINARY name, not the metric prefix, matching
// node_exporter_build_info and prometheus_build_info. It carries no system label:
// it describes the process, not any backend. version comes from the -X main.version
// ldflag; goversion is passed in rather than read from runtime so tests are
// deterministic.
func NewBuildInfoCollector(version, goversion string) prometheus.Collector {
	g := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   "kemp_exporter",
		Name:        "build_info",
		Help:        "Exporter build information; constant 1, with the running version and Go version in the `version` and `goversion` labels.",
		ConstLabels: prometheus.Labels{"version": version, "goversion": goversion},
	})
	g.Set(1)
	return g
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/kemp/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/kemp/state.go internal/kemp/buildinfo.go internal/kemp/state_test.go internal/kemp/buildinfo_test.go go.mod go.sum
git commit -m "feat(kemp): add kemp_up state sample and build-info collector"
```

---

## Task 12: Prometheus collector

**Files:**
- Create: `internal/kemp/prometheus.go`
- Test: `internal/kemp/prometheus_test.go`

**Interfaces:**
- Consumes: `SnapshotStore`, `Snapshot`, `Sample` (Task 8).
- Produces: `NewPromCollector(store *SnapshotStore) *PromCollector` implementing `prometheus.Collector`.

An **unchecked** collector: `Describe` emits nothing, so the metric-name set may vary per snapshot. That flexibility costs the registry's label-consistency enforcement, so the collector enforces it directly.

- [ ] **Step 1: Write the failing test**

Create `internal/kemp/prometheus_test.go`:

```go
package kemp

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestPromCollectorRendersSamples(t *testing.T) {
	store := NewSnapshotStore()
	store.Store(&Snapshot{
		BuiltAt: time.Now(),
		Systems: []*SystemSnapshot{{
			System: "lm-01",
			OK:     true,
			Samples: []Sample{
				upSample("lm-01", true),
				{Name: "kemp_tps", Labels: systemLabels("lm-01"), Value: 420},
				{
					Name:   "kemp_virtual_service_active_connections",
					Labels: vsLabels("lm-01", "web", "10.0.0.10", 443, "tcp"),
					Value:  42,
				},
			},
		}},
	})

	c := NewPromCollector(store)
	want := `
# HELP kemp_up Kemp LoadMaster metric kemp_up
# TYPE kemp_up gauge
kemp_up{system="lm-01"} 1
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want), "kemp_up"); err != nil {
		t.Fatalf("kemp_up: %v", err)
	}
	if got := testutil.CollectAndCount(c); got != 3 {
		t.Errorf("collected %d metrics, want 3", got)
	}
}

// The empty snapshot present before the first collection cycle must render cleanly,
// because the HTTP server starts before the loop does.
func TestPromCollectorEmptySnapshot(t *testing.T) {
	c := NewPromCollector(NewSnapshotStore())
	if got := testutil.CollectAndCount(c); got != 0 {
		t.Errorf("collected %d metrics from an empty snapshot, want 0", got)
	}
	reg := prometheus.NewRegistry()
	if err := reg.Register(c); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := reg.Gather(); err != nil {
		t.Fatalf("Gather on empty snapshot: %v", err)
	}
}

// Describe must emit nothing: that is what makes this an unchecked collector and
// lets the metric-name set change between snapshots.
func TestPromCollectorDescribeIsEmpty(t *testing.T) {
	ch := make(chan *prometheus.Desc, 8)
	NewPromCollector(NewSnapshotStore()).Describe(ch)
	close(ch)
	if n := len(ch); n != 0 {
		t.Errorf("Describe emitted %d descriptors, want 0", n)
	}
}

// Two series of one metric name with different label KEYS would make Gather fail.
// The collector drops the divergent one so a scrape degrades instead of erroring.
func TestPromCollectorDropsLabelKeyDrift(t *testing.T) {
	store := NewSnapshotStore()
	store.Store(&Snapshot{Systems: []*SystemSnapshot{{
		System: "lm-01",
		Samples: []Sample{
			{Name: "kemp_thing", Labels: []Label{{Key: "system", Value: "lm-01"}}, Value: 1},
			{Name: "kemp_thing", Labels: []Label{{Key: "other", Value: "x"}}, Value: 2},
		},
	}}})

	reg := prometheus.NewRegistry()
	if err := reg.Register(NewPromCollector(store)); err != nil {
		t.Fatalf("Register: %v", err)
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather must not fail on label drift: %v", err)
	}
	for _, f := range families {
		if f.GetName() == "kemp_thing" && len(f.GetMetric()) != 1 {
			t.Errorf("kemp_thing has %d series, want 1 (the divergent one dropped)", len(f.GetMetric()))
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/kemp/ -run TestPromCollector -v`
Expected: FAIL — `undefined: NewPromCollector`.

- [ ] **Step 3: Implement**

Create `internal/kemp/prometheus.go`:

```go
package kemp

import (
	"slices"

	"github.com/prometheus/client_golang/prometheus"
)

// PromCollector is an unchecked Prometheus collector: Describe emits nothing, so
// the metric-name set can vary from snapshot to snapshot. Collect reads the latest
// snapshot without blocking the collection loop.
type PromCollector struct {
	store *SnapshotStore
}

// NewPromCollector wraps the snapshot store as a prometheus.Collector.
func NewPromCollector(store *SnapshotStore) *PromCollector {
	return &PromCollector{store: store}
}

// Describe sends nothing (unchecked collector).
func (p *PromCollector) Describe(chan<- *prometheus.Desc) {}

// Collect renders every snapshot sample as a gauge metric.
//
// Because this is an unchecked collector, client_golang does not enforce a
// consistent label-key set per metric name during Gather — an inconsistency would
// surface as a failed scrape rather than a dropped series. So it is enforced here:
// the first label-key set seen for a name within a scrape defines that metric's
// schema, and later samples whose keys disagree are dropped. Losing one series beats
// failing the whole scrape.
func (p *PromCollector) Collect(ch chan<- prometheus.Metric) {
	snap := p.store.Load()
	schema := map[string][]string{}

	for _, sys := range snap.Systems {
		for _, s := range sys.Samples {
			keys := make([]string, len(s.Labels))
			vals := make([]string, len(s.Labels))
			for i, l := range s.Labels {
				keys[i], vals[i] = l.Key, l.Value
			}
			if want, ok := schema[s.Name]; ok {
				if !slices.Equal(want, keys) {
					continue // label-key drift for an already-seen metric name
				}
			} else {
				schema[s.Name] = keys
			}
			desc := prometheus.NewDesc(s.Name, "Kemp LoadMaster metric "+s.Name, keys, nil)
			m, err := prometheus.NewConstMetric(desc, prometheus.GaugeValue, s.Value, vals...)
			if err != nil {
				continue // skip rather than panic on an inconsistent label set
			}
			ch <- m
		}
	}
}

var _ prometheus.Collector = (*PromCollector)(nil)
```

**Note on metric type:** every sample renders as `prometheus.GaugeValue`, including the `_total` counters. This matches the family reference implementation — the `_total` naming still tells PromQL users and `docs/metrics.md` readers that `rate()` applies, and a gauge-typed cumulative value behaves identically under `rate()`/`increase()`. Do not change this without an ADR; it is a family-wide convention, not an oversight in this repo.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/kemp/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/kemp/prometheus.go internal/kemp/prometheus_test.go
git commit -m "feat(kemp): add unchecked Prometheus collector with label-key drift guard"
```

---

## Task 13: OTLP exporter

**Files:**
- Create: `internal/kemp/otlp.go`, `internal/telemetry/manager.go`
- Test: `internal/kemp/otlp_test.go`

**Interfaces:**
- Consumes: `SnapshotStore`, `Snapshot.MetricNames`, `Snapshot.SamplesByName` (Task 8); `config.OTelConfig` (Task 3).
- Produces:
  - `NewOTLPExporter(ctx, oc config.OTelConfig, store *SnapshotStore, serviceVersion string) (*OTLPExporter, error)`
  - `newOTLPExporter(reader sdkmetric.Reader, store *SnapshotStore, serviceVersion string) *OTLPExporter` — unexported, takes a reader so tests inject a `ManualReader`
  - `(*OTLPExporter).EnsureInstruments() error`, `(*OTLPExporter).Shutdown(ctx) error`

The family requirement is that collector tests assert through **both** export paths. This task supplies the OTLP half.

- [ ] **Step 1: Write the failing test**

Create `internal/kemp/otlp_test.go`:

```go
package kemp

import (
	"context"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// collectOTLP registers instruments and returns the gathered metrics by name.
func collectOTLP(t *testing.T, store *SnapshotStore) map[string]metricdata.Metrics {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	exp := newOTLPExporter(reader, store, "v0.0.0-test")
	if err := exp.EnsureInstruments(); err != nil {
		t.Fatalf("EnsureInstruments: %v", err)
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	out := map[string]metricdata.Metrics{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			out[m.Name] = m
		}
	}
	return out
}

func seededStore() *SnapshotStore {
	store := NewSnapshotStore()
	store.Store(&Snapshot{
		BuiltAt: time.Now(),
		Systems: []*SystemSnapshot{{
			System: "lm-01",
			OK:     true,
			Samples: []Sample{
				upSample("lm-01", true),
				{Name: "kemp_tps", Labels: systemLabels("lm-01"), Value: 420},
				{
					Name:   "kemp_virtual_service_active_connections",
					Labels: vsLabels("lm-01", "web", "10.0.0.10", 443, "tcp"),
					Value:  42,
				},
			},
		}},
	})
	return store
}

func TestOTLPExportsEverySample(t *testing.T) {
	got := collectOTLP(t, seededStore())
	for _, name := range []string{"kemp_up", "kemp_tps", "kemp_virtual_service_active_connections"} {
		if _, ok := got[name]; !ok {
			t.Errorf("%s missing from OTLP output; got %v", name, keysOf(got))
		}
	}
}

func TestOTLPCarriesLabelsAsAttributes(t *testing.T) {
	got := collectOTLP(t, seededStore())
	m, ok := got["kemp_virtual_service_active_connections"]
	if !ok {
		t.Fatal("metric missing")
	}
	gauge, ok := m.Data.(metricdata.Gauge[float64])
	if !ok {
		t.Fatalf("data type = %T, want Gauge[float64]", m.Data)
	}
	if len(gauge.DataPoints) != 1 {
		t.Fatalf("%d data points, want 1", len(gauge.DataPoints))
	}
	dp := gauge.DataPoints[0]
	if dp.Value != 42 {
		t.Errorf("value = %v, want 42", dp.Value)
	}
	if n := dp.Attributes.Len(); n != 5 {
		t.Errorf("%d attributes, want 5 (system,name,address,port,protocol)", n)
	}
	if v, ok := dp.Attributes.Value("system"); !ok || v.AsString() != "lm-01" {
		t.Errorf("system attribute = %v/%v, want lm-01", v.AsString(), ok)
	}
}

// EnsureInstruments runs after every cycle, so it must be idempotent — registering
// the same instrument twice would error or duplicate the series.
func TestOTLPEnsureInstrumentsIdempotent(t *testing.T) {
	store := seededStore()
	reader := sdkmetric.NewManualReader()
	exp := newOTLPExporter(reader, store, "v0.0.0-test")
	for i := 0; i < 3; i++ {
		if err := exp.EnsureInstruments(); err != nil {
			t.Fatalf("EnsureInstruments #%d: %v", i, err)
		}
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	count := 0
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == "kemp_up" {
				count++
			}
		}
	}
	if count != 1 {
		t.Errorf("kemp_up registered %d times, want 1", count)
	}
}

func keysOf(m map[string]metricdata.Metrics) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/kemp/ -run TestOTLP -v`
Expected: FAIL — `undefined: newOTLPExporter`.

- [ ] **Step 3: Add the OpenTelemetry dependencies**

```bash
go get go.opentelemetry.io/otel@v1.44.0
go get go.opentelemetry.io/otel/metric@v1.44.0
go get go.opentelemetry.io/otel/sdk@v1.44.0
go get go.opentelemetry.io/otel/sdk/metric@v1.44.0
go get go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc@v1.44.0
```

- [ ] **Step 4: Implement**

Create `internal/kemp/otlp.go`:

```go
package kemp

import (
	"context"
	"sync"

	"github.com/fjacquet/kemp_exporter/internal/config"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// OTLPExporter pushes the snapshot via OTLP using asynchronous observable gauges.
// The periodic reader drives collection: on each cycle every registered instrument's
// callback reads the latest snapshot and observes its samples. Both export paths
// therefore render from the same immutable snapshot and cannot disagree.
type OTLPExporter struct {
	provider *sdkmetric.MeterProvider
	meter    metric.Meter
	store    *SnapshotStore

	mu         sync.Mutex
	registered map[string]struct{}
}

// NewOTLPExporter creates an exporter pushing to an OTLP gRPC endpoint.
func NewOTLPExporter(ctx context.Context, oc config.OTelConfig, store *SnapshotStore, serviceVersion string) (*OTLPExporter, error) {
	opts := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(oc.Endpoint)}
	if oc.Insecure {
		opts = append(opts, otlpmetricgrpc.WithInsecure())
	}
	exp, err := otlpmetricgrpc.New(ctx, opts...)
	if err != nil {
		return nil, err
	}
	reader := sdkmetric.NewPeriodicReader(exp, sdkmetric.WithInterval(oc.Interval))
	return newOTLPExporter(reader, store, serviceVersion), nil
}

// newOTLPExporter builds the meter provider from a reader. Separated from
// NewOTLPExporter so tests inject a ManualReader instead of a live gRPC connection.
func newOTLPExporter(reader sdkmetric.Reader, store *SnapshotStore, serviceVersion string) *OTLPExporter {
	res, _ := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName("kemp-exporter"),
		semconv.ServiceVersion(serviceVersion),
	))
	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(reader),
		sdkmetric.WithResource(res),
	)
	return &OTLPExporter{
		provider:   provider,
		meter:      provider.Meter("kemp-exporter"),
		store:      store,
		registered: make(map[string]struct{}),
	}
}

// EnsureInstruments registers an observable gauge for every metric name in the
// current snapshot that does not already have one. Idempotent, so it is safe to
// call after every collection cycle and after a config reload.
func (e *OTLPExporter) EnsureInstruments() error {
	snap := e.store.Load()
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, name := range snap.MetricNames() {
		if _, ok := e.registered[name]; ok {
			continue
		}
		metricName := name // capture per iteration for the callback
		_, err := e.meter.Float64ObservableGauge(metricName,
			metric.WithFloat64Callback(func(_ context.Context, obs metric.Float64Observer) error {
				for _, s := range e.store.Load().SamplesByName(metricName) {
					obs.Observe(s.Value, metric.WithAttributes(attrsFor(s.Labels)...))
				}
				return nil
			}),
		)
		if err != nil {
			return err
		}
		e.registered[metricName] = struct{}{}
	}
	return nil
}

// Shutdown flushes and stops the meter provider.
func (e *OTLPExporter) Shutdown(ctx context.Context) error {
	return e.provider.Shutdown(ctx)
}

// attrsFor converts the sample's labels to OTLP attributes, preserving order.
func attrsFor(labels []Label) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, len(labels))
	for i, l := range labels {
		attrs[i] = attribute.String(l.Key, l.Value)
	}
	return attrs
}
```

Create `internal/telemetry/manager.go`:

```go
// Package telemetry owns the OTLP exporter lifecycle so main.go does not have to.
package telemetry

import (
	"context"

	"github.com/sirupsen/logrus"
)

// Shutdowner is anything with a Shutdown method — satisfied by kemp.OTLPExporter.
// Declaring the interface here rather than importing kemp keeps the dependency
// pointing one way: main wires the two together, neither package imports the other.
type Shutdowner interface {
	Shutdown(ctx context.Context) error
}

// ShutdownAll flushes and stops every provider, logging rather than returning
// errors: this runs during process shutdown, where there is nothing left to react.
func ShutdownAll(ctx context.Context, providers ...Shutdowner) {
	for _, p := range providers {
		if p == nil {
			continue
		}
		if err := p.Shutdown(ctx); err != nil {
			logrus.WithError(err).Warn("telemetry shutdown failed")
		}
	}
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/kemp/ ./internal/telemetry/ -v`
Expected: PASS. If the `semconv` import path errors, check the version directory that shipped with otel v1.44.0 and adjust — do not drop the resource attributes.

- [ ] **Step 6: Commit**

```bash
git add internal/kemp/otlp.go internal/kemp/otlp_test.go internal/telemetry/ go.mod go.sum
git commit -m "feat(kemp): add OTLP observable-gauge exporter reading the shared snapshot"
```

---

## Task 14: Collection loop

**Files:**
- Create: `internal/kemp/collector.go`
- Test: `internal/kemp/collector_test.go`, `internal/kemp/mock.go`

**Interfaces:**
- Consumes: `Client` (Task 7); derivations (Tasks 9, 10); `upSample` (Task 11); `SnapshotStore` (Task 8); `config.Collection` (Task 3).
- Produces:
  - `type CollectionLoop` with `NewCollectionLoop(clients []Client, cc config.Collection, store *SnapshotStore) *CollectionLoop`
  - `(*CollectionLoop).CollectOnce(ctx) *Snapshot`
  - `(*CollectionLoop).Run(ctx)`
  - `(*CollectionLoop).SetClients([]Client)` — for hot reload
  - `type MockClient` implementing `Client`, in `mock.go` (non-test file so other packages' tests can use it)

- [ ] **Step 1: Write the mock**

Create `internal/kemp/mock.go`:

```go
package kemp

import (
	"context"

	"github.com/fjacquet/kemp_exporter/internal/models"
)

// MockClient is a Client double for tests. It lives in a non-test file so tests in
// other packages (and main's wiring tests) can use it too.
type MockClient struct {
	SystemName string
	Transport  string
	Stats      *models.Statistics
	StatsErr   error
	VSInfo     []models.VirtualServiceInfo
	VSInfoErr  error

	StatsCalls  int
	VSInfoCalls int
}

// Name returns the configured system name.
func (m *MockClient) Name() string { return m.SystemName }

// TransportName reports the configured transport label.
func (m *MockClient) TransportName() string { return m.Transport }

// GetStatistics returns the canned statistics or error.
func (m *MockClient) GetStatistics(context.Context) (*models.Statistics, error) {
	m.StatsCalls++
	if m.StatsErr != nil {
		return nil, m.StatsErr
	}
	return m.Stats, nil
}

// ListVirtualServices returns the canned virtual-service metadata or error.
func (m *MockClient) ListVirtualServices(context.Context) ([]models.VirtualServiceInfo, error) {
	m.VSInfoCalls++
	if m.VSInfoErr != nil {
		return nil, m.VSInfoErr
	}
	return m.VSInfo, nil
}

var _ Client = (*MockClient)(nil)
```

- [ ] **Step 2: Write the failing test**

Create `internal/kemp/collector_test.go`:

```go
package kemp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fjacquet/kemp_exporter/internal/config"
)

func loopConfig() config.Collection {
	return config.Collection{
		Interval:      50 * time.Millisecond,
		Timeout:       2 * time.Second,
		MaxConcurrent: 4,
	}
}

func TestCollectOnceBuildsSnapshot(t *testing.T) {
	mc := &MockClient{
		SystemName: "lm-01",
		Transport:  "xml",
		Stats:      decodeStats(t, "stats.xml"),
		VSInfo:     decodeVSInfo(t, "listvs.xml"),
	}
	loop := NewCollectionLoop([]Client{mc}, loopConfig(), NewSnapshotStore())
	snap := loop.CollectOnce(context.Background())

	if len(snap.Systems) != 1 {
		t.Fatalf("%d systems in snapshot, want 1", len(snap.Systems))
	}
	sys := snap.Systems[0]
	if !sys.OK {
		t.Errorf("system OK = false, want true; err = %q", sys.Err)
	}
	if sys.TransportName != "xml" {
		t.Errorf("TransportName = %q, want xml", sys.TransportName)
	}
	if up, ok := findSample(sys.Samples, "kemp_up", "lm-01"); !ok || up.Value != 1 {
		t.Errorf("kemp_up = %+v, want 1", up)
	}
	// Health, virtual-service and real-server samples all landed.
	for _, name := range []string{
		"kemp_connections_per_second",
		"kemp_virtual_service_active_connections",
		"kemp_real_server_active_connections",
	} {
		if !hasSample(sys.Samples, name) {
			t.Errorf("%s missing from the snapshot", name)
		}
	}
}

// A failing target reports down and contributes NO stale series — frozen values
// that look live are worse than an obvious gap.
func TestCollectOnceFailedTargetEmitsOnlyDown(t *testing.T) {
	mc := &MockClient{SystemName: "lm-01", StatsErr: errors.New("connection refused")}
	loop := NewCollectionLoop([]Client{mc}, loopConfig(), NewSnapshotStore())
	snap := loop.CollectOnce(context.Background())

	sys := snap.Systems[0]
	if sys.OK {
		t.Error("system OK = true after a stats failure")
	}
	if sys.Err == "" {
		t.Error("system Err is empty after a failure")
	}
	if up, ok := findSample(sys.Samples, "kemp_up", "lm-01"); !ok || up.Value != 0 {
		t.Errorf("kemp_up = %+v, want 0", up)
	}
	if len(sys.Samples) != 1 {
		t.Errorf("%d samples for a down target, want only kemp_up", len(sys.Samples))
	}
}

// listvs failing must NOT discard the stats: metrics stay, names go empty.
func TestCollectOnceKeepsStatsWhenListVSFails(t *testing.T) {
	mc := &MockClient{
		SystemName: "lm-01",
		Stats:      decodeStats(t, "stats.xml"),
		VSInfoErr:  errors.New("listvs unavailable"),
	}
	loop := NewCollectionLoop([]Client{mc}, loopConfig(), NewSnapshotStore())
	sys := loop.CollectOnce(context.Background()).Systems[0]

	if !sys.OK {
		t.Errorf("system OK = false; a listvs failure must not mark the target down (err=%q)", sys.Err)
	}
	s, ok := findSample(sys.Samples, "kemp_virtual_service_active_connections")
	if !ok {
		t.Fatal("virtual-service metrics dropped when listvs failed")
	}
	if len(s.Labels) != 5 || s.Labels[1].Key != "name" || s.Labels[1].Value != "" {
		t.Errorf("labels = %+v, want five keys with an empty name value", s.Labels)
	}
	// Without listvs there is no status, so the status metrics stay absent.
	if hasSample(sys.Samples, "kemp_virtual_service_up") {
		t.Error("kemp_virtual_service_up emitted without listvs data")
	}
}

// One failing target must not take down the others.
func TestCollectOnceIsolatesTargetFailures(t *testing.T) {
	good := &MockClient{SystemName: "good", Stats: decodeStats(t, "stats.xml"), VSInfo: decodeVSInfo(t, "listvs.xml")}
	bad := &MockClient{SystemName: "bad", StatsErr: errors.New("unreachable")}
	loop := NewCollectionLoop([]Client{good, bad}, loopConfig(), NewSnapshotStore())
	snap := loop.CollectOnce(context.Background())

	if len(snap.Systems) != 2 {
		t.Fatalf("%d systems, want 2", len(snap.Systems))
	}
	byName := map[string]*SystemSnapshot{}
	for _, s := range snap.Systems {
		byName[s.System] = s
	}
	if !byName["good"].OK {
		t.Error("healthy target marked down because a sibling failed")
	}
	if byName["bad"].OK {
		t.Error("failing target marked up")
	}
}

func TestRunPublishesAndStops(t *testing.T) {
	mc := &MockClient{SystemName: "lm-01", Stats: decodeStats(t, "stats.xml"), VSInfo: decodeVSInfo(t, "listvs.xml")}
	store := NewSnapshotStore()
	loop := NewCollectionLoop([]Client{mc}, loopConfig(), store)

	ctx, cancel := context.WithCancel(context.Background())
	go loop.Run(ctx)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(store.Load().Systems) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(store.Load().Systems) == 0 {
		t.Fatal("no snapshot published within 3s")
	}
	cancel()

	// After cancellation the loop must stop issuing calls.
	time.Sleep(150 * time.Millisecond)
	before := mc.StatsCalls
	time.Sleep(200 * time.Millisecond)
	if mc.StatsCalls != before {
		t.Errorf("loop kept collecting after cancel: %d -> %d", before, mc.StatsCalls)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/kemp/ -run 'TestCollect|TestRun' -v`
Expected: FAIL — `undefined: NewCollectionLoop`.

- [ ] **Step 4: Implement**

```bash
go get golang.org/x/sync@v0.22.0
```

Create `internal/kemp/collector.go`:

```go
package kemp

import (
	"context"
	"sync"
	"time"

	"github.com/fjacquet/kemp_exporter/internal/config"
	"github.com/fjacquet/kemp_exporter/internal/models"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
)

// CollectionLoop polls every LoadMaster on an interval and publishes an immutable
// snapshot. Decoupling collection from scraping means backend API load depends on
// the interval alone, not on how many Prometheus servers scrape the exporter.
type CollectionLoop struct {
	cc    config.Collection
	store *SnapshotStore

	mu      sync.RWMutex
	clients []Client
}

// NewCollectionLoop builds a loop over the given clients.
func NewCollectionLoop(clients []Client, cc config.Collection, store *SnapshotStore) *CollectionLoop {
	return &CollectionLoop{cc: cc, store: store, clients: clients}
}

// SetClients swaps the target set, for config hot reload.
func (l *CollectionLoop) SetClients(clients []Client) {
	l.mu.Lock()
	l.clients = clients
	l.mu.Unlock()
}

// snapshotClients returns the current target set.
func (l *CollectionLoop) snapshotClients() []Client {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Client, len(l.clients))
	copy(out, l.clients)
	return out
}

// Run collects immediately, then on every interval tick until ctx is cancelled.
// Collecting up front means /metrics carries real data as soon as possible rather
// than after a full interval of emptiness.
func (l *CollectionLoop) Run(ctx context.Context) {
	l.store.Store(l.CollectOnce(ctx))

	ticker := time.NewTicker(l.cc.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.store.Store(l.CollectOnce(ctx))
		}
	}
}

// CollectOnce runs one full cycle across every target and returns the snapshot
// without publishing it. Exported so `--once` can collect, dump and exit.
func (l *CollectionLoop) CollectOnce(ctx context.Context) *Snapshot {
	clients := l.snapshotClients()
	results := make([]*SystemSnapshot, len(clients))

	cycleCtx, cancel := context.WithTimeout(ctx, l.cc.Timeout)
	defer cancel()

	g, gctx := errgroup.WithContext(cycleCtx)
	g.SetLimit(l.cc.MaxConcurrent)
	for i, c := range clients {
		i, c := i, c
		g.Go(func() error {
			results[i] = collectSystem(gctx, c)
			return nil // per-target failures degrade; they never fail the cycle
		})
	}
	_ = g.Wait()

	systems := make([]*SystemSnapshot, 0, len(results))
	for _, r := range results {
		if r != nil {
			systems = append(systems, r)
		}
	}
	return &Snapshot{BuiltAt: time.Now(), Systems: systems}
}

// collectSystem gathers one LoadMaster's samples.
//
// stats and listvs are fetched concurrently. A stats failure marks the target down
// and emits kemp_up=0 with nothing else — no stale series. A listvs failure is
// tolerated: the metrics stay and service names fall back to empty, because losing
// a label value is far better than losing the metrics.
func collectSystem(ctx context.Context, c Client) *SystemSnapshot {
	out := &SystemSnapshot{
		System:        c.Name(),
		LastScrape:    time.Now(),
		TransportName: c.TransportName(),
	}

	var (
		stats  *models.Statistics
		vsInfo []models.VirtualServiceInfo
		mu     sync.Mutex
	)

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		s, err := c.GetStatistics(gctx)
		if err != nil {
			return err
		}
		mu.Lock()
		stats = s
		mu.Unlock()
		return nil
	})
	g.Go(func() error {
		v, err := c.ListVirtualServices(gctx)
		if err != nil {
			// Tolerated: names degrade to empty, metrics survive.
			logrus.WithError(err).WithField("system", c.Name()).
				Warn("listvs failed; virtual-service names will be empty this cycle")
			return nil
		}
		mu.Lock()
		vsInfo = v
		mu.Unlock()
		return nil
	})

	if err := g.Wait(); err != nil || stats == nil {
		out.OK = false
		if err != nil {
			out.Err = err.Error()
		} else {
			out.Err = "no statistics returned"
		}
		logrus.WithError(err).WithField("system", c.Name()).Error("collection failed")
		out.Samples = []Sample{upSample(c.Name(), false)}
		return out
	}

	out.OK = true
	out.TransportName = c.TransportName() // detection may have resolved during the call
	out.Samples = append(out.Samples, upSample(c.Name(), true))
	out.Samples = append(out.Samples, deriveHealth(c.Name(), stats)...)
	out.Samples = append(out.Samples, deriveVirtualServices(c.Name(), stats, vsInfo)...)
	out.Samples = append(out.Samples, deriveRealServers(c.Name(), stats, vsInfo)...)
	return out
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/kemp/ -race -v`
Expected: PASS with no race warnings. The `-race` flag matters here — this is the first concurrent code in the repo.

- [ ] **Step 6: Commit**

```bash
git add internal/kemp/collector.go internal/kemp/collector_test.go internal/kemp/mock.go go.mod go.sum
git commit -m "feat(kemp): add collection loop with per-target failure isolation"
```

---

## Task 15: CLI and HTTP server

**Files:**
- Create: `main.go`, `internal/kemp/dump.go`
- Test: `internal/kemp/dump_test.go`, `main_test.go`

**Interfaces:**
- Consumes: everything from Tasks 3–14.
- Produces: the `kemp_exporter` binary with flags `--config`, `--debug`, `--once`, `--trace`; endpoints `/metrics`, `/health`, `/`.

Two behaviours here are load-bearing and directly tested:

1. **The HTTP server starts before the first collection.** Login plus a first poll can exceed the collection timeout; blocking startup on it would stall `/metrics` behind an unreachable appliance.
2. **`/health` reports snapshot age, not `kemp_up`.** `kemp_up` describes the backend. A wedged collection loop leaves every `kemp_up` at a stale 1, so liveness needs an independent signal.

- [ ] **Step 1: Write the failing tests**

Create `internal/kemp/dump_test.go`:

```go
package kemp

import (
	"strings"
	"testing"
)

// The --once --debug dump is the live-validation tool: its output gets diffed
// against docs/metrics.md. Sorted, exposition-style, one sample per line.
func TestDumpSamplesIsSortedExposition(t *testing.T) {
	snap := &Snapshot{Systems: []*SystemSnapshot{{
		System: "lm-01",
		Samples: []Sample{
			{Name: "kemp_tps", Labels: systemLabels("lm-01"), Value: 420},
			{Name: "kemp_up", Labels: systemLabels("lm-01"), Value: 1},
			{
				Name:   "kemp_virtual_service_active_connections",
				Labels: vsLabels("lm-01", "web", "10.0.0.10", 443, "tcp"),
				Value:  42,
			},
		},
	}}}

	var sb strings.Builder
	DumpSamples(&sb, snap)
	lines := strings.Split(strings.TrimSpace(sb.String()), "\n")

	if len(lines) != 3 {
		t.Fatalf("%d lines, want 3:\n%s", len(lines), sb.String())
	}
	if lines[0] != `kemp_tps{system="lm-01"} 420` {
		t.Errorf("line 0 = %q", lines[0])
	}
	if lines[1] != `kemp_up{system="lm-01"} 1` {
		t.Errorf("line 1 = %q", lines[1])
	}
	want := `kemp_virtual_service_active_connections{system="lm-01",name="web",address="10.0.0.10",port="443",protocol="tcp"} 42`
	if lines[2] != want {
		t.Errorf("line 2 =\n%q\nwant\n%q", lines[2], want)
	}
}
```

Create `main_test.go`:

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fjacquet/kemp_exporter/internal/kemp"
	"github.com/prometheus/client_golang/prometheus"
)

// /metrics must answer before any collection has happened — the server comes up
// first so an unreachable appliance cannot stall scrapes.
func TestMetricsHandlerServesBeforeFirstCollection(t *testing.T) {
	store := kemp.NewSnapshotStore()
	reg := prometheus.NewRegistry()
	if err := reg.Register(kemp.NewPromCollector(store)); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := reg.Register(kemp.NewBuildInfoCollector("v0.0.0-test", "go1.26.5")); err != nil {
		t.Fatalf("Register build info: %v", err)
	}

	srv := httptest.NewServer(metricsHandler(reg))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

// /health is driven by snapshot age, independent of kemp_up.
func TestHealthHandlerUsesSnapshotAge(t *testing.T) {
	store := kemp.NewSnapshotStore()
	h := healthHandler(store, time.Minute)

	// No snapshot yet: starting up, not yet healthy.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("pre-collection status = %d, want 503", rec.Code)
	}

	// Fresh snapshot: healthy.
	store.Store(&kemp.Snapshot{BuiltAt: time.Now()})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("fresh-snapshot status = %d, want 200", rec.Code)
	}

	// Stale snapshot: the loop is wedged even though kemp_up may still read 1.
	store.Store(&kemp.Snapshot{BuiltAt: time.Now().Add(-10 * time.Minute)})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("stale-snapshot status = %d, want 503", rec.Code)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./... -run 'TestDumpSamples|TestMetricsHandler|TestHealthHandler' -v`
Expected: FAIL — `undefined: DumpSamples`, `undefined: metricsHandler`.

- [ ] **Step 3: Implement the sample dump**

Create `internal/kemp/dump.go`:

```go
package kemp

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// DumpSamples writes every sample in exposition format, sorted, one per line.
//
// This is the live-validation tool: run `--once --debug` against a real appliance
// and diff the output against docs/metrics.md. It catches silently-absent metrics
// that kemp_up cannot — a collector reporting OK does not mean every field parsed.
func DumpSamples(w io.Writer, snap *Snapshot) {
	var lines []string
	for _, sys := range snap.Systems {
		for _, s := range sys.Samples {
			lines = append(lines, formatSample(s))
		}
	}
	sort.Strings(lines)
	for _, l := range lines {
		fmt.Fprintln(w, l)
	}
}

// formatSample renders one sample as name{k="v",...} value, preserving label order.
func formatSample(s Sample) string {
	var sb strings.Builder
	sb.WriteString(s.Name)
	if len(s.Labels) > 0 {
		sb.WriteByte('{')
		for i, l := range s.Labels {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(l.Key)
			sb.WriteString(`="`)
			sb.WriteString(l.Value)
			sb.WriteByte('"')
		}
		sb.WriteByte('}')
	}
	sb.WriteByte(' ')
	sb.WriteString(strconv.FormatFloat(s.Value, 'g', -1, 64))
	return sb.String()
}
```

- [ ] **Step 4: Implement main.go**

```bash
go get github.com/spf13/cobra@v1.10.2
```

Create `main.go`:

```go
// Command kemp_exporter exports Progress Kemp LoadMaster metrics to Prometheus and
// OTLP.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/fjacquet/kemp_exporter/internal/config"
	"github.com/fjacquet/kemp_exporter/internal/kemp"
	"github.com/fjacquet/kemp_exporter/internal/logging"
	"github.com/fjacquet/kemp_exporter/internal/telemetry"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

// version is injected at build time via -X main.version.
var version = "dev"

var (
	flagConfig string
	flagDebug  bool
	flagOnce   bool
	flagTrace  bool
)

func main() {
	root := &cobra.Command{
		Use:     "kemp_exporter",
		Short:   "Prometheus and OTLP exporter for Progress Kemp LoadMaster",
		Version: version,
		RunE:    run,
	}
	root.Flags().StringVar(&flagConfig, "config", "config.yaml", "path to the configuration file")
	root.Flags().BoolVar(&flagDebug, "debug", false, "enable debug logging; with --once, dump every collected sample")
	root.Flags().BoolVar(&flagOnce, "once", false, "run a single collection cycle and exit")
	root.Flags().BoolVar(&flagTrace, "trace", false, "log every API response body (never headers; auth responses are skipped)")

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// metricsHandler serves the registry. Split out so tests can exercise it without
// starting the whole process.
func metricsHandler(reg *prometheus.Registry) http.Handler {
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
}

// healthHandler reports liveness from snapshot AGE, deliberately independent of
// kemp_up. kemp_up describes the backend; a wedged collection loop would leave it
// at a stale 1 forever, so staleness is the only honest liveness signal.
func healthHandler(store *kemp.SnapshotStore, maxAge time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		snap := store.Load()
		if snap.BuiltAt.IsZero() {
			http.Error(w, "starting: no collection cycle has completed yet", http.StatusServiceUnavailable)
			return
		}
		if age := time.Since(snap.BuiltAt); age > maxAge {
			http.Error(w, fmt.Sprintf("stale: last collection %s ago", age.Round(time.Second)), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("ok\n")); err != nil {
			logrus.WithError(err).Debug("health response write failed")
		}
	})
}

// buildClients turns the configured systems into API clients.
func buildClients(cfg *config.Config, trace bool) ([]kemp.Client, error) {
	clients := make([]kemp.Client, 0, len(cfg.Systems))
	for _, sys := range cfg.Systems {
		c, err := kemp.NewSystemClient(sys, trace)
		if err != nil {
			return nil, err
		}
		clients = append(clients, c)
	}
	return clients, nil
}

func run(_ *cobra.Command, _ []string) error {
	// .env loads before interpolation; it never overrides real injected secrets.
	config.LoadDotEnv(flagConfig)

	cfg, err := config.Load(flagConfig)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := logging.Setup(cfg.Server.LogName, flagDebug); err != nil {
		return fmt.Errorf("setup logging: %w", err)
	}
	logrus.Debugf("configuration:\n%s", config.SafeConfig(cfg))

	clients, err := buildClients(cfg, flagTrace)
	if err != nil {
		return err
	}

	store := kemp.NewSnapshotStore()
	loop := kemp.NewCollectionLoop(clients, cfg.Collection, store)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// --once: collect, optionally dump, exit. The validation path.
	if flagOnce {
		snap := loop.CollectOnce(ctx)
		store.Store(snap)
		if flagDebug {
			kemp.DumpSamples(os.Stdout, snap)
		}
		for _, sys := range snap.Systems {
			if !sys.OK {
				logrus.WithFields(logrus.Fields{"system": sys.System, "error": sys.Err}).
					Warn("system collection failed")
			}
		}
		return nil
	}

	reg := prometheus.NewRegistry()
	if err := reg.Register(kemp.NewPromCollector(store)); err != nil {
		return fmt.Errorf("register collector: %w", err)
	}
	if err := reg.Register(kemp.NewBuildInfoCollector(version, runtime.Version())); err != nil {
		return fmt.Errorf("register build info: %w", err)
	}

	mux := http.NewServeMux()
	mux.Handle(cfg.Server.URI, metricsHandler(reg))
	// Health tolerates two missed cycles before reporting stale.
	mux.Handle("/health", healthHandler(store, 2*cfg.Collection.Interval+cfg.Collection.Timeout))
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte(`<html><head><title>kemp_exporter</title></head>
<body><h1>kemp_exporter</h1><p><a href="` + cfg.Server.URI + `">Metrics</a></p></body></html>` + "\n")); err != nil {
			logrus.WithError(err).Debug("index response write failed")
		}
	})

	addr := cfg.Server.Host + ":" + cfg.Server.Port
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Serve BEFORE the first collection: login plus a first poll can outlast the
	// collection timeout, and blocking startup on it would stall /metrics behind
	// an unreachable appliance.
	go func() {
		logrus.WithField("addr", addr).Info("serving metrics")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logrus.WithError(err).Fatal("http server failed")
		}
	}()

	var otlp *kemp.OTLPExporter
	if cfg.OTel.Enabled {
		otlp, err = kemp.NewOTLPExporter(ctx, cfg.OTel, store, version)
		if err != nil {
			return fmt.Errorf("start OTLP exporter: %w", err)
		}
		logrus.WithField("endpoint", cfg.OTel.Endpoint).Info("OTLP export enabled")
	}

	go loop.Run(ctx)

	// Register OTLP instruments as new metric names appear in the snapshot.
	if otlp != nil {
		go func() {
			t := time.NewTicker(cfg.Collection.Interval)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					if err := otlp.EnsureInstruments(); err != nil {
						logrus.WithError(err).Warn("OTLP instrument registration failed")
					}
				}
			}
		}()
	}

	// Hot reload: rebuild clients and swap them into the running loop.
	watcher, err := config.NewWatcher(flagConfig, func(newCfg *config.Config) {
		newClients, err := buildClients(newCfg, flagTrace)
		if err != nil {
			logrus.WithError(err).Error("reload: rebuilding clients failed; keeping previous targets")
			return
		}
		loop.SetClients(newClients)
		logrus.WithField("systems", len(newClients)).Info("reload: target set updated")
	})
	if err != nil {
		return fmt.Errorf("start config watcher: %w", err)
	}
	defer func() { _ = watcher.Close() }()
	watcher.Start(ctx)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	sig := <-stop
	logrus.WithField("signal", sig.String()).Info("shutting down")

	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if otlp != nil {
		telemetry.ShutdownAll(shutdownCtx, otlp)
	}
	return srv.Shutdown(shutdownCtx)
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./... -race`
Expected: PASS across every package.

- [ ] **Step 6: Verify the binary end to end**

```bash
go build -o bin/kemp_exporter .
./bin/kemp_exporter --help
./bin/kemp_exporter --config config.yaml --once --debug
```

The third command must fail to reach a LoadMaster and exit **0** after logging a per-system warning — the failure path is the expected outcome with no appliance. It must not panic and must not hang.

- [ ] **Step 7: Commit**

```bash
git add main.go main_test.go internal/kemp/dump.go internal/kemp/dump_test.go go.mod go.sum
git commit -m "feat: add CLI, HTTP server started before first collection, and sample dump"
```

---

## Task 16: Build, container, and release pipeline

**Files:**
- Create: `Makefile`, `Dockerfile`, `Dockerfile.goreleaser`, `.goreleaser.yaml`, `.golangci.yml`, `.github/workflows/{ci,security,release,docs}.yml`, `.github/dependabot.yml`

**Interfaces:**
- Consumes: the buildable binary from Task 15.
- Produces: `make ci` as the green gate; `make release-snapshot` producing local artifacts.

No new Go code. This task is judged by `make ci` passing and `goreleaser check` succeeding.

- [ ] **Step 1: Write the Makefile**

Copy `/Users/fjacquet/Projects/ppdd_exporter/Makefile` verbatim, then apply exactly these changes:

1. `BIN := bin/ppdd_exporter` → `BIN := bin/kemp_exporter`
2. Add the two targets `ppdd` lacks (family drift noted in `stack.md`), placed after `build`:

```makefile
docker:
	docker build -t kemp_exporter:$(VERSION) .

run-cli: build
	$(BIN) --config config.yaml --once --debug
```

3. Add `docker run-cli test-coverage` to the `.PHONY` list.
4. Add a `test-coverage` target after `test-race`:

```makefile
test-coverage: test
	go tool cover -html=$(COVER) -o coverage.html
	@echo "coverage report: coverage.html"
```

5. Replace the `demo`/`demo-ghcr`/`demo-down` bodies' compose files if they reference `ppdd` — they do not; leave them.

Verify the target contract is complete:

```bash
grep -E '^(tools|fmt-check|fmt|format|vet|lint|test|test-race|test-coverage|vuln|ci|sure|cli|build|sbom|release|release-snapshot|docker|run-cli|clean):' Makefile
```

`stack.md` names `fmt` and `cli`; `ppdd` calls them `format` and `build`. Add aliases so both names work:

```makefile
fmt: format
cli: build
```

and add `fmt cli` to `.PHONY`.

- [ ] **Step 2: Write the Dockerfiles**

Create `Dockerfile`:

```dockerfile
# Build stage
FROM golang:1.26-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/kemp_exporter .

# Runtime stage
FROM alpine:3.22
# Copy the CA bundle from the builder rather than `apk add ca-certificates`:
# apk fetches the index from the Alpine CDN over TLS, which fails behind a corporate
# MITM proxy because the bare alpine image has no CA bundle yet to validate the proxy
# certificate. adduser and mkdir are busybox builtins and need no network.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
RUN adduser -D -u 10001 kemp
COPY --from=builder /out/kemp_exporter /usr/local/bin/kemp_exporter
COPY config.yaml /etc/kemp_exporter/config.yaml
USER 10001
EXPOSE 9447
ENTRYPOINT ["/usr/local/bin/kemp_exporter"]
CMD ["--config", "/etc/kemp_exporter/config.yaml"]
```

Create `Dockerfile.goreleaser`:

```dockerfile
FROM alpine:3.22
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 kemp
# TARGETPLATFORM is required: buildx writes each platform's binary into its own
# subdirectory. A flat `COPY kemp_exporter` only worked with the retired
# build-push-action job and silently produces a broken multi-arch image.
ARG TARGETPLATFORM
COPY ${TARGETPLATFORM}/kemp_exporter /usr/local/bin/kemp_exporter
COPY config.yaml /etc/kemp_exporter/config.yaml
USER 10001
EXPOSE 9447
ENTRYPOINT ["/usr/local/bin/kemp_exporter"]
CMD ["--config", "/etc/kemp_exporter/config.yaml"]
```

The `apk add` here is tolerated because the release image builds in CI with open egress; the local `./Dockerfile` is the one that must survive a corporate proxy.

- [ ] **Step 3: Write `.goreleaser.yaml`**

Copy `/Users/fjacquet/Projects/pstore_exporter/.goreleaser.yaml` — the family reference for the `dockers_v2` block — then substitute:

- every `pstore_exporter` → `kemp_exporter`
- every `powerstore` → `kemp`
- the GHCR image → `ghcr.io/fjacquet/kemp_exporter`
- the binary name in `archives` and `builds` → `kemp_exporter`

Confirm the copied file has all of: `version: 2`; `builds` with `CGO_ENABLED=0`, `goos: [linux, darwin, windows]`, `goarch: [amd64, arm64]`, `-trimpath`, `ldflags: -s -w -X main.version={{ .Version }}`, `mod_timestamp: {{ .CommitTimestamp }}`; `archives` including `LICENSE README.md config.yaml` with a Windows `zip` override; `checksum` sha256; `sboms` using `cyclonedx-gomod` with the `../` module path; `dockers_v2` with `sbom: true` and multi-arch platforms; `homebrew_casks` with `skip_upload` when the token is absent; `changelog: use: github-native`.

- [ ] **Step 4: Write the CI callers and dependabot config**

Copy the four stubs from `fjacquet/ci/templates/workflows/` into `.github/workflows/`:

```bash
git clone --depth 1 https://github.com/fjacquet/ci /tmp/fjacquet-ci
cp /tmp/fjacquet-ci/templates/workflows/{ci,security,release,docs}.yml .github/workflows/
```

Keep them thin — `uses:` plus caller `permissions:` plus `secrets:` passthrough, roughly 12 lines each. Do **not** re-inline workflow steps, action SHA pins, or harden-runner boilerplate: those live centrally in `fjacquet/ci` now. Secrets to pass through: `CODECOV_TOKEN` on `ci.yml`, `HOMEBREW_TAP_GITHUB_TOKEN` on `release.yml` (optional — the cask self-skips when absent).

If the clone fails or the templates directory has moved, copy `.github/workflows/` from `/Users/fjacquet/Projects/pstore_exporter/` instead and change only the repo-specific names.

Create `.github/dependabot.yml` — `gomod` and `docker` only. No `github-actions` ecosystem: those actions are pinned centrally.

```yaml
---
version: 2
updates:
  - package-ecosystem: gomod
    directory: "/"
    schedule:
      interval: weekly
    open-pull-requests-limit: 5
  - package-ecosystem: docker
    directory: "/"
    schedule:
      interval: weekly
    open-pull-requests-limit: 5
```

- [ ] **Step 5: Write `.golangci.yml`**

Copy `/Users/fjacquet/Projects/ppdd_exporter/.golangci.yml` unchanged. If that file does not exist, copy from `pstore_exporter`. Do not author a new lint config — the family shares one.

- [ ] **Step 6: Run the gate**

```bash
make tools
make ci
```

Expected: PASS — gofmt clean, `go vet` clean, `golangci-lint` clean, `go test -race` green, `govulncheck` clean.

If `govulncheck` reports a stdlib finding, bump the `go` directive in `go.mod` to the patch release that clears it and re-run. Do not suppress it.

```bash
goreleaser check
make release-snapshot
ls dist/
```

Expected: `goreleaser check` reports no errors; `dist/` contains binaries for every platform, `checksums.txt`, and an SBOM.

- [ ] **Step 7: Commit**

```bash
git add Makefile Dockerfile Dockerfile.goreleaser .goreleaser.yaml .golangci.yml .github/
git commit -m "build: add Makefile contract, containers, GoReleaser, and fjacquet/ci callers"
```

---

## Task 17: Observability quickstart stack

**Files:**
- Create: `docker-compose.yml`, `docker-compose.ghcr.yml`, `prometheus.yml`, `deploy/prometheus/kemp.rules.yml`, `grafana/provisioning/datasources/datasource.yml`, `grafana/provisioning/dashboards/dashboards.yml`, `grafana/kemp-overview.json`, `deploy/kemp_exporter.service`, `deploy/kemp_exporter.env.example`
- Test: `internal/dashboards/dashboards_test.go`

**Interfaces:**
- Consumes: the metric names from Tasks 9–11.
- Produces: a one-command demo stack, alert rules, and the systemd deployment path.

The dashboard replaces Grafana 12160, which is SNMP-sourced and shares no metrics with `kemp_`. 12160's panel layout is the reference; its queries are not reusable.

- [ ] **Step 1: Write the dashboard guard test**

A dashboard referencing a metric the exporter never emits renders as an empty panel with no error. This test makes that a build failure instead.

Create `internal/dashboards/dashboards_test.go`:

```go
package dashboards

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// knownMetrics is every metric name the exporter can emit. Keep it in sync with
// docs/metrics.md; a dashboard referencing anything outside this set is a bug.
var knownMetrics = map[string]bool{
	"kemp_up":                                  true,
	"kemp_exporter_build_info":                 true,
	"kemp_connections_per_second":              true,
	"kemp_bytes_per_second":                    true,
	"kemp_packets_per_second":                  true,
	"kemp_cpu_idle_percent":                    true,
	"kemp_cpu_user_percent":                    true,
	"kemp_cpu_system_percent":                  true,
	"kemp_memory_free_bytes":                   true,
	"kemp_memory_used_bytes":                   true,
	"kemp_memory_used_percent":                 true,
	"kemp_tps":                                 true,
	"kemp_tps_ssl":                             true,
	"kemp_interface_bytes_read_total":          true,
	"kemp_interface_bytes_written_total":       true,
	"kemp_virtual_service_up":                  true,
	"kemp_virtual_service_status":              true,
	"kemp_virtual_service_active_connections":  true,
	"kemp_virtual_service_connections_per_second": true,
	"kemp_virtual_service_connections_total":   true,
	"kemp_virtual_service_packets_total":       true,
	"kemp_virtual_service_bytes_total":         true,
	"kemp_virtual_service_bytes_read_total":    true,
	"kemp_virtual_service_bytes_written_total": true,
	"kemp_real_server_up":                      true,
	"kemp_real_server_status":                  true,
	"kemp_real_server_active_connections":      true,
	"kemp_real_server_connections_per_second":  true,
	"kemp_real_server_connections_total":       true,
	"kemp_real_server_packets_total":           true,
	"kemp_real_server_bytes_total":             true,
	"kemp_real_server_bytes_read_total":        true,
	"kemp_real_server_bytes_written_total":     true,
}

var metricRef = regexp.MustCompile(`\bkemp_[a-z0-9_]+`)

// collectExprs walks arbitrary decoded JSON collecting every expr/query string.
func collectExprs(v any, out *[]string) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if s, ok := val.(string); ok && (k == "expr" || k == "query" || k == "definition") {
				*out = append(*out, s)
				continue
			}
			collectExprs(val, out)
		}
	case []any:
		for _, item := range t {
			collectExprs(item, out)
		}
	}
}

func TestDashboardsReferenceOnlyKnownMetrics(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "grafana", "*.json"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no dashboard JSON found under grafana/")
	}

	for _, p := range paths {
		t.Run(filepath.Base(p), func(t *testing.T) {
			raw, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			var doc any
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatalf("dashboard is not valid JSON: %v", err)
			}
			var exprs []string
			collectExprs(doc, &exprs)
			if len(exprs) == 0 {
				t.Fatal("dashboard contains no queries")
			}

			var unknown []string
			for _, e := range exprs {
				for _, ref := range metricRef.FindAllString(e, -1) {
					if !knownMetrics[ref] {
						unknown = append(unknown, ref+"  (in: "+e+")")
					}
				}
			}
			sort.Strings(unknown)
			if len(unknown) > 0 {
				t.Errorf("dashboard references metrics the exporter never emits:\n  %s",
					strings.Join(unknown, "\n  "))
			}
		})
	}
}

// Per-second GAUGES must never be wrapped in rate(): they are already rates.
// Cumulative _total counters may and should use rate()/increase().
func TestDashboardsDoNotRateGauges(t *testing.T) {
	gauges := []string{
		"kemp_connections_per_second",
		"kemp_bytes_per_second",
		"kemp_packets_per_second",
		"kemp_virtual_service_connections_per_second",
		"kemp_real_server_connections_per_second",
		"kemp_tps",
		"kemp_tps_ssl",
		"kemp_memory_used_percent",
		"kemp_cpu_idle_percent",
	}
	paths, _ := filepath.Glob(filepath.Join("..", "..", "grafana", "*.json"))
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		var doc any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		var exprs []string
		collectExprs(doc, &exprs)
		for _, e := range exprs {
			for _, g := range gauges {
				for _, fn := range []string{"rate(", "irate(", "increase("} {
					if strings.Contains(e, fn) && strings.Contains(e, g) {
						t.Errorf("%s: %s applied to gauge %s — use sum/avg instead:\n  %s",
							filepath.Base(p), fn, g, e)
					}
				}
			}
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/dashboards/ -v`
Expected: FAIL — "no dashboard JSON found under grafana/".

- [ ] **Step 3: Build the dashboard**

Create `grafana/kemp-overview.json`. Base it on `/Users/fjacquet/Projects/ppdd_exporter/grafana/` (the single-overview family reference) for structure — `__inputs`, `templating`, `panels`, `schemaVersion` — and populate the panels from 12160's layout:

| 12160 panel | Replacement query |
|---|---|
| VS up count | `count(kemp_virtual_service_up{system="$system"} == 1) OR on() vector(0)` |
| VS down count | `count(kemp_virtual_service_up{system="$system"} == 0) OR on() vector(0)` |
| Active connections | `sum(kemp_virtual_service_active_connections{system="$system"})` |
| VS outbound bits/s | `rate(kemp_virtual_service_bytes_written_total{system="$system"}[5m]) * 8` |
| Interface in bits/s | `rate(kemp_interface_bytes_read_total{system="$system"}[5m]) * 8` |
| Interface out bits/s | `rate(kemp_interface_bytes_written_total{system="$system"}[5m]) * 8` |
| CPU busy % | `100 - kemp_cpu_idle_percent{system="$system", cpu="total"}` |
| Free memory | `kemp_memory_free_bytes{system="$system"}` |
| TPS | `kemp_tps{system="$system"}` and `kemp_tps_ssl{system="$system"}` |
| Top 5 VS by traffic | `topk(5, increase(kemp_virtual_service_bytes_written_total{system="$system"}[1d]))` |
| Degraded services (new) | `kemp_virtual_service_status{system="$system", status!="Up"}` |
| *Disk available* | **dropped** — no REST equivalent |

Requirements: a `system` template variable defined as `label_values(kemp_up, system)`; every panel using the provisioned datasource; `rate()` only on `_total` counters.

- [ ] **Step 4: Write the provisioning, compose, and Prometheus files**

Copy these four from `/Users/fjacquet/Projects/ppdd_exporter/`, substituting `ppdd`→`kemp`, port `9441`→`9447`, `PPDD1_`→`KEMP1_`, and the GHCR image → `ghcr.io/fjacquet/kemp_exporter:latest`:

- `grafana/provisioning/datasources/datasource.yml`
- `grafana/provisioning/dashboards/dashboards.yml`
- `docker-compose.yml` and `docker-compose.ghcr.yml`

Create `prometheus.yml`:

```yaml
---
global:
  scrape_interval: 30s
rule_files:
  - /etc/prometheus/rules/*.yml
scrape_configs:
  - job_name: kemp_exporter
    static_configs:
      - targets: ["kemp_exporter:9447"]
```

Create `deploy/prometheus/kemp.rules.yml`. Every value alert is paired with an `absent()` companion, because the absent-not-zero policy means a parse failure removes the series rather than zeroing it — without the companion, a vanished metric silently stops alerting:

```yaml
---
groups:
  - name: kemp
    rules:
      - alert: KempTargetDown
        expr: kemp_up == 0
        for: 5m
        labels:
          severity: critical
        annotations:
          summary: "LoadMaster {{ $labels.system }} unreachable"
          description: "The exporter has failed to collect from {{ $labels.system }} for 5 minutes."

      - alert: KempExporterNotReporting
        expr: absent(kemp_up)
        for: 10m
        labels:
          severity: critical
        annotations:
          summary: "No kemp_up series at all"
          description: "The exporter is not reporting any LoadMaster. Check that it is running and scraped."

      - alert: KempVirtualServiceDown
        expr: kemp_virtual_service_up == 0
        for: 5m
        labels:
          severity: critical
        annotations:
          summary: "Virtual service {{ $labels.name }} ({{ $labels.address }}:{{ $labels.port }}) is down"

      - alert: KempVirtualServiceDegraded
        expr: kemp_virtual_service_status{status="Sick"} == 1
        for: 10m
        labels:
          severity: warning
        annotations:
          summary: "Virtual service {{ $labels.name }} is Sick"
          description: "Still serving traffic, but the LoadMaster reports it degraded."

      - alert: KempRealServerDown
        expr: kemp_real_server_up == 0
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "Real server {{ $labels.address }}:{{ $labels.port }} is down"

      - alert: KempMemoryLow
        expr: kemp_memory_used_percent > 90
        for: 15m
        labels:
          severity: warning
        annotations:
          summary: "LoadMaster {{ $labels.system }} memory above 90%"

      - alert: KempMemoryMetricMissing
        expr: kemp_up == 1 unless on(system) kemp_memory_used_percent
        for: 30m
        labels:
          severity: warning
        annotations:
          summary: "Memory metrics missing for a reachable LoadMaster {{ $labels.system }}"
          description: "The appliance is up but memory fields did not parse, so KempMemoryLow cannot fire."

      - alert: KempCPUHigh
        expr: 100 - kemp_cpu_idle_percent{cpu="total"} > 90
        for: 15m
        labels:
          severity: warning
        annotations:
          summary: "LoadMaster {{ $labels.system }} CPU above 90%"
```

- [ ] **Step 5: Write the systemd unit**

Create `deploy/kemp_exporter.service`:

```ini
[Unit]
Description=Kemp LoadMaster Prometheus exporter
Documentation=https://fjacquet.github.io/kemp_exporter/
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=kemp_exporter
Group=kemp_exporter
EnvironmentFile=/etc/kemp_exporter/kemp_exporter.env
ExecStart=/usr/local/bin/kemp_exporter --config /etc/kemp_exporter/config.yaml
# Reuses the built-in SIGHUP hot reload, so `systemctl reload` swaps the target set
# without dropping a collection cycle.
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
RestartSec=10
StandardOutput=journal
StandardError=journal

NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
PrivateDevices=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictAddressFamilies=AF_INET AF_INET6
RestrictNamespaces=true
RestrictRealtime=true
LockPersonality=true
MemoryDenyWriteExecute=true

[Install]
WantedBy=multi-user.target
```

Create `deploy/kemp_exporter.env.example` — install it as `0600 root:kemp_exporter`:

```bash
# /etc/kemp_exporter/kemp_exporter.env — install 0600 root:kemp_exporter
KEMP1_HOSTNAME=10.0.0.1
KEMP1_APIKEY=replace-me
# KEMP1_USERNAME=bal
# KEMP1_PASSWORD=replace-me
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/dashboards/ -v`
Expected: PASS. A failure naming an unknown metric means the dashboard and the exporter disagree — fix the dashboard, or add the metric to both `knownMetrics` and `docs/metrics.md` if it should exist.

- [ ] **Step 7: Verify the stack starts**

```bash
cp .env.example .env
docker compose up --build -d
curl -s localhost:9447/metrics | grep kemp_exporter_build_info
curl -s -o /dev/null -w '%{http_code}\n' localhost:9090/-/ready
docker compose down --remove-orphans
```

Expected: the build-info metric is present; Prometheus returns 200. `kemp_up` will be `0` — there is no LoadMaster to reach, which is the expected demo state.

- [ ] **Step 8: Commit**

```bash
git add docker-compose.yml docker-compose.ghcr.yml prometheus.yml deploy/ grafana/ internal/dashboards/
git commit -m "feat(deploy): add compose stack, dashboard, alert rules, and systemd unit"
```

---

## Task 18: Documentation and ADRs

**Files:**
- Create: `README.md`, `CLAUDE.md`, `CHANGELOG.md`, `LICENSE`, `SECURITY.md`, `CONTRIBUTING.md`, `mkdocs.yml`, `docs/index.md`, `docs/metrics.md`, `docs/dashboards.md`, `docs/deployment/docker.md`, `docs/deployment/systemd.md`, `docs/adr/index.md`, `docs/adr/0001-…` through `docs/adr/0008-…`

**Interfaces:**
- Consumes: every metric name and decision from Tasks 1–17.
- Produces: a published MkDocs site and the ADR set.

- [ ] **Step 1: Write `docs/metrics.md`**

The catalog. One row per metric, with four columns: **Metric**, **Type**, **Labels**, **Status**.

`Type` is `gauge` or `counter` — and where they differ from the naming, say why (`kemp_tps` is a gauge with no `_total`; the `_total` counters render as Prometheus gauge type by family convention, see Task 12's note).

`Status` is **confirmed** or **unconfirmed**. Mark as unconfirmed everything inferred rather than documented, which as of this plan is: the `stats` response's `CPU`, `Memory`, `Network` and `TPS` element paths; the `listvs` `NickName`/`Status` field names; the entire JSON envelope shape (`Success.Data`, the `status`/`code` keys); the JSON login path `/access/login`; the token header `X-API-Key`; and the real-server `Status` field.

Close the file with the live-validation checklist:

````markdown
## Live validation (outstanding)

No LoadMaster was available when this exporter was built. Every **unconfirmed** row
above is inferred from the Kemp API documentation and the `giantswarm/kemp-client`
struct tags, and is exercised only against fixtures.

To validate against a real appliance:

```bash
kemp_exporter --config real.yaml --once --debug --trace 2>trace.log | sort > samples.txt
```

Then:

1. Diff `samples.txt` against this catalog. A documented metric that does not appear
   means its source field did not parse — check `trace.log` for the real element name.
2. Confirm the transport the exporter selected (logged at startup as
   `detected LoadMaster API transport`).
3. Update the element paths in `internal/models/statistics.go`, refresh the fixtures
   in `internal/kemp/testdata/`, and flip the affected rows to **confirmed**.
4. `trace.log` contains full response bodies. It may include hostnames and virtual
   service names — treat it as sensitive and delete it when finished.
````

- [ ] **Step 2: Write the ADRs**

Eight files in `docs/adr/`, named `NNNN-kebab-title.md`, each with a first-level heading matching the decision title and the sections **Status**, **Context**, **Decision**, **Consequences**. Use `/Users/fjacquet/Projects/ppdd_exporter/docs/adr/` as the template.

| File | Title | The point it must make |
|---|---|---|
| `0001-supply-chain-and-release-hardening.md` | Supply-chain and release hardening | SHA-pinned actions centrally in `fjacquet/ci`; `persist-credentials: false`; `cache: false` on release; CycloneDX SBOM per release |
| `0002-snapshot-collection-model.md` | Snapshot collection model | Backend API load tracks the interval, not the scraper count; readers never block the loop |
| `0003-hand-rolled-resty-client.md` | Hand-rolled resty client | No official Kemp Go SDK (PowerShell, Java, Python only). `giantswarm/kemp-client` fails criterion 1 (HTTP Basic only, no modern auth) and criterion 4 (hardcodes `InsecureSkipVerify: true` in `kemp.go`, disabling TLS verification with no opt-out) |
| `0004-dual-transport-single-model.md` | Dual transport, single model, runtime detection | Why one model beats pflex's two-pipeline split here (same command, same fields, different encoding); why detection caches; why a static API key deviates from bearer+refresh (LoadMaster keys are long-lived by design) |
| `0005-metric-naming-and-units.md` | Metric naming and units | Per-second values are gauges (`sum`/`avg`, never `rate()`); cumulative values carry `_total` and `rate()` is correct there; `kemp_tps` is a gauge and deliberately has no suffix; bytes not kilobytes; percentages on 0–100 |
| `0006-label-key-union-invariant.md` | Label-key union invariant | One label-key set per metric name; unresolved names are empty values, not missing keys; the `_status` metrics are their own families with a sixth key; the collector drops divergent series rather than failing the scrape |
| `0007-own-dashboard-not-grafana-12160.md` | Own dashboard instead of Grafana 12160 | 12160 is `snmp_exporter`-sourced (`vSstate`, `ifHCInOctets`, `ssCpuIdle`, `dskAvail`, label `device`) with zero overlap with `kemp_`; renaming to match would break three naming rules at once; we reuse its layout, not its queries; the disk panel has no REST equivalent and is dropped |
| `0008-config-hot-reload.md` | Config hot reload | SIGHUP plus file-watch; watch the directory not the inode (rename-based writes); a bad file never takes the process down |

Create `docs/adr/index.md` listing all eight with one-line summaries.

- [ ] **Step 3: Write `docs/dashboards.md` and the deployment guides**

`docs/dashboards.md`: what the overview dashboard shows, the `system` template variable, and an explicit note that Grafana 12160 is **not** compatible and why (pointing at ADR 0007) — someone will try it otherwise.

`docs/deployment/docker.md`: the compose quickstart, `.env` variables, and the GHCR variant.

`docs/deployment/systemd.md`: install, operate, harden. Include the macOS note — `brew services` is **not** wired up (the cask only drops the binary; there is no service block), so a `launchd` job must be registered by hand.

- [ ] **Step 4: Write the top-level docs**

`README.md` with the canonical six badges in order: CI workflow status (pointing at the caller `ci.yml`), latest release (`include_prereleases&sort=semver`), Go Report Card, Go version (`go-mod/go-version`), license, docs (linking to the Pages site). Then: what it does, quickstart, configuration, metric summary, and a short "relationship to `giantswarm/prometheus-kemp-exporter`" section stating this is a rewrite and naming the concrete reasons.

`CLAUDE.md`: overview, commands, architecture, load-bearing constraints (absent-not-zero; label-key invariant; no-4xx-retry; HTTP-before-collect; no inline suppressions; `Num` lives in `models` to avoid an import cycle), testing, CI/CD.

`CHANGELOG.md` in Keep a Changelog format with an `[Unreleased]` section. Standing practice: on each release, move `[Unreleased]` → `[X.Y.Z] - DATE` rather than letting it accumulate.

`LICENSE` (Apache 2.0, matching the family and the upstream project), `SECURITY.md`, and `CONTRIBUTING.md` — copy from `ppdd_exporter` and change the repo name.

`mkdocs.yml`: Material theme, nav covering index, metrics, dashboards, deployment (docker, systemd), and ADRs. Copy `ppdd_exporter/mkdocs.yml` and adjust `site_name`, `repo_url`, and the nav.

- [ ] **Step 5: Verify the docs build**

```bash
make docs
```

Expected: PASS. `--strict` is on, so a broken internal link or a nav entry pointing at a missing file fails the build.

- [ ] **Step 6: Full gate**

```bash
make ci
go test ./... -race
```

Expected: both green.

- [ ] **Step 7: Commit**

```bash
git add README.md CLAUDE.md CHANGELOG.md LICENSE SECURITY.md CONTRIBUTING.md mkdocs.yml docs/
git commit -m "docs: add metric catalog, ADRs, deployment guides, and MkDocs site"
```

---

## Definition of done

- [ ] `make ci` green: gofmt, `go vet`, `golangci-lint`, `go test -race`, `govulncheck`.
- [ ] `goreleaser check` clean; `make release-snapshot` produces binaries, checksums and an SBOM.
- [ ] `make docs` builds `--strict`.
- [ ] `docker compose up --build` serves `/metrics` on 9447 with `kemp_exporter_build_info` present.
- [ ] No `//nolint` or `// nosemgrep` anywhere in the tree: `grep -rE '//\s*(nolint|nosemgrep)' --include='*.go' .` returns nothing.
- [ ] Every metric in `docs/metrics.md` carries a **confirmed**/**unconfirmed** marker, and the live-validation checklist is present.
- [ ] Eight ADRs exist with `Status`/`Context`/`Decision`/`Consequences`, listed in `docs/adr/index.md`.
- [ ] Both export paths asserted: Prometheus registry gather (Task 12) and OTLP `ManualReader` (Task 13).
- [ ] The transport parity test passes (Task 7) — the guard on the single-model design.

## Known follow-ups (not in scope)

1. **Live validation** — every unconfirmed wire path, per `docs/metrics.md`.
2. **Disk metrics** — no REST equivalent for 12160's `dskAvail` was identified. Revisit once an appliance is reachable and the full `stats` payload is visible.
3. **Extended surface** — SubVS statistics, HA/cluster state, WAF counters, TLS certificate expiry. Deferred from v0.1.0; certificate expiry is the highest-value addition for a load balancer.

