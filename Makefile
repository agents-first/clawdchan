.PHONY: all build test lint tidy clean install install-openclaw install-binaries run-relay help

BIN := ./bin
CMDS := clawdchan clawdchan-relay clawdchan-mcp

# Optional OpenClaw overrides for `make install-openclaw`:
#   make install-openclaw OPENCLAW_URL=wss://gw.local OPENCLAW_TOKEN=xxx
# Leave blank for the default interactive prompt (`make install`).
OPENCLAW_URL       ?=
OPENCLAW_TOKEN     ?=
OPENCLAW_DEVICE_ID ?=

# Recipes use Unix shell syntax (mkdir -p, rm -rf, case, ||). Force bash on
# Windows — GNU Make would otherwise default to cmd.exe and fail.
ifeq ($(OS),Windows_NT)
SHELL := bash.exe
.SHELLFLAGS := -c
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
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -Command "$$gobin = go env GOBIN; if (-not $$gobin) { $$gobin = Join-Path (go env GOPATH) 'bin' }; Write-Host \"Installed clawdchan, clawdchan-relay, clawdchan-mcp to $$gobin\"; & (Join-Path $$gobin 'clawdchan.exe') setup; if ($$LASTEXITCODE -ne 0) { $$global:LASTEXITCODE = 0 }"
else
	@GOBIN="$$(go env GOBIN)"; if [ -z "$$GOBIN" ]; then GOBIN="$$(go env GOPATH)/bin"; fi; \
	  GOBIN=$$(printf '%s' "$$GOBIN" | tr '\\\\' '/'); \
	  echo "Installed clawdchan, clawdchan-relay, clawdchan-mcp to $$GOBIN"; \
	  "$$GOBIN/clawdchan" setup || true
endif

# install-openclaw bypasses the OpenClaw prompt by passing flags straight into
# `clawdchan setup`. Pass OPENCLAW_URL=none to clear a previously-saved
# gateway. The rest of setup still runs — binaries, PATH, daemon prompt — so
# Claude Code config is added when missing and untouched when present.
install-openclaw: install-binaries
	@if [ -z "$(OPENCLAW_URL)" ]; then \
	  echo "error: set OPENCLAW_URL=wss://... (or OPENCLAW_URL=none to disable)" >&2; exit 2; \
	fi
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -Command "$$gobin = go env GOBIN; if (-not $$gobin) { $$gobin = Join-Path (go env GOPATH) 'bin' }; & (Join-Path $$gobin 'clawdchan.exe') setup -openclaw-url='$(OPENCLAW_URL)' -openclaw-token='$(OPENCLAW_TOKEN)' -openclaw-device-id='$(OPENCLAW_DEVICE_ID)'; if ($$LASTEXITCODE -ne 0) { $$global:LASTEXITCODE = 0 }"
else
	@GOBIN="$$(go env GOBIN)"; if [ -z "$$GOBIN" ]; then GOBIN="$$(go env GOPATH)/bin"; fi; \
	  GOBIN=$$(printf '%s' "$$GOBIN" | tr '\\\\' '/'); \
	  "$$GOBIN/clawdchan" setup \
	    -openclaw-url="$(OPENCLAW_URL)" \
	    -openclaw-token="$(OPENCLAW_TOKEN)" \
	    -openclaw-device-id="$(OPENCLAW_DEVICE_ID)" || true
endif

run-relay:
	go run ./cmd/clawdchan-relay -addr :8787

clean:
	rm -rf $(BIN)
