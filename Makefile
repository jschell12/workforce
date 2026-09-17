# wf is a Go binary. `make install` is what sync-ai-config.sh calls.
#
# The binary is NOT committed: it is platform-specific and multi-megabyte, and a
# repo that carries one grows a copy per push and tempts the reverse sync into
# copying it back.

BIN     ?= $(HOME)/.local/bin/workforce
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GO      ?= go

.PHONY: build test check install clean

build:
	$(GO) build -ldflags "-X main.version=$(VERSION)" -o wf ./cmd/wf

test:
	$(GO) test ./...

# What CI and a person should run. vet catches a class the tests do not, and
# gofmt -l failing loudly beats a diff nobody notices.
# Depends on build, and points the suite at what it built. Without both it ran
# against ${HERE}/workforce, which since the port is a file that does not exist,
# so `make check` reported 72 failures on a clean clone while the code was fine.
# A documented entry point that fails is worse than none.
check: build test
	$(GO) vet ./...
	@test -z "$$($(GO)fmt -l . 2>/dev/null)" || { echo "gofmt: files need formatting:"; $(GO)fmt -l .; exit 1; }
	WORKFORCE_BIN="$(CURDIR)/wf" ./test-workforce.sh

# Installs to BIN and points wf at it. `wf` is what a person types; `workforce`
# is what scripts, plists and permission profiles name, so both must exist.
#
# Built to a temp path and moved into place, because the file being replaced may
# be executing: a launchd job runs it every 120 seconds, and writing over a
# running binary is how you get "text file busy" or a truncated one.
install:
	@mkdir -p $(dir $(BIN))
	$(GO) build -ldflags "-X main.version=$(VERSION)" -o $(BIN).new ./cmd/wf
	@mv -f $(BIN).new $(BIN)
	@chmod 755 $(BIN)
	@ln -sf $(BIN) $(dir $(BIN))wf
	@echo "installed $$($(BIN) version) to $(BIN)"

clean:
	rm -f wf $(BIN).new
