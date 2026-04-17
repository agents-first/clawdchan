// Package openclaw is the OpenClaw host binding for ClawdChan (phase 2).
//
// It embeds the core (as a library or sidecar; to be confirmed against the
// OpenClaw plugin runtime) and implements HumanSurface by routing AskHuman
// through OpenClaw's gateway to the user's configured messaging channel.
package openclaw
