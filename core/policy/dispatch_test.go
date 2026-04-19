package policy

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestParseOutcome verifies every branch of the stdout-to-outcome decoder
// without spawning a subprocess: empty, bad JSON, declined, answer with
// various intents, intent coercion.
func TestParseOutcome(t *testing.T) {
	cases := []struct {
		name          string
		in            string
		wantDecline   bool
		wantReply     string
		wantIntent    string
		wantCollab    bool
		wantReasonSub string
	}{
		{
			name:        "empty",
			in:          "",
			wantDecline: true, wantReasonSub: "empty stdout",
		},
		{
			name:        "whitespace only",
			in:          "   \n\t",
			wantDecline: true, wantReasonSub: "empty stdout",
		},
		{
			name:        "not json",
			in:          "hello, i am not json",
			wantDecline: true, wantReasonSub: "not JSON",
		},
		{
			name:        "explicit decline",
			in:          `{"declined":"i refuse"}`,
			wantDecline: true, wantReasonSub: "i refuse",
		},
		{
			name:        "empty object",
			in:          `{}`,
			wantDecline: true, wantReasonSub: "neither answer nor decline",
		},
		{
			name:       "answer defaults to ask",
			in:         `{"answer":"42"}`,
			wantReply:  "42",
			wantIntent: "ask",
		},
		{
			name:       "answer with say intent",
			in:         `{"answer":"done","intent":"say"}`,
			wantReply:  "done",
			wantIntent: "say",
		},
		{
			name:       "answer with collab",
			in:         `{"answer":"one more round","collab":true}`,
			wantReply:  "one more round",
			wantIntent: "ask",
			wantCollab: true,
		},
		{
			name:       "ask_human intent silently coerced to ask",
			in:         `{"answer":"secret?","intent":"ask_human"}`,
			wantReply:  "secret?",
			wantIntent: "ask",
		},
		{
			name:       "notify_human intent silently coerced to ask",
			in:         `{"answer":"fyi","intent":"notify_human"}`,
			wantReply:  "fyi",
			wantIntent: "ask",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseOutcome([]byte(c.in))
			if err != nil {
				t.Fatalf("parseOutcome err=%v", err)
			}
			if got.Declined != c.wantDecline {
				t.Fatalf("declined=%v want=%v (reason=%q)", got.Declined, c.wantDecline, got.DeclineReason)
			}
			if c.wantReasonSub != "" && !strings.Contains(got.DeclineReason, c.wantReasonSub) {
				t.Fatalf("decline reason %q missing %q", got.DeclineReason, c.wantReasonSub)
			}
			if got.Reply != c.wantReply {
				t.Fatalf("reply=%q want=%q", got.Reply, c.wantReply)
			}
			if got.Intent != c.wantIntent && !c.wantDecline {
				t.Fatalf("intent=%q want=%q", got.Intent, c.wantIntent)
			}
			if got.Collab != c.wantCollab {
				t.Fatalf("collab=%v want=%v", got.Collab, c.wantCollab)
			}
		})
	}
}

// TestEnabled covers the enable-gate logic — nil receiver, empty Command,
// and a populated Command all behave correctly.
func TestEnabled(t *testing.T) {
	var nilD *SubprocessDispatcher
	if nilD.Enabled() {
		t.Fatalf("nil dispatcher reports enabled")
	}
	empty := &SubprocessDispatcher{}
	if empty.Enabled() {
		t.Fatalf("empty-command dispatcher reports enabled")
	}
	emptyFirst := &SubprocessDispatcher{Command: []string{""}}
	if emptyFirst.Enabled() {
		t.Fatalf("dispatcher with empty first arg reports enabled")
	}
	ok := &SubprocessDispatcher{Command: []string{"/bin/true"}}
	if !ok.Enabled() {
		t.Fatalf("populated-command dispatcher reports disabled")
	}
}

// TestDispatchSubprocess spawns real subprocesses to verify the end-to-end
// stdin/stdout contract. Skipped on Windows because the test scripts are
// POSIX shell and /bin/sh isn't available there.
func TestDispatchSubprocess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts needed for this test aren't portable to windows")
	}
	dir := t.TempDir()

	ok := writeScript(t, dir, "ok.sh", `#!/bin/sh
read _ # discard stdin — we don't need it for this test
echo '{"answer":"hello back","intent":"say"}'
`)
	noread := writeScript(t, dir, "noread.sh", `#!/bin/sh
# Does not consume stdin. Dispatch should still complete; Go closes stdin on our end.
echo '{"answer":"ignored stdin","collab":true}'
`)
	failing := writeScript(t, dir, "fail.sh", `#!/bin/sh
echo "boom" 1>&2
exit 2
`)
	silent := writeScript(t, dir, "silent.sh", `#!/bin/sh
exit 0
`)
	slow := writeScript(t, dir, "slow.sh", `#!/bin/sh
sleep 5
echo '{"answer":"late"}'
`)

	req := DispatchRequest{
		Ask:  DispatchEnvelope{Text: "Hi!"},
		Peer: DispatchPeer{Alias: "Alice"},
		Self: DispatchSelf{Alias: "me"},
	}

	t.Run("ok", func(t *testing.T) {
		d := &SubprocessDispatcher{Command: []string{ok}, Timeout: 5 * time.Second}
		out, err := d.Dispatch(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if out.Declined {
			t.Fatalf("unexpected decline: %s", out.DeclineReason)
		}
		if out.Reply != "hello back" {
			t.Fatalf("reply=%q", out.Reply)
		}
		if out.Intent != "say" {
			t.Fatalf("intent=%q", out.Intent)
		}
	})

	t.Run("script ignores stdin", func(t *testing.T) {
		d := &SubprocessDispatcher{Command: []string{noread}, Timeout: 5 * time.Second}
		out, err := d.Dispatch(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if out.Reply != "ignored stdin" || !out.Collab {
			t.Fatalf("outcome=%+v", out)
		}
	})

	t.Run("nonzero exit declines", func(t *testing.T) {
		d := &SubprocessDispatcher{Command: []string{failing}, Timeout: 5 * time.Second}
		out, _ := d.Dispatch(context.Background(), req)
		if !out.Declined {
			t.Fatalf("expected decline, got %+v", out)
		}
		if !strings.Contains(out.DeclineReason, "boom") {
			t.Fatalf("expected stderr 'boom' in decline reason: %q", out.DeclineReason)
		}
	})

	t.Run("silent exit 0 declines", func(t *testing.T) {
		d := &SubprocessDispatcher{Command: []string{silent}, Timeout: 5 * time.Second}
		out, _ := d.Dispatch(context.Background(), req)
		if !out.Declined {
			t.Fatalf("expected decline on silent output, got %+v", out)
		}
	})

	t.Run("timeout declines", func(t *testing.T) {
		d := &SubprocessDispatcher{Command: []string{slow}, Timeout: 300 * time.Millisecond}
		out, _ := d.Dispatch(context.Background(), req)
		if !out.Declined {
			t.Fatalf("expected decline on timeout, got %+v", out)
		}
	})

	t.Run("hop ceiling declines without spawning", func(t *testing.T) {
		d := &SubprocessDispatcher{Command: []string{ok}, MaxCollabRounds: 3}
		hopReq := req
		hopReq.Policy.CollabRounds = 4
		out, _ := d.Dispatch(context.Background(), hopReq)
		if !out.Declined {
			t.Fatalf("expected decline on hop ceiling, got %+v", out)
		}
		if !strings.Contains(out.DeclineReason, "ceiling") {
			t.Fatalf("expected ceiling in decline reason: %q", out.DeclineReason)
		}
	})
}

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}
