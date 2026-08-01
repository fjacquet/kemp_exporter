# Docker deployment

## Compose quickstart (build from source)

```bash
cp .env.example .env    # then edit KEMP1_HOSTNAME / KEMP1_APIKEY
docker compose up --build
```

This brings up three services:

| Service | Image | Port | Purpose |
|---|---|---|---|
| `kemp_exporter` | built from this repo's `Dockerfile` | `9448` | `/metrics` |
| `prometheus` | `prom/prometheus:latest` | `9090` | scrapes the exporter, loads `deploy/prometheus/kemp.rules.yml` |
| `grafana` | `grafana/grafana:latest` | `3000` | auto-provisioned datasource + `Kemp LoadMaster — Overview` dashboard |

Grafana logs in as `admin` / `${GRAFANA_ADMIN_PASSWORD:-admin}` (change the default
for anything beyond a local demo). `docker compose down` (or `make demo-down`) tears
the stack down.

**There is no LoadMaster appliance bundled with this stack.** `config.yaml` (mounted
read-only into the exporter container) ships with an obvious placeholder
host/API key sourced from `KEMP1_HOSTNAME`/`KEMP1_APIKEY`. Until those point at a
real, reachable LoadMaster, the exporter starts fine but every collection cycle
fails: `kemp_up` reads `0` and there are no virtual-service or real-server series at
all. `kemp_exporter_build_info` is still present, and Prometheus/Grafana both come up
healthy — that is the expected, honest state of a quickstart with nothing real to
scrape, not a bug.

## `.env` variables

| Variable | Default | Meaning |
|---|---|---|
| `KEMP1_HOSTNAME` | `10.0.0.1` | LoadMaster hostname/IP for the `lm-prod-01` system entry in `config.yaml`. |
| `KEMP1_APIKEY` | `replace-me` | XML-transport static API key, ideally scoped read-only. |
| `KEMP1_USERNAME` / `KEMP1_PASSWORD` | unset | Optional JSON-transport session credentials, if the appliance runs firmware 7.2.50+ with session management enabled. |
| `KEMP1_SKIP_CERTIFICATE` | unset (`false`) | Only consumed if `config.yaml`'s `insecureSkipVerify` references it; leave unset for TLS verification on. |
| `GRAFANA_ADMIN_PASSWORD` | `admin` | Grafana admin password for the bundled Grafana service. |

`config.yaml` is the source of truth for the target list — for multiple LoadMasters,
add one entry per appliance under `systems`; `.env` only supplies the placeholder
single-target quickstart's credentials.

## Standalone container

```bash
make docker
docker run -p 9448:9448 \
  -e KEMP1_HOSTNAME='lm-prod-01.example.com' \
  -e KEMP1_APIKEY='your-read-only-api-key' \
  kemp_exporter:dev
```

## GHCR variant (published image, no local build)

```bash
cp .env.example .env    # then edit KEMP1_HOSTNAME / KEMP1_APIKEY
docker compose -f docker-compose.ghcr.yml up
```

Pin a specific version with `KEMP_TAG` (defaults to `:latest`):

```bash
KEMP_TAG=0.1.0 docker compose -f docker-compose.ghcr.yml up
```

Refresh a running GHCR-based stack with:

```bash
docker compose -f docker-compose.ghcr.yml pull
```

The same "no bundled LoadMaster" caveat applies here as to the build-from-source
compose file.
