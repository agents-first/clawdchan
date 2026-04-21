package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// cmdPathSetup ensures the directory holding the clawdchan binaries is on
// the user's shell PATH so that Claude Code (and the user's own shell) can
// launch `clawdchan`, `clawdchan-mcp`, and `clawdchan-relay` by bare name.
//
// Claude Code launches `clawdchan-mcp` from .mcp.json via a `command` string
// — when that's a bare name, CC's inherited PATH must resolve it. Without
// path-setup, users end up either wiring absolute paths into .mcp.json by
// hand or getting confusing "command not found" failures the first time
// they open a CC session after installing.
//
// This command is idempotent: if the dir is already on PATH in the current
// process, or the shell profile already references it, we say so and exit.
// Otherwise we explain, prompt [Y/n], and append one line to the correct
// profile for the user's login shell.
func cmdPathSetup(args []string) error {
	fs := flag.NewFlagSet("path-setup", flag.ExitOnError)
	yes := fs.Bool("y", false, "assume yes (non-interactive)")
	fs.Parse(args)

	bin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve self: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(bin); err == nil {
		bin = resolved
	}
	binDir := filepath.Dir(bin)

	if pathContains(binDir) {
		fmt.Printf("[ok] %s is on PATH.\n", binDir)
		return nil
	}

	if runtime.GOOS == "windows" {
		fmt.Printf("[warn] %s is not on PATH.\n", binDir)
		fmt.Printf("       Add it for your user with PowerShell:\n")
		fmt.Printf("           [Environment]::SetEnvironmentVariable(\"PATH\", \"%s;\" + [Environment]::GetEnvironmentVariable(\"PATH\", \"User\"), \"User\")\n", binDir)
		fmt.Println("       Or add it via System Properties → Environment Variables. Restart your shell afterward.")
		return nil
	}

	profile, shellName := detectShellProfile()
	if profile == "" {
		fmt.Printf("[warn] %s is not on PATH.\n", binDir)
		fmt.Printf("       Couldn't detect your shell profile. Append this line to it:\n")
		fmt.Printf(`           export PATH="%s:$PATH"`+"\n", binDir)
		return nil
	}

	if alreadyReferenced(profile, binDir) {
		fmt.Printf("[ok] %s is referenced in %s — restart your shell (or `source %s`) to pick it up.\n",
			binDir, profile, profile)
		return nil
	}

	fmt.Println()
	fmt.Printf("clawdchan binaries are installed to %s, which is not on your PATH.\n", binDir)
	fmt.Printf("Claude Code needs to launch `clawdchan-mcp` by bare name from .mcp.json; without a\n")
	fmt.Printf("PATH entry, that fails and you get a silent MCP-server-missing error in CC.\n")
	fmt.Println()
	fmt.Printf("This will append one line to your %s profile at %s:\n", shellName, profile)
	fmt.Println()
	if shellName == "fish" {
		fmt.Printf(`    set -gx PATH %s $PATH`+"\n", binDir)
	} else {
		fmt.Printf(`    export PATH="%s:$PATH"`+"\n", binDir)
	}
	fmt.Println()

	if !*yes {
		if !stdinIsTTY() {
			fmt.Println("(non-interactive session — not editing your shell profile. Re-run with -y, or add the line above yourself.)")
			return nil
		}
		ok, err := promptYN(fmt.Sprintf("Append to %s? [Y/n]: ", profile), true)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Println("Skipped. Add the line above yourself and restart your shell.")
			return nil
		}
	}

	if err := appendToProfile(profile, binDir, shellName); err != nil {
		return fmt.Errorf("append to %s: %w", profile, err)
	}
	fmt.Printf("Appended to %s.\n", profile)
	fmt.Printf("Restart your terminal (or run `source %s`) to use `clawdchan` by bare name.\n", profile)
	return nil
}

// pathContains reports whether dir is a component of the current process's
// PATH. Compared with filepath.Clean so `.` / trailing slashes don't cause
// false negatives.
func pathContains(dir string) bool {
	target := filepath.Clean(dir)
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if d == "" {
			continue
		}
		if filepath.Clean(d) == target {
			return true
		}
	}
	return false
}

// detectShellProfile returns the path to the user's shell config file for
// PATH exports, and the shell's short name. Returns ("", "") if the shell is
// unrecognized — the caller falls back to printing manual instructions.
func detectShellProfile() (profile, shell string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", ""
	}
	base := filepath.Base(os.Getenv("SHELL"))
	switch base {
	case "zsh":
		return filepath.Join(home, ".zshrc"), "zsh"
	case "bash":
		// macOS login shells read .bash_profile; Linux interactive shells read
		// .bashrc. Prefer the file that already exists; otherwise default to
		// the platform convention.
		profile := filepath.Join(home, ".bash_profile")
		if runtime.GOOS != "darwin" {
			profile = filepath.Join(home, ".bashrc")
		}
		return profile, "bash"
	case "fish":
		return filepath.Join(home, ".config", "fish", "config.fish"), "fish"
	}
	return "", ""
}

// alreadyReferenced returns true if the profile file already contains
// binDir anywhere — indicating a prior path-setup run or manual edit.
func alreadyReferenced(profile, binDir string) bool {
	data, err := os.ReadFile(profile)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), binDir)
}

// appendToProfile opens profile in append mode and writes the shell-specific
// PATH export. Creates the file (and its parent dir, for fish) if missing.
func appendToProfile(profile, binDir, shell string) error {
	if err := os.MkdirAll(filepath.Dir(profile), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(profile, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	var line string
	if shell == "fish" {
		line = fmt.Sprintf("\n# Added by `clawdchan path-setup`\nset -gx PATH %s $PATH\n", binDir)
	} else {
		line = fmt.Sprintf("\n# Added by `clawdchan path-setup`\nexport PATH=\"%s:$PATH\"\n", binDir)
	}
	_, err = f.WriteString(line)
	return err
}
