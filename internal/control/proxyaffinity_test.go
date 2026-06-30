package control

import (
	"context"
	"testing"
)

func TestProxyAffinityStickiness(t *testing.T) {
	ctx := context.Background()
	bindCalls := 0
	bound := map[string]bool{"already": true}
	sticky := StickyBindProxy(
		func(_ context.Context, jid string) (bool, error) { return bound[jid], nil },
		func(_ context.Context, jid, cc string) error { bindCalls++; bound[jid] = true; return nil },
	)
	_ = sticky(ctx, "already", "US") // 已绑 → 不 bind
	_ = sticky(ctx, "fresh", "US")   // 未绑 → bind 一次
	_ = sticky(ctx, "fresh", "US")   // 现已绑 → 不再 bind
	if bindCalls != 1 { t.Fatalf("bindCalls=%d want 1 (粘性)", bindCalls) }
}
