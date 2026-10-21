# Every target builds without cgo, so each binary is static. Only `race`
# turns cgo on, because the race detector needs it.
export CGO_ENABLED := 0

GO            ?= go
BUF           ?= buf
GOLANGCI_LINT ?= $(shell $(GO) env GOPATH)/bin/golangci-lint
VERSION       ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS       := -s -w -X github.com/iantanwx/loop/internal/cli.Version=$(VERSION)
PLATFORMS     := linux/amd64 linux/arm64 darwin/arm64
DESCRIPTOR    := gen/loop.binpb

# `make gates` is the definition of done. A later unit adds its gate here.
GATES := lint test race proto-check

.PHONY: build build-all test race lint proto proto-check gates $(PLATFORMS)

build:
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/loop ./cmd/loop

build-all: $(PLATFORMS)

$(PLATFORMS):
	GOOS=$(word 1,$(subst /, ,$@)) GOARCH=$(word 2,$(subst /, ,$@)) \
		$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/loop-$(subst /,-,$@) ./cmd/loop

test:
	$(GO) test ./...

race:
	CGO_ENABLED=1 $(GO) test -race ./...

lint:
	$(GOLANGCI_LINT) run ./...
	$(GO) vet ./...

proto:
	$(BUF) lint
	$(BUF) generate
	$(BUF) build --exclude-source-info -o $(DESCRIPTOR)

# Generate into a scratch directory and compare, so drift, a stale file
# and a missing file all fail whether or not git tracks gen/ yet.
proto-check:
	$(BUF) lint
	@tmp=$$(mktemp -d) && trap 'rm -rf "$$tmp"' EXIT && \
		$(BUF) generate -o "$$tmp" && \
		$(BUF) build --exclude-source-info -o "$$tmp/$(DESCRIPTOR)" && \
		diff -r -x embed.go gen "$$tmp/gen" || \
		{ echo "gen/ differs from a fresh buf generate; run make proto" >&2; exit 1; }

gates: $(GATES)
