// Package cluster manages active account sessions and orchestrates graceful
// shutdown. The live whatsmeow connection is abstracted behind Conn and the
// advisory device lock behind DeviceLockHandle so session lifecycle, the
// Registry, and the Supervisor are all unit-testable with fakes. The only file
// that touches a real whatsmeow client is conn_whatsmeow.go.
package cluster

import (
	"context"
	"errors"
	"testing"

	"github.com/acme/wadist/internal/dispatch"
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

func TestGracefulCloseSequence(t *testing.T) {
	rc := &recordingConn{}
	sess := NewSession("jid-1", rc, &fakeLock{healthy: true})
	sess.GracefulClose(context.Background(), 0) // linger=0 skips wait

	want := []string{"present:off", "disconnect"}
	if len(rc.calls) != 2 || rc.calls[0] != want[0] || rc.calls[1] != want[1] {
		t.Fatalf("calls=%v want %v", rc.calls, want)
	}
}

type recordingSender struct{ called bool }

func (s *recordingSender) Send(_ context.Context, _, _ string, _ *dispatch.MediaHandle) (string, error) {
	s.called = true
	return "mid-1", nil
}

func TestFenceOnSendBlocksWhenUnhealthy(t *testing.T) {
	reg := NewRegistry()
	rc := &recordingConn{}
	rs := &recordingSender{}
	sess := newSessionWithSender("jid-1", rc, &fakeLock{healthy: false}, rs) // lock 不健康
	reg.Add(sess)

	sender := NewRoutingSenderWithPolicy(reg, true, TypingPolicy{})
	_, err := sender.Send(context.Background(), "jid-1", "1555000", "hi", nil)
	if !errors.Is(err, ErrLostOwnership) {
		t.Fatalf("want ErrLostOwnership, got %v", err)
	}
	if rs.called {
		t.Fatal("底层 sender 不应被调用")
	}
}

func TestTypingSequenceAroundSend(t *testing.T) {
	reg := NewRegistry()
	rc := &recordingConn{}
	rs := &recordingSender{}
	sess := newSessionWithSender("jid-2", rc, &fakeLock{healthy: true}, rs)
	reg.Add(sess)

	sender := NewRoutingSenderWithPolicy(reg, true, TypingPolicy{Min: 0, Max: 0}) // 抖动 0 便于断言
	if _, err := sender.Send(context.Background(), "jid-2", "1555111", "hi", nil); err != nil {
		t.Fatal(err)
	}
	// 期望顺序：typing:on 在 send 前、typing:off 在 send 后
	want := []string{"typing:on", "typing:off"}
	if len(rc.calls) != 2 || rc.calls[0] != want[0] || rc.calls[1] != want[1] {
		t.Fatalf("calls=%v want %v (typing 包裹 send)", rc.calls, want)
	}
	if !rs.called {
		t.Fatal("send 应发生")
	}
}

func TestRoutingSenderWarmRequestOnMiss(t *testing.T) {
	reg := NewRegistry()
	var requested string
	sender := NewRoutingSender(reg).WithWarmRequest(func(jid string) { requested = jid })
	_, err := sender.Send(context.Background(), "absent-jid", "1555", "hi", nil)
	if err == nil {
		t.Fatal("expected no-session error")
	}
	if requested != "absent-jid" {
		t.Fatalf("warmReq=%q want absent-jid", requested)
	}
}

func TestNoTypingWhenPlainRoutingSender(t *testing.T) {
	reg := NewRegistry()
	rc := &recordingConn{} // 实现 PresenceConn，但 plain sender 不应触发 typing
	rs := &recordingSender{}
	sess := newSessionWithSender("jid-3", rc, &fakeLock{healthy: true}, rs)
	reg.Add(sess)

	sender := NewRoutingSender(reg) // 旧构造 = AntifpOff 等价，typingOn=false
	if _, err := sender.Send(context.Background(), "jid-3", "1555222", "hi", nil); err != nil {
		t.Fatal(err)
	}
	// conn 能力存在，但 plain sender 不得发送任何 typing/presence 调用
	if len(rc.calls) != 0 {
		t.Fatalf("plain sender 不应产生 typing/presence 调用，got %v", rc.calls)
	}
	if !rs.called {
		t.Fatal("send 仍应发生")
	}
}
