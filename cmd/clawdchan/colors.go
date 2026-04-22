package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/mattn/go-isatty"
)

// colorEnabled controls whether the style helpers emit ANSI escapes.
// Evaluated once at package init: respects NO_COLOR (off), FORCE_COLOR
// (on, even when piped) and otherwise tracks stdout-is-a-TTY. This is
// the standard contract — see https://no-color.org.
var colorEnabled = func() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("FORCE_COLOR") != "" {
		return true
	}
	return isatty.IsTerminal(os.Stdout.Fd())
}()

// style wraps s in an ANSI escape pair. When colorEnabled is false
// (non-TTY, NO_COLOR set, piped output) it returns s unchanged so
// scripts that grep our output still see plain text.
func style(s, code string) string {
	if !colorEnabled {
		return s
	}
	return code + s + "\x1b[0m"
}

func bold(s string) string   { return style(s, "\x1b[1m") }
func dim(s string) string    { return style(s, "\x1b[2m") }
func red(s string) string    { return style(s, "\x1b[31m") }
func green(s string) string  { return style(s, "\x1b[32m") }
func yellow(s string) string { return style(s, "\x1b[33m") }
func cyan(s string) string   { return style(s, "\x1b[36m") }

// printBanner emits the setup banner — a figlet "standard"-style
// ASCII rendering of "ClawdChan" tinted Claude-orange, followed by
// a dim tagline. Degrades to plain text under NO_COLOR / non-TTY
// via hexColor.
//
// The art is hand-written (not generated at runtime) so there's no
// figlet dependency and the bytes are exactly what we ship. If you
// edit the letters, preserve trailing whitespace — each line is
// right-padded so the orange block is a clean rectangle.
func printBanner() {
	lines := []string{
		"   ____ _                   _  ____ _                 ",
		"  / ___| | __ ___      ____| |/ ___| |__   __ _ _ __  ",
		" | |   | |/ _` \\ \\ /\\ / / _` | |   | '_ \\ / _` | '_ \\ ",
		" | |___| | (_| |\\ V  V / (_| | |___| | | | (_| | | | |",
		"  \\____|_|\\__,_| \\_/\\_/ \\__,_|\\____|_| |_|\\__,_|_| |_|",
	}
	fmt.Println()
	for _, l := range lines {
		fmt.Println(hexColor(l, claudeOrange))
	}
	fmt.Println(dim("  let my Claude talk to yours."))
}

// hexColor wraps s in a 24-bit foreground color from "#RRGGBB". Falls
// back to plain when color is disabled or the hex is malformed.
func hexColor(s, h string) string {
	if !colorEnabled {
		return s
	}
	r, g, b, ok := parseHex(h)
	if !ok {
		return s
	}
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm%s\x1b[0m", r, g, b, s)
}

// okTag is the green "[ok]" marker used throughout the setup flow.
// Centralized so the look stays consistent across agent files.
func okTag() string { return green("[ok]") }

// warnTag is the yellow "[warn]" marker for non-fatal notes.
func warnTag() string { return yellow("[warn]") }

// Agent brand hexes. Keep the sourcing comment next to each constant
// so future edits see at a glance which are official and which are
// placeholders to be corrected. Rendered via 24-bit truecolor ANSI —
// modern terminals (iTerm2, Terminal.app, WezTerm, modern xterm,
// GNOME, etc.) all support this. NO_COLOR still forces plain.
const (
	// claudeOrange is the Anthropic-published "Claude Orange". Source:
	// https://github.com/anthropics/skills/blob/main/skills/brand-guidelines/SKILL.md
	claudeOrange = "#D97757"
	// geminiBlue is the primary blue of the (pre-2025) Gemini
	// gradient. Google does not publish a single flagship hex — this
	// is community-sourced (brandcolorcode.com/gemini).
	geminiBlue = "#4796E3"
	// codexGreen is unverified. Codex CLI has no published brand hex;
	// we borrow the ChatGPT-adjacent green so Codex is visually
	// distinct from Copilot's purple and Gemini's blue.
	codexGreen = "#10A37F"
	// copilotPurple is GitHub's published "Copilot Purple". Source:
	// https://brand.github.com/brand-identity/copilot
	copilotPurple = "#8534F3"
	// openclawRed is a placeholder (lobster red) — no hex is
	// documented for the in-tree OpenClaw host. Change when we adopt
	// a real brand color.
	openclawRed = "#E34234"
)

// agentColor returns the brand hex for an agent key ("cc", "gemini",
// "codex", "copilot", "openclaw"). Returns "" for unknown keys.
func agentColor(key string) string {
	switch key {
	case "cc":
		return claudeOrange
	case "gemini":
		return geminiBlue
	case "codex":
		return codexGreen
	case "copilot":
		return copilotPurple
	case "openclaw":
		return openclawRed
	}
	return ""
}

// agentStyle wraps s in bold + the agent's brand color, using 24-bit
// ANSI truecolor. Falls back to plain bold when color is disabled or
// the key is unknown.
func agentStyle(key, s string) string {
	if !colorEnabled {
		return s
	}
	h := agentColor(key)
	if h == "" {
		return bold(s)
	}
	r, g, b, ok := parseHex(h)
	if !ok {
		return bold(s)
	}
	return fmt.Sprintf("\x1b[1;38;2;%d;%d;%dm%s\x1b[0m", r, g, b, s)
}

// parseHex parses a "#RRGGBB" string into 0..255 components.
func parseHex(h string) (r, g, b int, ok bool) {
	if len(h) != 7 || h[0] != '#' {
		return 0, 0, 0, false
	}
	parse := func(s string) (int, bool) {
		n, err := strconv.ParseInt(s, 16, 0)
		if err != nil {
			return 0, false
		}
		return int(n), true
	}
	rr, ok1 := parse(h[1:3])
	gg, ok2 := parse(h[3:5])
	bb, ok3 := parse(h[5:7])
	if !ok1 || !ok2 || !ok3 {
		return 0, 0, 0, false
	}
	return rr, gg, bb, true
}
