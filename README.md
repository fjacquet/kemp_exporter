# kemp_exporter

[![CI](https://github.com/fjacquet/kemp_exporter/actions/workflows/ci.yml/badge.svg)](https://github.com/fjacquet/kemp_exporter/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/fjacquet/kemp_exporter?include_prereleases&sort=semver)](https://github.com/fjacquet/kemp_exporter/releases/latest)
[![Go Report Card](https://goreportcard.com/badge/github.com/fjacquet/kemp_exporter)](https://goreportcard.com/report/github.com/fjacquet/kemp_exporter)
[![Go Version](https://img.shields.io/github/go-mod/go-version/fjacquet/kemp_exporter)](go.mod)
[![License](https://img.shields.io/github/license/fjacquet/kemp_exporter)](LICENSE)

A Go Prometheus + OTLP exporter for **Progress Kemp LoadMaster** appliances. One process
monitors many LoadMasters, polls each on an interval, and serves metrics at `/metrics`
(Prometheus) and/or pushes them via OTLP.

## Quick start

```bash
make cli
export KEMP1_HOSTNAME='lm-prod-01.example.com'
export KEMP1_APIKEY='your-read-only-api-key'
./bin/kemp_exporter --config config.yaml
# metrics: http://localhost:9447/metrics
```

## Container image

```bash
make docker
docker run -p 9447:9447 \
  -e KEMP1_HOSTNAME='lm-prod-01.example.com' \
  -e KEMP1_APIKEY='your-read-only-api-key' \
  kemp_exporter:dev
```

## Configuration

See `config.yaml` for the full schema: server, collection interval/timeout/concurrency,
optional OTLP export, and one entry per LoadMaster under `systems`. Secrets
(`apiKey`, `password`) accept `${ENV_VAR}` references and are resolved at load time; the
config also hot-reloads on `SIGHUP` or file change.

## License

Apache-2.0.
