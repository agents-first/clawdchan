//go:build darwin

package notify

import (
	"os/exec"
	"strings"
)

func dispatch(title, body string) error {
	script := `display notification "` + escape(body) + `" with title "` + escape(title) + `"`
	cmd := exec.Command("osascript", "-e", script)
	return cmd.Run()
}

func escape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}
