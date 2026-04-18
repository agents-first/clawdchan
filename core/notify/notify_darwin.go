//go:build darwin

package notify

import (
	"os/exec"
	"strings"
)

// dispatch fires a macOS notification via osascript. The `sound name
// "default"` clause is important — without it, notifications are silent and
// easy to miss (they render as a banner that auto-dismisses).
func dispatch(title, body string) error {
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
