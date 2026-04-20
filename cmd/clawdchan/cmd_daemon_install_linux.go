//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func registerWindowsAppID() error   { return nil }
func unregisterWindowsAppID() error { return nil }

func registerURLScheme(exePath string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	appsDir := filepath.Join(home, ".local", "share", "applications")
	if err := os.MkdirAll(appsDir, 0o755); err != nil {
		return err
	}

	desktopFile := filepath.Join(appsDir, "clawdchan-url.desktop")
	content := fmt.Sprintf(`[Desktop Entry]
Name=ClawdChan URL Handler
Exec=%s consume --web %%u
Type=Application
Terminal=false
MimeType=x-scheme-handler/clawdchan;
`, exePath)

	if err := os.WriteFile(desktopFile, []byte(content), 0o644); err != nil {
		return err
	}

	if out, err := exec.Command("xdg-mime", "default", "clawdchan-url.desktop", "x-scheme-handler/clawdchan").CombinedOutput(); err != nil {
		return fmt.Errorf("xdg-mime default: %w: %s", err, string(out))
	}
	return nil
}

func unregisterURLScheme() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	desktopFile := filepath.Join(home, ".local", "share", "applications", "clawdchan-url.desktop")
	return os.Remove(desktopFile)
}
