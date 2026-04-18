// Package claudecode is the Claude Code host binding for ClawdChan.
//
// It embeds the core as a library and exposes a peer-centric MCP server:
// message / inbox / reply / decline. Threads are resolved internally — the
// agent never sees thread IDs. HumanSurface returns errors from Ask so the
// node does not auto-reply; the envelope stays in the store and is surfaced
// to Claude via clawdchan_inbox on the next user turn. Ambient inbound
// awareness (OS toasts like "Alice's agent replied — ask me about it") is
// driven by the separate `clawdchan daemon` process, not by this host.
package claudecode
