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
	@echo "  install      Install binaries to GOPATH/bin, wire PATH, offer daemon setup"
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
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -Command "$$gobin = go env GOBIN; if (-not $$gobin) { $$gobin = Join-Path (go env GOPATH) 'bin' }; Write-Host \"Installed clawdchan, clawdchan-relay, clawdchan-mcp to $$gobin\"; & (Join-Path $$gobin 'clawdchan.exe') setup; if ($$LASTEXITCODE -ne 0) { $$global:LASTEXITCODE = 0 }"
else
	@GOBIN="$$(go env GOBIN)"; if [ -z "$$GOBIN" ]; then GOBIN="$$(go env GOPATH)/bin"; fi; \
	  GOBIN=$$(printf '%s' "$$GOBIN" | tr '\\\\' '/'); \
	  echo "Installed clawdchan, clawdchan-relay, clawdchan-mcp to $$GOBIN"; \
	  "$$GOBIN/clawdchan" setup || true
endif

run-relay:
	go run ./cmd/clawdchan-relay -addr :8787

clean:
	rm -rf $(BIN)
