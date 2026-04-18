//go:build darwin

package notify

import (
	"os"
	"os/exec"
	"strings"
)

// dispatch fires a macOS notification.
//
// First choice is terminal-notifier (brew install terminal-notifier): it
// attributes notifications to its own bundle, which macOS automatically
// registers in Notification Center on first use. That's the only reliable
// path on modern macOS. It also supports subtitles, grouping, and click
// activators.
//
// Fallback is osascript's `display notification`, which attributes to
// "Script Editor". If Script Editor was never opened interactively it's
// often missing from Notification Center's allow-list, and notifications
// silently drop despite osascript returning 0.
func dispatch(m Message) error {
	if p := findTerminalNotifier(); p != "" {
		args := []string{
			"-title", m.Title,
			"-message", m.Body,
			"-sound", "default",
		}
		if m.Subtitle != "" {
			args = append(args, "-subtitle", m.Subtitle)
		}
		if m.ActivateApp != "" {
			args = append(args, "-activate", m.ActivateApp)
		}
		// Group by title so a burst from the same peer collapses into one
		// entry in Notification Center instead of stacking.
		args = append(args, "-group", "clawdchan")
		return exec.Command(p, args...).Run()
	}
	// osascript fallback: no subtitle support, just concatenate into body.
	body := m.Body
	if m.Subtitle != "" {
		body = m.Subtitle + "\n" + body
	}
	script := `display notification "` + escape(body) +
		`" with title "` + escape(m.Title) +
		`" sound name "default"`
	return exec.Command("osascript", "-e", script).Run()
}

// findTerminalNotifier locates the terminal-notifier binary. PATH lookup
// fails under launchd, which scrubs PATH to /usr/bin:/bin:/usr/sbin:/sbin —
// excluding both Homebrew prefixes. Probe the standard install locations
// directly so the daemon (running as a LaunchAgent) finds the same binary
// the user installed via brew.
func findTerminalNotifier() string {
	if p, err := exec.LookPath("terminal-notifier"); err == nil {
		return p
	}
	for _, p := range []string{
		"/opt/homebrew/bin/terminal-notifier", // Apple Silicon brew
		"/usr/local/bin/terminal-notifier",    // Intel brew
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func escape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}
