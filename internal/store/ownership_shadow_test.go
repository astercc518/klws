// internal/store/ownership_shadow_test.go
package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeOwnership struct {
	acquireErr error
	healthy    bool
}

func (f *fakeOwnership) Acquire(ctx context.Context, jid, node string) (LockHandle, error) {
	if f.acquireErr != nil {
		return nil, f.acquireErr
	}
	return fakeHandle{healthy: f.healthy}, nil
}
func (f *fakeOwnership) Heartbeat(context.Context, string) error        { return nil }
func (f *fakeOwnership) Deregister(context.Context, string) error       { return nil }
func (f *fakeOwnership) StaleOwned(context.Context, time.Duration) ([]string, error) {
	return nil, nil
}
func (f *fakeOwnership) Unowned(context.Context) ([]string, error) { return nil, nil }

type fakeHandle struct{ healthy bool }

func (h fakeHandle) Healthy(context.Context) bool { return h.healthy }
func (h fakeHandle) Release(context.Context)       {}

func TestShadowAuthoritativeAndDivergence(t *testing.T) {
	ctx := context.Background()
	var diverged []string
	// PG 授权成功；Redis 影子失败 → 记一次 acquire 分歧，但返回 PG 结果(成功)
	s := newShadowOwnership(
		&fakeOwnership{acquireErr: nil},
		&fakeOwnership{acquireErr: errors.New("redis boom")},
		func(op string) { diverged = append(diverged, op) },
	)
	h, err := s.Acquire(ctx, "jid", "node")
	if err != nil || h == nil {
		t.Fatalf("authoritative(PG) success must propagate: %v", err)
	}
	if len(diverged) != 1 || diverged[0] != "acquire" {
		t.Fatalf("expected one acquire divergence, got %v", diverged)
	}
}
