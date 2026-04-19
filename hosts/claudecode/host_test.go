package claudecode

import (
	"context"
	"errors"
	"testing"

	"github.com/vMaroon/ClawdChan/core/envelope"
	"github.com/vMaroon/ClawdChan/core/surface"
)

func TestHumanSurfaceAskReturnsErrAsyncReply(t *testing.T) {
	_, err := (HumanSurface{}).Ask(context.Background(), envelope.ThreadID{}, envelope.Envelope{})
	if !errors.Is(err, surface.ErrAsyncReply) {
		t.Fatalf("Ask() error = %v, want errors.Is(..., surface.ErrAsyncReply)", err)
	}
}
