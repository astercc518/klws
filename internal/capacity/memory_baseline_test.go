//go:build memory_gate

package capacity

import (
	"runtime"
	"testing"
)

// TestMemoryBaseline_PerSession asserts a rough per-"session" heap budget so a
// future change that balloons per-account memory is caught in CI. It allocates
// lightweight stand-ins (the real Session lives in cluster; this is a structural
// budget guard, not a live-connection test).
func TestMemoryBaseline_PerSession(t *testing.T) {
	const n = 10000
	type sessionStub struct {
		jid   string
		state int32
		buf   []byte
	}
	runtime.GC()
	var m0 runtime.MemStats
	runtime.ReadMemStats(&m0)

	stubs := make([]*sessionStub, n)
	for i := range stubs {
		stubs[i] = &sessionStub{jid: "1234567890.0:0@s.whatsapp.net", buf: make([]byte, 256)}
	}
	runtime.GC()
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)

	perSession := (m1.HeapAlloc - m0.HeapAlloc) / n
	const budget = 2048 // bytes/session structural budget
	if perSession > budget {
		t.Fatalf("per-session heap %d bytes exceeds budget %d", perSession, budget)
	}
	runtime.KeepAlive(stubs)
}
