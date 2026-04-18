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
	"github.com/vMaroon/ClawdChan/core/notify"
	"github.com/vMaroon/ClawdChan/core/surface"
	"github.com/vMaroon/ClawdChan/internal/listenerreg"
)

// cmdDaemon runs a persistent ClawdChan listener that holds the relay link,
// ingests inbound envelopes into the local store, and fires an OS notification
// when something meaningful lands. The notification copy tells the user to
// "ask Claude about it" — we rely on that prompt plus the UserPromptSubmit
// hook digest to resurface context when the user next engages the agent.
//
// This supersedes `clawdchan listen -follow`: listen is for tailing traffic
// in a terminal; daemon is for ambient, toast-driven awareness.
func cmdDaemon(args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	quiet := fs.Bool("quiet", false, "suppress OS notifications (stderr logging only)")
	fs.Parse(args)

	c, err := loadConfig()
	if err != nil {
		return err
	}

	d := &daemonSurface{quiet: *quiet}
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

	nid := n.Identity()
	unreg, regErr := listenerreg.Register(
		c.DataDir, listenerreg.KindCLI,
		hex.EncodeToString(nid[:]), c.RelayURL, c.Alias,
	)
	if regErr != nil {
		fmt.Fprintf(os.Stderr, "warn: listener registry: %v\n", regErr)
	}
	defer unreg()

	fmt.Printf("clawdchan daemon running\n")
	fmt.Printf("  node:  %s\n", hex.EncodeToString(nid[:]))
	fmt.Printf("  relay: %s\n", c.RelayURL)
	if *quiet {
		fmt.Println("  notifications: off (stderr only)")
	} else {
		fmt.Println("  notifications: on")
	}
	fmt.Println("holding relay link. Ctrl-C to stop.")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	return nil
}

// daemonSurface receives inbound envelopes (via Notify/Ask) and fires
// OS notifications. Debounces per peer within debounceWindow so a rapid
// burst from one peer yields one toast, not ten.
type daemonSurface struct {
	quiet bool
	node  *node.Node

	mu   sync.Mutex
	last map[identity.NodeID]time.Time
}

const debounceWindow = 30 * time.Second

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

	d.mu.Lock()
	if d.last == nil {
		d.last = map[identity.NodeID]time.Time{}
	}
	now := time.Now()
	if t, ok := d.last[env.From.NodeID]; ok && now.Sub(t) < debounceWindow {
		d.mu.Unlock()
		fmt.Fprintf(os.Stderr, "[debounced] from=%s intent=%s\n", env.From.Alias, intentName(env.Intent))
		return
	}
	d.last[env.From.NodeID] = now
	d.mu.Unlock()

	alias := env.From.Alias
	if alias == "" {
		if p, err := d.node.GetPeer(context.Background(), env.From.NodeID); err == nil && p.Alias != "" {
			alias = p.Alias
		}
	}
	if alias == "" {
		alias = hex.EncodeToString(env.From.NodeID[:4])
	}

	title, body := notificationCopy(alias, env.Intent, env.Content, d.isNewSession(tid))
	fmt.Fprintf(os.Stderr, "[notify] %s — %s\n", title, body)
	if !d.quiet {
		if err := notify.Dispatch(title, body); err != nil {
			fmt.Fprintf(os.Stderr, "[notify] dispatch error: %v\n", err)
		}
	}
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

// notificationCopy produces the toast title and body. The body always ends
// with a call-to-action ("ask me about it") so the user learns the UX: they
// can't be interrupted mid-session by the agent, but they know how to
// resume — just talk to Claude.
func notificationCopy(alias string, intent envelope.Intent, c envelope.Content, newSession bool) (title, body string) {
	title = "ClawdChan"
	switch intent {
	case envelope.IntentAskHuman:
		body = fmt.Sprintf("%s is waiting on your answer — ask me about it.", alias)
	case envelope.IntentNotifyHuman:
		if newSession {
			body = fmt.Sprintf("%s wants to tell you something — ask me about it.", alias)
		} else {
			body = fmt.Sprintf("%s sent an update — ask me when ready.", alias)
		}
	default:
		if newSession {
			if preview := introPreview(c); preview != "" {
				body = fmt.Sprintf("%s's agent wants to start something: %q — ask me about it.", alias, preview)
			} else {
				body = fmt.Sprintf("%s's agent wants to start something — ask me about it.", alias)
			}
		} else {
			body = fmt.Sprintf("%s's agent replied — ask me to continue.", alias)
		}
	}
	return
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
