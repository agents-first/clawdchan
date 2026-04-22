package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"
)

// cmdUninstall reverses what `clawdchan setup` installs. The two
// local artifacts — the background daemon and the ~/.clawdchan data
// dir — are removed directly when confirmed. The rest of setup
// writes to files outside our trust boundary (~/.claude.json,
// ~/.claude/settings.json, per-project .mcp.json / .claude/*) so we
// print the exact manual steps instead of editing them blindly — the
// user owns those files.
func cmdUninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: clawdchan uninstall [-force] [-keep-data]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Reverse `clawdchan setup`:")
		fmt.Fprintln(os.Stderr, "  1. stop and remove the background daemon service")
		fmt.Fprintln(os.Stderr, "  2. delete the data dir (unless -keep-data)")
		fmt.Fprintln(os.Stderr, "  3. print the exact commands for the Claude Code / OpenClaw")
		fmt.Fprintln(os.Stderr, "     cleanup steps that touch files we don't own")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Flags:")
		fs.PrintDefaults()
	}
	force := fs.Bool("force", false, "skip confirmation")
	keepData := fs.Bool("keep-data", false, "preserve the data dir (identity, pairings, threads)")
	fs.Parse(args)

	dataDir := defaultDataDir()

	fmt.Println("🐾 ClawdChan uninstall")
	fmt.Printf("   data dir: %s\n", dataDir)
	fmt.Printf("   keep data: %t\n", *keepData)
	fmt.Println()

	if !*force {
		fmt.Print("Continue? [y/N]: ")
		r := bufio.NewReader(os.Stdin)
		line, _ := r.ReadString('\n')
		if ans := strings.ToLower(strings.TrimSpace(line)); ans != "y" && ans != "yes" {
			fmt.Println("aborted")
			return nil
		}
	}

	// Step 1: daemon service
	fmt.Println("[1/3] daemon service")
	if daemonAlreadyInstalled() {
		if err := daemonUninstall(nil); err != nil {
			fmt.Printf("  note: daemon uninstall failed: %v\n", err)
		}
	} else {
		fmt.Println("  (no daemon service installed; skipping)")
	}

	// Step 2: data dir
	fmt.Println("[2/3] data directory")
	if *keepData {
		fmt.Printf("  (kept %s — remove manually with `rm -rf %s` when ready)\n", dataDir, dataDir)
	} else if _, err := os.Stat(dataDir); err == nil {
		if err := os.RemoveAll(dataDir); err != nil {
			fmt.Printf("  note: could not remove %s: %v\n", dataDir, err)
		} else {
			fmt.Printf("  [ok] removed %s\n", dataDir)
		}
	} else {
		fmt.Printf("  (%s does not exist; skipping)\n", dataDir)
	}

	// Step 3: external cleanup hints. Each agent prints its own block
	// (see agents.go) so adding a host is one-stop.
	fmt.Println("[3/3] manual cleanup (files we don't own)")
	fmt.Println()
	for _, a := range allAgents() {
		hints := a.uninstallHints()
		if len(hints) == 0 {
			continue
		}
		for _, line := range hints {
			fmt.Printf("  %s\n", line)
		}
		fmt.Println()
	}
	fmt.Println("  If you wired OpenClaw:")
	fmt.Println("    openclaw mcp remove clawdchan")
	fmt.Println("    # and delete CLAWDCHAN_GUIDE.md from each agent workspace")
	fmt.Println()
	fmt.Println("  PATH entries added by `clawdchan path-setup` live in your shell rc")
	fmt.Println("  (~/.zshrc, ~/.bashrc, ~/.config/fish/config.fish). They point at")
	fmt.Println("  $GOPATH/bin and don't hurt anything when clawdchan is gone, but")
	fmt.Println("  remove the `# clawdchan` block if you want a clean rc.")
	fmt.Println()
	fmt.Println("  Uninstall the binaries themselves:")
	fmt.Println("    rm \"$(go env GOPATH)/bin/clawdchan\"")
	fmt.Println("    rm \"$(go env GOPATH)/bin/clawdchan-mcp\"")
	fmt.Println("    rm \"$(go env GOPATH)/bin/clawdchan-relay\"")
	fmt.Println()
	fmt.Println("✅ local state removed. Follow the manual steps above to finish.")
	return nil
}
