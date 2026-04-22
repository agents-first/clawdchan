package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/agents-first/clawdchan/core/envelope"
	"github.com/agents-first/clawdchan/core/identity"
	"github.com/agents-first/clawdchan/core/notify"
	"github.com/agents-first/clawdchan/core/policy"
)

// dispatch is the single entry point for inbound envelopes. It decides
// whether to (a) stay silent because a session is already live,
// (b) drop the toast for debounce, or (c) fire an OS notification.
// Everything the daemon does in response to inbound traffic flows
// through here.
func (d *daemonSurface) dispatch(tid envelope.ThreadID, env envelope.Envelope) {
	if env.From.NodeID == d.node.Identity() {
		return
	}

	// Active-session suppression: once a thread is already mid-session,
	// every new envelope is back-and-forth the user has signed up for —
	// regardless of intent. Session initiation (fresh thread, or thread
	// gone quiet for longer than the active window) toasts; follow-ups
	// stay silent and surface through the MCP inbox on Claude's next turn.
	if reason, active := d.inActiveSession(tid, env.From.NodeID, env.CreatedAtMs); active {
		if d.verbose {
			fmt.Fprintf(os.Stderr, "[active-session] suppressed toast from=%s intent=%s reason=%s\n", env.From.Alias, intentName(env.Intent), reason)
		}
		return
	}

	if !d.claimNotify(env.From.NodeID) {
		if d.verbose {
			fmt.Fprintf(os.Stderr, "[debounced] from=%s intent=%s\n", env.From.Alias, intentName(env.Intent))
		}
		return
	}

	d.fireNotification(tid, env)
}

// fireNotification builds and dispatches the OS toast for this envelope.
// Assumes upstream gates (active-session, debounce, self-filter) have
// already passed.
func (d *daemonSurface) fireNotification(tid envelope.ThreadID, env envelope.Envelope) {
	alias := d.resolveAlias(env)
	msg := notificationCopy(alias, env.Intent, env.Content, d.isNewSession(tid))
	msg.ActivateApp = preferredActivateBundle()
	if err := notify.Dispatch(msg); err != nil {
		fmt.Fprintf(os.Stderr, "notify: %v\n", err)
	}
	if d.verbose {
		fmt.Fprintf(os.Stderr, "[notify] %s | %s | %s\n", msg.Title, msg.Subtitle, msg.Body)
	}
}

// resolveAlias picks the display alias for a sender: renamed-in-store
// first, envelope-declared second, short hex last. This matches the
// precedence the MCP tools apply when listing peers so the toast and the
// inbox agree on the same name.
func (d *daemonSurface) resolveAlias(env envelope.Envelope) string {
	if p, err := d.node.GetPeer(context.Background(), env.From.NodeID); err == nil && p.Alias != "" {
		return p.Alias
	}
	if env.From.Alias != "" {
		return env.From.Alias
	}
	return hex.EncodeToString(env.From.NodeID[:4])
}

// claimNotify records the current time as the last-notify time for
// this peer and returns false if the previous notify was within
// debounceWindow. A rapid burst from one peer collapses to one toast.
func (d *daemonSurface) claimNotify(peer identity.NodeID) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.last == nil {
		d.last = map[identity.NodeID]time.Time{}
	}
	now := time.Now()
	if t, ok := d.last[peer]; ok && now.Sub(t) < debounceWindow {
		return false
	}
	d.last[peer] = now
	return true
}

// inActiveSession reports whether thread tid is already mid-session at
// the moment the incoming envelope arrived. Two conditions qualify, and
// either is enough:
//
//  1. we sent an envelope on any thread with this peer within
//     activeExchangeWindow — the peer is expecting our attention and
//     their reply is the back half of that exchange;
//  2. a collab-sync marker appeared on this thread within
//     activeCollabWindow — two sub-agents are running a live loop on
//     this thread and every envelope in it (collab-sync, ask_human,
//     say, ...) is a round of that loop, not a fresh invitation.
//
// The distinction matters because a toast is only useful for "something
// new started"; once a session is underway the MCP inbox is the right
// surface and a banner per round is noise. Returns a short reason tag
// for the verbose log so we can tell the two conditions apart in
// debugging.
func (d *daemonSurface) inActiveSession(tid envelope.ThreadID, peer identity.NodeID, incomingMs int64) (string, bool) {
	if d.recentOutbound(peer) {
		return "recent-outbound", true
	}
	envs, err := d.node.ListEnvelopes(context.Background(), tid, 0)
	if err != nil {
		return "", false
	}
	cutoffMs := incomingMs - activeCollabWindow.Milliseconds()
	for _, e := range envs {
		if e.CreatedAtMs >= incomingMs || e.CreatedAtMs < cutoffMs {
			continue
		}
		if isCollabSync(e.Content) {
			return "active-collab", true
		}
	}
	return "", false
}

// recentOutbound returns true if any envelope on any thread with this
// peer was sent by us within activeExchangeWindow.
func (d *daemonSurface) recentOutbound(peer identity.NodeID) bool {
	threads, err := d.node.ListThreads(context.Background())
	if err != nil {
		return false
	}
	cutoff := time.Now().Add(-activeExchangeWindow).UnixMilli()
	me := d.node.Identity()
	for _, t := range threads {
		if t.PeerID != peer {
			continue
		}
		envs, err := d.node.ListEnvelopes(context.Background(), t.ID, 0)
		if err != nil {
			continue
		}
		for _, e := range envs {
			if e.From.NodeID == me && e.CreatedAtMs > cutoff {
				return true
			}
		}
	}
	return false
}

// isNewSession reports whether this thread has no prior outbound envelope
// from us. True means a peer-initiated conversation we haven't spoken on yet.
func (d *daemonSurface) isNewSession(tid envelope.ThreadID) bool {
	envs, err := d.node.ListEnvelopes(context.Background(), tid, 0)
	if err != nil {
		return true
	}
	me := d.node.Identity()
	for _, e := range envs {
		if e.From.NodeID == me {
			return false
		}
	}
	return true
}

// notificationCopy produces a three-line toast: Title / Subtitle / Body.
// The preview of what the peer actually said goes in the SUBTITLE (not the
// body), because macOS native banner rendering often clips the body line —
// users see title + subtitle but have to hover or swipe to reveal the body.
// Keeping the content preview in subtitle makes it visible at a glance.
//
// Body holds the short CTA so the user learns the UX: they can't be
// interrupted mid-session by the agent, but they know how to resume.
//
// A ContentDigest with Title="clawdchan:collab_sync" gets differentiated
// copy: the sender's sub-agent is waiting live, so the receiver's toast
// reads as an invitation to match pace, not just a reply.
func notificationCopy(alias string, intent envelope.Intent, c envelope.Content, newSession bool) notify.Message {
	preview := introPreview(c)
	msg := notify.Message{Title: "ClawdChan"}

	if isCollabSync(c) {
		msg.Subtitle = fmt.Sprintf("%s is collabing live", alias)
		if preview != "" {
			msg.Subtitle += `: "` + preview + `"`
		}
		msg.Body = "Engage live or pace it — ask me about it in Claude Code."
		return msg
	}

	var subject string
	switch intent {
	case envelope.IntentAskHuman:
		subject = fmt.Sprintf("%s asks", alias)
		msg.Body = "Ask me about it in Claude Code."
	case envelope.IntentNotifyHuman:
		if newSession {
			subject = fmt.Sprintf("%s has something to tell you", alias)
		} else {
			subject = fmt.Sprintf("%s sent an update", alias)
		}
		msg.Body = "Ask me about it in Claude Code."
	default:
		if newSession {
			subject = fmt.Sprintf("%s's agent wants to start", alias)
		} else {
			subject = fmt.Sprintf("%s's agent replied", alias)
		}
		msg.Body = "Ask me to continue in Claude Code."
	}

	if preview != "" {
		msg.Subtitle = subject + `: "` + preview + `"`
	} else {
		msg.Subtitle = subject
	}
	return msg
}

// isCollabSync reports whether c is a live-collab envelope by matching
// the reserved Content.Title. The daemon keys off this for differentiated
// notification copy and active-session suppression.
func isCollabSync(c envelope.Content) bool {
	return c.Kind == envelope.ContentDigest && c.Title == policy.CollabSyncTitle
}

func introPreview(c envelope.Content) string {
	switch c.Kind {
	case envelope.ContentDigest:
		if c.Title != "" && !strings.HasPrefix(c.Title, "clawdchan:") {
			return truncate(c.Title, 60)
		}
		return truncate(c.Body, 60)
	case envelope.ContentText:
		return truncate(c.Text, 60)
	}
	return ""
}

func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

// preferredActivateBundle picks a macOS bundle id for terminal-notifier to
// focus when the user clicks the toast. We sniff which terminal-like app is
// actually running and return its bundle; fall back to Terminal.app. This
// makes "click the notification → switch back to your Claude Code window"
// work for the common terminals without requiring user config.
func preferredActivateBundle() string {
	candidates := []struct{ proc, bundle string }{
		{"ghostty", "com.mitchellh.ghostty"},
		{"iTerm2", "com.googlecode.iterm2"},
		{"iTerm", "com.googlecode.iterm2"},
		{"WarpTerminal", "dev.warp.Warp-Stable"},
		{"kitty", "net.kovidgoyal.kitty"},
		{"Alacritty", "org.alacritty"},
		{"Terminal", "com.apple.Terminal"},
	}
	for _, c := range candidates {
		if err := exec.Command("pgrep", "-xq", c.proc).Run(); err == nil {
			return c.bundle
		}
	}
	return "com.apple.Terminal"
}
