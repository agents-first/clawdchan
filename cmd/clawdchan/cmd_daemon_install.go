package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/mattn/go-isatty"

	"github.com/agents-first/clawdchan/core/notify"
	"github.com/agents-first/clawdchan/internal/listenerreg"
)

const launchdLabel = "com.vmaroon.clawdchan.daemon"
const systemdUnit = "clawdchan-daemon.service"
const windowsTaskName = "ClawdChan Daemon"

// --- setup (interactive) ---------------------------------------------------

// daemonSetup is the friendly entry point called by `make install`: it
// explains what the daemon is, where it gets installed, and prompts the user
// before doing anything. Skipped cleanly when ClawdChan isn't initialized
// yet, or when stdin isn't a TTY (so unattended `make install` doesn't
// silently register a system service). If already installed, setup offers a
// reinstall and otherwise starts the existing service immediately.
func daemonSetup(args []string) error {
	fs := flag.NewFlagSet("daemon setup", flag.ExitOnError)
	yes := fs.Bool("y", false, "assume yes (non-interactive)")
	fs.Parse(args)

	if _, err := loadConfig(); err != nil {
		fmt.Println("  ClawdChan is not initialized yet — run `clawdchan init` first, then rerun `make install`.")
		return nil
	}

	if daemonAlreadyInstalled() {
		fmt.Println("  [ok] already installed. Remove with `clawdchan daemon uninstall`, inspect with `clawdchan daemon status`.")
		if !*yes && stdinIsTTY() {
			redo, _ := promptYN("  Reinstall (recreate service, fire a fresh test notification)? [y/N]: ", false)
			if redo {
				return daemonInstall([]string{"-force"})
			}
		}
		fmt.Println("  Starting the installed daemon now.")
		return daemonStartInstalled()
	}

	fmt.Println("  Holds a persistent relay link so peers can reach you when no")
	fmt.Println("  Claude Code session is open; fires an OS banner on inbound.")
	switch runtime.GOOS {
	case "darwin":
		fmt.Println("  Will write ~/Library/LaunchAgents/" + launchdLabel + ".plist and start under launchd.")
		fmt.Println("  Logs: ~/Library/Logs/clawdchan-daemon.log. Test notification fires on install.")
	case "linux":
		fmt.Println("  Will write ~/.config/systemd/user/" + systemdUnit + " and enable it.")
		fmt.Println("  Logs: `journalctl --user -u " + systemdUnit + "`.")
	case "windows":
		fmt.Println("  Will create Scheduled Task \"" + windowsTaskName + "\" (no admin needed), triggered at each user logon.")
	default:
		fmt.Println("  This platform does not support automatic install — skipping.")
		return nil
	}

	if !*yes {
		if !stdinIsTTY() {
			fmt.Println("  (non-interactive session — skipping daemon install. Run `clawdchan daemon install` to add it manually.)")
			return nil
		}
		ok, err := promptYN("  Install the daemon now? [Y/n]: ", true)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Println("  Skipped. Run `clawdchan daemon install` any time.")
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
// input, or when stdin is not a terminal (so non-interactive callers silently
// fall back to the default rather than guessing). Callers that want a
// context-specific non-TTY message should check stdinIsTTY() themselves.
func promptYN(prompt string, defaultYes bool) (bool, error) {
	if !stdinIsTTY() {
		return defaultYes, nil
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

// daemonStartInstalled starts an already-installed daemon service without
// rewriting service files or firing a fresh test notification.
func daemonStartInstalled() error {
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		plistPath := filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")
		domain := "gui/" + strconv.Itoa(os.Getuid())
		_ = exec.Command("launchctl", "bootout", domain, plistPath).Run()
		if out, err := exec.Command("launchctl", "bootstrap", domain, plistPath).CombinedOutput(); err != nil {
			return fmt.Errorf("launchctl bootstrap: %w: %s", err, string(out))
		}
		fmt.Printf("daemon started, logs at %s\n", filepath.Join(home, "Library", "Logs", "clawdchan-daemon.log"))
		return nil
	case "linux":
		if out, err := exec.Command("systemctl", "--user", "enable", "--now", systemdUnit).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl enable --now: %w: %s", err, string(out))
		}
		fmt.Println("daemon started and enabled for auto-start at login.")
		return nil
	case "windows":
		// Keep the notifier AppID registered before starting so toasts keep working.
		if err := registerWindowsAppID(); err != nil {
			fmt.Printf("note: AUMID registration failed (%v); toasts will fall back to defaults\n", err)
		}
		if err := restartWindowsTask(); err != nil {
			return err
		}
		fmt.Println("daemon restarted. It will auto-start at each logon.")
		return nil
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

	// Register the AUMID *before* firing the test notification. Without this
	// HKCU entry, WinRT rejects CreateToastNotifier with the ClawdChan id.
	if err := registerWindowsAppID(); err != nil {
		fmt.Printf("note: AUMID registration failed (%v); toasts will fall back to defaults\n", err)
	}

	existing := exec.Command("schtasks", "/Query", "/TN", windowsTaskName).Run() == nil
	if existing && !force {
		fmt.Printf("already installed: task %q (use -force to overwrite)\n", windowsTaskName)
	} else {
		taskRun := fmt.Sprintf(`"%s" daemon run`, bin)
		args := []string{"/Create",
			"/TN", windowsTaskName,
			"/TR", taskRun,
			"/SC", "ONLOGON",
			"/RL", "LIMITED",
			"/F"}
		// Pin the task to the current user so schtasks doesn't need admin.
		// Without /RU, ONLOGON defaults to "any user" which requires elevation.
		if u, err := user.Current(); err == nil && strings.TrimSpace(u.Username) != "" {
			args = append(args, "/RU", u.Username)
		}
		out, err := exec.Command("schtasks", args...).CombinedOutput()
		if err != nil {
			// If Windows denied the task creation, retry via UAC elevation.
			// We no longer rely on English-only output strings like "Access is denied",
			// instead assuming failure for ONLOGON needs elevation (common case).
			fmt.Println("Scheduled Task creation failed/needs admin — a Windows UAC prompt will appear.")
			if elevErr := elevatedSchtasksCreate(args); elevErr != nil {
				return fmt.Errorf("schtasks /Create (elevated): %w (original error: %s)", elevErr, string(out))
			}
		}
		fmt.Printf("created scheduled task %q\n", windowsTaskName)
	}

	if err := restartWindowsTask(); err != nil {
		return err
	}
	fmt.Println("daemon restarted. It will auto-start at each logon.")

	verifyTestNotification()
	return nil
}

// restartWindowsTask ensures the scheduled-task daemon process is replaced.
// /End is best-effort (it fails when no instance is running), then /Run starts
// a fresh instance.
func restartWindowsTask() error {
	_, _ = exec.Command("schtasks", "/End", "/TN", windowsTaskName).CombinedOutput()
	if out, err := exec.Command("schtasks", "/Run", "/TN", windowsTaskName).CombinedOutput(); err != nil {
		return fmt.Errorf("schtasks /Run: %w: %s", err, string(out))
	}
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
		
		// Use /Query to check if the task exists without relying on English-only error strings.
		if exec.Command("schtasks", "/Query", "/TN", windowsTaskName).Run() != nil {
			fmt.Println("not installed")
			_ = unregisterWindowsAppID()
			return nil
		}
		
		out, err := exec.Command("schtasks", "/Delete", "/TN", windowsTaskName, "/F").CombinedOutput()
		if err != nil {
			return fmt.Errorf("schtasks /Delete: %w: %s", err, string(out))
		}
		_ = unregisterWindowsAppID()
		fmt.Printf("uninstalled scheduled task %q\n", windowsTaskName)
		return nil
	default:
		return fmt.Errorf("daemon uninstall not supported on %s", runtime.GOOS)
	}
}

// --- status ----------------------------------------------------------------

func daemonStatus(args []string) error {
	fs := flag.NewFlagSet("daemon status", flag.ExitOnError)
	verbose := fs.Bool("v", false, "show OpenClaw connection state and recent log lines")
	fs.Parse(args)

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
		if *verbose {
			if e.OpenClawHostActive {
				fmt.Printf("  openclaw: connected  url=%s device=%s\n",
					e.OpenClawURL, e.OpenClawDeviceID)
			} else {
				fmt.Println("  openclaw: not active")
			}
		}
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

	if *verbose {
		if err := printDaemonLog(c.DataDir, 40); err != nil && !errors.Is(err, os.ErrNotExist) {
			fmt.Printf("warn: could not read daemon log: %v\n", err)
		}
	}
	return nil
}

func daemonLogPath(dataDir string) string {
	return filepath.Join(dataDir, "daemon.log")
}

func printDaemonLog(dataDir string, lines int) error {
	path := daemonLogPath(dataDir)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	all := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	start := 0
	if len(all) > lines {
		start = len(all) - lines
	}
	tail := all[start:]
	fmt.Printf("\n--- daemon log (last %d lines: %s) ---\n", len(tail), path)
	for _, l := range tail {
		fmt.Println(l)
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

// elevatedSchtasksCreate reruns "schtasks /Create ..." via a UAC-elevated
// PowerShell launch. Uses -Wait so the caller can proceed only after the
// elevated process exits, and checks the resulting task exists so a user
// who dismisses the UAC prompt gets a clear error instead of silent success.
func elevatedSchtasksCreate(args []string) error {
	quoted := make([]string, 0, len(args))
	for _, a := range args {
		quoted = append(quoted, "'"+strings.ReplaceAll(a, "'", "''")+"'")
	}
	ps := fmt.Sprintf(
		"Start-Process -FilePath 'schtasks.exe' -ArgumentList %s -Verb RunAs -Wait -WindowStyle Hidden",
		strings.Join(quoted, ","),
	)
	cmd := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", ps)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("powershell Start-Process: %w: %s", err, string(out))
	}
	if err := exec.Command("schtasks", "/Query", "/TN", windowsTaskName).Run(); err != nil {
		return fmt.Errorf("task %q not present after elevation (UAC likely cancelled)", windowsTaskName)
	}
	return nil
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
