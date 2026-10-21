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

.PHONY: build build-all test race lint proto proto-check gates beads-contract beads-contract-race orca-contract $(PLATFORMS)

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

# Tagged files are skipped by an untagged run, so lint and vet them too.
LINT_TAGS := beads orca

lint:
	$(GOLANGCI_LINT) run ./...
	$(GOLANGCI_LINT) run --build-tags '$(LINT_TAGS)' ./...
	$(GO) vet ./...
	$(GO) vet -tags '$(LINT_TAGS)' ./...

# The beads contract runs the pinned bd and dolt (LOOP_BD, LOOP_DOLT). It
# joins GATES once release provisioning (U16) installs them in CI.
beads-contract:
	$(GO) test -tags beads -count=1 ./internal/beadsx/...

beads-contract-race:
	CGO_ENABLED=1 $(GO) test -race -tags beads -count=1 ./internal/beadsx/...

# The orca contract starts real agents on this host, which spends harness
# quota and creates worktrees, so it runs only by hand. It needs
# LOOP_ORCA_ROOT, LOOP_ORCA_REPO and LOOP_ORCA_MODEL.
orca-contract:
	$(GO) test -tags orca -run TestOrcaContract -v -timeout 60m ./internal/agenthost/orca/

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
