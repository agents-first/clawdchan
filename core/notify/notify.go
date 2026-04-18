// Package notify fires best-effort OS notifications. Used by the ClawdChan
// daemon to tell the user "Alice replied — ask me about it" without pulling
// them into a terminal.
//
// The dispatcher is platform-specific: terminal-notifier preferred on darwin
// (falling back to osascript), notify-send on linux, PowerShell balloon on
// windows, no-op elsewhere. All calls are best-effort; failures are logged
// by the caller, not surfaced.
package notify

// Message is a richer notification payload. Title, Subtitle, and Body are
// three separate lines on platforms that support it (macOS with
// terminal-notifier; Windows balloon collapses subtitle+body). ActivateApp,
// when non-empty, is the macOS bundle id (e.g. "com.apple.Terminal") or
// Windows AppID to focus when the user clicks the notification. Ignored
// where not supported.
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
