// Dispatch is the agent-cadence collab path: when a remote peer sends an
// envelope marked for live collaboration (Content.Title="clawdchan:collab_sync"),
// the local daemon spawns a configured agent subprocess to answer it at
// agent speed instead of waiting on the human's next Claude Code turn. The
// subprocess receives the ask plus a slice of recent thread context on stdin
// and prints a JSON answer to stdout. The daemon routes the answer back as a
// normal envelope on the same thread.
//
// This lives in core/policy so it stays host-agnostic: daemon + MCP + any
// future host can share the same dispatch contract. The subprocess boundary
// is deliberate — it keeps the daemon from having to link whichever agent
// runtime is in use (Claude Code, OpenClaw, a user's own script) and
// isolates crashes. A misbehaving dispatcher kills its own process, not the
// daemon.
package policy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// CollabSyncTitle is the reserved Content.Title value that marks an envelope
// as part of an active live-collab exchange. It is the single wire contract
// between sender and receiver — the sender's host sets it, the receiver's
// daemon keys off it to decide whether to dispatch.
const CollabSyncTitle = "clawdchan:collab_sync"

// DispatchRequest is the JSON payload written to the subprocess's stdin. It
// is stable across versions — the Version field discriminates future
// breaking changes. Keep fields descriptive; the subprocess is free-form
// (could be a shell script, a Python wrapper, a second Claude Code session)
// and has to make sense of it without access to the Go type definitions.
type DispatchRequest struct {
	Version       int                 `json:"version"`
	Ask           DispatchEnvelope    `json:"ask"`
	ThreadContext []DispatchEnvelope  `json:"thread_context"`
	Peer          DispatchPeer        `json:"peer"`
	Self          DispatchSelf        `json:"self"`
	Policy        DispatchPolicyHints `json:"policy"`
}

// DispatchEnvelope is a trimmed-for-subprocess view of an envelope. It drops
// the signature and the raw CBOR blob — the subprocess trusts that the
// daemon already verified the envelope before invoking dispatch.
type DispatchEnvelope struct {
	EnvelopeID  string `json:"envelope_id"`
	ThreadID    string `json:"thread_id"`
	FromNode    string `json:"from_node"`
	FromAlias   string `json:"from_alias"`
	FromRole    string `json:"from_role"` // "agent" | "human"
	Intent      string `json:"intent"`    // say | ask | notify_human | ask_human
	CreatedAtMs int64  `json:"created_at_ms"`
	Kind        string `json:"kind"` // "text" | "digest"
	Text        string `json:"text,omitempty"`
	Title       string `json:"title,omitempty"`
	Body        string `json:"body,omitempty"`
	Direction   string `json:"direction"` // "in" | "out"
	Collab      bool   `json:"collab"`    // true if Title == CollabSyncTitle
}

// DispatchPeer is the counterpart node's identity from the local store.
type DispatchPeer struct {
	NodeID         string `json:"node_id"`
	Alias          string `json:"alias"`
	Trust          string `json:"trust"`
	HumanReachable bool   `json:"human_reachable"`
}

// DispatchSelf identifies the local node to the subprocess so it can sign
// its answer as the right agent.
type DispatchSelf struct {
	NodeID string `json:"node_id"`
	Alias  string `json:"alias"`
}

// DispatchPolicyHints tells the subprocess how it was invoked so it can
// self-regulate. The daemon enforces the hard ceilings itself; these are
// hints for user-facing messaging inside the subprocess.
type DispatchPolicyHints struct {
	// CollabRounds is how many collab-sync envelopes have flowed on this
	// thread in the recent window. Lets the subprocess know when it's
	// approaching the ceiling and should wind down gracefully.
	CollabRounds int `json:"collab_rounds"`
	// MaxCollabRounds is the ceiling the daemon will enforce. The
	// subprocess SHOULD stop requesting more collab rounds before this is
	// hit; the daemon will refuse further dispatch otherwise.
	MaxCollabRounds int `json:"max_collab_rounds"`
}

// DispatchResponse is the JSON the subprocess prints on stdout. Exactly one
// of Answer or Declined should be populated. An empty response, a parse
// error, or a non-zero exit code is treated as a decline ("dispatch
// failed").
type DispatchResponse struct {
	// Answer is the text to send back on the thread.
	Answer string `json:"answer,omitempty"`
	// Declined explains why the subprocess refused to answer. Shown to the
	// remote peer as a plain-text reply prefixed with "[declined]".
	Declined string `json:"declined,omitempty"`
	// Collab, when true, marks the outbound reply as a collab-sync
	// envelope so the remote side knows the subprocess is still live.
	// When false (the default) the reply is a plain message, signalling
	// "I answered; engage at your pace."
	Collab bool `json:"collab,omitempty"`
	// Intent overrides the default outbound intent ("ask"). Accepts
	// "say" | "ask". "ask_human" / "notify_human" are not allowed from
	// dispatch — those are the human's prerogative.
	Intent string `json:"intent,omitempty"`
}

// DispatchOutcome summarizes what the caller should do with the response.
// The caller translates this into a node.Send call.
type DispatchOutcome struct {
	// Reply is the envelope text to send back. Empty means nothing to send.
	Reply string
	// Intent is the envelope intent ("say" or "ask").
	Intent string
	// Collab marks the reply as another collab-sync round.
	Collab bool
	// Declined is true when the subprocess refused to answer; the caller
	// should fall back to the original surfacing path (OS toast, MCP inbox).
	Declined bool
	// DeclineReason is the subprocess's declared reason, when present.
	DeclineReason string
}

// Dispatcher is the contract the daemon calls into. Tests swap it; a nil
// Dispatcher means dispatch is disabled and the daemon follows the classic
// toast-and-wait path.
type Dispatcher interface {
	// Enabled reports whether dispatch is configured and should be
	// attempted for this request. Callers check this before building the
	// (potentially expensive) ThreadContext slice.
	Enabled() bool
	// Dispatch runs the configured subprocess to answer req. Returns the
	// outcome or an error — an error is treated the same as a decline by
	// the caller, i.e. fall back to the classic surfacing path.
	Dispatch(ctx context.Context, req DispatchRequest) (DispatchOutcome, error)
}

// SubprocessDispatcher is the concrete implementation. It shells out to a
// configured command, writes the request to stdin, and reads a single JSON
// response from stdout with an overall timeout.
type SubprocessDispatcher struct {
	// Command is the argv of the subprocess to spawn. The first entry is
	// the executable; subsequent entries are arguments. Empty means
	// disabled.
	Command []string
	// Timeout bounds total subprocess wall time. Zero uses DefaultTimeout.
	Timeout time.Duration
	// MaxCollabRounds is the ceiling on collab envelopes per thread in the
	// recent window. Zero uses DefaultMaxCollabRounds. Exceeded → decline.
	MaxCollabRounds int
}

// DefaultTimeout and DefaultMaxCollabRounds are used when SubprocessDispatcher
// is instantiated with zero values.
const (
	DefaultTimeout          = 120 * time.Second
	DefaultMaxCollabRounds  = 12
	dispatchRequestVersion  = 1
	dispatchMaxResponseSize = 1 << 18 // 256 KiB — generous for one envelope's reply
)

// Enabled reports whether the dispatcher has a command configured.
func (d *SubprocessDispatcher) Enabled() bool {
	return d != nil && len(d.Command) > 0 && d.Command[0] != ""
}

// Dispatch runs the subprocess and returns its outcome. A non-zero exit
// code, a context cancellation, a stdout that fails to parse, or an empty
// response are all mapped to a decline.
func (d *SubprocessDispatcher) Dispatch(ctx context.Context, req DispatchRequest) (DispatchOutcome, error) {
	if !d.Enabled() {
		return DispatchOutcome{}, errors.New("dispatch: not enabled")
	}
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	maxRounds := d.MaxCollabRounds
	if maxRounds <= 0 {
		maxRounds = DefaultMaxCollabRounds
	}
	req.Version = dispatchRequestVersion
	req.Policy.MaxCollabRounds = maxRounds
	if req.Policy.CollabRounds > maxRounds {
		return DispatchOutcome{
			Declined:      true,
			DeclineReason: fmt.Sprintf("dispatch refused: collab rounds %d exceed ceiling %d", req.Policy.CollabRounds, maxRounds),
		}, nil
	}

	body, err := json.Marshal(req)
	if err != nil {
		return DispatchOutcome{}, fmt.Errorf("dispatch: marshal request: %w", err)
	}

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// #nosec G204 -- Command is user-configured via config.json; the local
	// node operator is the one trusted to set it.
	cmd := exec.CommandContext(cctx, d.Command[0], d.Command[1:]...)
	cmd.Stdin = bytes.NewReader(body)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		reason := err.Error()
		if stderr.Len() > 0 {
			reason = fmt.Sprintf("%s: %s", reason, truncate(stderr.String(), 400))
		}
		return DispatchOutcome{
			Declined:      true,
			DeclineReason: "dispatch subprocess failed: " + reason,
		}, nil
	}

	if stdout.Len() > dispatchMaxResponseSize {
		return DispatchOutcome{
			Declined:      true,
			DeclineReason: fmt.Sprintf("dispatch subprocess response exceeded %d bytes", dispatchMaxResponseSize),
		}, nil
	}

	return parseOutcome(stdout.Bytes())
}

// parseOutcome decodes subprocess stdout into a DispatchOutcome. It is
// exported to tests indirectly via Dispatch — splitting it out keeps the
// parse path trivially testable without spawning a subprocess.
func parseOutcome(raw []byte) (DispatchOutcome, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return DispatchOutcome{
			Declined:      true,
			DeclineReason: "dispatch subprocess returned empty stdout",
		}, nil
	}
	var resp DispatchResponse
	if err := json.Unmarshal(trimmed, &resp); err != nil {
		return DispatchOutcome{
			Declined:      true,
			DeclineReason: fmt.Sprintf("dispatch subprocess output not JSON: %s", truncate(string(trimmed), 200)),
		}, nil
	}
	if resp.Declined != "" {
		return DispatchOutcome{
			Declined:      true,
			DeclineReason: resp.Declined,
		}, nil
	}
	if resp.Answer == "" {
		return DispatchOutcome{
			Declined:      true,
			DeclineReason: "dispatch subprocess returned neither answer nor decline",
		}, nil
	}
	intent := resp.Intent
	switch intent {
	case "", "ask":
		intent = "ask"
	case "say":
		intent = "say"
	default:
		// Don't let the subprocess choose human-facing intents; silently
		// coerce to "ask". The subprocess cannot speak as the human.
		intent = "ask"
	}
	return DispatchOutcome{
		Reply:  resp.Answer,
		Intent: intent,
		Collab: resp.Collab,
	}, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}
