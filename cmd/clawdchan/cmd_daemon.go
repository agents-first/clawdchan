package main

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/vMaroon/ClawdChan/core/envelope"
	"github.com/vMaroon/ClawdChan/core/identity"
	"github.com/vMaroon/ClawdChan/core/node"
	"github.com/vMaroon/ClawdChan/core/notify"
	"github.com/vMaroon/ClawdChan/core/pairing"
	"github.com/vMaroon/ClawdChan/core/policy"
	"github.com/vMaroon/ClawdChan/core/store"
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

	// When running as a background service (no terminal), redirect all log
	// output to ~/.clawdchan/daemon.log so `daemon status -v` can tail it.
	if !stdinIsTTY() {
		logPath := daemonLogPath(c.DataDir)
		lf, lerr := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if lerr == nil {
			log.SetOutput(lf)
			log.SetFlags(log.Ldate | log.Ltime)
			defer lf.Close()
		}
	}

	dispatcher, dispatchCfg := buildDispatcher(c)
	d := &daemonSurface{
		verbose:     *verbose,
		dispatcher:  dispatcher,
		dispatchCfg: dispatchCfg,
	}
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

	// Auto-discover OpenClaw gateway when not explicitly configured.
	if *openClawURL == "" {
		if ws, tok, err := discoverOpenClaw(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "openclaw auto-discover: %v\n", err)
		} else if ws != "" {
			*openClawURL = ws
			*openClawToken = tok
			fmt.Printf("openclaw: auto-discovered gateway at %s\n", ws)
		}
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
	hub        *openclaw.Hub
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

// parseOpenClawDashboardURL parses a URL in the format emitted by
// "openclaw dashboard": http://127.0.0.1:18789/#token=<token>
// It returns the WebSocket URL (http→ws, https→wss) and the bearer token.
func parseOpenClawDashboardURL(raw string) (wsURL, token string, err error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", "", err
	}
	if u.Host == "" {
		return "", "", fmt.Errorf("no host in URL %q", raw)
	}
	fv, err := url.ParseQuery(u.Fragment)
	if err != nil || fv.Get("token") == "" {
		return "", "", fmt.Errorf("no token in URL fragment %q", u.Fragment)
	}
	token = fv.Get("token")
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	}
	u.Fragment = ""
	return u.String(), token, nil
}

// discoverOpenClaw runs "openclaw dashboard", reads its stdout for a URL
// containing #token=, and returns the parsed WebSocket URL and token.
// Returns ("", "", nil) when openclaw is not installed or the gateway is
// unreachable — the caller treats that as "skip OpenClaw silently".
func discoverOpenClaw(ctx context.Context) (wsURL, token string, err error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "openclaw", "dashboard")
	stdout, pipeErr := cmd.StdoutPipe()
	if pipeErr != nil {
		return "", "", nil
	}
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return "", "", nil // openclaw not installed
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// Try the whole line as a URL first.
		if ws, tok, err := parseOpenClawDashboardURL(line); err == nil {
			return ws, tok, nil
		}
		// The URL may be embedded in a longer line ("Dashboard at http://...").
		if idx := strings.Index(line, "http"); idx >= 0 {
			candidate := line[idx:]
			if sp := strings.IndexByte(candidate, ' '); sp > 0 {
				candidate = candidate[:sp]
			}
			if ws, tok, err := parseOpenClawDashboardURL(candidate); err == nil {
				return ws, tok, nil
			}
		}
	}
	return "", "", nil
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

	keyPath := filepath.Join(n.DataDir(), "openclaw_device.key")
	deviceKey, err := openclaw.LoadOrCreateDeviceKey(keyPath)
	if err != nil {
		return nil, fmt.Errorf("openclaw device key: %w", err)
	}
	bridge := openclaw.NewBridge(wsURL, token, deviceID, deviceKey)
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
		peer, err := n.GetPeer(ctx, th.PeerID)
		switch {
		case err == nil:
			_ = bridge.SessionsSend(ctx, sid, openclaw.PeerContext(n.Alias(), peer.Alias))
		case errors.Is(err, store.ErrNotFound):
			peerAlias := hex.EncodeToString(th.PeerID[:])
			if len(peerAlias) > 8 {
				peerAlias = peerAlias[:8]
			}
			_ = bridge.SessionsSend(ctx, sid, openclaw.PeerContext(n.Alias(), peerAlias))
		}

		subCtx, subCancel := context.WithCancel(ctx)
		cleanup.cancelSubs = append(cleanup.cancelSubs, subCancel)
		go bridge.RunSubscriber(subCtx, sid, n, th.ID)
	}
	hub := openclaw.NewHub(n, bridge, sm)
	go func() {
		if err := hub.Start(ctx); err != nil {
			log.Printf("openclaw: hub session error: %v", err)
		}
	}()
	cleanup.hub = hub

	keep := cleanup
	cleanup = nil
	return keep, nil
}

// daemonSurface receives inbound envelopes and fires OS notifications.
// Debounces per peer within debounceWindow so a rapid burst from one peer
// yields one toast, not ten.
//
// When a dispatcher is configured and a peer sends an envelope marked
// with the collab-sync title, daemonSurface takes an alternate path: it
// feeds the ask to the configured subprocess and routes the subprocess's
// answer back as a normal envelope. This is the agent-cadence collab
// path — see core/policy/dispatch.go for the wire contract, and
// dispatchCollab below for the orchestration.
type daemonSurface struct {
	verbose     bool
	node        *node.Node
	dispatcher  policy.Dispatcher
	dispatchCfg *dispatchConfig

	mu   sync.Mutex
	last map[identity.NodeID]time.Time

	// inFlightMu/inFlight tracks per-peer active dispatches so we don't
	// spawn two subprocesses for one peer's burst of collab asks. While a
	// dispatch is running for a peer, additional asks from that peer
	// fall through to the OS-toast path and will be picked up after the
	// current dispatch finishes.
	inFlightMu sync.Mutex
	inFlight   map[identity.NodeID]bool
}

// buildDispatcher constructs a policy.Dispatcher from the config's
// agent_dispatch block. Returns nil when dispatch is disabled or
// unconfigured — the daemon treats that as "no dispatcher" and falls
// through to the classic toast-and-wait path.
func buildDispatcher(c config) (policy.Dispatcher, *dispatchConfig) {
	if c.Dispatch == nil || !c.Dispatch.Enabled || len(c.Dispatch.Command) == 0 {
		return nil, c.Dispatch
	}
	timeout := time.Duration(c.Dispatch.TimeoutSeconds) * time.Second
	d := &policy.SubprocessDispatcher{
		Command:         append([]string(nil), c.Dispatch.Command...),
		Timeout:         timeout,
		MaxCollabRounds: c.Dispatch.MaxCollabRounds,
	}
	return d, c.Dispatch
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

	// Agent-cadence collab path: if the envelope is a collab-sync ask
	// from a peer AND we have a dispatcher configured AND no dispatch is
	// already in flight for this peer, spawn the configured subprocess
	// to answer at agent speed. Successful dispatch is silent (the
	// sender's sub-agent sees the reply via clawdchan_await / inbox);
	// a decline or error falls through to the OS-toast path so the
	// human sees something happened.
	if d.dispatcher != nil && d.dispatcher.Enabled() && isCollabSync(env.Content) &&
		(env.Intent == envelope.IntentAsk || env.Intent == envelope.IntentSay) {
		if d.claimDispatch(env.From.NodeID) {
			go d.runCollabDispatch(tid, env)
			return
		}
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

// isCollabSync reports whether c is a live-collab envelope by matching
// the reserved Content.Title. The daemon keys off this to decide whether
// to take the dispatcher path or fall through to the OS-toast path.
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

// claimDispatch is a per-peer one-in-flight lock. Returns true if the
// caller owns the dispatch slot and must release it with releaseDispatch
// when done. Used to guarantee we never run two subprocesses for one
// peer at the same time — an honest sub-agent loop shouldn't produce
// overlapping asks, but relay retries or client bugs shouldn't force us
// to fan out parallel Claude invocations either.
func (d *daemonSurface) claimDispatch(peer identity.NodeID) bool {
	d.inFlightMu.Lock()
	defer d.inFlightMu.Unlock()
	if d.inFlight == nil {
		d.inFlight = map[identity.NodeID]bool{}
	}
	if d.inFlight[peer] {
		return false
	}
	d.inFlight[peer] = true
	return true
}

func (d *daemonSurface) releaseDispatch(peer identity.NodeID) {
	d.inFlightMu.Lock()
	delete(d.inFlight, peer)
	d.inFlightMu.Unlock()
}

// runCollabDispatch runs the configured subprocess for one collab-sync
// ask and routes the answer (or a decline) back to the peer as a normal
// envelope. On dispatch error or decline we fall back to firing the OS
// toast so the local human still learns something happened.
func (d *daemonSurface) runCollabDispatch(tid envelope.ThreadID, env envelope.Envelope) {
	defer d.releaseDispatch(env.From.NodeID)

	ctx, cancel := context.WithTimeout(context.Background(), d.dispatchTimeout()+30*time.Second)
	defer cancel()

	req, err := d.buildDispatchRequest(ctx, tid, env)
	if err != nil {
		if d.verbose {
			fmt.Fprintf(os.Stderr, "[dispatch] build request failed: %v\n", err)
		}
		d.fallbackToToast(tid, env, fmt.Sprintf("dispatch setup failed: %v", err))
		return
	}

	if d.verbose {
		fmt.Fprintf(os.Stderr, "[dispatch] invoking agent for peer=%s rounds=%d\n", env.From.Alias, req.Policy.CollabRounds)
	}
	outcome, err := d.dispatcher.Dispatch(ctx, req)
	if err != nil {
		if d.verbose {
			fmt.Fprintf(os.Stderr, "[dispatch] dispatcher error: %v\n", err)
		}
		d.fallbackToToast(tid, env, fmt.Sprintf("dispatcher error: %v", err))
		return
	}

	if outcome.Declined {
		if d.verbose {
			fmt.Fprintf(os.Stderr, "[dispatch] declined: %s\n", outcome.DeclineReason)
		}
		d.sendDispatchDecline(ctx, tid, outcome.DeclineReason)
		d.fallbackToToast(tid, env, "dispatch declined — human engagement needed")
		return
	}

	// Successful dispatch: send the subprocess's answer back as a normal
	// envelope. If the subprocess asked for another collab round, mark
	// the outbound envelope as collab-sync so the remote sub-agent keeps
	// iterating.
	intent := envelope.IntentAsk
	if outcome.Intent == "say" {
		intent = envelope.IntentSay
	}
	content := envelope.Content{Kind: envelope.ContentText, Text: outcome.Reply}
	if outcome.Collab {
		content = envelope.Content{Kind: envelope.ContentDigest, Title: policy.CollabSyncTitle, Body: outcome.Reply}
	}
	if err := d.node.Send(ctx, tid, intent, content); err != nil {
		if d.verbose {
			fmt.Fprintf(os.Stderr, "[dispatch] send reply failed: %v\n", err)
		}
		d.fallbackToToast(tid, env, fmt.Sprintf("send reply failed: %v", err))
		return
	}
	if d.verbose {
		fmt.Fprintf(os.Stderr, "[dispatch] replied (collab=%v intent=%s len=%d)\n", outcome.Collab, outcome.Intent, len(outcome.Reply))
	}
}

func (d *daemonSurface) dispatchTimeout() time.Duration {
	if d.dispatchCfg == nil || d.dispatchCfg.TimeoutSeconds <= 0 {
		return policy.DefaultTimeout
	}
	return time.Duration(d.dispatchCfg.TimeoutSeconds) * time.Second
}

// buildDispatchRequest assembles the JSON payload that will be written
// to the subprocess's stdin. ThreadContext is bounded by
// config.MaxContext (default 20) — enough for a subprocess to reason
// about the exchange without bloating the prompt.
func (d *daemonSurface) buildDispatchRequest(ctx context.Context, tid envelope.ThreadID, ask envelope.Envelope) (policy.DispatchRequest, error) {
	peer, err := d.node.GetPeer(ctx, ask.From.NodeID)
	if err != nil {
		return policy.DispatchRequest{}, err
	}
	envs, err := d.node.ListEnvelopes(ctx, tid, 0)
	if err != nil {
		return policy.DispatchRequest{}, err
	}

	maxCtx := 20
	if d.dispatchCfg != nil && d.dispatchCfg.MaxContext > 0 {
		maxCtx = d.dispatchCfg.MaxContext
	}
	me := d.node.Identity()
	rounds := countCollabRounds(envs, policy.DefaultMaxCollabRounds*2+1)
	peerAlias := ask.From.Alias
	if peer.Alias != "" {
		peerAlias = peer.Alias
	}

	// Keep the tail of the thread so the subprocess sees the running
	// exchange, not ancient chatter. The ask itself is excluded from
	// ThreadContext because it's in req.Ask explicitly.
	tail := envs
	if len(tail) > maxCtx {
		tail = tail[len(tail)-maxCtx:]
	}
	thread := make([]policy.DispatchEnvelope, 0, len(tail))
	for _, e := range tail {
		if e.EnvelopeID == ask.EnvelopeID {
			continue
		}
		thread = append(thread, serializeDispatchEnvelope(e, me))
	}

	req := policy.DispatchRequest{
		Ask:           serializeDispatchEnvelope(ask, me),
		ThreadContext: thread,
		Peer: policy.DispatchPeer{
			NodeID:         hex.EncodeToString(peer.NodeID[:]),
			Alias:          peerAlias,
			Trust:          trustLabel(peer),
			HumanReachable: peer.HumanReachable,
		},
		Self: policy.DispatchSelf{
			NodeID: hex.EncodeToString(me[:]),
			Alias:  d.node.Alias(),
		},
		Policy: policy.DispatchPolicyHints{
			CollabRounds: rounds,
		},
	}
	return req, nil
}

// countCollabRounds counts envelopes on the thread that are marked with
// the collab-sync title, capped at maxCount so scans of long threads
// remain bounded. The caller uses this as a hop counter to let the
// dispatcher decline before runaway loops.
func countCollabRounds(envs []envelope.Envelope, maxCount int) int {
	n := 0
	for _, e := range envs {
		if e.Content.Kind == envelope.ContentDigest && e.Content.Title == policy.CollabSyncTitle {
			n++
			if n >= maxCount {
				return n
			}
		}
	}
	return n
}

func serializeDispatchEnvelope(e envelope.Envelope, me identity.NodeID) policy.DispatchEnvelope {
	dir := "in"
	if e.From.NodeID == me {
		dir = "out"
	}
	kind := "text"
	if e.Content.Kind == envelope.ContentDigest {
		kind = "digest"
	}
	role := "agent"
	if e.From.Role == envelope.RoleHuman {
		role = "human"
	}
	collab := e.Content.Kind == envelope.ContentDigest && e.Content.Title == policy.CollabSyncTitle
	return policy.DispatchEnvelope{
		EnvelopeID:  hex.EncodeToString(e.EnvelopeID[:]),
		ThreadID:    hex.EncodeToString(e.ThreadID[:]),
		FromNode:    hex.EncodeToString(e.From.NodeID[:]),
		FromAlias:   e.From.Alias,
		FromRole:    role,
		Intent:      intentName(e.Intent),
		CreatedAtMs: e.CreatedAtMs,
		Kind:        kind,
		Text:        e.Content.Text,
		Title:       e.Content.Title,
		Body:        e.Content.Body,
		Direction:   dir,
		Collab:      collab,
	}
}

func trustLabel(p pairing.Peer) string {
	switch p.Trust {
	case pairing.TrustPaired:
		return "paired"
	case pairing.TrustBridged:
		return "bridged"
	case pairing.TrustRevoked:
		return "revoked"
	default:
		return "unknown"
	}
}

// sendDispatchDecline posts a plain-text reply to close the collab loop
// gracefully. The sender's sub-agent is still in a `clawdchan_await`
// cycle — without this nudge it would burn its whole timeout waiting
// for an answer that will never come.
func (d *daemonSurface) sendDispatchDecline(ctx context.Context, tid envelope.ThreadID, reason string) {
	if reason == "" {
		reason = "dispatch declined"
	}
	msg := "[collab-dispatch declined] " + reason
	if err := d.node.Send(ctx, tid, envelope.IntentSay, envelope.Content{Kind: envelope.ContentText, Text: msg}); err != nil && d.verbose {
		fmt.Fprintf(os.Stderr, "[dispatch] send decline failed: %v\n", err)
	}
}

// fallbackToToast runs the classic toast path for an envelope that the
// dispatcher couldn't handle. Kept separate from the main dispatch()
// body so the happy path doesn't have to thread "skip-toast" booleans.
func (d *daemonSurface) fallbackToToast(tid envelope.ThreadID, env envelope.Envelope, note string) {
	if d.verbose && note != "" {
		fmt.Fprintf(os.Stderr, "[dispatch] fallback toast: %s\n", note)
	}
	if env.Intent != envelope.IntentAskHuman && d.recentlySentTo(env.From.NodeID) {
		return
	}
	d.mu.Lock()
	if d.last == nil {
		d.last = map[identity.NodeID]time.Time{}
	}
	now := time.Now()
	if t, ok := d.last[env.From.NodeID]; ok && now.Sub(t) < debounceWindow {
		d.mu.Unlock()
		return
	}
	d.last[env.From.NodeID] = now
	d.mu.Unlock()

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
