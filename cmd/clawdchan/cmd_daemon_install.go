package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/mattn/go-isatty"

	"github.com/vMaroon/ClawdChan/core/notify"
	"github.com/vMaroon/ClawdChan/internal/listenerreg"
)

const launchdLabel = "com.vmaroon.clawdchan.daemon"
const systemdUnit = "clawdchan-daemon.service"
const windowsTaskName = "ClawdChan Daemon"

// --- setup (interactive) ---------------------------------------------------

// daemonSetup is the friendly entry point called by `make install`: it
// explains what the daemon is, where it gets installed, and prompts the user
// before doing anything. Skipped cleanly when ClawdChan isn't initialized
// yet, when the daemon is already installed, or when stdin isn't a TTY (so
// unattended `make install` doesn't silently register a system service).
func daemonSetup(args []string) error {
	fs := flag.NewFlagSet("daemon setup", flag.ExitOnError)
	yes := fs.Bool("y", false, "assume yes (non-interactive)")
	fs.Parse(args)

	if _, err := loadConfig(); err != nil {
		fmt.Println("ClawdChan is not initialized yet — run `clawdchan init` first, then rerun `make install` to set up the daemon.")
		return nil
	}

	if daemonAlreadyInstalled() {
		fmt.Println("ClawdChan daemon is already installed. Skipping setup.")
		fmt.Println("(Remove with `clawdchan daemon uninstall`, check state with `clawdchan daemon status`.)")
		return nil
	}

	fmt.Println()
	fmt.Println("ClawdChan daemon — install as a background service?")
	fmt.Println()
	fmt.Println("The daemon holds a persistent relay link for your node so peers can")
	fmt.Println("reach you even when no Claude Code session is open. When a peer")
	fmt.Println("messages you, it fires a native OS notification like")
	fmt.Println(`  "Alice's agent replied — ask me to continue."`)
	fmt.Println("and you resume by saying anything to Claude.")
	fmt.Println()
	switch runtime.GOOS {
	case "darwin":
		fmt.Println("This will:")
		fmt.Println("  - write ~/Library/LaunchAgents/" + launchdLabel + ".plist")
		fmt.Println("  - start the daemon under launchd (auto-starts at each login)")
		fmt.Println("  - log to ~/Library/Logs/clawdchan-daemon.log")
		fmt.Println("  - fire a test notification (macOS may ask you to allow osascript)")
	case "linux":
		fmt.Println("This will:")
		fmt.Println("  - write ~/.config/systemd/user/" + systemdUnit)
		fmt.Println("  - run `systemctl --user enable --now " + systemdUnit + "`")
		fmt.Println("  - auto-start at login; logs via `journalctl --user -u " + systemdUnit + "`")
	case "windows":
		fmt.Println("This will:")
		fmt.Println("  - create a Scheduled Task \"" + windowsTaskName + "\" (no admin needed)")
		fmt.Println("  - trigger at each user logon under your account")
		fmt.Println("  - start the daemon now")
	default:
		fmt.Println("This platform does not support automatic install — skipping.")
		return nil
	}
	fmt.Println()
	fmt.Println("Remove any time with `clawdchan daemon uninstall`.")
	fmt.Println()

	if !*yes {
		ok, err := promptYN("Install the daemon now? [Y/n]: ", true)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Println("Skipped. Run `clawdchan daemon install` any time.")
			return nil
		}
	}

	return daemonInstall(nil)
}

// daemonAlreadyInstalled reports whether the platform-native service file
// for the daemon already exists. Used by setup to stay idempotent under
// repeated `make install` runs.
func daemonAlreadyInstalled() bool {
	switch runtime.GOOS {
	case "darwin":
		home, _ := os.UserHomeDir()
		return fileExists(filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist"))
	case "linux":
		home, _ := os.UserHomeDir()
		return fileExists(filepath.Join(home, ".config", "systemd", "user", systemdUnit))
	case "windows":
		return exec.Command("schtasks", "/Query", "/TN", windowsTaskName).Run() == nil
	}
	return false
}

// promptYN reads a Y/n answer from stdin. Returns defaultYes on EOF, empty
// input, or when stdin is not a terminal (so non-interactive `make install`
// from a CI harness silently skips rather than guessing).
func promptYN(prompt string, defaultYes bool) (bool, error) {
	if !stdinIsTTY() {
		fmt.Println("(non-interactive session — skipping daemon install. Run `clawdchan daemon install` to add it manually.)")
		return false, nil
	}
	fmt.Print(prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) {
			return defaultYes, nil
		}
		return false, err
	}
	ans := strings.ToLower(strings.TrimSpace(line))
	if ans == "" {
		return defaultYes, nil
	}
	return ans == "y" || ans == "yes", nil
}

func stdinIsTTY() bool {
	fd := os.Stdin.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

// --- install ---------------------------------------------------------------

func daemonInstall(args []string) error {
	fs := flag.NewFlagSet("daemon install", flag.ExitOnError)
	force := fs.Bool("force", false, "overwrite an existing plist/unit and reload")
	fs.Parse(args)

	bin, err := resolveSelfBinary()
	if err != nil {
		return err
	}
	switch runtime.GOOS {
	case "darwin":
		return installLaunchd(bin, *force)
	case "linux":
		return installSystemd(bin, *force)
	case "windows":
		return installWindowsTask(bin, *force)
	default:
		return fmt.Errorf("daemon install not supported on %s — run `clawdchan daemon run` in a terminal", runtime.GOOS)
	}
}

func installLaunchd(bin string, force bool) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	agentDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		return err
	}
	plistPath := filepath.Join(agentDir, launchdLabel+".plist")
	logDir := filepath.Join(home, "Library", "Logs")
	_ = os.MkdirAll(logDir, 0o755)
	logPath := filepath.Join(logDir, "clawdchan-daemon.log")

	exists := fileExists(plistPath)
	if exists && !force {
		fmt.Printf("already installed: %s (use -force to overwrite)\n", plistPath)
	} else {
		plist := fmt.Sprintf(launchdPlistTmpl, launchdLabel, bin, logPath, logPath)
		if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
			return err
		}
		fmt.Printf("wrote %s\n", plistPath)
	}

	// Reload: bootout ignores absence, bootstrap (re)loads and starts.
	domain := "gui/" + strconv.Itoa(os.Getuid())
	_ = exec.Command("launchctl", "bootout", domain, plistPath).Run()
	if out, err := exec.Command("launchctl", "bootstrap", domain, plistPath).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootstrap: %w: %s", err, string(out))
	}
	fmt.Printf("daemon started, logs at %s\n", logPath)
	fmt.Println("daemon will auto-start at each login.")

	verifyTestNotification()
	return nil
}

func installSystemd(bin string, force bool) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	unitDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return err
	}
	unitPath := filepath.Join(unitDir, systemdUnit)

	exists := fileExists(unitPath)
	if exists && !force {
		fmt.Printf("already installed: %s (use -force to overwrite)\n", unitPath)
	} else {
		content := fmt.Sprintf(systemdUnitTmpl, bin)
		if err := os.WriteFile(unitPath, []byte(content), 0o644); err != nil {
			return err
		}
		fmt.Printf("wrote %s\n", unitPath)
	}

	if out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w: %s", err, string(out))
	}
	if out, err := exec.Command("systemctl", "--user", "enable", "--now", systemdUnit).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl enable --now: %w: %s", err, string(out))
	}
	fmt.Println("daemon started and enabled for auto-start at login.")
	fmt.Printf("logs: journalctl --user -u %s -f\n", systemdUnit)

	verifyTestNotification()
	return nil
}

// --- windows ---------------------------------------------------------------

// installWindowsTask registers the daemon as a Scheduled Task that runs at
// logon. schtasks doesn't require admin for per-user logon triggers with
// LIMITED run level. Binary paths containing spaces are rejected — the /TR
// quoting through Go's Windows arg escaping plus CreateProcess's own parsing
// is too fragile; users on such paths can install the task manually.
func installWindowsTask(bin string, force bool) error {
	if strings.Contains(bin, " ") {
		return fmt.Errorf("binary path contains spaces (%s); install clawdchan to a path without spaces, or create the Scheduled Task manually with Action = %q, Arguments = 'daemon run', Trigger = At log on", bin, bin)
	}

	existing := exec.Command("schtasks", "/Query", "/TN", windowsTaskName).Run() == nil
	if existing && !force {
		fmt.Printf("already installed: task %q (use -force to overwrite)\n", windowsTaskName)
	} else {
		taskRun := fmt.Sprintf(`"%s" daemon run`, bin)
		out, err := exec.Command("schtasks", "/Create",
			"/TN", windowsTaskName,
			"/TR", taskRun,
			"/SC", "ONLOGON",
			"/RL", "LIMITED",
			"/F").CombinedOutput()
		if err != nil {
			return fmt.Errorf("schtasks /Create: %w: %s", err, string(out))
		}
		fmt.Printf("created scheduled task %q\n", windowsTaskName)
	}

	if out, err := exec.Command("schtasks", "/Run", "/TN", windowsTaskName).CombinedOutput(); err != nil {
		return fmt.Errorf("schtasks /Run: %w: %s", err, string(out))
	}
	fmt.Println("daemon started. It will auto-start at each logon.")

	verifyTestNotification()
	return nil
}

// --- test notification + guided fix ---------------------------------------

// verifyTestNotification fires a test toast and, when stdin is a TTY, asks
// the user whether they saw it. If not, we open the platform's notification
// settings so the user can flip the allow-toggle on without digging through
// menus. This is the most common failure mode: the bundle is registered
// but the per-app "Allow Notifications" switch defaults to off or gets
// toggled off by Focus Mode, and the user has no way to discover that —
// the fire-and-pray approach returns exit 0 and never reports the drop.
func verifyTestNotification() {
	m := notify.Message{
		Title:    "ClawdChan",
		Subtitle: "Daemon installed",
		Body:     "You'll get notifications when peers message you.",
	}
	if err := notify.Dispatch(m); err != nil {
		fmt.Printf("note: test notification returned %v (install otherwise ok)\n", err)
		return
	}

	if !stdinIsTTY() {
		fmt.Println("sent a test notification. (non-interactive — not asking; if you don't see it, check your notification settings.)")
		return
	}

	ok, err := promptYN("Did you see a test notification? [Y/n]: ", true)
	if err != nil {
		return
	}
	if ok {
		return
	}

	fmt.Println()
	fmt.Println("Your OS is suppressing the notification despite the delivery API reporting success.")
	fmt.Println("Most common causes:")
	fmt.Println("  - Focus Mode / Do Not Disturb is on.")
	fmt.Println("  - The per-app 'Allow Notifications' switch is off for the notifier backend.")
	switch runtime.GOOS {
	case "darwin":
		fmt.Println()
		if _, err := exec.LookPath("terminal-notifier"); err == nil {
			fmt.Println("Look for 'terminal-notifier' in the list and turn 'Allow Notifications' on.")
		} else {
			fmt.Println("Consider `brew install terminal-notifier` — osascript's notifications are attributed to")
			fmt.Println("'Script Editor', which is missing or unregistered on many Macs. terminal-notifier ships its")
			fmt.Println("own registered bundle and is the reliable path.")
		}
		fmt.Println()
		fmt.Println("Opening System Settings → Notifications for you…")
		_ = exec.Command("open", "x-apple.systempreferences:com.apple.Notifications-Settings.extension").Run()
	case "linux":
		fmt.Println()
		fmt.Println("Check your notification daemon (e.g. dunst, mako, or the desktop-environment-provided one) is running.")
		fmt.Println("`notify-send 'test' 'test'` from your shell should produce a toast; if it doesn't, the notification daemon is the problem.")
	case "windows":
		fmt.Println()
		fmt.Println("Open Settings → System → Notifications. Ensure 'Get notifications from apps and other senders' is on.")
		fmt.Println("Also check Focus Assist isn't suppressing them.")
		_ = exec.Command("explorer", "ms-settings:notifications").Run()
	}
	fmt.Println()
	fmt.Println("Once allowed, rerun `clawdchan daemon install -force` to fire another test and confirm.")
}

// --- uninstall -------------------------------------------------------------

func daemonUninstall(_ []string) error {
	switch runtime.GOOS {
	case "darwin":
		home, _ := os.UserHomeDir()
		plistPath := filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")
		domain := "gui/" + strconv.Itoa(os.Getuid())
		_ = exec.Command("launchctl", "bootout", domain, plistPath).Run()
		if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		fmt.Printf("uninstalled %s\n", plistPath)
		return nil
	case "linux":
		_ = exec.Command("systemctl", "--user", "disable", "--now", systemdUnit).Run()
		home, _ := os.UserHomeDir()
		unitPath := filepath.Join(home, ".config", "systemd", "user", systemdUnit)
		if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
		fmt.Printf("uninstalled %s\n", unitPath)
		return nil
	case "windows":
		_ = exec.Command("schtasks", "/End", "/TN", windowsTaskName).Run()
		out, err := exec.Command("schtasks", "/Delete", "/TN", windowsTaskName, "/F").CombinedOutput()
		if err != nil {
			if strings.Contains(string(out), "does not exist") || strings.Contains(string(out), "cannot find") {
				fmt.Println("not installed")
				return nil
			}
			return fmt.Errorf("schtasks /Delete: %w: %s", err, string(out))
		}
		fmt.Printf("uninstalled scheduled task %q\n", windowsTaskName)
		return nil
	default:
		return fmt.Errorf("daemon uninstall not supported on %s", runtime.GOOS)
	}
}

// --- status ----------------------------------------------------------------

func daemonStatus(_ []string) error {
	c, err := loadConfig()
	if err != nil {
		return err
	}
	entries, err := listenerreg.List(c.DataDir)
	if err != nil {
		return err
	}
	running := 0
	for _, e := range entries {
		if e.Kind != listenerreg.KindCLI {
			continue
		}
		running++
		fmt.Printf("running: pid=%d alias=%s relay=%s started=%s\n",
			e.PID, e.Alias, e.RelayURL,
			time.UnixMilli(e.StartedMs).Format(time.RFC3339))
	}
	if running == 0 {
		fmt.Println("not running")
	}

	switch runtime.GOOS {
	case "darwin":
		home, _ := os.UserHomeDir()
		plistPath := filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")
		if fileExists(plistPath) {
			fmt.Printf("installed: %s\n", plistPath)
		} else {
			fmt.Println("not installed as a LaunchAgent — run `clawdchan daemon install` to make it auto-start at login.")
		}
	case "linux":
		home, _ := os.UserHomeDir()
		unitPath := filepath.Join(home, ".config", "systemd", "user", systemdUnit)
		if fileExists(unitPath) {
			fmt.Printf("installed: %s\n", unitPath)
		} else {
			fmt.Println("not installed as a systemd user unit — run `clawdchan daemon install`.")
		}
	case "windows":
		if exec.Command("schtasks", "/Query", "/TN", windowsTaskName).Run() == nil {
			fmt.Printf("installed: scheduled task %q\n", windowsTaskName)
		} else {
			fmt.Println("not installed as a scheduled task — run `clawdchan daemon install`.")
		}
	}
	return nil
}

// --- helpers ---------------------------------------------------------------

// resolveSelfBinary returns the absolute path of the currently-running
// clawdchan binary, with symlinks resolved so launchd / systemd don't depend
// on a volatile alias.
func resolveSelfBinary() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve clawdchan binary: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p, nil
	}
	return resolved, nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

const launchdPlistTmpl = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>daemon</string>
    <string>run</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ProcessType</key><string>Background</string>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`

const systemdUnitTmpl = `[Unit]
Description=ClawdChan daemon — holds relay link, fires OS notifications on inbound
After=network-online.target

[Service]
Type=simple
ExecStart=%s daemon run
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`
