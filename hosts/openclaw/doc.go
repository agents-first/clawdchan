// Package openclaw is the OpenClaw host binding for ClawdChan.
//
// It turns the clawdchan daemon into an "always-on" OpenClaw agent: inbound
// envelopes are delivered into per-peer OpenClaw sessions over the Gateway
// Protocol (WebSocket + JSON), and assistant turns emitted by OpenClaw flow
// back out as envelopes on the corresponding thread.
//
// The binding implements both HumanSurface and AgentSurface. Notify and Ask
// render the envelope ([clawdchan · from <alias> · <intent>]\n<body>) and
// push it to the peer's session via sessions.send; Ask returns
// surface.ErrAsyncReply so the core waits for the out-of-band reply that
// arrives on the same session's sessions.messages subscription. The
// subscriber routes turns back to the node: a turn received while a pending
// ask is open becomes SubmitHumanReply; otherwise it becomes Send with
// role=agent.
//
// Per-peer session identifiers are cached in the core store
// (openclaw_sessions) so that restarting the daemon does not create fresh
// OpenClaw sessions for already-paired peers.
//
// This package depends on core but the reverse is forbidden — see the
// host-agnostic-core invariant in CLAUDE.md.
package openclaw
