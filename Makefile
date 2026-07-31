BIN  := bin/kemp_exporter
DIST ?= dist
COVER ?= coverage.out

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

# Pinned tool versions (installed by `make tools`).
GOLANGCI_VERSION     ?= v2.12.2
GORELEASER_VERSION   ?= v2.16.0
CYCLONEDX_GOMOD_VERSION ?= latest

.PHONY: all clean clean-dist install tools tools-sbom \
        lint format fmt fmt-check vet test test-race build cli vuln sbom \
        security docs coverage-upload release release-snapshot \
        sure ci demo demo-ghcr demo-down docker run-cli test-coverage

.DEFAULT_GOAL := all

all: clean lint test build

# --- tooling ---

# Install pinned dev/CI tooling into $GOBIN / $GOPATH/bin.
tools:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)
	go install golang.org/x/vuln/cmd/govulncheck@latest
	go install github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION)

# Just the SBOM generator — used by the release pipeline (GoReleaser sboms hook).
tools-sbom:
	go install github.com/CycloneDX/cyclonedx-gomod/cmd/cyclonedx-gomod@$(CYCLONEDX_GOMOD_VERSION)

# --- quality gates ---

install:
	go mod download

fmt-check:
	@test -z "$$(gofmt -l . | tee /dev/stderr)"

format:
	golangci-lint fmt

fmt: format

vet:
	go vet ./...

lint:
	golangci-lint run --timeout=5m

test:
	go test -race -coverprofile=$(COVER) -covermode=atomic ./...

test-race:
	go test -race -cover ./...

test-coverage: test
	go tool cover -html=$(COVER) -o coverage.html
	@echo "coverage report: coverage.html"

build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) .

docker:
	docker build -t kemp_exporter:$(VERSION) .

run-cli: build
	$(BIN) --config config.yaml --once --debug

cli: build

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

# --- artifacts ---

sbom:
	mkdir -p $(DIST)
	go run github.com/CycloneDX/cyclonedx-gomod/cmd/cyclonedx-gomod@latest mod -json -output $(DIST)/sbom.cdx.json

security:  # advisory: reports findings but never blocks the build (CodeQL/osv are the blocking gates)
	uvx semgrep scan --config auto --skip-unknown-extensions || true

docs:
	uvx --with mkdocs-material --with pymdown-extensions mkdocs build --strict --site-dir site

coverage-upload:
	uvx --from codecov-cli codecov upload-process --file $(COVER) || true

release: tools-sbom
	goreleaser release --clean

release-snapshot: tools-sbom
	goreleaser release --snapshot --clean
	@echo "release artifacts in $(DIST)/"

# --- aggregate gates ---

# Local convenience gate.
sure: fmt-check vet test build

# Aggregate gate run by CI: lint + test + build + vuln.
ci: lint test build vuln

# --- demo stacks ---

demo:
	docker compose up --build

demo-ghcr:
	docker compose -f docker-compose.ghcr.yml up

demo-down:
	docker compose down --remove-orphans
	docker compose -f docker-compose.ghcr.yml down --remove-orphans

# --- clean ---

clean-dist:
	rm -rf $(DIST)

clean: clean-dist
	rm -f $(BIN) site $(COVER) *.sarif
