# Security Policy

## Reporting a Vulnerability

**Please do not open a public GitHub issue for security vulnerabilities.**

Report vulnerabilities through one of these private channels:

1. **GitHub private security advisory** (preferred):
   <https://github.com/fjacquet/kemp_exporter/security/advisories/new>

2. **Email**: Contact the maintainer at the email address listed in the GitHub
   profile (<https://github.com/fjacquet>).

Include in your report:

- A description of the vulnerability and its potential impact.
- Steps to reproduce or a proof-of-concept if available.
- The version(s) affected.

You will receive an acknowledgement within 72 hours and a resolution timeline
once the issue is assessed. Please allow time for a fix to be prepared before
public disclosure.

## Supported Versions

| Version | Supported |
|---------|-----------|
| 0.1.x   | Yes       |

## Security Notes

### Credentials

LoadMaster credentials are supplied to the exporter in one of two ways:

- **Environment-variable interpolation** in the config file: `apiKey:
  "${KEMP1_APIKEY}"` (XML transport) or `password: "${KEMP1_PASSWORD}"` (JSON
  transport). Set the variable in the process environment, an `EnvironmentFile`
  (systemd), or a secrets manager; never write the literal value into
  `config.yaml`.
- **`passwordFile`**: a path to a file containing the password, readable only by
  the exporter process user.

Never commit credentials to version control. The `.gitignore` excludes common
secret file patterns (including `.env`), but review your config before
committing.

The exporter's own outbound HTTP client never logs full request URLs or bodies
by default: `resty`'s built-in logger (which would otherwise print the full URL,
apikey query parameter included, on a transport failure) is replaced with a
no-op. `--trace` opts in to logging full response bodies for debugging; it never
logs request headers, and it skips authentication-endpoint responses
specifically to avoid ever writing a session token to a log file. Treat any
`--trace` output as sensitive.

### TLS Verification

The `insecureSkipVerify` config option disables TLS certificate verification for
LoadMasters using self-signed certificates. This is an **operator opt-in**
setting, per-target and defaulting to `false` — never hardcoded to `true`
anywhere in this codebase (see
[ADR 0003](docs/adr/0003-hand-rolled-resty-client.md), which documents this as
the specific defect this project avoided by not depending on
`giantswarm/kemp-client`, whose `kemp.go` hardcodes `InsecureSkipVerify: true`
with no opt-out). Use it only for lab or air-gapped environments where a trusted
certificate cannot be issued. In production, provide a valid certificate and
leave this option unset (or `false`). The minimum negotiated TLS version is 1.2.

### Exposed Endpoints

The exporter registers five HTTP endpoints:

- `/metrics` (path configurable via `server.uri`) — Prometheus metrics
  (read-only, no write path).
- `/livez` and `/readyz` — static, always-`200` liveness/readiness probes.
  They read no exporter state and cannot fail; point orchestrator probes at
  these, not at `/health` (see
  [ADR 0009](docs/adr/0009-always-200-probes-and-port-9448.md)).
- `/health` — human/dashboard diagnostic endpoint. Always answers `200`, with
  the collection-freshness verdict (`ok`, `starting: …`, `stale: …`) in the
  body; no sensitive data.
- `/` — landing page linking to the metrics endpoint.

There is no authentication on these endpoints by default. If your environment
requires it, place the exporter behind a reverse proxy or use Prometheus's
built-in TLS/auth configuration to scrape via HTTPS with a bearer token.

The exporter is designed to hold **read-only** API credentials to the
LoadMaster and performs no write operations against the appliance.

### Supply chain

Release artifacts (`make release-snapshot` / GoReleaser) include a CycloneDX
SBOM per release, generated locally and independently reproducible. CI runs
`govulncheck` and `golangci-lint` on every push/PR, and Semgrep scanning is wired
via `make security`, which `make ci` runs as a blocking gate (production code
only — Semgrep's defaults skip `*_test.go`). See
[ADR 0001](docs/adr/0001-supply-chain-and-release-hardening.md) for exactly
what this repository enforces directly versus what it inherits from the shared
`fjacquet/ci` workflows.
