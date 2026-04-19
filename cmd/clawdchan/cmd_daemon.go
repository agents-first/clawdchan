package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/vMaroon/ClawdChan/core/envelope"
	"github.com/vMaroon/ClawdChan/core/identity"
	"github.com/vMaroon/ClawdChan/core/node"
	"github.com/vMaroon/ClawdChan/core/notify"
	"github.com/vMaroon/ClawdChan/core/surface"
	"github.com/vMaroon/ClawdChan/hosts/openclaw"
	"github.com/vMaroon/ClawdChan/internal/listenerreg"
)

// cmdDaemon dispatches the daemon subcommand.
//
//	clawdchan daemon              # foreground run (alias for `run`)
//	clawdchan daemon run          # foreground run
//	clawdchan daemon setup        # interactive: explain + prompt [Y/n], then install
//	clawdchan daemon install      # non-interactive install
//	clawdchan daemon uninstall    # stop and remove
//	clawdchan daemon status       # report install + running state
//
// The daemon itself holds the relay link, ingests inbound envelopes into the
// local SQLite store, and fires an OS notification per peer (debounced) when
// something lands. Notification copy is a prompt to the user — "Alice's agent
// replied, ask me to continue" — that teaches the async UX.
func cmdDaemon(args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return daemonRun(args)
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "run":
		return daemonRun(rest)
	case "setup":
		return daemonSetup(rest)
	case "install":
		return daemonInstall(rest)
	case "uninstall", "remove":
		return daemonUninstall(rest)
	case "status":
		return daemonStatus(rest)
	default:
		return fmt.Errorf("unknown daemon subcommand %q (use run|setup|install|uninstall|status)", sub)
	}
}

// --- run --------------------------------------------------------------------

func daemonRun(args []string) error {
	fs := flag.NewFlagSet("daemon run", flag.ExitOnError)
	verbose := fs.Bool("v", false, "log every inbound to stderr (default: silent — only OS notifications)")
	openClawURL := fs.String("openclaw", "", "OpenClaw gateway WebSocket URL (optional)")
	openClawToken := fs.String("openclaw-token", "", "OpenClaw gateway bearer token")
	openClawDeviceID := fs.String("openclaw-device-id", "clawdchan-daemon", "OpenClaw gateway device id")
	fs.Parse(args)

	c, err := loadConfig()
	if err != nil {
		return err
	}

	d := &daemonSurface{verbose: *verbose}
	n, err := node.New(node.Config{
		DataDir:  c.DataDir,
		RelayURL: c.RelayURL,
		Alias:    c.Alias,
		Human:    d,
		Agent:    &daemonAgent{d: d},
	})
	if err != nil {
		return err
	}
	defer n.Close()
	d.node = n

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := n.Start(ctx); err != nil {
		return err
	}
	defer n.Stop()

	if *openClawURL == "" && c.OpenClawURL != "" {
		*openClawURL = c.OpenClawURL
	}
	if *openClawToken == "" && c.OpenClawToken != "" {
		*openClawToken = c.OpenClawToken
	}
	if c.OpenClawDeviceID != "" && *openClawDeviceID == "clawdchan-daemon" {
		*openClawDeviceID = c.OpenClawDeviceID
	}

	ocRuntime, err := enableOpenClawMode(ctx, n, *openClawURL, *openClawToken, *openClawDeviceID)
	if err != nil {
		return err
	}
	if ocRuntime != nil {
		defer ocRuntime.Close()
	}

	nid := n.Identity()
	unreg, regErr := listenerreg.Register(
		c.DataDir, listenerreg.KindCLI,
		hex.EncodeToString(nid[:]), c.RelayURL, c.Alias,
		listenerreg.RegisterOptions{
			OpenClawHostActive: ocRuntime != nil,
			OpenClawURL:        *openClawURL,
			OpenClawToken:      *openClawToken,
			OpenClawDeviceID:   *openClawDeviceID,
		},
	)
	if regErr != nil {
		fmt.Fprintf(os.Stderr, "warn: listener registry: %v\n", regErr)
	}
	defer unreg()

	fmt.Printf("clawdchan daemon running (node=%s relay=%s)\n",
		hex.EncodeToString(nid[:])[:16], c.RelayURL)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	return nil
}

type openClawRuntime struct {
	bridge     *openclaw.Bridge
	cancelSubs []context.CancelFunc
}

func (r *openClawRuntime) Close() {
	if r == nil {
		return
	}
	for _, cancel := range r.cancelSubs {
		cancel()
	}
	if r.bridge != nil {
		_ = r.bridge.Close()
	}
}

func enableOpenClawMode(ctx context.Context, n *node.Node, wsURL, token, deviceID string) (*openClawRuntime, error) {
	if wsURL == "" {
		return nil, nil
	}
	cleanup := &openClawRuntime{}
	defer func() {
		if cleanup == nil {
			return
		}
		cleanup.Close()
	}()

	bridge := openclaw.NewBridge(wsURL, token, deviceID)
	if err := bridge.Connect(ctx); err != nil {
		return nil, fmt.Errorf("openclaw connect: %w", err)
	}
	cleanup.bridge = bridge

	sm := openclaw.NewSessionMap(bridge, n.Store())
	peers, err := n.ListPeers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list peers for openclaw sessions: %w", err)
	}
	for _, p := range peers {
		if _, err := sm.EnsureSessionFor(ctx, p.NodeID); err != nil {
			return nil, fmt.Errorf("ensure openclaw session for peer %x: %w", p.NodeID[:8], err)
		}
	}

	n.SetHumanSurface(openclaw.NewHumanSurface(sm, bridge))
	n.SetAgentSurface(openclaw.NewAgentSurface(sm, bridge))

	threads, err := n.ListThreads(ctx)
	if err != nil {
		return nil, fmt.Errorf("list threads for openclaw subscribers: %w", err)
	}
	for _, th := range threads {
		sid, err := sm.EnsureSessionForThread(ctx, th.ID)
		if err != nil {
			return nil, fmt.Errorf("ensure openclaw session for thread %x: %w", th.ID[:], err)
		}
		subCtx, subCancel := context.WithCancel(ctx)
		cleanup.cancelSubs = append(cleanup.cancelSubs, subCancel)
		go bridge.RunSubscriber(subCtx, sid, n, th.ID)
	}

	keep := cleanup
	cleanup = nil
	return keep, nil
}

// daemonSurface receives inbound envelopes and fires OS notifications.
// Debounces per peer within debounceWindow so a rapid burst from one peer
// yields one toast, not ten.
type daemonSurface struct {
	verbose bool
	node    *node.Node

	mu   sync.Mutex
	last map[identity.NodeID]time.Time
}

const debounceWindow = 30 * time.Second
const activeExchangeWindow = 60 * time.Second

func (d *daemonSurface) Notify(_ context.Context, tid envelope.ThreadID, env envelope.Envelope) error {
	d.dispatch(tid, env)
	return nil
}

func (d *daemonSurface) Ask(_ context.Context, tid envelope.ThreadID, env envelope.Envelope) (envelope.Content, error) {
	d.dispatch(tid, env)
	// Returning an error means the node won't auto-reply. The envelope stays
	// in the store and the user sees it via the MCP inbox on their next turn.
	return envelope.Content{}, errors.New("daemon: ask_human is async; surfaces via MCP")
}

func (d *daemonSurface) Reachability() surface.Reachability                         { return surface.ReachableSync }
func (d *daemonSurface) PresentThread(_ context.Context, _ envelope.ThreadID) error { return nil }

type daemonAgent struct{ d *daemonSurface }

func (a *daemonAgent) OnMessage(_ context.Context, env envelope.Envelope) error {
	a.d.dispatch(env.ThreadID, env)
	return nil
}

func (d *daemonSurface) dispatch(tid envelope.ThreadID, env envelope.Envelope) {
	if env.From.NodeID == d.node.Identity() {
		return
	}

	// Active-exchange suppression: if we've sent to this peer within the
	// last activeExchangeWindow, they're expecting our attention — no need
	// to toast their reply. ask_human bypasses this because the human
	// must see the question regardless of whether an agent loop is live.
	if env.Intent != envelope.IntentAskHuman && d.recentlySentTo(env.From.NodeID) {
		if d.verbose {
			fmt.Fprintf(os.Stderr, "[active-exchange] suppressed toast from=%s intent=%s\n", env.From.Alias, intentName(env.Intent))
		}
		return
	}

	d.mu.Lock()
	if d.last == nil {
		d.last = map[identity.NodeID]time.Time{}
	}
	now := time.Now()
	if t, ok := d.last[env.From.NodeID]; ok && now.Sub(t) < debounceWindow {
		d.mu.Unlock()
		if d.verbose {
			fmt.Fprintf(os.Stderr, "[debounced] from=%s intent=%s\n", env.From.Alias, intentName(env.Intent))
		}
		return
	}
	d.last[env.From.NodeID] = now
	d.mu.Unlock()

	// Prefer the store's local alias (the user may have renamed this peer via
	// `clawdchan peer rename` or clawdchan_peer_rename) over the envelope's
	// self-declared one. Fall back to envelope alias, then to a short hex.
	alias := ""
	if p, err := d.node.GetPeer(context.Background(), env.From.NodeID); err == nil && p.Alias != "" {
		alias = p.Alias
	}
	if alias == "" {
		alias = env.From.Alias
	}
	if alias == "" {
		alias = hex.EncodeToString(env.From.NodeID[:4])
	}

	msg := notificationCopy(alias, env.Intent, env.Content, d.isNewSession(tid))
	msg.ActivateApp = preferredActivateBundle()
	if err := notify.Dispatch(msg); err != nil {
		fmt.Fprintf(os.Stderr, "notify: %v\n", err)
	}
	if d.verbose {
		fmt.Fprintf(os.Stderr, "[notify] %s | %s | %s\n", msg.Title, msg.Subtitle, msg.Body)
	}
}

// recentlySentTo returns true if any envelope on any thread with this peer
// was sent by us within the activeExchangeWindow. Used to suppress toasts
// during a live back-and-forth — the user is clearly engaged, so the OS
// banner would be noise.
func (d *daemonSurface) recentlySentTo(peer identity.NodeID) bool {
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

// isCollabSync reports whether c is a live-collab envelope by matching the
// reserved Content.Title. Kept here (not imported from the host package) so
// the daemon doesn't depend on hosts/; the constant is part of the wire
// contract between sender and receiver.
func isCollabSync(c envelope.Content) bool {
	return c.Kind == envelope.ContentDigest && c.Title == "clawdchan:collab_sync"
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
