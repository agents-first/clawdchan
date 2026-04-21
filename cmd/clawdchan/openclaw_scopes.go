package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Without operator.write + operator.admin the gateway closes the subagent
// WS with 1008 "pairing required" and no tools are reachable.
var openClawRequiredOperatorScopes = []string{
	"operator.admin",
	"operator.read",
	"operator.write",
}

// ensureOpenClawOperatorScopes patches the local operator token written by
// OpenClaw (~/.openclaw/identity/device-auth.json and the matching entry in
// ~/.openclaw/devices/paired.json) so it carries the scopes subagents need.
// Idempotent. Returns true only if a file was rewritten — the caller uses
// that to decide whether the gateway must be restarted. Missing files are
// treated as "nothing to do" (device may not be paired yet).
func ensureOpenClawOperatorScopes() (bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, err
	}
	authPath := filepath.Join(home, ".openclaw", "identity", "device-auth.json")
	pairedPath := filepath.Join(home, ".openclaw", "devices", "paired.json")

	auth, err := readJSONMap(authPath)
	if err != nil || auth == nil {
		return false, err
	}
	authChanged := patchOperatorTokenScopes(auth)
	if authChanged {
		if err := writeJSONMap(authPath, auth); err != nil {
			return false, err
		}
	}

	deviceID, _ := auth["deviceId"].(string)
	paired, err := readJSONMap(pairedPath)
	if err != nil || paired == nil || deviceID == "" {
		return authChanged, err
	}
	entry, _ := paired[deviceID].(map[string]any)
	if entry == nil {
		return authChanged, nil
	}

	// The gateway checks scopes, approvedScopes, and tokens.operator.scopes
	// at different handshake phases — keep all three in sync.
	pairedChanged := patchOperatorTokenScopes(entry)
	for _, f := range []string{"scopes", "approvedScopes"} {
		if merged, ch := mergeScopeList(entry[f]); ch {
			entry[f] = merged
			pairedChanged = true
		}
	}
	if pairedChanged {
		if err := writeJSONMap(pairedPath, paired); err != nil {
			return authChanged, err
		}
	}
	return authChanged || pairedChanged, nil
}

// patchOperatorTokenScopes updates m.tokens.operator.scopes in place.
func patchOperatorTokenScopes(m map[string]any) bool {
	tokens, _ := m["tokens"].(map[string]any)
	op, _ := tokens["operator"].(map[string]any)
	if op == nil {
		return false
	}
	merged, ch := mergeScopeList(op["scopes"])
	if ch {
		op["scopes"] = merged
	}
	return ch
}

// mergeScopeList returns the sorted union of existing scopes and the
// required set. Extras (e.g. operator.pairing) are preserved.
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

func readJSONMap(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return out, nil
}

// writeJSONMap preserves the file's existing permission bits; the 0600
// fallback is deliberate — device-auth.json carries a bearer token.
func writeJSONMap(path string, v any) error {
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
