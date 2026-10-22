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
GATES := lint test race proto-check pack-check

.PHONY: build build-all test race lint proto proto-check pack-check gates acceptance beads-contract beads-contract-race orca-contract \
	release-dist release-sign release-dry $(PLATFORMS)

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
LINT_TAGS := beads orca acceptance

lint:
	$(GOLANGCI_LINT) run ./...
	$(GOLANGCI_LINT) run --build-tags '$(LINT_TAGS)' ./...
	$(GO) vet ./...
	$(GO) vet -tags '$(LINT_TAGS)' ./...

# The beads contract runs the pinned bd and dolt (LOOP_BD, LOOP_DOLT). CI
# runs it in the acceptance job, which provisions both.
beads-contract:
	$(GO) test -tags beads -count=1 ./internal/beadsx/...

beads-contract-race:
	CGO_ENABLED=1 $(GO) test -race -tags beads -count=1 ./internal/beadsx/...

# The parity suite (U17) runs the real server over real beads, with the
# fake agent host and synthetic GitHub. It needs the pinned bd and dolt
# (LOOP_BD, LOOP_DOLT, or ~/.cache/loop-dev/bin), and it skips without
# them. It stays out of GATES so a box with no beads binaries still runs
# gates. CI runs it in its own job.
acceptance:
	$(GO) test -count=1 -timeout 30m -tags acceptance ./acceptance/...

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

# The pack gate (U15, R39) checks every command reference in the skill pack
# against the schema this binary builds, and the vocabulary the pack may
# hold. Both prose linters then read the pack and the policy template. They
# run with python3 -I and use the standard library only. The advisory STE
# rules print nothing here, because they never fail a run.
PYTHON     ?= python3
PACK_PROSE  = $(shell find pack docs/project-policy-template -name '*.md' | LC_ALL=C sort)

pack-check:
	$(GO) run ./internal/pack/cmd/pack-check
	$(PYTHON) -I tools/prose/ste-lint.py --selftest
	$(PYTHON) -I tools/prose/unslop-lint.py --selftest
	$(PYTHON) -I tools/prose/ste-lint.py --disable passive-voice,present-perfect $(PACK_PROSE)
	$(PYTHON) -I tools/prose/unslop-lint.py $(PACK_PROSE)

gates: $(GATES)

# Releases (U16, KTD26). A tag starts .github/workflows/release.yml, which
# runs release-dist and uploads an unsigned draft. The operator signs on
# an offline machine with release-sign, which reads the secret key from
# RELEASE_KEY and never runs in CI. release-dry runs the whole path with a
# throwaway key in a temporary directory.
DIST         ?= dist/$(VERSION)
RELEASE_TOOL := $(GO) run ./internal/release/cmd/loop-release
TRUSTED_KEYS := internal/release/trusted_keys.pub

release-dist:
	@case "$(VERSION)" in v*.*.*) ;; *) echo "VERSION=$(VERSION) is not a release version vMAJOR.MINOR.PATCH" >&2; exit 1;; esac
	rm -rf $(DIST) && mkdir -p $(DIST)
	for p in $(PLATFORMS); do \
		GOOS=$${p%/*} GOARCH=$${p#*/} $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(DIST)/loop-$${p%/*}-$${p#*/} ./cmd/loop || exit 1; \
	done
	$(RELEASE_TOOL) dist -dir $(DIST) -version $(VERSION) $(if $(MIN_FROM),-min-from $(MIN_FROM)) $(if $(NO_ROLLBACK),-no-rollback)

release-sign:
	@test -n "$(RELEASE_KEY)" || { echo "set RELEASE_KEY to the offline secret key file" >&2; exit 1; }
	$(RELEASE_TOOL) sign -dir $(DIST) -key $(RELEASE_KEY)
	$(RELEASE_TOOL) verify -dir $(DIST) -pub $(TRUSTED_KEYS)

release-dry:
	@tmp=$$(mktemp -d) && trap 'rm -rf "$$tmp"' EXIT && v=v0.0.0-dry && \
	$(MAKE) --no-print-directory release-dist VERSION=$$v DIST=$$tmp/$$v && \
	$(RELEASE_TOOL) keygen -pub $$tmp/key.pub -sec $$tmp/key.sec && \
	$(RELEASE_TOOL) sign -dir $$tmp/$$v -key $$tmp/key.sec && \
	$(RELEASE_TOOL) verify -dir $$tmp/$$v -pub $$tmp/key.pub && \
	for p in $(PLATFORMS); do \
		f=$$tmp/$$v/loop-$${p%/*}-$${p#*/}; file "$$f" 2>/dev/null || ls -l "$$f"; \
	done && \
	if [ "$$($(GO) env GOOS)/$$($(GO) env GOARCH)" = linux/amd64 ]; then \
		$$tmp/$$v/loop-linux-amd64 version | grep -q '"version":"v0.0.0-dry"' && echo "loop-linux-amd64 runs and reports v0.0.0-dry"; \
	fi && \
	cp -R $$tmp/$$v $$tmp/tampered && printf x >> $$tmp/tampered/loop-linux-arm64 && \
	if $(RELEASE_TOOL) verify -dir $$tmp/tampered -pub $$tmp/key.pub 2>/dev/null; then \
		echo "release-dry: a tampered binary verified" >&2; exit 1; \
	fi && echo "release-dry: a tampered binary is refused" && \
	$(RELEASE_TOOL) keygen -pub $$tmp/other.pub -sec $$tmp/other.sec >/dev/null && \
	if $(RELEASE_TOOL) verify -dir $$tmp/$$v -pub $$tmp/other.pub 2>/dev/null; then \
		echo "release-dry: another key verified" >&2; exit 1; \
	fi && echo "release-dry: a signature by another key is refused"
