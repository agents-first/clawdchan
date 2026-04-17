// Package claudecode is the Claude Code host binding for ClawdChan (phase 1).
//
// It embeds the core as a library, exposes an MCP server with pair / send /
// poll / open_thread tools, and implements HumanSurface by routing AskHuman
// into the current Claude Code session.
package claudecode
