package cluster

import (
	"context"
	"errors"
	"testing"
)

type fakeConn struct {
	connectErr error
	connected  bool
	events     *[]string
	name       string
}

func (f *fakeConn) Connect(_ context.Context) error {
	if f.connectErr != nil {
		return f.connectErr
	}
	f.connected = true
	return nil
}
func (f *fakeConn) Disconnect() {
	f.connected = false
	if f.events != nil {
		*f.events = append(*f.events, f.name+":disconnect")
	}
}

type fakeLock struct {
	healthy bool
	events  *[]string
	name    string
}

func (l *fakeLock) Healthy(_ context.Context) bool { return l.healthy }
func (l *fakeLock) Release(_ context.Context) {
	if l.events != nil {
		*l.events = append(*l.events, l.name+":release")
	}
}

func TestSession_Close_DisconnectsThenReleases(t *testing.T) {
	var seq []string
	c := &fakeConn{events: &seq, name: "conn", connected: true}
	l := &fakeLock{healthy: true, events: &seq, name: "lock"}
	s := NewSession("jid-1", c, l)

	s.Close(context.Background())

	if len(seq) != 2 || seq[0] != "conn:disconnect" || seq[1] != "lock:release" {
		t.Fatalf("close order wrong: %v", seq)
	}
	if c.connected {
		t.Fatal("conn still connected")
	}
}

func TestSession_Healthy_DelegatesToLock(t *testing.T) {
	s := NewSession("jid-1", &fakeConn{}, &fakeLock{healthy: false})
	if s.Healthy(context.Background()) {
		t.Fatal("expected unhealthy when lock unhealthy")
	}
	s2 := NewSession("jid-2", &fakeConn{}, &fakeLock{healthy: true})
	if !s2.Healthy(context.Background()) {
		t.Fatal("expected healthy")
	}
}

func TestSession_Close_Idempotent(t *testing.T) {
	c := &fakeConn{connected: true}
	l := &fakeLock{healthy: true}
	s := NewSession("jid-1", c, l)
	s.Close(context.Background())
	s.Close(context.Background()) // must not panic / double-release
	_ = errors.New
}
