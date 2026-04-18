//go:build darwin

package notify

import (
	"os/exec"
	"strings"
)

// dispatch fires a macOS notification.
//
// First choice is terminal-notifier (brew install terminal-notifier): it
// attributes notifications to its own bundle, which macOS automatically
// registers in Notification Center on first use. That's the only reliable
// path on modern macOS.
//
// Fallback is osascript's `display notification`, which attributes to
// "Script Editor". If Script Editor was never opened interactively it's
// often missing from Notification Center's allow-list, and notifications
// silently drop despite osascript returning 0. That's why this fallback
// exists but isn't the default: it looks broken to new users.
func dispatch(title, body string) error {
	if p, err := exec.LookPath("terminal-notifier"); err == nil {
		return exec.Command(p,
			"-title", title,
			"-message", body,
			"-sound", "default",
		).Run()
	}
	script := `display notification "` + escape(body) +
		`" with title "` + escape(title) +
		`" sound name "default"`
	return exec.Command("osascript", "-e", script).Run()
}

func escape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}
