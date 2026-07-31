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
