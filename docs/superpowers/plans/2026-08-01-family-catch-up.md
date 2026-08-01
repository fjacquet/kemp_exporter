# Family standard catch-up (probes, port 9448, container health) Implementation Plan

> **Note for agentic workers:** This plan is written to be executed by a fresh
> agent with no context beyond this document. Every code block is the literal
> text to write. Do not paraphrase, do not "adapt", do not substitute
> placeholders. Work the tasks in order; each task ends with a green gate and a
> commit. If something in this plan contradicts what you find on disk, stop and
> report it rather than improvising.

**Goal**

Bring `kemp_exporter` up to the family standard that two 2026-08-01 efforts
applied to the eight repos in the `exporter-standards` table and silently skipped
here:

1. Always-200 `/livez` and `/readyz` probe endpoints, and a `/health` that no
   longer returns 503 — the starting/stale information moves from the status code
   into the response body.
2. Container `HEALTHCHECK` in both Dockerfiles and `healthcheck:` in both compose
   files, on the unpinned `alpine:latest` base.
3. **Breaking:** the metrics port moves `9447` → `9448`, because `kemp_exporter`
   and `nsr_exporter` both listen on 9447 today and cannot coexist on one host.
   nsr is the older repo and keeps 9447.

**Architecture**

`main.go` builds one explicit `http.ServeMux` inside `startServing`
(`main.go:169-190`). Today it registers three routes: `cfg.Server.URI` →
`metricsHandler`, `/health` → `healthHandler`, and `/` → `indexHandler`. This work
adds two more routes to that same mux and rewrites one handler. There is no new
package, no new file in `internal/`, and no change to the snapshot model, the
collection loop, the config schema (only its default *value*), or either export
path.

The probe handler reads no state at all. That is the whole point: a probe wired to
`/livez` can never be the reason a healthy process is restarted or depooled.
`/health` keeps every bit of its diagnostic value — it just reports it in the body.

**Tech Stack**

Go 1.26.5, `net/http` standard library only (no new dependency), `httptest` for
handler tests, `logrus` for the existing debug logging, Docker/BuildKit + Docker
Compose v2 for the container work, `hadolint` for Dockerfile linting, MkDocs
Material for the docs site.

**Spec**

`/Users/fjacquet/Projects/obs_exporter/docs/superpowers/specs/2026-08-01-family-standard-catch-up-design.md`
— this plan implements its **Plan B — `kemp_exporter`** section, under decisions 1
and 5 and the "Canonical patterns" section.

---

## Global Constraints

These apply to every task below. Violating any one of them has already cost a
sibling repo a post-review fix wave.

- **`127.0.0.1`, never `localhost`, in every healthcheck.** Alpine's busybox
  `wget` resolves `localhost` via `::1` first and these exporters bind IPv4 only,
  so a `localhost`-based check fails at runtime with connection refused. It passes
  `hadolint` **and** `docker compose config` while being completely broken. There
  is no lint that catches this — only actually running the container does.
- **`HEALTHCHECK` timeout is `5s` in BOTH the Dockerfile and the compose
  `healthcheck:`.** The Alpine effort shipped a 5s/10s mismatch across all eight
  family repos and had to correct it in every single final review. The full
  parameter set is `--interval=30s --timeout=5s --start-period=10s --retries=3`
  and its compose equivalent `interval: 30s / timeout: 5s / retries: 3 /
  start_period: 10s`.
- **hadolint findings `DL3025`, `DL3007` and `DL3066` are expected, not
  defects.** `DL3025` (shell-form `CMD`) is unavoidable given the required
  `... || exit 1` syntax; `DL3007` (`alpine:latest` unpinned) is decision 5 of the
  spec, deliberately taken family-wide; `DL3066` is a standing family finding. Do
  **not** add inline suppressions — this repo forbids `// nosemgrep`, `//nolint`
  and `# hadolint ignore` family-wide and the Definition of Done greps for them.
  Do not treat these three as blocking.
- **Verification means building and running the image**, then asserting
  `docker inspect --format='{{.State.Health.Status}}' <container>` prints
  `healthy`. Reading the Dockerfile is not verification. Task 5 is dedicated to
  this and must not be skipped or reported as "verified by inspection".
- **The ADR number is confirmed by `ls docs/adr/` before writing the file**, never
  assumed. A prior effort in a sibling repo shipped literal `ADR-000N`
  placeholders into committed Dockerfile comments because a number was assumed.
  This plan expects `0009` (there are eight ADRs today) but the listing is the
  authority.
- **The new ADR needs a row in `docs/adr/index.md`** *and* an entry in
  `mkdocs.yml`'s `nav:` — this repo's nav lists every ADR file explicitly, so
  omitting it makes the ADR unreachable from the site.
  This is a discoverability requirement, **not** a build gate: with no
  `validation:` block in `mkdocs.yml`, a docs file absent from `nav:` is an INFO
  notice and `mkdocs build --strict` still exits 0 (verified empirically). What
  `--strict` *does* fail on is the reverse — a `nav:` entry pointing at a file
  that does not exist — and on broken internal links.
- **After the port change, re-run `git grep -n 9447` and confirm the only
  remaining hits are the two historical documents** (`docs/superpowers/plans/2026-07-31-kemp-exporter.md`
  and `docs/superpowers/specs/2026-07-30-kemp-exporter-design.md`). Those are
  records of what was decided at the time and stay as written, exactly like
  historical ADRs. Every other hit must be gone.
- **No inline suppressions anywhere.** `make ci` runs `golangci-lint` and
  `semgrep` as blocking gates. Findings are fixed by restructuring.
- **Apple Silicon note:** building `Dockerfile.goreleaser` locally requires a
  binary cross-compiled for Linux and laid out per-platform. Use
  `GOOS=linux GOARCH=arm64 go build ...` into `linux/arm64/kemp_exporter` and pass
  `--build-arg TARGETPLATFORM=linux/arm64`, or the container exits immediately
  with `exec format error`. The exact commands are in Task 5.
- Run every command from the repo root, `/Users/fjacquet/Projects/kemp_exporter`.

## File Structure

| Path | Action | What changes |
|---|---|---|
| `main.go` | Modify | Add `staticOKHandler`; register `/livez` + `/readyz` in `startServing`; rewrite `healthHandler` to always return 200 |
| `main_test.go` | Modify | Rewrite `TestHealthHandlerUsesSnapshotAge` for 200-in-all-three-cases + body assertions; add `TestProbeEndpointsAlwaysOKBeforeFirstCollection` |
| `config.yaml` | Modify | `port: "9447"` → `"9448"` |
| `internal/config/config.go` | Modify | Default port `"9447"` → `"9448"` |
| `internal/config/config_test.go` | Modify | Default-port assertion `9447` → `9448` |
| `prometheus.yml` | Modify | Scrape target `kemp_exporter:9447` → `:9448` |
| `Dockerfile` | Modify | `alpine:3.22` → `alpine:latest`; `EXPOSE 9448`; add `HEALTHCHECK` |
| `Dockerfile.goreleaser` | Modify | `alpine:3.22` → `alpine:latest`; `EXPOSE 9448`; add `HEALTHCHECK` |
| `docker-compose.yml` | Modify | Port mapping `9448:9448`; add `healthcheck:` |
| `docker-compose.ghcr.yml` | Modify | Port mapping `9448:9448`; add `healthcheck:` |
| `README.md` | Modify | Two port references; probe endpoints noted |
| `CONTRIBUTING.md` | Modify | One port reference |
| `CLAUDE.md` | Modify | Metrics-port line |
| `docs/index.md` | Modify | One port reference |
| `docs/deployment/docker.md` | Modify | Two port references + healthcheck note |
| `docs/deployment/systemd.md` | Modify | One port reference |
| `docs/adr/0009-always-200-probes-and-port-9448.md` | Create | The decision record |
| `docs/adr/index.md` | Modify | Add the 0009 row |
| `mkdocs.yml` | Modify | Add the 0009 nav entry |
| `CHANGELOG.md` | Modify | `Breaking` / `Changed` / `Added` under `## [Unreleased]` (line 11) |
| `docs/superpowers/plans/2026-07-31-kemp-exporter.md` | **Leave alone** | Historical record |
| `docs/superpowers/specs/2026-07-30-kemp-exporter-design.md` | **Leave alone** | Historical record |

---

### Task 1: Rewrite `/health` to always return 200

**Files:**
- Modify: `/Users/fjacquet/Projects/kemp_exporter/main_test.go`
- Modify: `/Users/fjacquet/Projects/kemp_exporter/main.go`

**Interfaces:**
- Consumes: `kemp.SnapshotStore` (`store.Load()` returning `*kemp.Snapshot` with a
  `BuiltAt time.Time`), `time.Duration` max age.
- Produces: `healthHandler(store *kemp.SnapshotStore, maxAge time.Duration) http.Handler`
  — unchanged signature, changed behaviour: status is always `200`; the body is
  `"starting: ...\n"`, `"stale: ...\n"` or `"ok\n"`.

- [ ] **Step 1: Rewrite the failing test.** In `main_test.go`, replace the whole
  of `TestHealthHandlerUsesSnapshotAge` — its doc comment on line 200 through its
  closing brace on line 227 — with this. It asserts 200 in all three cases and
  moves the starting/fresh/stale distinction onto the body:

```go
// /health is driven by snapshot age, independent of kemp_up -- and it ALWAYS
// answers 200. The starting/stale distinction lives in the body, never in the
// status code: /health used to return 503 while starting or stale, which meant
// any orchestrator probe pointed at it would restart or depool an exporter that
// was merely waiting on its first cycle, or whose appliance was briefly
// unreachable. /livez and /readyz are the endpoints probes should use; they read
// no state and cannot fail. /health is the diagnostic endpoint, and a diagnostic
// endpoint that refuses to answer is useless precisely when it is needed.
func TestHealthHandlerUsesSnapshotAge(t *testing.T) {
	store := kemp.NewSnapshotStore()
	h := healthHandler(store, time.Minute)

	get := func() (int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
		return rec.Code, rec.Body.String()
	}

	// No snapshot yet: starting up. 200, and the body says which.
	code, body := get()
	if code != http.StatusOK {
		t.Errorf("pre-collection status = %d, want 200", code)
	}
	if !strings.Contains(body, "starting") {
		t.Errorf("pre-collection body = %q, want it to report %q", body, "starting")
	}

	// Fresh snapshot: healthy, and the body must not claim otherwise.
	store.Store(&kemp.Snapshot{BuiltAt: time.Now()})
	code, body = get()
	if code != http.StatusOK {
		t.Errorf("fresh-snapshot status = %d, want 200", code)
	}
	if !strings.HasPrefix(body, "ok") {
		t.Errorf("fresh-snapshot body = %q, want it to start with %q", body, "ok")
	}

	// Stale snapshot: the loop is wedged even though kemp_up may still read 1.
	// Still 200 -- the body is what carries the bad news.
	store.Store(&kemp.Snapshot{BuiltAt: time.Now().Add(-10 * time.Minute)})
	code, body = get()
	if code != http.StatusOK {
		t.Errorf("stale-snapshot status = %d, want 200", code)
	}
	if !strings.Contains(body, "stale") {
		t.Errorf("stale-snapshot body = %q, want it to report %q", body, "stale")
	}
}
```

- [ ] **Step 2: Watch it fail.** Run:

```bash
cd /Users/fjacquet/Projects/kemp_exporter && go test -run TestHealthHandlerUsesSnapshotAge ./...
```

  Expect failures on the pre-collection and stale-snapshot cases
  (`status = 503, want 200`). If it passes, the edit did not land — stop and
  check. No new import is needed: `strings`, `time`, `net/http` and
  `net/http/httptest` are all already imported by `main_test.go`.

- [ ] **Step 3: Implement.** In `main.go`, replace `healthHandler` — its doc
  comment on lines 71-73 through its closing brace on line 90 — with:

```go
// healthHandler reports collection freshness from snapshot AGE, deliberately
// independent of kemp_up. kemp_up describes the backend; a wedged collection loop
// would leave it at a stale 1 forever, so staleness is the only honest freshness
// signal.
//
// It ALWAYS answers 200. The starting/stale information is carried in the body,
// never in the status code. This handler used to return 503 in both of those
// cases, which made it unsafe as an orchestrator probe target: a Kubernetes
// liveness probe would restart an exporter that was merely waiting on its first
// collection cycle, and a readiness probe would depool one whose appliance was
// briefly unreachable -- neither of which the exporter process can fix by dying.
// /livez and /readyz (staticOKHandler) exist for probes; /health exists for a
// human or a dashboard asking "is collection actually current?", and an endpoint
// that answers that question by refusing to answer is useless exactly when it
// matters.
func healthHandler(store *kemp.SnapshotStore, maxAge time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		snap := store.Load()
		body := "ok\n"
		switch {
		case snap.BuiltAt.IsZero():
			body = "starting: no collection cycle has completed yet\n"
		case time.Since(snap.BuiltAt) > maxAge:
			body = fmt.Sprintf("stale: last collection %s ago\n",
				time.Since(snap.BuiltAt).Round(time.Second))
		}
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(body)); err != nil {
			logrus.WithError(err).Debug("health response write failed")
		}
	})
}
```

  Note the `switch` evaluates `IsZero()` first, so `time.Since` on a zero
  `BuiltAt` (a ~2000-year duration) never reaches the stale branch.

- [ ] **Step 4: Watch it pass.** Run:

```bash
cd /Users/fjacquet/Projects/kemp_exporter && go test -run TestHealthHandlerUsesSnapshotAge ./... && go vet ./...
```

  Both must be clean. `http.Error` is no longer called by this function; confirm
  nothing else in `main.go` lost its last use of an import by running `go build ./...`.

- [ ] **Step 5: Commit.**

```bash
cd /Users/fjacquet/Projects/kemp_exporter && git add main.go main_test.go && git commit -m "$(cat <<'EOF'
fix(health): /health always answers 200, staleness moves to the body

/health returned 503 while starting and while stale, which made it unsafe as an
orchestrator probe target: a liveness probe would restart an exporter merely
waiting on its first collection cycle, and a readiness probe would depool one
whose appliance was briefly unreachable. Neither is something the process can fix
by dying. The starting/stale distinction is preserved verbatim -- it just lives in
the response body now.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_016k1BGLiKCXm3iQnfKdUaKv
EOF
)"
```

---

### Task 2: Add `/livez` and `/readyz` probe endpoints

**Files:**
- Modify: `/Users/fjacquet/Projects/kemp_exporter/main_test.go`
- Modify: `/Users/fjacquet/Projects/kemp_exporter/main.go`

**Interfaces:**
- Consumes: nothing. `staticOKHandler` reads no state whatsoever — that is its
  entire contract.
- Produces: `staticOKHandler(w http.ResponseWriter, _ *http.Request)`, registered
  on the `startServing` mux at `/livez` and `/readyz`, each answering `200` with
  body `ok`.

- [ ] **Step 1: Write the failing test.** Append this to `main_test.go`, directly
  after the `TestHealthHandlerUsesSnapshotAge` function you rewrote in Task 1. It
  goes through `startServing` deliberately — a handler that exists but was never
  registered would pass a direct-call test and still 404 in production:

```go
// /livez and /readyz must answer 200 before any collection cycle has completed,
// and they must be wired into the mux startServing actually builds -- calling
// staticOKHandler directly would pass even if nobody registered it, which is
// exactly the regression worth guarding. /health is checked here too, for the
// same wiring reason.
//
// Never probe /metrics instead: rendering the full exposition on every probe tick
// is needless load, and it can block behind a slow collection cycle -- which is
// the failure these endpoints exist to be immune to.
func TestProbeEndpointsAlwaysOKBeforeFirstCollection(t *testing.T) {
	store := kemp.NewSnapshotStore()
	reg := prometheus.NewRegistry()
	if err := reg.Register(kemp.NewPromCollector(store)); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// A deliberately slow first collection: the probes must answer while it is
	// still in flight, not merely after it finishes.
	slow := &kemp.MockClient{
		SystemName: "lm-01",
		Stats:      &models.Statistics{},
		StatsDelay: 300 * time.Millisecond,
	}
	loop := kemp.NewCollectionLoop([]kemp.Client{slow}, config.Collection{
		Interval:      time.Hour,
		Timeout:       2 * time.Second,
		MaxConcurrent: 1,
	}, store)

	cfg := &config.Config{Server: config.Server{Host: "127.0.0.1", Port: "0", URI: "/metrics"}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv, ln, err := startServing(ctx, cfg, reg, store, loop)
	if err != nil {
		t.Fatalf("startServing: %v", err)
	}
	defer func() { _ = srv.Close() }()

	for _, path := range []string{"/livez", "/readyz", "/health"} {
		resp, err := http.Get("http://" + ln.Addr().String() + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		code := resp.StatusCode
		_ = resp.Body.Close()
		if code != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200 before the first collection cycle", path, code)
		}
	}
}
```

- [ ] **Step 2: Watch it fail.** Run:

```bash
cd /Users/fjacquet/Projects/kemp_exporter && go test -run TestProbeEndpointsAlwaysOKBeforeFirstCollection ./...
```

  Expect a compile error on the undefined `staticOKHandler`? No — this test does
  not name it. Expect instead two failures: `GET /livez status = 404` and
  `GET /readyz status = 404`, because the mux falls through to `indexHandler` on
  `/`... which returns 200. **If both probe paths report 200 at this point, the
  test is not proving anything** — `indexHandler` is registered on `/` and matches
  every unregistered path. Confirm the test is meaningful by also asserting the
  body, adding these three lines inside the loop, immediately after
  `code := resp.StatusCode`:

```go
		bodyBytes, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			t.Fatalf("read %s body: %v", path, readErr)
		}
```

  and after the status check:

```go
		if strings.Contains(string(bodyBytes), "<html>") {
			t.Errorf("GET %s served the index landing page: the route is not registered", path)
		}
```

  Add `"io"` to `main_test.go`'s import block (alphabetically, after `"errors"`).
  Re-run; the two probe paths must now fail on the `<html>` assertion. That is the
  real failing state.

- [ ] **Step 3: Implement the handler.** In `main.go`, insert this function
  immediately after `healthHandler`'s closing brace and before the `indexPage`
  var block:

```go
// staticOKHandler always answers 200 -- no snapshot state, no collection state,
// nothing that can make it fail. /livez and /readyz both use it: a probe wired
// here can never be the reason a healthy process gets restarted or pulled from
// rotation. /health remains the endpoint for anything that wants to know whether
// collection is actually current.
func staticOKHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}
```

- [ ] **Step 4: Register the routes.** In `startServing`, replace this block:

```go
	mux := http.NewServeMux()
	mux.Handle(cfg.Server.URI, metricsHandler(reg))
	// Health tolerates two missed cycles before reporting stale.
	mux.Handle("/health", healthHandler(store, 2*cfg.Collection.Interval+cfg.Collection.Timeout))
	mux.HandleFunc("/", indexHandler(cfg.Server.URI))
```

  with:

```go
	mux := http.NewServeMux()
	mux.Handle(cfg.Server.URI, metricsHandler(reg))
	// Health tolerates two missed cycles before reporting stale. It always
	// answers 200; the staleness verdict is in the body (see healthHandler).
	mux.Handle("/health", healthHandler(store, 2*cfg.Collection.Interval+cfg.Collection.Timeout))
	// Fixed probe paths, both wired to a handler that reads nothing and cannot
	// fail. Deliberately NOT /metrics: rendering the full exposition on every
	// probe tick is needless load and can block behind a slow collection cycle.
	mux.HandleFunc("/livez", staticOKHandler)
	mux.HandleFunc("/readyz", staticOKHandler)
	mux.HandleFunc("/", indexHandler(cfg.Server.URI))
```

- [ ] **Step 5: Watch it pass, then run the whole suite.**

```bash
cd /Users/fjacquet/Projects/kemp_exporter && go test -run 'TestProbeEndpointsAlwaysOKBeforeFirstCollection|TestHealthHandlerUsesSnapshotAge' ./... && make sure
```

  `make sure` runs `fmt-check`, `vet`, `test` and `build`. All must be clean.

- [ ] **Step 6: Commit.**

```bash
cd /Users/fjacquet/Projects/kemp_exporter && git add main.go main_test.go && git commit -m "$(cat <<'EOF'
feat(health): add always-200 /livez and /readyz probe endpoints

Both are wired to one handler that reads no state at all, so a probe pointed at
them can never be the reason a healthy process is restarted or depooled. Matches
the family standard (obs_exporter ADR-0013 lineage). Deliberately not /metrics:
rendering the full exposition per probe tick is needless load and can block behind
a slow collection cycle.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_016k1BGLiKCXm3iQnfKdUaKv
EOF
)"
```

---

### Task 3: Move the metrics port 9447 → 9448 (BREAKING)

`kemp_exporter` and `nsr_exporter` both bind 9447 today and cannot run on the same
host. nsr is the older repo (v0.12.4 vs kemp's v0.1.0) and keeps 9447; kemp moves
to 9448, the next free port after the family's 9438–9446 block.

**Files:**
- Modify: `config.yaml`, `internal/config/config.go`, `internal/config/config_test.go`,
  `prometheus.yml`, `README.md`, `CONTRIBUTING.md`, `CLAUDE.md`, `docs/index.md`,
  `docs/deployment/docker.md`, `docs/deployment/systemd.md`
- (The two Dockerfiles and the two compose files also carry the port; they are
  handled in Tasks 4 and 5 together with the healthchecks, so the port and the
  healthcheck land in one coherent edit per file.)

**Interfaces:**
- Consumes: nothing new.
- Produces: `config.Load` defaults `Server.Port` to `"9448"`; shipped `config.yaml`
  says `9448`; every user-facing document says `9448`.

- [ ] **Step 1: Inventory every occurrence.** Run:

```bash
cd /Users/fjacquet/Projects/kemp_exporter && git grep -n 9447
```

  Expect exactly 15 tracked files. Two of them are **historical records that must
  not be touched**: `docs/superpowers/plans/2026-07-31-kemp-exporter.md` and
  `docs/superpowers/specs/2026-07-30-kemp-exporter-design.md`. They record what
  was decided on 2026-07-30/31 and stay as written, exactly like a superseded ADR.
  Everything else changes.

- [ ] **Step 2: Change the default in code, test-first.** In
  `internal/config/config_test.go`, change lines 32-33 from:

```go
	if cfg.Server.Port != "9447" {
		t.Errorf("Server.Port = %q, want \"9447\"", cfg.Server.Port)
	}
```

  to:

```go
	if cfg.Server.Port != "9448" {
		t.Errorf("Server.Port = %q, want \"9448\"", cfg.Server.Port)
	}
```

  Run `go test ./internal/config/` and watch it fail with
  `Server.Port = "9447", want "9448"`.

- [ ] **Step 3: Change the default.** In `internal/config/config.go` line 240,
  change:

```go
		cfg.Server.Port = "9447"
```

  to:

```go
		cfg.Server.Port = "9448"
```

  Run `go test ./internal/config/` and watch it pass.

- [ ] **Step 4: Change the shipped config.** In `config.yaml` line 4, change
  `  port: "9447"` to `  port: "9448"`.

- [ ] **Step 5: Change the scrape target.** In `prometheus.yml` line 9, change
  `      - targets: ["kemp_exporter:9447"]` to
  `      - targets: ["kemp_exporter:9448"]`.

- [ ] **Step 6: Change the four user-facing prose files.**
  - `README.md` line 23: `# metrics: http://localhost:9447/metrics` →
    `# metrics: http://localhost:9448/metrics`
  - `README.md` line 30: `docker run -p 9447:9447 \` → `docker run -p 9448:9448 \`
  - `CONTRIBUTING.md` line 130:
    `The exporter metrics are at <http://localhost:9447/metrics>; Grafana at` →
    `The exporter metrics are at <http://localhost:9448/metrics>; Grafana at`
  - `CLAUDE.md` line 15: `` `9447`. OTLP gRPC: `4317`. `` →
    `` `9448` (moved from 9447 in v0.2.0; 9447 collided with `nsr_exporter`). OTLP gRPC: `4317`. ``

- [ ] **Step 7: Change the three docs-site files.**
  - `docs/index.md` line 19: `# metrics: http://localhost:9447/metrics` →
    `# metrics: http://localhost:9448/metrics`
  - `docs/deployment/docker.md` line 14: the table cell `` `9447` `` → `` `9448` ``
  - `docs/deployment/docker.md` line 49: `docker run -p 9447:9447 \` →
    `docker run -p 9448:9448 \`
  - `docs/deployment/systemd.md` line 43:
    `curl -s http://localhost:9447/metrics | grep kemp_exporter_build_info` →
    `curl -s http://localhost:9448/metrics | grep kemp_exporter_build_info`

- [ ] **Step 8: Re-grep and confirm.** Run:

```bash
cd /Users/fjacquet/Projects/kemp_exporter && git grep -n 9447
```

  The **only** remaining hits allowed are inside
  `docs/superpowers/plans/2026-07-31-kemp-exporter.md` and
  `docs/superpowers/specs/2026-07-30-kemp-exporter-design.md`. Four files still
  legitimately hold `9447` at this moment — `Dockerfile`,
  `Dockerfile.goreleaser`, `docker-compose.yml`, `docker-compose.ghcr.yml` — and
  are fixed in Tasks 4 and 5. If any *other* file appears, fix it now. Then:

```bash
cd /Users/fjacquet/Projects/kemp_exporter && git grep -n 9448 | wc -l
```

  Expect at least 12 hits.

- [ ] **Step 9: Gate and commit.**

```bash
cd /Users/fjacquet/Projects/kemp_exporter && make sure
```

```bash
cd /Users/fjacquet/Projects/kemp_exporter && git add config.yaml internal/config/config.go internal/config/config_test.go prometheus.yml README.md CONTRIBUTING.md CLAUDE.md docs/index.md docs/deployment/docker.md docs/deployment/systemd.md && git commit -m "$(cat <<'EOF'
feat(config)!: move the metrics port from 9447 to 9448

BREAKING CHANGE: the default metrics port is now 9448. kemp_exporter and
nsr_exporter both bound 9447 and could not run on the same host. nsr is the older
repo and keeps 9447; 9448 is the next free port after the family's 9438-9446
block. Operators pinning `server.port` in their own config.yaml are unaffected;
anyone relying on the default must update their scrape config and any published
port mapping.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_016k1BGLiKCXm3iQnfKdUaKv
EOF
)"
```

---

### Task 4: Dockerfiles — `alpine:latest`, port 9448, `HEALTHCHECK`

**Files:**
- Modify: `/Users/fjacquet/Projects/kemp_exporter/Dockerfile`
- Modify: `/Users/fjacquet/Projects/kemp_exporter/Dockerfile.goreleaser`

**Interfaces:**
- Consumes: `/livez` from Task 2, on port 9448 from Task 3.
- Produces: both images `EXPOSE 9448` and declare a `HEALTHCHECK` that busybox
  `wget`s `http://127.0.0.1:9448/livez`.

- [ ] **Step 1: Edit `Dockerfile`.** Replace line 14, `FROM alpine:3.22`, with:

```dockerfile
# Unpinned by family decision (see ADR 0009): all fifteen of Fred's exporter repos
# share `alpine:latest`. This is the one input in this build whose contents can
# change between two builds of the same commit -- the Go toolchain, the linters
# and every GitHub Action are pinned per ADR 0001. Uniformity across the family was
# chosen over reproducibility here; revisiting it is a family-wide decision.
FROM alpine:latest
```

  Then replace line 24, `EXPOSE 9447`, with:

```dockerfile
EXPOSE 9448

# Probes /livez, never /metrics: /livez reads no state and cannot block behind a
# slow collection cycle, whereas rendering the full exposition every 30s just to
# answer a healthcheck is pure waste.
#
# 127.0.0.1 and NOT localhost: busybox wget resolves localhost via ::1 first and
# this exporter binds IPv4 only, so a localhost-based check fails at runtime with
# connection refused -- while passing hadolint and `docker compose config`.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget --quiet --tries=1 --spider http://127.0.0.1:9448/livez || exit 1
```

- [ ] **Step 2: Edit `Dockerfile.goreleaser`.** Replace line 1,
  `FROM alpine:3.22`, with:

```dockerfile
# Unpinned by family decision (see ADR 0009): all fifteen of Fred's exporter repos
# share `alpine:latest`. Uniformity across the family was chosen over
# reproducibility here; revisiting it is a family-wide decision.
FROM alpine:latest
```

  Then replace line 10, `EXPOSE 9447`, with:

```dockerfile
EXPOSE 9448

# Probes /livez, never /metrics. 127.0.0.1 and NOT localhost: busybox wget
# resolves localhost via ::1 first and this exporter binds IPv4 only, so a
# localhost-based check fails at runtime while passing every static check.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget --quiet --tries=1 --spider http://127.0.0.1:9448/livez || exit 1
```

- [ ] **Step 3: Lint both.** Run:

```bash
cd /Users/fjacquet/Projects/kemp_exporter && docker run --rm -i hadolint/hadolint < Dockerfile ; echo "--- goreleaser ---" ; docker run --rm -i hadolint/hadolint < Dockerfile.goreleaser
```

  `DL3025` (shell-form CMD), `DL3007` (`alpine:latest` unpinned) and `DL3066` are
  **expected** — see Global Constraints. Any *other* finding is a real defect and
  must be fixed. Do not add `# hadolint ignore` lines.

- [ ] **Step 4: Sanity-check the port is gone.** Run:

```bash
cd /Users/fjacquet/Projects/kemp_exporter && grep -n "9447\|alpine:3" Dockerfile Dockerfile.goreleaser
```

  Expect no output at all.

- [ ] **Step 5: Commit.**

```bash
cd /Users/fjacquet/Projects/kemp_exporter && git add Dockerfile Dockerfile.goreleaser && git commit -m "$(cat <<'EOF'
feat(docker): HEALTHCHECK on /livez, alpine:latest, port 9448

Both images now declare a container healthcheck against /livez on 127.0.0.1 --
never localhost, which busybox wget resolves via ::1 first while the exporter
binds IPv4 only. The alpine:3.22 pin is dropped for alpine:latest to match all
fifteen family repos (ADR 0009).

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_016k1BGLiKCXm3iQnfKdUaKv
EOF
)"
```

---

### Task 5: Compose healthchecks, port 9448, and RUNTIME verification

This task is the one that catches the `localhost`/`::1` class of bug. It is not
complete until `docker inspect` prints `healthy` for an image built from each of
the two Dockerfiles.

**Files:**
- Modify: `/Users/fjacquet/Projects/kemp_exporter/docker-compose.yml`
- Modify: `/Users/fjacquet/Projects/kemp_exporter/docker-compose.ghcr.yml`

**Interfaces:**
- Consumes: the `HEALTHCHECK`-bearing images from Task 4.
- Produces: both compose files publish `9448:9448` and declare a matching
  `healthcheck:` on the `kemp_exporter` service.

- [ ] **Step 1: Edit `docker-compose.yml`.** In the `kemp_exporter` service,
  replace lines 28-29:

```yaml
    ports:
      - "9447:9447"
```

  with:

```yaml
    ports:
      - "9448:9448"
```

  and then, immediately after the `volumes:` block (after line 36,
  `      - ./config.yaml:/etc/kemp_exporter/config.yaml:ro`) and before
  `    restart: unless-stopped`, insert:

```yaml
    # Mirrors the image's own HEALTHCHECK exactly, timeout included: a 5s image /
    # 10s compose mismatch shipped across eight sibling repos and had to be
    # corrected in every one. 127.0.0.1, never localhost -- busybox wget tries ::1
    # first and this exporter binds IPv4 only.
    healthcheck:
      test: ["CMD", "wget", "--quiet", "--tries=1", "--spider", "http://127.0.0.1:9448/livez"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 10s
```

- [ ] **Step 2: Edit `docker-compose.ghcr.yml`.** In the `kemp_exporter` service,
  replace lines 21-22:

```yaml
    ports:
      - "9447:9447"
```

  with:

```yaml
    ports:
      - "9448:9448"
```

  and then, immediately after line 27
  (`      - ./config.yaml:/etc/kemp_exporter/config.yaml:ro`) and before
  `    restart: unless-stopped`, insert:

```yaml
    # Mirrors the image's own HEALTHCHECK exactly, timeout included. 127.0.0.1,
    # never localhost -- busybox wget tries ::1 first and this exporter binds
    # IPv4 only.
    healthcheck:
      test: ["CMD", "wget", "--quiet", "--tries=1", "--spider", "http://127.0.0.1:9448/livez"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 10s
```

- [ ] **Step 3: Validate both compose files.** Run:

```bash
cd /Users/fjacquet/Projects/kemp_exporter && docker compose -f docker-compose.yml config -q && docker compose -f docker-compose.ghcr.yml config -q && echo "compose OK"
```

  Remember: this passing proves nothing about the healthcheck actually working.
  Steps 4-7 are what prove that.

- [ ] **Step 4: Build and run the local `Dockerfile` image.** Run:

```bash
cd /Users/fjacquet/Projects/kemp_exporter && docker build -t kemp_exporter:healthcheck-verify .
```

```bash
cd /Users/fjacquet/Projects/kemp_exporter && docker run -d --name kemp_hc_local -p 9448:9448 \
  -e KEMP1_HOSTNAME=10.0.0.1 -e KEMP1_APIKEY=replace-me \
  kemp_exporter:healthcheck-verify
```

  The exporter will fail every collection cycle (there is no LoadMaster) — that is
  expected and is precisely the state the healthcheck must survive: `/livez`
  reports 200 regardless of backend reachability.

- [ ] **Step 5: Assert it reports healthy.** The `start-period` is 10s and the
  interval 30s, so allow up to ~45s. Run:

```bash
for i in $(seq 1 15); do
  s=$(docker inspect --format='{{.State.Health.Status}}' kemp_hc_local)
  echo "attempt $i: $s"
  [ "$s" = "healthy" ] && break
  sleep 5
done
docker inspect --format='{{.State.Health.Status}}' kemp_hc_local
```

  The final line **must** print `healthy`. If it prints `unhealthy`, read the probe
  output with:

```bash
docker inspect --format='{{json .State.Health.Log}}' kemp_hc_local
```

  `wget: can't connect to remote host` almost always means a `localhost` slipped
  in somewhere, or the port in the healthcheck does not match the port the process
  bound. Fix and re-verify; do not proceed on `unhealthy` or `starting`.

- [ ] **Step 6: Tear down and build the goreleaser image.** The goreleaser
  Dockerfile expects a pre-built Linux binary laid out per-platform. On Apple
  Silicon:

```bash
cd /Users/fjacquet/Projects/kemp_exporter && docker rm -f kemp_hc_local
```

```bash
cd /Users/fjacquet/Projects/kemp_exporter && mkdir -p linux/arm64 && \
  GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=healthcheck-verify" \
    -o linux/arm64/kemp_exporter . && \
  docker build -f Dockerfile.goreleaser --build-arg TARGETPLATFORM=linux/arm64 \
    -t kemp_exporter:goreleaser-verify .
```

  On an amd64 host, substitute `GOARCH=amd64` and
  `TARGETPLATFORM=linux/amd64` throughout, and `linux/amd64/` for the output
  directory. Omitting `TARGETPLATFORM`, or building an arm64 binary for an amd64
  platform arg, produces a container that exits immediately with
  `exec format error`.

- [ ] **Step 7: Run and assert the goreleaser image is healthy too.**

```bash
cd /Users/fjacquet/Projects/kemp_exporter && docker run -d --name kemp_hc_rel -p 9448:9448 \
  -e KEMP1_HOSTNAME=10.0.0.1 -e KEMP1_APIKEY=replace-me \
  kemp_exporter:goreleaser-verify
```

```bash
for i in $(seq 1 15); do
  s=$(docker inspect --format='{{.State.Health.Status}}' kemp_hc_rel)
  echo "attempt $i: $s"
  [ "$s" = "healthy" ] && break
  sleep 5
done
docker inspect --format='{{.State.Health.Status}}' kemp_hc_rel
```

  Must print `healthy`.

- [ ] **Step 8: Clean up the verification artifacts.**

```bash
cd /Users/fjacquet/Projects/kemp_exporter && docker rm -f kemp_hc_rel ; \
  docker rmi kemp_exporter:healthcheck-verify kemp_exporter:goreleaser-verify ; \
  rm -rf linux/
```

  Confirm `git status` shows no stray `linux/` directory before committing.

- [ ] **Step 9: Commit.**

```bash
cd /Users/fjacquet/Projects/kemp_exporter && git add docker-compose.yml docker-compose.ghcr.yml && git commit -m "$(cat <<'EOF'
feat(compose): healthcheck on /livez, publish port 9448

Mirrors each image's own HEALTHCHECK exactly, 5s timeout included. Verified by
building and running both the local and the goreleaser image and confirming
`docker inspect --format='{{.State.Health.Status}}'` reports healthy -- with no
LoadMaster reachable, which is the state /livez must be immune to.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_016k1BGLiKCXm3iQnfKdUaKv
EOF
)"
```

---

### Task 6: ADR, index row, mkdocs nav, CHANGELOG

**Files:**
- Create: `/Users/fjacquet/Projects/kemp_exporter/docs/adr/0009-always-200-probes-and-port-9448.md`
- Modify: `/Users/fjacquet/Projects/kemp_exporter/docs/adr/index.md`
- Modify: `/Users/fjacquet/Projects/kemp_exporter/mkdocs.yml`
- Modify: `/Users/fjacquet/Projects/kemp_exporter/CHANGELOG.md`

**Interfaces:**
- Consumes: the decisions implemented in Tasks 1-5.
- Produces: a MADR-format record reachable from both `docs/adr/index.md` and the
  MkDocs nav, plus release notes under `## [Unreleased]`.

- [ ] **Step 1: Confirm the ADR number.** Run:

```bash
cd /Users/fjacquet/Projects/kemp_exporter && ls docs/adr/
```

  Expect `0001`…`0008` plus `index.md`, making `0009` the next free number. **If
  the listing shows anything else, use the actual next free number and adjust every
  `0009` in this task accordingly** — including the filename, the ADR title, the
  index row, the nav entry and the two Dockerfile comments written in Task 4 (grep
  `grep -rn "ADR 0009" Dockerfile Dockerfile.goreleaser docs/`). A sibling repo
  shipped literal `ADR-000N` placeholders into committed Dockerfile comments by
  assuming instead of listing.

- [ ] **Step 2: Write the ADR.** Create
  `docs/adr/0009-always-200-probes-and-port-9448.md` with exactly this content:

```markdown
# 0009. Static liveness/readiness probes, an always-200 /health, and port 9448

- **Status:** accepted
- **Date:** 2026-08-01
- **Deciders:** Frederic Jacquet

## Context and problem statement

Three problems, all discovered together when the exporter family's standard was
audited against the repos it had actually been applied to.

First, `/health` returned `503` in two cases: before the first collection cycle
completed, and whenever the newest snapshot was older than two collection
intervals plus one timeout. That is honest reporting, but it makes `/health`
actively dangerous as an orchestrator probe target — and it is the only
health-shaped endpoint the exporter had, so it is the one operators wire up. A
Kubernetes liveness probe pointed at it restarts an exporter that is merely
waiting on its first cycle; a readiness probe depools one whose LoadMaster is
briefly unreachable. Neither condition is one the process can fix by dying, and a
restart resets the snapshot, making the starting case self-perpetuating under a
tight `initialDelaySeconds`.

Second, the images carried no `HEALTHCHECK` and the compose files no
`healthcheck:`, so Docker and Compose had no signal at all beyond "the process has
not exited".

Third, `kemp_exporter` and `nsr_exporter` both defaulted to port `9447`. The two
cannot run on the same host without one of them being reconfigured. The family's
`9438`–`9446` port block exists precisely to prevent this; neither repo was in the
family table when it was allocated, which is how the collision survived a release
of each.

## Considered options

- **Keep `/health` as the only endpoint, and document that probes must tolerate
  503.** Costs nothing to implement and pushes the problem onto every operator,
  who will get it wrong once each.
- **Add `/livez` and `/readyz` but leave `/health` returning 503.** Fixes the probe
  story for anyone who reads the docs, leaves a loaded gun for anyone who does not.
- **Add `/livez` and `/readyz` as always-200 static handlers, and make `/health`
  always answer 200 with the verdict in its body.** Probes get an endpoint that
  cannot fail; diagnostics keep every bit of information they had.
- For the port: **move `nsr_exporter` to 9448** (it is the older, more widely
  deployed repo) versus **move `kemp_exporter` to 9448** (it is at v0.1.0 with a
  far smaller install base).

## Decision outcome

Chosen: **static `/livez` + `/readyz`, an always-200 `/health`, a container
`HEALTHCHECK` on `/livez`, and `kemp_exporter` moves to port 9448.**

`/livez` and `/readyz` are both wired to one handler that reads no state
whatsoever — not the snapshot, not the collection loop, not `kemp_up`. It writes
`200 ok` and nothing else. A probe pointed at it can never be the reason a healthy
process is restarted or pulled from rotation, which is the only property a probe
endpoint actually needs.

`/health` keeps the snapshot-age logic verbatim and reports `starting: …`,
`stale: last collection Ns ago`, or `ok` in the body, always under a `200`. It
remains the endpoint for a human or a dashboard asking whether collection is
current. An endpoint that answers that question by refusing to answer is useless
exactly when it matters.

Neither probe touches `/metrics`. Rendering the full exposition every thirty
seconds to answer a healthcheck is needless load, and it can block behind a slow
collection cycle — the failure mode these endpoints exist to be immune to.

The `HEALTHCHECK` uses `http://127.0.0.1:9448/livez`, never `localhost`. Alpine's
busybox `wget` resolves `localhost` via `::1` first and this exporter binds IPv4
only, so a `localhost`-based check fails at runtime with connection refused while
passing both `hadolint` and `docker compose config`. The timeout is `5s` in the
image and in every compose file alike.

On the port: `nsr_exporter` is at v0.12.4 against this repo's v0.1.0, so
`kemp_exporter` is the one that moves. `9448` is the next free port after the
family's `9438`–`9446` block. This is a **breaking change** for anyone relying on
the default; operators who pin `server.port` in their own `config.yaml` are
unaffected.

The base image also drops its `alpine:3.22` pin for `alpine:latest`, matching all
fifteen repos in the family. This cuts against ADR 0001's supply-chain posture —
the Go toolchain, the linters and every GitHub Action are pinned, so
`alpine:latest` becomes the one input whose contents can change between two builds
of the same commit, which is exactly what the SBOM and provenance attestations
exist to pin down. Uniformity across fifteen repos was chosen over reproducibility
on three; revisiting it is a family-wide decision, not a per-repo one.

### Consequences

- Probes are safe to wire by default; `/health` is safe to wire by accident.
- Nothing in the exporter can now report unhealthy via HTTP status. Genuine
  backend failure is visible where it always was: `kemp_up`, the `/health` body,
  and the bundled Prometheus alert rules.
- Users upgrading must update their scrape configuration and any published port
  mapping from `9447` to `9448`, or pin `server.port: "9447"` in their config.
- The published image's contents can differ between two builds of the same commit,
  by the amount `alpine:latest` moved.
```

- [ ] **Step 3: Add the index row.** In `docs/adr/index.md`, append this line
  immediately after the `0008` row (line 17):

```markdown
| [0009](0009-always-200-probes-and-port-9448.md) | Static `/livez` + `/readyz`, an always-200 `/health`, container `HEALTHCHECK`, and the move to port 9448 | accepted |
```

- [ ] **Step 4: Add the mkdocs nav entry.** In `mkdocs.yml`, append this line
  immediately after the `0008 Config hot reload` entry, at the same indentation:

```yaml
      - 0009 Probes, always-200 health, port 9448: adr/0009-always-200-probes-and-port-9448.md
```

- [ ] **Step 5: Write the CHANGELOG entries.** In `CHANGELOG.md`, replace line 11
  — the bare `## [Unreleased]` heading — with:

```markdown
## [Unreleased]

### Breaking

- The default metrics port moves from `9447` to `9448`. `kemp_exporter` and
  `nsr_exporter` both defaulted to `9447` and could not run on the same host;
  `nsr_exporter` is the older repo and keeps it. Update your Prometheus scrape
  configuration and any published container port mapping, or pin
  `server.port: "9447"` in your own `config.yaml` to keep the old behaviour. See
  [ADR 0009](docs/adr/0009-always-200-probes-and-port-9448.md).

### Changed

- `/health` now always answers `200`. It previously returned `503` before the
  first collection cycle and whenever the snapshot went stale, which made it
  unsafe as an orchestrator probe target — a liveness probe would restart an
  exporter that was merely starting up. The starting/stale verdict is unchanged;
  it is now reported in the response body (`starting: …`, `stale: last collection
  Ns ago`, or `ok`) instead of the status code.
- The container base image drops its `alpine:3.22` pin for `alpine:latest`,
  matching the rest of the exporter family.

### Added

- `/livez` and `/readyz` endpoints, both always `200`, both reading no exporter
  state at all. These are the endpoints orchestrator probes should target;
  `/health` remains the diagnostic endpoint. Neither probes `/metrics`, which
  would render the full exposition on every tick and can block behind a slow
  collection cycle.
- `HEALTHCHECK` in both `Dockerfile` and `Dockerfile.goreleaser`, and a matching
  `healthcheck:` in `docker-compose.yml` and `docker-compose.ghcr.yml`, all
  probing `http://127.0.0.1:9448/livez` on a 30s interval with a 5s timeout.
```

- [ ] **Step 6: Build the docs site.** Run:

```bash
cd /Users/fjacquet/Projects/kemp_exporter && uvx --with mkdocs-material --with pymdown-extensions mkdocs build --strict --site-dir site
```

  `--strict` fails on a `nav:` entry pointing at a missing file, and on broken
  internal links. A docs file *absent* from the nav is only an INFO notice and
  does not fail the build — add the nav entry for discoverability, but do not
  expect `--strict` to catch a missing one.

- [ ] **Step 7: Commit.**

```bash
cd /Users/fjacquet/Projects/kemp_exporter && git add docs/adr/0009-always-200-probes-and-port-9448.md docs/adr/index.md mkdocs.yml CHANGELOG.md && git commit -m "$(cat <<'EOF'
docs: record ADR 0009 and the unreleased breaking/changed/added entries

Covers the always-200 /health, the new /livez and /readyz probes, the container
HEALTHCHECK, the alpine:latest base, and the breaking move from port 9447 to 9448.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_016k1BGLiKCXm3iQnfKdUaKv
EOF
)"
```

---

### Task 7: Documentation sweep for claims the change falsifies

Every repo in the sibling Alpine effort needed a post-review fix wave for exactly
this: pages still asserting things the change made false. This task exists to
prevent that here.

**Files:**
- Modify: whichever of `README.md`, `docs/index.md`, `docs/deployment/docker.md`,
  `docs/deployment/systemd.md` the greps below implicate.

**Interfaces:**
- Consumes: everything from Tasks 1-6.
- Produces: no user-facing document claims `/health` returns 503, references
  `9447`, or pins Alpine 3.22; the probe endpoints are documented where operators
  will look for them.

- [ ] **Step 1: Grep for falsified claims.** Run:

```bash
cd /Users/fjacquet/Projects/kemp_exporter && git grep -n -i "9447\|503\|alpine:3\|service unavailable\|unhealthy" -- README.md CONTRIBUTING.md CLAUDE.md docs/ ':!docs/superpowers/'
```

  Every hit outside `docs/adr/` (where ADR 0009 legitimately *describes* the old
  503 behaviour, and the historical ADRs are records) must be reviewed and fixed.
  Expect zero `9447` hits after Task 3.

- [ ] **Step 2: Document the endpoints in the README.** In `README.md`, insert
  this section immediately after the `## Container image` block (after line 34,
  the closing ```` ``` ````) and before `## Configuration`:

```markdown
## Health and probe endpoints

| Path | Always 200? | Use it for |
|---|---|---|
| `/livez` | yes | Liveness probes. Reads no exporter state; cannot fail. |
| `/readyz` | yes | Readiness probes. Same handler as `/livez`. |
| `/health` | yes | Diagnostics. The body reports `ok`, `starting: …`, or `stale: last collection Ns ago`. |

Point orchestrator probes at `/livez` and `/readyz`, never at `/metrics` —
rendering the full exposition on every probe tick is needless load and can block
behind a slow collection cycle. `/health` never returns a failure status: whether a
LoadMaster is actually reachable is answered by the `kemp_up` metric and by the
`/health` body, not by an HTTP status code that would get a healthy process
restarted. See [ADR 0009](docs/adr/0009-always-200-probes-and-port-9448.md).
```

- [ ] **Step 3: Document the container healthcheck.** In
  `docs/deployment/docker.md`, insert this section immediately after the
  `## Standalone container` code block (after line 54, the closing ```` ``` ````)
  and before `## GHCR variant (published image, no local build)`:

```markdown
## Container healthcheck

Both images declare a `HEALTHCHECK` that probes `http://127.0.0.1:9448/livez`
every 30s (5s timeout, 10s start period, 3 retries), and both compose files
declare the identical check. `docker ps` therefore reports the exporter's health
directly:

```bash
docker inspect --format='{{.State.Health.Status}}' kemp_exporter
```

`/livez` reads no exporter state, so a `healthy` container means the process is
serving HTTP — **not** that any LoadMaster is reachable. That question is answered
by `kemp_up` and by the `/health` response body. The check deliberately uses
`127.0.0.1` rather than `localhost`: busybox `wget` resolves `localhost` via `::1`
first and the exporter binds IPv4 only.
```

- [ ] **Step 4: Document the probe endpoints for systemd users.** In
  `docs/deployment/systemd.md`, immediately after the `curl` code block that ends
  on line 44, insert:

```markdown
The probe endpoints answer on the same port and never fail:

```bash
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:9448/livez   # 200
curl -s http://localhost:9448/health                                    # ok | starting: … | stale: …
```
```

- [ ] **Step 5: Rebuild the docs and re-grep.**

```bash
cd /Users/fjacquet/Projects/kemp_exporter && uvx --with mkdocs-material --with pymdown-extensions mkdocs build --strict --site-dir site && git grep -n 9447 -- ':!docs/superpowers/'
```

  The grep must produce **no output**.

- [ ] **Step 6: Commit.**

```bash
cd /Users/fjacquet/Projects/kemp_exporter && git add README.md docs/deployment/docker.md docs/deployment/systemd.md && git commit -m "$(cat <<'EOF'
docs: document /livez, /readyz, the always-200 /health, and the healthcheck

Covers the README endpoint table, the Docker healthcheck section, and the systemd
probe examples. Swept for claims the change falsifies: no user-facing page still
references port 9447 or a 503 from /health.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_016k1BGLiKCXm3iQnfKdUaKv
EOF
)"
```

---

### Task 8: Full gate

**Files:** none modified unless the gate finds something.

**Interfaces:**
- Consumes: the complete change set.
- Produces: a green `make ci`.

- [ ] **Step 1: Run the full CI gate.**

```bash
cd /Users/fjacquet/Projects/kemp_exporter && make ci
```

  This runs `golangci-lint run`, `go test -race -coverprofile`, `go build`,
  `govulncheck` and `semgrep scan --config auto`. All five must pass. Fix any
  finding by restructuring — inline `//nolint`, `// nosemgrep` and `//#nosec`
  suppressions are forbidden family-wide and the Definition of Done greps for
  them.

- [ ] **Step 2: Confirm no suppressions crept in.**

```bash
cd /Users/fjacquet/Projects/kemp_exporter && git grep -n "nolint\|nosemgrep\|#nosec\|hadolint ignore" -- ':!docs/superpowers/'
```

  Must produce no output.

- [ ] **Step 3: Confirm the working tree is clean and review the full diff.**

```bash
cd /Users/fjacquet/Projects/kemp_exporter && git status --short && git log --oneline main..HEAD
```

  `git status --short` must be empty (no stray `linux/`, no `site/` — `site/` is
  gitignored). The log should show six commits from Tasks 1-7.

- [ ] **Step 4: Fix and re-run if anything failed.** Do not report completion on a
  red gate.

---

## Self-Review

Before declaring this work complete, confirm each of the following by running the
command and reading its output — not by recalling that you did it.

- [ ] `go test ./...` is green, including `TestHealthHandlerUsesSnapshotAge`
  asserting **200 in all three cases** and `TestProbeEndpointsAlwaysOKBeforeFirstCollection`.
- [ ] `make ci` is green end to end.
- [ ] `git grep -n 9447 -- ':!docs/superpowers/'` produces **no output**. The two
  historical documents under `docs/superpowers/` still contain `9447` and were
  correctly left alone.
- [ ] `grep -n alpine Dockerfile Dockerfile.goreleaser` shows `alpine:latest` in
  both, no `3.22`.
- [ ] `grep -n HEALTHCHECK -A1 Dockerfile Dockerfile.goreleaser` shows
  `127.0.0.1:9448/livez` in both, `--timeout=5s` in both, and **no** `localhost`.
- [ ] `grep -n -A5 healthcheck docker-compose.yml docker-compose.ghcr.yml` shows
  `127.0.0.1:9448/livez` and `timeout: 5s` in both — matching the Dockerfiles
  exactly.
- [ ] Both images were **built and run**, and
  `docker inspect --format='{{.State.Health.Status}}'` printed `healthy` for each.
  This is not satisfied by reading the Dockerfile.
- [ ] `hadolint` on both Dockerfiles reports only `DL3025`, `DL3007` and/or
  `DL3066`, and no `# hadolint ignore` comment was added.
- [ ] `ls docs/adr/` shows `0009-always-200-probes-and-port-9448.md`, its number
  was confirmed by listing rather than assumed, and no literal `000N` placeholder
  survives anywhere: `git grep -n "000N"` produces no output.
- [ ] `docs/adr/index.md` has a `0009` row, and `mkdocs.yml`'s `nav:` has a `0009`
  entry.
- [ ] `mkdocs build --strict` is clean.
- [ ] `CHANGELOG.md` has `Breaking`, `Changed` and `Added` subsections under
  `## [Unreleased]`, and the `Breaking` entry names the port move explicitly.
- [ ] `git grep -n "nolint\|nosemgrep\|#nosec\|hadolint ignore" -- ':!docs/superpowers/'`
  produces no output.
- [ ] `git status --short` is empty — no leftover `linux/` build directory from the
  goreleaser verification.
