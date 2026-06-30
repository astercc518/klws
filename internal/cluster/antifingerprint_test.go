// Package cluster manages active account sessions and orchestrates graceful
// shutdown. The live whatsmeow connection is abstracted behind Conn and the
// advisory device lock behind DeviceLockHandle so session lifecycle, the
// Registry, and the Supervisor are all unit-testable with fakes. The only file
// that touches a real whatsmeow client is conn_whatsmeow.go.
package cluster

import (
	"context"
	"testing"
)

type recordingConn struct {
	connected bool
	calls     []string
}

func (c *recordingConn) Connect(context.Context) error { c.connected = true; return nil }
func (c *recordingConn) Disconnect()                   { c.calls = append(c.calls, "disconnect") }
func (c *recordingConn) SetPresence(_ context.Context, available bool) error {
	if available {
		c.calls = append(c.calls, "present:on")
	} else {
		c.calls = append(c.calls, "present:off")
	}
	return nil
}
func (c *recordingConn) SendTyping(_ context.Context, _ string, composing bool) error {
	if composing {
		c.calls = append(c.calls, "typing:on")
	} else {
		c.calls = append(c.calls, "typing:off")
	}
	return nil
}

func TestRecordingConnSatisfiesPresenceConn(t *testing.T) {
	var _ Conn = (*recordingConn)(nil)
	var _ PresenceConn = (*recordingConn)(nil)
}
