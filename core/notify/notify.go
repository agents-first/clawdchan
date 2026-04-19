// Package notify fires best-effort OS notifications. Used by the ClawdChan
// daemon to tell the user "Alice replied — ask me about it" without pulling
// them into a terminal.
//
// The dispatcher is platform-specific: terminal-notifier preferred on darwin
// (falling back to osascript), notify-send on linux, WinRT toast via
// PowerShell on windows, no-op elsewhere. All calls are best-effort;
// failures are logged by the caller, not surfaced.
package notify

// WindowsAppID is the AppUserModelID ClawdChan registers on Windows. The
// daemon installer writes an `HKCU\Software\Classes\AppUserModelId` entry
// under this id so WinRT toasts are attributed to "ClawdChan" (visible in
// the Action Center and Settings → Notifications) instead of borrowing
// another app's identity. The Windows notify backend uses this id when
// creating the toast notifier.
const WindowsAppID = "ClawdChan"

// Message is a richer notification payload. Title, Subtitle, and Body are
// three separate lines on platforms that support it (macOS with
// terminal-notifier, Windows ToastGeneric). ActivateApp is the macOS bundle
// id (e.g. "com.apple.Terminal") to focus when the user clicks the toast.
// Ignored on Windows — Windows toast activation requires a COM-registered
// background activator which we don't ship; clicks dismiss.
type Message struct {
	Title       string
	Subtitle    string
	Body        string
	ActivateApp string
}

// Dispatch fires a desktop notification for m. Returns nil on unsupported
// platforms or when the backend is unavailable.
func Dispatch(m Message) error {
	return dispatch(m)
}
