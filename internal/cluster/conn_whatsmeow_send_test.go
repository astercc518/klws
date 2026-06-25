package cluster

import (
	"context"
	"testing"

	"github.com/acme/wadist/internal/dispatch"
)

// The live text path needs a real device+proxy and is validated manually
// (white-paper Week-2). The one thing we CAN assert in-process is that media
// sends are rejected (Module A is text-only) — and that *waConn satisfies
// SessionSender at compile time (the var _ assertion in conn_whatsmeow.go).
func TestWAConnRejectsMedia(t *testing.T) {
	c := &waConn{} // nil client is fine: media check happens before any client use
	_, err := c.Send(context.Background(), "15551234567", "hi", &dispatch.MediaHandle{})
	if err == nil {
		t.Fatal("expected media send to be rejected in Module A")
	}
}
