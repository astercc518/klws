package warmup

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Clock 返回当前时间,便于测试注入。
type Clock func() time.Time

// Service 是养号业务编排:enroll / 回复信号 / 毕业评估 / 降级 / 手动控制。
// PairAndWarm(池内互发)在 scripts.go / pair.go 中扩展(P5b)。
type Service struct {
	store    *Store
	clock    Clock
	sender   Sender
	accounts AccountLookup
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

// RecordReply 累加入站回复信号。非池内号(无 profile)静默跳过 —— 底层
// IncReplies 是仅动 replies_received 一列的原子 UPDATE,对不存在的 jid 天然
// 影响 0 行,无需先 Get 判 ErrNotFound。也因此不会用陈旧快照覆盖同一时刻
// worker tick 写入的 warmup_messages_sent/stage/paused 等其它列。
func (s *Service) RecordReply(ctx context.Context, jid string) error {
	return s.store.IncReplies(ctx, jid)
}

// EvaluateAndPromote 在信号达标时把 WARMING 号升 MATURE,返回是否毕业。
// 信号判定仍需读当前快照(Get),但状态迁移落库走 PromoteCol —— 条件
// UPDATE(WHERE stage='WARMING')只动 stage/matured_at 两列,且天然防重复
// 毕业的竞态(两次并发 tick 只有一次能真正 affect 一行)。
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
	affected, err := s.store.PromoteCol(ctx, jid, s.clock())
	if err != nil {
		return false, err
	}
	return affected, nil
}

// Demote 把 MATURE 号退回 WARMING(封号信号/health 掉)。reason 仅用于日志/审计。
// 非池内号(无 profile)静默跳过,与 RecordReply 一致。保留一次轻量 Get 只是
// 为了区分 ErrNotFound(静默跳过) vs 非法迁移(WARMING/NEW 再收到 DEMOTE ->
// ErrInvalidTransition,调用方 webhook_evolution.go 已把该错误当良性吞掉)——
// 真正落库走 DemoteCol,条件 UPDATE(WHERE stage='MATURE')只动
// stage/matured_at/online_since 三列,不覆盖并发写入的其它字段。
func (s *Service) Demote(ctx context.Context, jid, reason string) error {
	p, err := s.store.Get(ctx, jid)
	if errors.Is(err, ErrNotFound) {
		return nil // 非池内号(无 profile)静默跳过,与 RecordReply 一致
	}
	if err != nil {
		return err
	}
	if _, err := Transition(p.Stage, EventDemote); err != nil {
		return err
	}
	affected, err := s.store.DemoteCol(ctx, jid, s.clock())
	if err != nil {
		return err
	}
	if !affected {
		// 竞态:Get 时还是 MATURE,落库前被另一并发 Demote 抢先改走。
		return ErrInvalidTransition
	}
	return nil
}

// SetLane 改车道(后台手动)。先校验车道合法,避免把账号写成一个
// PolicyFor 永远查不到的车道(那样后续 EvaluateAndPromote/PairAndWarm
// 都会因 ErrNotFound 卡死这个号)。落库走 SetLaneCol,仅动 lane 一列。
func (s *Service) SetLane(ctx context.Context, jid string, lane Lane) error {
	if lane != LaneFast && lane != LaneStandard {
		return fmt.Errorf("warmup: invalid lane %q", lane)
	}
	return s.store.SetLaneCol(ctx, jid, lane)
}

// SetPaused 暂停/恢复养号(后台手动)。仅动 paused 一列。
func (s *Service) SetPaused(ctx context.Context, jid string, paused bool) error {
	return s.store.SetPausedCol(ctx, jid, paused)
}

// ForcePromote 无视信号强制毕业(后台手动)。落库走 PromoteCol(条件
// UPDATE,仅动 stage/matured_at)。先 Get 是为了保留现有错误语义:
// jid 不存在 -> ErrNotFound;存在但非 WARMING -> ErrInvalidTransition。
func (s *Service) ForcePromote(ctx context.Context, jid string) error {
	p, err := s.store.Get(ctx, jid)
	if err != nil {
		return err
	}
	if _, err := Transition(p.Stage, EventPromote); err != nil {
		return err
	}
	affected, err := s.store.PromoteCol(ctx, jid, s.clock())
	if err != nil {
		return err
	}
	if !affected {
		// 竞态:Get 时还是 WARMING,落库前被另一并发 promote 抢先改走。
		return ErrInvalidTransition
	}
	return nil
}
