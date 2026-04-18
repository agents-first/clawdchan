.PHONY: all build test lint tidy clean install run-relay help

BIN := ./bin
CMDS := clawdchan clawdchan-relay clawdchan-mcp

# Recipes use Unix shell syntax (mkdir -p, rm -rf, case, ||). Force bash on
# Windows — GNU Make would otherwise default to cmd.exe and fail.
ifeq ($(OS),Windows_NT)
SHELL := bash.exe
.SHELLFLAGS := -c
endif

all: build

help:
	@echo "Targets:"
	@echo "  build        Build all binaries into ./bin"
	@echo "  test         Run the full test suite"
	@echo "  tidy         go mod tidy"
	@echo "  install      Install binaries to GOPATH/bin"
	@echo "  run-relay    Run a local relay on :8787"
	@echo "  clean        Remove ./bin"

build: $(addprefix $(BIN)/, $(CMDS))

$(BIN)/%: FORCE
	@mkdir -p $(BIN)
	go build -o $@ ./cmd/$*

FORCE:

test:
	go test ./... -timeout 120s

tidy:
	go mod tidy

install:
	go install ./cmd/clawdchan ./cmd/clawdchan-relay ./cmd/clawdchan-mcp
	@GOBIN="$$(go env GOBIN)"; if [ -z "$$GOBIN" ]; then GOBIN="$$(go env GOPATH)/bin"; fi; \
	  GOBIN=$$(printf '%s' "$$GOBIN" | tr '\\\\' '/'); \
	  echo "Installed binaries to $$GOBIN"; \
	  if command -v clawdchan >/dev/null 2>&1; then \
	    echo "  [ok] clawdchan resolves on PATH"; \
	    echo "  Running: clawdchan doctor"; \
	    clawdchan doctor || true; \
	  else \
	    echo "  [warn] clawdchan is not on your PATH."; \
	    echo "  Claude Code launches clawdchan-mcp via its 'command' string; bare names must resolve on PATH."; \
	    echo "  Add $$GOBIN to your PATH (e.g. export PATH=\"$$GOBIN:\$$PATH\" in your shell profile),"; \
	    echo "  or wire an absolute path into .mcp.json: clawdchan init -write-mcp <dir>"; \
	  fi

run-relay:
	go run ./cmd/clawdchan-relay -addr :8787

clean:
	rm -rf $(BIN)
