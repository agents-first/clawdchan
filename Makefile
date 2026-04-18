.PHONY: all build test lint tidy clean install run-relay help

BIN := ./bin
CMDS := clawdchan clawdchan-relay clawdchan-mcp

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
	  echo "Installed binaries to $$GOBIN"; \
	  case ":$$PATH:" in \
	    *":$$GOBIN:"*) \
	      echo "  $$GOBIN is on your PATH. ✓"; \
	      echo "  Running: clawdchan doctor"; \
	      "$$GOBIN/clawdchan" doctor || true; \
	      ;; \
	    *) \
	      echo "  WARNING: $$GOBIN is not on your PATH."; \
	      echo "  Claude Code launches clawdchan-mcp via its 'command' string; bare names must resolve on PATH."; \
	      echo "  Add to your shell profile:"; \
	      echo "    export PATH=\"$$GOBIN:\$$PATH\""; \
	      echo "  Or wire an absolute path into .mcp.json: clawdchan init -write-mcp <dir>"; \
	      ;; \
	  esac

run-relay:
	go run ./cmd/clawdchan-relay -addr :8787

clean:
	rm -rf $(BIN)
