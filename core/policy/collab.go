// Package policy holds host-agnostic policy helpers that sit above core —
// inbound gating, notification policy, and the reserved wire markers that
// let sender and receiver coordinate without host-specific conventions.
package policy

// CollabSyncTitle is the reserved Content.Title value that marks an
// envelope as part of an active live-collab exchange. It's the single
// wire contract between sender and receiver: the sender's host (Claude
// Code, OpenClaw, ...) sets it on outbound envelopes when a sub-agent is
// running a live loop, and the receiver keys off it to differentiate
// "live collab" traffic from ordinary asks (e.g. notification copy,
// active-session suppression).
const CollabSyncTitle = "clawdchan:collab_sync"
