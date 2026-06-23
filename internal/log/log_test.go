package log

import (
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestZapAdapter_LogsAndSubTagsModule(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	l := New(zap.New(core))

	l.Infof("hello %s", "world")
	l.Sub("store").Warnf("locked %d", 7)

	entries := logs.All()
	if len(entries) != 2 {
		t.Fatalf("got %d log entries, want 2", len(entries))
	}
	if entries[0].Message != "hello world" {
		t.Fatalf("entry0 msg = %q, want %q", entries[0].Message, "hello world")
	}
	// Sub("store") must attach a module field.
	got := entries[1].ContextMap()["module"]
	if got != "store" {
		t.Fatalf("entry1 module field = %v, want %q", got, "store")
	}
}
