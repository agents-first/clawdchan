package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Without operator.write + operator.admin the gateway closes the subagent WS
// with 1008 "pairing required" and no tools (including ClawdChan) are reachable.
var openClawRequiredOperatorScopes = []string{
	"operator.admin",
	"operator.read",
	"operator.write",
}

// ensureOpenClawOperatorScopes patches the local operator token stored by
// OpenClaw so it carries the scopes subagents need. It touches two files:
//
//	~/.openclaw/identity/device-auth.json  — tokens.operator.scopes
//	~/.openclaw/devices/paired.json        — entry[deviceId].{scopes,approvedScopes,tokens.operator.scopes}
//
// Idempotent. Returns true only if a file was rewritten — the caller uses
// that to decide whether the gateway must be restarted. Missing files are
// treated as "nothing to do" (user may not have paired this device yet).
func ensureOpenClawOperatorScopes() (changed bool, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, err
	}
	authPath := filepath.Join(home, ".openclaw", "identity", "device-auth.json")
	pairedPath := filepath.Join(home, ".openclaw", "devices", "paired.json")

	authData, err := os.ReadFile(authPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	var auth map[string]any
	if err := json.Unmarshal(authData, &auth); err != nil {
		return false, fmt.Errorf("parse %s: %w", authPath, err)
	}

	authChanged := false
	if tokens, ok := auth["tokens"].(map[string]any); ok {
		if op, ok := tokens["operator"].(map[string]any); ok {
			if merged, ch := mergeScopeList(op["scopes"]); ch {
				op["scopes"] = merged
				authChanged = true
			}
		}
	}
	if authChanged {
		if err := writeJSONPreservingMode(authPath, auth); err != nil {
			return false, err
		}
	}

	deviceID, _ := auth["deviceId"].(string)

	pairedData, err := os.ReadFile(pairedPath)
	if err != nil {
		if os.IsNotExist(err) {
			return authChanged, nil
		}
		return authChanged, err
	}
	var paired map[string]any
	if err := json.Unmarshal(pairedData, &paired); err != nil {
		return authChanged, fmt.Errorf("parse %s: %w", pairedPath, err)
	}

	pairedChanged := false
	if deviceID != "" {
		if entry, ok := paired[deviceID].(map[string]any); ok {
			pairedChanged = mergePairedEntryScopes(entry)
		}
	}
	if pairedChanged {
		if err := writeJSONPreservingMode(pairedPath, paired); err != nil {
			return authChanged, err
		}
	}

	return authChanged || pairedChanged, nil
}

// mergePairedEntryScopes keeps scopes, approvedScopes, and
// tokens.operator.scopes in sync — the gateway checks different ones at
// different handshake phases, so skipping one leaves the entry half-broken.
func mergePairedEntryScopes(entry map[string]any) bool {
	changed := false
	for _, field := range []string{"scopes", "approvedScopes"} {
		if merged, ch := mergeScopeList(entry[field]); ch {
			entry[field] = merged
			changed = true
		}
	}
	if tokens, ok := entry["tokens"].(map[string]any); ok {
		if op, ok := tokens["operator"].(map[string]any); ok {
			if merged, ch := mergeScopeList(op["scopes"]); ch {
				op["scopes"] = merged
				changed = true
			}
		}
	}
	return changed
}

// mergeScopeList returns the sorted union of existing scopes and the
// required set. Extra scopes outside the required set (e.g. operator.pairing)
// are preserved.
func mergeScopeList(raw any) ([]any, bool) {
	existing, _ := raw.([]any)
	set := make(map[string]struct{}, len(existing)+len(openClawRequiredOperatorScopes))
	for _, v := range existing {
		if s, ok := v.(string); ok {
			set[s] = struct{}{}
		}
	}
	added := false
	for _, s := range openClawRequiredOperatorScopes {
		if _, ok := set[s]; !ok {
			set[s] = struct{}{}
			added = true
		}
	}
	if !added {
		return existing, false
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]any, len(keys))
	for i, k := range keys {
		out[i] = k
	}
	return out, true
}

// writeJSONPreservingMode keeps the file's existing permission bits; 0600
// fallback is deliberate — device-auth.json carries a bearer token.
func writeJSONPreservingMode(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	return os.WriteFile(path, append(data, '\n'), mode)
}
