# Build stage
# Patch-pinned, per the project's supply-chain constraint and to match go.mod's
# `go 1.26.5`: a floating 1.26 tag silently changes the toolchain under a
# reproducible build.
FROM docker.io/library/golang:1.27rc1-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/kemp_exporter .

# Runtime stage
# Unpinned by family decision (see ADR 0009): all fifteen of Fred's exporter repos
# share `alpine:latest`. This is the one input in this build whose contents can
# change between two builds of the same commit -- the Go toolchain, the linters
# and every GitHub Action are pinned per ADR 0001. Uniformity across the family was
# chosen over reproducibility here; revisiting it is a family-wide decision.
FROM docker.io/library/alpine:latest
# Copy the CA bundle from the builder rather than `apk add ca-certificates`:
# apk fetches the index from the Alpine CDN over TLS, which fails behind a corporate
# MITM proxy because the bare alpine image has no CA bundle yet to validate the proxy
# certificate. adduser and mkdir are busybox builtins and need no network.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
RUN adduser -D -u 10001 kemp
COPY --from=builder /out/kemp_exporter /usr/local/bin/kemp_exporter
COPY config.yaml /etc/kemp_exporter/config.yaml
USER 10001
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
ENTRYPOINT ["/usr/local/bin/kemp_exporter"]
CMD ["--config", "/etc/kemp_exporter/config.yaml"]
