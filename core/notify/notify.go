// Package notify fires best-effort OS notifications. Used by the ClawdChan
// daemon to tell the user "Alice replied — ask me about it" without pulling
// them into a terminal.
//
// The dispatcher is platform-specific: osascript on darwin, notify-send on
// linux, no-op elsewhere. All calls are best-effort; failures are logged by
// the caller, not surfaced.
package notify

// Dispatch fires a desktop notification with the given title and body.
// Returns nil on unsupported platforms or when the backend is unavailable.
func Dispatch(title, body string) error {
	return dispatch(title, body)
}
