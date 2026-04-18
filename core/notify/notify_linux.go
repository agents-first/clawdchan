//go:build linux

package notify

import (
	"os/exec"
	"strings"
)

func dispatch(m Message) error {
	if _, err := exec.LookPath("notify-send"); err != nil {
		return nil
	}
	// notify-send has no subtitle; fold into body with a blank line so
	// it renders as "heading\n\ndetail" in most notification daemons.
	body := m.Body
	if m.Subtitle != "" {
		body = m.Subtitle + "\n\n" + strings.TrimSpace(m.Body)
	}
	return exec.Command("notify-send", m.Title, body).Run()
}
