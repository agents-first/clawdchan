//go:build windows

package main
import (
	"fmt"
	"os"
	"os/exec"

	"github.com/agents-first/clawdchan/core/notify"
)

// registerWindowsAppID writes the HKCU\Software\Classes\AppUserModelId entry
// that WinRT requires before it will accept toasts bound to a custom AppID.
// Without this, CreateToastNotifier("ClawdChan") throws and no toast fires.
//
// This is per-user state and does not require admin. It mirrors what app
// installers do when they ship a Start Menu shortcut — the registry form is
// equivalent and simpler to install/uninstall cleanly.
func registerWindowsAppID() error {
	key := `HKCU\Software\Classes\AppUserModelId\` + notify.WindowsAppID
	if out, err := exec.Command("reg", "add", key,
		"/v", "DisplayName", "/t", "REG_SZ", "/d", "ClawdChan", "/f").CombinedOutput(); err != nil {
		return fmt.Errorf("reg add DisplayName: %w: %s", err, string(out))
	}
	if out, err := exec.Command("reg", "add", key,
		"/v", "ShowInSettings", "/t", "REG_DWORD", "/d", "1", "/f").CombinedOutput(); err != nil {
		return fmt.Errorf("reg add ShowInSettings: %w: %s", err, string(out))
	}
	if exe, err := os.Executable(); err == nil {
		if out, err := exec.Command("reg", "add", key,
			"/v", "IconUri", "/t", "REG_SZ", "/d", exe, "/f").CombinedOutput(); err != nil {
			return fmt.Errorf("reg add IconUri: %w: %s", err, string(out))
		}
	}
	return nil
}

// unregisterWindowsAppID removes the HKCU AUMID entry. Best-effort — errors
// are ignored because uninstall shouldn't fail on cleanup of a stale entry.
func unregisterWindowsAppID() error {
	key := `HKCU\Software\Classes\AppUserModelId\` + notify.WindowsAppID
	return exec.Command("reg", "delete", key, "/f").Run()
}
