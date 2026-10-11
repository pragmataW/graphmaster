# graphmaster build entry points. cgo is required (tree-sitter, sqlite).

GO       ?= go
TAGS     := sqlite_fts5 sqlite_omit_load_extension
PKG      := github.com/pragmataW/graphmaster
BIN      ?= graphmaster
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -s -w \
	-X $(PKG)/internal/version.Version=$(VERSION) \
	-X $(PKG)/internal/version.Commit=$(COMMIT) \
	-X $(PKG)/internal/version.Date=$(DATE)

export CGO_ENABLED := 1

.PHONY: all build test test-short vet lint tidy clean \
	release-check release-snapshot verify-dist test-install lint-sh \
	bench bench-k8s perf-gates

all: build

build:
	$(GO) build -tags "$(TAGS)" -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/graphmaster

test:
	$(GO) test -race -tags "$(TAGS)" ./...

test-short:
	$(GO) test -short -tags "$(TAGS)" ./...

vet:
	$(GO) vet -tags "$(TAGS)" ./...

lint:
	golangci-lint run --build-tags "$(TAGS)" ./...

tidy:
	$(GO) mod tidy
	$(GO) mod verify

clean:
	rm -f $(BIN)
	rm -rf dist

# --- release ---------------------------------------------------------------
# Snapshot builds need goreleaser v2 and zig (brew install goreleaser zig);
# the SBOM step is skipped when syft is not installed. Nothing is published
# or signed locally: tagged releases run in .github/workflows/release.yml.

comma := ,
SNAPSHOT_SKIP ?= publish,sign$(if $(shell command -v syft 2>/dev/null),,$(comma)sbom)

release-check:
	goreleaser check

release-snapshot:
	goreleaser release --snapshot --clean --skip=$(SNAPSHOT_SKIP)
	sh scripts/verify-dist.sh dist

verify-dist:
	sh scripts/verify-dist.sh dist

# install.sh against fake file:// releases (and dist/ when present).
test-install: lint-sh
	sh scripts/install_test.sh

lint-sh:
	@if command -v shellcheck >/dev/null 2>&1; then \
		shellcheck -s sh install.sh scripts/*.sh; \
	else \
		echo "shellcheck not installed; skipping"; \
	fi

# --- performance -----------------------------------------------------------
# bench runs the synthetic gates (bench/synth.sh) into $(BENCH_OUT);
# perf-gates also checks the results against bench/thresholds.json with
# bench/check.sh, adding the Kubernetes run when $(BENCH_OUT)/k8s.jsonl exists.
# A BENCH_GATES subset is checked with -partial, so skipped gates do not fail.

BENCH_OUT    ?= bench-out
BENCH_GATES  ?=
PERF_PROFILE ?= plan

bench:
	OUT=$(BENCH_OUT) bench/synth.sh $(BENCH_GATES)

bench-k8s: build
	GRAPHMASTER_BIN=$(CURDIR)/$(BIN) RESULTS=$(BENCH_OUT)/k8s.jsonl bench/k8s.sh

perf-gates:
	@mkdir -p $(BENCH_OUT)
	@rc=0; \
	OUT=$(BENCH_OUT) bench/synth.sh $(BENCH_GATES) || rc=1; \
	set -- ; [ -f $(BENCH_OUT)/k8s.jsonl ] && set -- -k8s $(BENCH_OUT)/k8s.jsonl; \
	for f in $(BENCH_OUT)/bench-*.txt; do [ -f "$$f" ] && set -- "$$@" -bench "$$f"; done; \
	if [ $$# -gt 0 ]; then \
		bench/check.sh -profile $(PERF_PROFILE) $(if $(BENCH_GATES),-partial) "$$@" || rc=1; \
	fi; \
	exit $$rc
