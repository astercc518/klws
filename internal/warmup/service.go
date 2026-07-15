package warmup

import (
	"context"
	"errors"
	"time"
)

// Clock 返回当前时间,便于测试注入。
type Clock func() time.Time

// Service 是养号业务编排:enroll / 回复信号 / 毕业评估 / 降级 / 手动控制。
// PairAndWarm(池内互发)在 scripts.go / pair.go 中扩展(P5b)。
type Service struct {
	store *Store
	clock Clock
}

func NewService(store *Store, clock Clock) *Service {
	return &Service{store: store, clock: clock}
}

// Store exposes the underlying persistence layer for admin API handlers/tests
// that need to read rows the Service's own methods don't surface (list/overview/policies).
func (s *Service) Store() *Store { return s.store }

// Enroll 让新号进 WARMING。幂等(已有 profile 不动)。
func (s *Service) Enroll(ctx context.Context, jid string, tenantID int64, lane Lane) error {
	return s.store.EnrollIfAbsent(ctx, jid, tenantID, lane, s.clock())
}

// RecordReply 累加入站回复信号。非池内号(无 profile)静默跳过。
func (s *Service) RecordReply(ctx context.Context, jid string) error {
	p, err := s.store.Get(ctx, jid)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	p.RepliesReceived++
	return s.store.Save(ctx, p, s.clock())
}

// EvaluateAndPromote 在信号达标时把 WARMING 号升 MATURE,返回是否毕业。
func (s *Service) EvaluateAndPromote(ctx context.Context, jid string) (bool, error) {
	p, err := s.store.Get(ctx, jid)
	if err != nil {
		return false, err
	}
	if p.Stage != StageWarming {
		return false, nil
	}
	pol, err := s.store.PolicyFor(ctx, p.Lane)
	if err != nil {
		return false, err
	}
	var onlineHours float64
	if p.OnlineSince != nil {
		onlineHours = s.clock().Sub(*p.OnlineSince).Hours()
	}
	if !MeetsPromotion(p.WarmupMessagesSent, p.RepliesReceived, onlineHours, pol) {
		return false, nil
	}
	next, err := Transition(p.Stage, EventPromote)
	if err != nil {
		return false, err
	}
	now := s.clock()
	p.Stage = next
	p.MaturedAt = &now
	if err := s.store.Save(ctx, p, now); err != nil {
		return false, err
	}
	return true, nil
}

// Demote 把 MATURE 号退回 WARMING(封号信号/health 掉)。reason 仅用于日志/审计。
func (s *Service) Demote(ctx context.Context, jid, reason string) error {
	p, err := s.store.Get(ctx, jid)
	if err != nil {
		return err
	}
	next, err := Transition(p.Stage, EventDemote)
	if err != nil {
		return err
	}
	now := s.clock()
	p.Stage = next
	p.MaturedAt = nil
	p.OnlineSince = &now // 重置在线基准,重新计时养号
	return s.store.Save(ctx, p, now)
}

// SetLane 改车道(后台手动)。
func (s *Service) SetLane(ctx context.Context, jid string, lane Lane) error {
	p, err := s.store.Get(ctx, jid)
	if err != nil {
		return err
	}
	p.Lane = lane
	return s.store.Save(ctx, p, s.clock())
}

// SetPaused 暂停/恢复养号(后台手动)。
func (s *Service) SetPaused(ctx context.Context, jid string, paused bool) error {
	p, err := s.store.Get(ctx, jid)
	if err != nil {
		return err
	}
	p.Paused = paused
	return s.store.Save(ctx, p, s.clock())
}

// ForcePromote 无视信号强制毕业(后台手动)。
func (s *Service) ForcePromote(ctx context.Context, jid string) error {
	p, err := s.store.Get(ctx, jid)
	if err != nil {
		return err
	}
	next, err := Transition(p.Stage, EventPromote)
	if err != nil {
		return err
	}
	now := s.clock()
	p.Stage = next
	p.MaturedAt = &now
	return s.store.Save(ctx, p, now)
}
