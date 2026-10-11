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

.PHONY: all build test test-short vet lint tidy clean

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
