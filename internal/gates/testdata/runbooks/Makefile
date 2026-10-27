# A fixture for the runbooks gate. Each kind of target that a runbook may
# name is here once.
PLATFORMS := linux/amd64 linux/arm64
GO        ?= go

.PHONY: build test race \
	proto-check $(PLATFORMS)

build:
	$(GO) build ./...

test race: build
	$(GO) test ./...

$(PLATFORMS):
	GOOS=$(word 1,$(subst /, ,$@)) $(GO) build ./...

dist/%.tar: build
	tar cf $@ bin
