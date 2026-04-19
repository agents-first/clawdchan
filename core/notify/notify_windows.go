//go:build windows

package notify

import (
	"context"
	"encoding/xml"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// dispatch fires a Windows native toast notification using the WinRT
// ToastGeneric template through PowerShell. ToastGeneric accepts up to
// three independent <text> elements (title, subtitle, body) and renders
// them with proper styling — unlike the legacy ToastText* templates,
// which collapse everything into a single wrapping line.
//
// The notifier is bound to WindowsAppID, which the daemon installer
// registers in HKCU\Software\Classes\AppUserModelId. Without that
// registration WinRT rejects the Show() call, so callers should install
// the daemon first (or run `clawdchan daemon install -force`).
func dispatch(m Message) error {
	// Build the toast XML. encoding/xml does proper escaping and
	// canonicalization — important because title/body are arbitrary
	// peer-supplied strings that can contain <, >, &, ".
	xmlDoc, err := buildToastXML(m)
	if err != nil {
		return err
	}

	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType=WindowsRuntime] | Out-Null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType=WindowsRuntime] | Out-Null
$xml = New-Object Windows.Data.Xml.Dom.XmlDocument
$xml.LoadXml(%s)
$toast = [Windows.UI.Notifications.ToastNotification]::new($xml)
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier(%s).Show($toast)
`, psQuote(xmlDoc), psQuote(WindowsAppID))

	// 5s ceiling: first-run PowerShell JIT can take a second or two,
	// but anything past that is a hang (missing WinRT, corrupt profile,
	// Defender scan). Don't block the daemon's notification loop on it.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "powershell",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-Command", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("toast dispatch: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// buildToastXML renders m as a ToastGeneric XML document. Empty fields are
// omitted so the toast height adapts (one-line toast when only Title is
// set, two-line for Title+Body, three-line when Subtitle is also present).
func buildToastXML(m Message) (string, error) {
	type textNode struct {
		XMLName xml.Name `xml:"text"`
		Text    string   `xml:",chardata"`
	}
	type binding struct {
		XMLName  xml.Name   `xml:"binding"`
		Template string     `xml:"template,attr"`
		Texts    []textNode `xml:"text"`
	}
	type visual struct {
		XMLName xml.Name `xml:"visual"`
		Binding binding  `xml:"binding"`
	}
	type audio struct {
		XMLName xml.Name `xml:"audio"`
		Src     string   `xml:"src,attr"`
	}
	type toast struct {
		XMLName xml.Name `xml:"toast"`
		Visual  visual   `xml:"visual"`
		Audio   audio    `xml:"audio"`
	}

	texts := []textNode{{Text: m.Title}}
	if m.Subtitle != "" {
		texts = append(texts, textNode{Text: m.Subtitle})
	}
	if m.Body != "" {
		texts = append(texts, textNode{Text: m.Body})
	}

	doc := toast{
		Visual: visual{
			Binding: binding{Template: "ToastGeneric", Texts: texts},
		},
		Audio: audio{Src: "ms-winsoundevent:Notification.Default"},
	}
	buf, err := xml.Marshal(doc)
	if err != nil {
		return "", err
	}
	return string(buf), nil
}

// psQuote produces a PowerShell single-quoted string, escaping any internal
// single quotes by doubling them (PS convention). Single-quoted strings
// don't interpolate, so toast content can't inject commands.
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
