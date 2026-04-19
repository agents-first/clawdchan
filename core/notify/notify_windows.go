//go:build windows

package notify

import (
	"fmt"
	"os/exec"
	"strings"
)

// dispatch fires a Windows native Toast notification using WinRT APIs via PowerShell.
// This is more robust than legacy BalloonTips on Windows 10/11 as it doesn't
// require a visible tray icon and correctly populates the Action Center.
func dispatch(m Message) error {
	// ToastText02 is a template with a bold title and a regular body.
	body := m.Body
	if m.Subtitle != "" {
		body = m.Subtitle + "\n" + strings.TrimSpace(m.Body)
	}

	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null
$template = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02)
$textNodes = $template.GetElementsByTagName("text")
$textNodes.Item(0).AppendChild($template.CreateTextNode(%s)) | Out-Null
$textNodes.Item(1).AppendChild($template.CreateTextNode(%s)) | Out-Null
$toast = [Windows.UI.Notifications.ToastNotification]::new($template)
# Using a more standard AppID that Windows is less likely to ignore
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier("windows.immersivecontrolpanel_cw5n1h2txyewy!microsoft.windows.controlpanel").Show($toast)
`, psQuote(m.Title), psQuote(body))

	cmd := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", script)
	return cmd.Run()
}

// psQuote produces a PowerShell single-quoted string, escaping any internal
// single quotes by doubling them (PS convention). Single-quoted strings
// don't interpolate, so title/body content can't inject commands.
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
