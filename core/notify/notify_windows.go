//go:build windows

package notify

import (
	"fmt"
	"os/exec"
	"strings"
)

// dispatch fires a Windows notification using a PowerShell balloon tip.
// Balloon tips are a legacy .NET primitive, but on Windows 10/11 the OS
// forwards them into the Action Center toast stream — so the user sees a
// real toast with title + body text and hears the standard notification
// sound. No third-party installs required; works on any Windows with
// PowerShell (all supported versions).
//
// The 6s ShowBalloonTip + 7s Start-Sleep lets the toast render and sit in
// the Action Center before the NotifyIcon object is disposed.
func dispatch(m Message) error {
	// Balloon tips don't have a separate subtitle; fold into body.
	body := m.Body
	if m.Subtitle != "" {
		body = m.Subtitle + "\n" + strings.TrimSpace(m.Body)
	}
	script := fmt.Sprintf(`$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
$ni = New-Object System.Windows.Forms.NotifyIcon
$ni.Icon = [System.Drawing.SystemIcons]::Information
$ni.BalloonTipTitle = %s
$ni.BalloonTipText = %s
$ni.BalloonTipIcon = [System.Windows.Forms.ToolTipIcon]::Info
$ni.Visible = $true
$ni.ShowBalloonTip(6000)
Start-Sleep -Seconds 7
$ni.Dispose()
`, psQuote(m.Title), psQuote(body))

	cmd := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", "-")
	cmd.Stdin = strings.NewReader(script)
	return cmd.Run()
}

// psQuote produces a PowerShell single-quoted string, escaping any internal
// single quotes by doubling them (PS convention). Single-quoted strings
// don't interpolate, so title/body content can't inject commands.
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
