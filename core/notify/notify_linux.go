//go:build linux

package notify

import "os/exec"

func dispatch(title, body string) error {
	if _, err := exec.LookPath("notify-send"); err != nil {
		return nil
	}
	return exec.Command("notify-send", title, body).Run()
}
