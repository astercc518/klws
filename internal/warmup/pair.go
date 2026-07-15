package warmup

import (
	"context"
	"math/rand"
	"time"
)

// Sender 抽象 Evolution 发送(SendText + 打字态)。
//
// NOTE: 签名对齐真实 internal/cluster.EvoClient(SendText 返回 SendResult,
// SendTyping 用 composing bool 而非 durationMs),但为保持 warmup 包不 import
// cluster,这里用 error-only 返回值 + composing bool 的瘦接口;Task 15 的适配器
// 负责桥接 *EvoClient → Sender(丢弃 SendResult)。
type Sender interface {
	SendText(ctx context.Context, instanceName, to, text string) error
	SendTyping(ctx context.Context, instanceName, to string, composing bool) error
}

// Account 是养号互发所需的账号连接信息。
type Account struct {
	JID          string
	InstanceName string
	PhoneNumber  string
	Lang         string
	Online       bool
}

// AccountLookup 解析 jid → 连接信息(由 store.Manager 实现,见 Task 15 适配器)。
type AccountLookup interface {
	WarmupAccount(ctx context.Context, jid string) (Account, error)
}

// Sleeper 抽象抖动等待,测试注入 no-op。
type Sleeper func(d time.Duration)

// jitter 返回 3–90s 的随机间隔。
func jitter(rng *rand.Rand) time.Duration {
	return time.Duration(3+rng.Intn(88)) * time.Second
}

// WithSender/WithAccounts 注入 PairAndWarm 依赖(不破坏 NewService 签名)。
func (s *Service) WithSender(sender Sender) *Service     { s.sender = sender; return s }
func (s *Service) WithAccounts(l AccountLookup) *Service { s.accounts = l; return s }

// PairAndWarm 拉 WARMING 池,过滤(ONLINE + 未暂停 + 当日未超 warmingCap),两两配对,
// 每对随机挑脚本、逐句带打字态与随机抖动交替发送,并 bump 计数。返回配对数与消息数。
func (s *Service) PairAndWarm(ctx context.Context, batch int, rng *rand.Rand, sleep Sleeper) (int, int, error) {
	if s.sender == nil || s.accounts == nil {
		return 0, 0, nil // 未注入依赖(P5a-only 运行),空跑
	}
	profiles, err := s.store.ListByStage(ctx, StageWarming, batch)
	if err != nil {
		return 0, 0, err
	}
	now := s.clock()
	today := now.Truncate(24 * time.Hour)

	type eligible struct {
		p   Profile
		acc Account
	}
	var pool []eligible
	for _, p := range profiles {
		if p.Paused {
			continue
		}
		acc, err := s.accounts.WarmupAccount(ctx, p.AccountJID)
		if err != nil || !acc.Online {
			continue
		}
		pol, err := s.store.PolicyFor(ctx, p.Lane)
		if err != nil {
			continue
		}
		sentToday := p.WarmupSentToday
		if p.WarmupSentDate == nil || !p.WarmupSentDate.Equal(today) {
			sentToday = 0
		}
		if sentToday >= DailyCap(pol, StageWarming, 0) {
			continue
		}
		pool = append(pool, eligible{p: p, acc: acc})
	}

	pairs, messages := 0, 0
	for i := 0; i+1 < len(pool); i += 2 {
		a, b := pool[i], pool[i+1]
		scripts, err := s.store.LoadScripts(ctx, a.acc.Lang)
		if err != nil || len(scripts) == 0 {
			continue // 无匹配脚本:跳过(不 fallback 到写死单句)
		}
		sc, ok := PickScript(scripts, rng)
		if !ok {
			continue
		}
		for _, turn := range sc.Turns {
			var fromAcc, toAcc Account
			if turn.From == "A" {
				fromAcc, toAcc = a.acc, b.acc
			} else {
				fromAcc, toAcc = b.acc, a.acc
			}
			text := RenderTurn(turn, rng)
			_ = s.sender.SendTyping(ctx, fromAcc.InstanceName, toAcc.PhoneNumber, true)
			sleep(jitter(rng))
			if err := s.sender.SendText(ctx, fromAcc.InstanceName, toAcc.PhoneNumber, text); err != nil {
				continue // 单句失败跳过,不中断整批
			}
			messages++
		}
		s.bumpSent(ctx, a.p, today, now)
		s.bumpSent(ctx, b.p, today, now)
		pairs++
	}
	return pairs, messages, nil
}

// bumpSent 累加养号发送计数(warmup_messages_sent + 当日 warmup_sent_today)。
func (s *Service) bumpSent(ctx context.Context, p Profile, today, now time.Time) {
	if p.WarmupSentDate == nil || !p.WarmupSentDate.Equal(today) {
		p.WarmupSentToday = 0
	}
	p.WarmupMessagesSent++
	p.WarmupSentToday++
	d := today
	p.WarmupSentDate = &d
	_ = s.store.Save(ctx, p, now)
}
