.PHONY: all build test lint tidy clean install install-openclaw install-binaries install-deps run-relay help

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
	@echo "Everyday:"
	@echo "  build             Build all binaries into ./bin"
	@echo "  test              Run the full test suite"
	@echo "  tidy              go mod tidy"
	@echo ""
	@echo "Install:"
	@echo "  install           Interactive setup: binaries + macOS deps + PATH + daemon"
	@echo "  install-binaries  go install the binaries only — no prompts, no service"
	@echo "  install-openclaw  Scripted OpenClaw wiring (OPENCLAW_URL=... [OPENCLAW_TOKEN=...])"
	@echo ""
	@echo "Ops:"
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

# install-deps installs optional OS-level helpers the daemon likes having.
# Today: macOS `terminal-notifier` (osascript-attributed banners often get
# silently dropped on recent macOS, terminal-notifier registers its own
# bundle and behaves). Linux / Windows: noop.
install-deps:
	@if [ "$$(uname 2>/dev/null)" != "Darwin" ]; then exit 0; fi; \
	if command -v terminal-notifier >/dev/null 2>&1; then \
	  echo "[ok] terminal-notifier present ($$(command -v terminal-notifier))"; \
	  exit 0; \
	fi; \
	if ! command -v brew >/dev/null 2>&1; then \
	  echo "[warn] terminal-notifier missing (macOS banners may drop); brew not found — install manually"; \
	  exit 0; \
	fi; \
	if [ ! -t 0 ]; then \
	  echo "[warn] terminal-notifier missing; run: brew install terminal-notifier"; \
	  exit 0; \
	fi; \
	printf 'terminal-notifier is recommended on macOS (osascript banners are often dropped).\nInstall via `brew install terminal-notifier`? [Y/n] '; \
	read ans; \
	case "$$ans" in \
	  n|N|no|NO) echo "[skipped] install later: brew install terminal-notifier" ;; \
	  *) brew install terminal-notifier ;; \
	esac

# install is the default UX: go install + macOS deps + interactive setup.
# Re-running is idempotent — existing Claude Code config and MCP wiring
# are preserved, we only add what's missing. OpenClaw is asked about
# inside setup; skipping leaves config untouched.
install: install-binaries install-deps
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
