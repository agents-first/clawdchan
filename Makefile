.PHONY: all build test lint tidy clean install install-openclaw install-binaries run-relay help

BIN := ./bin
CMDS := clawdchan clawdchan-relay clawdchan-mcp

# Optional OpenClaw overrides for `make install-openclaw`:
#   make install-openclaw OPENCLAW_URL=wss://gw.local OPENCLAW_TOKEN=xxx
# Leave blank for the default interactive prompt (`make install`).
OPENCLAW_URL       ?=
OPENCLAW_TOKEN     ?=
OPENCLAW_DEVICE_ID ?=

# Exe extension: .exe on Windows, empty elsewhere.
EXE :=

# Recipes use Unix shell syntax (mkdir -p, rm -rf, case, ||). Force bash on
# Windows — GNU Make would otherwise default to cmd.exe and fail.
ifeq ($(OS),Windows_NT)
# Prefer Git Bash over WSL's bash (System32\bash.exe runs Linux and can't see
# Windows Go). Use the 8.3 short path so spaces don't break SHELL parsing.
# Override with: make SHELL=/path/to/bash.exe install
ifneq ($(wildcard C:/PROGRA~1/Git/bin/bash.exe),)
SHELL := C:/PROGRA~1/Git/bin/bash.exe
else ifneq ($(wildcard C:/PROGRA~1/Git/usr/bin/bash.exe),)
SHELL := C:/PROGRA~1/Git/usr/bin/bash.exe
else
SHELL := bash.exe
endif
.SHELLFLAGS := -c
EXE := .exe
endif

all: build

help:
	@echo "Targets:"
	@echo "  build             Build all binaries into ./bin"
	@echo "  test              Run the full test suite"
	@echo "  tidy              go mod tidy"
	@echo "  install           Interactive setup: binaries + PATH + OpenClaw? + daemon"
	@echo "  install-openclaw  Scripted: install + configure OpenClaw from env vars"
	@echo "                    (OPENCLAW_URL=... OPENCLAW_TOKEN=... [OPENCLAW_DEVICE_ID=...])"
	@echo "  install-binaries  Install binaries only — no prompts, no service install"
	@echo "  run-relay         Run a local relay on :8787"
	@echo "  clean             Remove ./bin"

build: $(addprefix $(BIN)/, $(CMDS))

$(BIN)/%: FORCE
	@mkdir -p $(BIN)
	go build -o $@ ./cmd/$*

FORCE:

test:
	go test ./... -timeout 120s

tidy:
	go mod tidy

install-binaries:
	go install ./cmd/clawdchan ./cmd/clawdchan-relay ./cmd/clawdchan-mcp

# install is the default UX: go install + interactive setup. Re-running is
# idempotent — existing Claude Code config and MCP wiring are preserved, we
# only add what's missing. OpenClaw is asked about inside setup; skipping
# leaves config untouched.
install: install-binaries
	@GOBIN="$$(go env GOBIN)"; if [ -z "$$GOBIN" ]; then GOBIN="$$(go env GOPATH)/bin"; fi; \
	  GOBIN=$$(printf '%s' "$$GOBIN" | tr '\\\\' '/'); \
	  echo "Installed clawdchan, clawdchan-relay, clawdchan-mcp to $$GOBIN"; \
	  "$$GOBIN/clawdchan$(EXE)" setup || true

# install-openclaw bypasses the OpenClaw prompt by passing flags straight into
# `clawdchan setup`. Pass OPENCLAW_URL=none to clear a previously-saved
# gateway. The rest of setup still runs — binaries, PATH, daemon prompt — so
# Claude Code config is added when missing and untouched when present.
install-openclaw: install-binaries
	@if [ -z "$(OPENCLAW_URL)" ]; then \
	  echo "error: set OPENCLAW_URL=wss://... (or OPENCLAW_URL=none to disable)" >&2; exit 2; \
	fi
	@GOBIN="$$(go env GOBIN)"; if [ -z "$$GOBIN" ]; then GOBIN="$$(go env GOPATH)/bin"; fi; \
	  GOBIN=$$(printf '%s' "$$GOBIN" | tr '\\\\' '/'); \
	  "$$GOBIN/clawdchan$(EXE)" setup \
	    -openclaw-url="$(OPENCLAW_URL)" \
	    -openclaw-token="$(OPENCLAW_TOKEN)" \
	    -openclaw-device-id="$(OPENCLAW_DEVICE_ID)" || true

run-relay:
	go run ./cmd/clawdchan-relay -addr :8787

clean:
	rm -rf $(BIN)
