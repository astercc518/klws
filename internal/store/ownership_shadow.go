// internal/store/ownership_shadow.go
package store

import (
	"context"
	"time"
)

// shadowOwnership 以 authoritative(PG) 为准返回结果，同时影子运行 shadow(Redis)
// 并比对决策；不一致时调 onDiverge(op)。用于零风险灰度验证 Redis 逻辑。
type shadowOwnership struct {
	auth, shadow Ownership
	onDiverge    func(op string)
}

func newShadowOwnership(auth, shadow Ownership, onDiverge func(op string)) *shadowOwnership {
	if onDiverge == nil {
		onDiverge = func(string) {}
	}
	return &shadowOwnership{auth: auth, shadow: shadow, onDiverge: onDiverge}
}

func (s *shadowOwnership) Acquire(ctx context.Context, jid, nodeID string) (LockHandle, error) {
	authH, authErr := s.auth.Acquire(ctx, jid, nodeID)
	_, shErr := s.shadow.Acquire(ctx, jid, nodeID)
	if (authErr == nil) != (shErr == nil) {
		s.onDiverge("acquire")
	}
	return authH, authErr // 始终返回权威结果
}

func (s *shadowOwnership) Heartbeat(ctx context.Context, nodeID string) error {
	_ = s.shadow.Heartbeat(ctx, nodeID)
	return s.auth.Heartbeat(ctx, nodeID)
}

func (s *shadowOwnership) Deregister(ctx context.Context, nodeID string) error {
	_ = s.shadow.Deregister(ctx, nodeID)
	return s.auth.Deregister(ctx, nodeID)
}

func (s *shadowOwnership) StaleOwned(ctx context.Context, st time.Duration) ([]string, error) {
	a, err := s.auth.StaleOwned(ctx, st)
	if sh, e2 := s.shadow.StaleOwned(ctx, st); e2 == nil && len(sh) != len(a) {
		s.onDiverge("stale")
	}
	return a, err
}

func (s *shadowOwnership) Unowned(ctx context.Context) ([]string, error) {
	a, err := s.auth.Unowned(ctx)
	if sh, e2 := s.shadow.Unowned(ctx); e2 == nil && len(sh) != len(a) {
		s.onDiverge("unowned")
	}
	return a, err
}
