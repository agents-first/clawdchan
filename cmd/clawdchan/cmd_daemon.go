package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/vMaroon/ClawdChan/core/envelope"
	"github.com/vMaroon/ClawdChan/core/identity"
	"github.com/vMaroon/ClawdChan/core/node"
	"github.com/vMaroon/ClawdChan/core/policy"
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
//
// When a dispatcher is configured and a peer sends an envelope marked
// with the collab-sync title, daemonSurface takes an alternate path: it
// feeds the ask to the configured subprocess and routes the subprocess's
// answer back as a normal envelope. This is the agent-cadence collab
// path — see core/policy/dispatch.go for the wire contract, and
// daemon_dispatch.go for the orchestration. The notification-policy
// gates that decide "should this inbound toast" live in daemon_notify.go.
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

// activeCollabWindow: if this thread has seen a collab-sync envelope
// (either direction) within this window, a newly-arriving collab-sync is
// treated as a continuation of an ongoing live session rather than a
// fresh invitation. Toast fires on session start; the back-and-forth
// that follows stays silent and surfaces through the MCP inbox instead.
const activeCollabWindow = 3 * time.Minute

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
