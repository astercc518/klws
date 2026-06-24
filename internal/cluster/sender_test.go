package cluster

import (
	"context"
	"errors"
	"testing"

	"github.com/acme/wadist/internal/dispatch"
)

// sessionSender 把一个可发送会话塞进 Session 以便 RoutingSender 路由（测试用）。
// 生产中由 waConn 提供发送能力；此处用 fake。
type fakeSendConn struct {
	fakeConn
	sendErr error
	id      string
	gotJID  string
}

func TestRoutingSender_UnknownJID(t *testing.T) {
	reg := NewRegistry()
	rs := NewRoutingSender(reg)
	_, err := rs.Send(context.Background(), "jid-x", "+100", "hi", nil)
	if err == nil {
		t.Fatal("expected error for unknown jid (must not silently succeed)")
	}
}

func TestRoutingSender_RoutesToSession(t *testing.T) {
	reg := NewRegistry()
	sc := &fakeSendConn{id: "wamid.1"}
	// Session 必须能对外暴露其发送能力；见实现说明（Session 持有可选 SessionSender）。
	s := newSessionWithSender("jid-1", sc, &fakeLock{healthy: true}, sc)
	reg.Add(s)
	rs := NewRoutingSender(reg)
	id, err := rs.Send(context.Background(), "jid-1", "+100", "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if id != "wamid.1" {
		t.Fatalf("got id %q", id)
	}
	_ = errors.New
	_ = dispatch.MediaHandle{}
}

func (f *fakeSendConn) Send(_ context.Context, phone, body string, _ *dispatch.MediaHandle) (string, error) {
	if f.sendErr != nil {
		return "", f.sendErr
	}
	return f.id, nil
}
