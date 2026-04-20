package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestAgentGuideBodiesMatch enforces that the behavioral body of the
// Claude Code slash-command file and the OpenClaw-workspace-deployed
// guide stay in sync. The slash-command file has extra frontmatter and
// a $ARGUMENTS suffix that the deployed copy does not; everything else
// — conduct rules, tool guidance, structure — must match byte-for-byte.
//
// If this test fails, update both files together:
//   - hosts/claudecode/plugin/commands/clawdchan.md
//   - const clawdchanGuideMarkdown in cmd_setup.go
func TestAgentGuideBodiesMatch(t *testing.T) {
	pluginPath := filepath.Join("..", "..", "hosts", "claudecode", "plugin", "commands", "clawdchan.md")
	raw, err := os.ReadFile(pluginPath)
	if err != nil {
		t.Fatalf("read plugin guide: %v", err)
	}

	pluginBody := stripFrontmatter(string(raw))
	pluginBody = strings.TrimRight(pluginBody, "\n")
	pluginBody = strings.TrimSuffix(pluginBody, "$ARGUMENTS")
	pluginBody = strings.TrimRight(pluginBody, "\n")

	// The deployed guide starts with "# ClawdChan agent guide", the slash
	// command with a shorter preamble. Compare only the conduct-rules +
	// intents + tool-reference portions — the first-action and on-down
	// material. We key on the first "## First action" section header.
	anchor := "## First action every session"
	pluginFrom := indexOrFail(t, pluginBody, anchor, "plugin guide")
	embeddedFrom := indexOrFail(t, clawdchanGuideMarkdown, anchor, "embedded guide")

	pluginTail := normalize(pluginBody[pluginFrom:])
	embeddedTail := normalize(strings.TrimRight(clawdchanGuideMarkdown[embeddedFrom:], "\n"))

	if pluginTail != embeddedTail {
		t.Fatalf("agent guide bodies have drifted. Update both hosts/claudecode/plugin/commands/clawdchan.md and cmd_setup.go:clawdchanGuideMarkdown together.\n\n--- plugin ---\n%s\n\n--- embedded ---\n%s", pluginTail, embeddedTail)
	}
}

var frontmatterRE = regexp.MustCompile(`(?s)\A---\n.*?\n---\n`)

func stripFrontmatter(s string) string {
	return frontmatterRE.ReplaceAllString(s, "")
}

// normalize collapses trailing whitespace on each line so that one file
// with trailing spaces doesn't spuriously diff against one without.
func normalize(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.Join(lines, "\n")
}

func indexOrFail(t *testing.T, s, anchor, label string) int {
	t.Helper()
	i := strings.Index(s, anchor)
	if i < 0 {
		t.Fatalf("%s: missing anchor %q", label, anchor)
	}
	return i
}
