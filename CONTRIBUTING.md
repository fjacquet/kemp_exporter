# Contributing to kemp_exporter

Thank you for contributing! This guide covers prerequisites, local development
workflow, invariants you must not break, and the PR checklist.

## Prerequisites

| Tool | Version | How to get |
|------|---------|------------|
| Go | see `go.mod` | <https://go.dev/dl/> |
| golangci-lint | pinned in Makefile | `make tools` |
| govulncheck | pinned in Makefile | `make tools` |
| goreleaser | pinned in Makefile | `make tools` |
| semgrep | latest | `pip install semgrep` or `brew install semgrep` (invoked via `uvx` by `make security`) |
| mkdocs + mkdocs-material | latest | invoked via `uvx` by `make docs` |
| Docker + Compose | any recent | for the demo stack target |

Run `make tools` to install the pinned Go tooling (golangci-lint, goreleaser,
govulncheck) into your `$GOPATH/bin`; `make tools-sbom` additionally installs
`cyclonedx-gomod` for local SBOM generation.

## Local Development Workflow

```bash
# Build the binary
make cli

# Run unit tests
make test

# Full CI gate (lint + test-race + build + govulncheck)
make ci

# Local convenience gate (fmt-check + vet + test + build)
make sure
```

Individual targets:

| Target | What it does |
|--------|-------------|
| `make fmt-check` | Fail if gofmt would change anything (used by CI) |
| `make format` / `make fmt` | `golangci-lint fmt` |
| `make vet` | `go vet ./...` |
| `make lint` | `golangci-lint run` |
| `make test` | `go test -race -coverprofile=... ./...` |
| `make test-race` | Race-detector test + coverage summary |
| `make vuln` | `govulncheck ./...` |
| `make ci` | lint + test + build + vuln |
| `make sure` | fmt-check + vet + test + build |
| `make docs` | `mkdocs build --strict` |
| `make demo` | `docker compose up --build` (exporter + Prometheus + Grafana) |

## Test-Driven Development

New features and bug fixes must include tests. The project follows a TDD
approach:

1. Write a failing test that describes the expected behaviour.
2. Write the minimum code to make it pass.
3. Refactor.

Run the race detector locally before opening a PR: `make test-race`.

## Load-bearing invariants

Two standing tests guard the design decisions this project depends on — do not
weaken or delete them without an accompanying ADR:

1. **Transport parity** (`internal/kemp/transport_parity_test.go`) — the XML and
   JSON transports must decode identical fixtures to an identical `Statistics`.
   This is the guard behind the single-model design in
   [ADR 0004](docs/adr/0004-dual-transport-single-model.md): if you add a new
   field, wire it into both `internal/models/statistics.go`'s `xml:`/`json:`
   tags, not just one.
2. **Dashboard-metric consistency** (`internal/dashboards/dashboards_test.go`) —
   every metric a Grafana dashboard queries must exist in the exporter's own
   `knownMetrics` set, and no per-second gauge may ever be wrapped in `rate()`.
   Update `knownMetrics` (and `docs/metrics.md`) whenever you add or rename a
   metric.

Other invariants enforced throughout the codebase (see `CLAUDE.md` for the full
list): absent-not-zero (never emit a fabricated `0`), one label-key set per
metric name (`internal/kemp/metrics.go`'s shared constructors), no retry on 4xx
responses, and no inline lint/semgrep suppressions.

## Semgrep Gate

The CI pipeline runs Semgrep for security checks (`make security`), and
`make ci` runs it too — it is a BLOCKING gate: the target no longer ends in
`|| true`, and a finding fails the build. Note the scope: Semgrep's default
ignore rules skip `*_test.go`, so this gates production code only.
This project policy prohibits inline suppressions (`//nolint` for the linter,
`// nosemgrep` for Semgrep). If a finding is a false positive, address it in the
project-level `.golangci.yml` or Semgrep config rather than suppressing inline.

## Commit Style

Use [Conventional Commits](https://www.conventionalcommits.org/) for commit
messages:

```
<type>(<scope>): <short summary>

[optional body]
[optional footer]
```

Common types: `feat`, `fix`, `docs`, `test`, `refactor`, `ci`, `chore`.

Examples:
```
feat(kemp): add SubVS statistics collection
fix(transport): handle empty listvs response without panic
docs(adr): add ADR 0009 for new design decision
```

## Running the Docker Compose Stack

```bash
cp .env.example .env    # then edit KEMP1_HOSTNAME / KEMP1_APIKEY

# Build-from-source stack (exporter + Prometheus + Grafana)
docker compose up --build

# Or use the GHCR-published image without building
docker compose -f docker-compose.ghcr.yml up
```

The exporter metrics are at <http://localhost:9447/metrics>; Grafana at
<http://localhost:3000> (`admin` / `admin` by default — see `docs/deployment/docker.md`).

## Pull Request Checklist

Before opening a PR, confirm all of the following:

- [ ] `make ci` passes locally (lint, test-race, build, govulncheck)
- [ ] New or changed metrics are wired through both transports (parity invariant)
      and documented in `docs/metrics.md` with a confirmed/unconfirmed marker
- [ ] New or changed dashboard-visible metrics are added to
      `internal/dashboards/dashboards_test.go`'s `knownMetrics` map
- [ ] No inline `//nolint` or `// nosemgrep` suppressions added
- [ ] Commit messages follow Conventional Commits style
- [ ] If the change is architecturally significant, a new ADR has been added
      under `docs/adr/` and referenced in `docs/adr/index.md`
- [ ] `make docs` (`mkdocs build --strict`) passes if docs were changed
