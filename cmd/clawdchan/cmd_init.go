package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/vMaroon/ClawdChan/core/node"
)

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	dataDir := fs.String("data", defaultDataDir(), "data directory (holds config and sqlite store)")
	relay := fs.String("relay", defaultPublicRelay, "relay URL (ws:// or wss://)")
	alias := fs.String("alias", "", "display alias sent during pairing")
	writeMCP := fs.String("write-mcp", "", "also write a .mcp.json at this directory wired to this install's absolute clawdchan-mcp path")
	fs.Parse(args)

	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		return err
	}
	c := config{DataDir: *dataDir, RelayURL: *relay, Alias: *alias}
	if err := saveConfig(c); err != nil {
		return err
	}
	n, err := node.New(node.Config{DataDir: c.DataDir, RelayURL: c.RelayURL, Alias: c.Alias})
	if err != nil {
		return err
	}
	defer n.Close()
	fmt.Printf("initialized clawdchan node\n")
	fmt.Printf("  data dir: %s\n", c.DataDir)
	fmt.Printf("  relay:    %s\n", c.RelayURL)
	fmt.Printf("  alias:    %s\n", c.Alias)
	nid := n.Identity()
	fmt.Printf("  node id:  %s\n", hex.EncodeToString(nid[:]))

	mcpBin, mcpErr := resolveMCPBinary()
	if mcpErr != nil {
		fmt.Printf("\nclawdchan-mcp not on PATH: %v\n", mcpErr)
		fmt.Printf("Install the MCP server so Claude Code can launch it:\n")
		fmt.Printf("  make install    # then ensure $(go env GOPATH)/bin is on your PATH\n")
	} else {
		fmt.Printf("  mcp binary: %s\n", mcpBin)
	}

	if *writeMCP != "" {
		path, err := writeProjectMCP(*writeMCP, mcpBin)
		if err != nil {
			return fmt.Errorf("write .mcp.json: %w", err)
		}
		fmt.Printf("\nWrote %s\n", path)
		fmt.Printf("You must exit and restart your Claude Code session for the new MCP server to load.\n")
	} else {
		fmt.Printf("\nNext: add an .mcp.json to your project root, or rerun with -write-mcp <dir>.\n")
		fmt.Printf("After wiring MCP, exit and restart your Claude Code session.\n")
	}
	return nil
}

// resolveMCPBinary returns the absolute path to clawdchan-mcp, preferring PATH
// and falling back to $(go env GOPATH)/bin. Returns an error if neither works.
func resolveMCPBinary() (string, error) {
	if p, err := exec.LookPath("clawdchan-mcp"); err == nil {
		return p, nil
	}
	gopath := os.Getenv("GOPATH")
	if gopath == "" {
		if home, err := os.UserHomeDir(); err == nil {
			gopath = filepath.Join(home, "go")
		}
	}
	if gopath != "" {
		candidate := filepath.Join(gopath, "bin", "clawdchan-mcp")
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate, nil
		}
	}
	return "", errors.New("clawdchan-mcp not found on PATH or in $(go env GOPATH)/bin; run `make install`")
}

func writeProjectMCP(dir, mcpBin string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, ".mcp.json")
	bin := mcpBin
	if bin == "" {
		bin = "clawdchan-mcp"
	}
	payload := map[string]any{
		"mcpServers": map[string]any{
			"clawdchan": map[string]any{
				"command": bin,
			},
		},
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return "", err
	}
	return path, nil
}
