// internal/control/janitor.go
package control

import (
	"context"
	"log"
	"time"
)

// DeadProxyAccount 是绑在死代理上的活跃账号（携带死代理国家码，供同国重绑）。
type DeadProxyAccount struct {
	JID string
	CC  string
}

type JanitorConfig struct {
	Batch int // 单轮处理上限
}

// JanitorDeps 注入回调，保持 control 零引擎依赖。
type JanitorDeps struct {
	ListDeadProxyAccounts func(ctx context.Context, limit int) ([]DeadProxyAccount, error)
	Release               func(ctx context.Context, jid string) error
	Evict                 func(ctx context.Context, jid string, linger time.Duration)
	RequestWarm           func(ctx context.Context, jid, cc string) error
	OnRebind              func() // 可空：每成功触发一次重绑调用一次（接指标）
}

type Janitor struct {
	cfg  JanitorConfig
	deps JanitorDeps
}

func NewJanitor(cfg JanitorConfig, deps JanitorDeps) *Janitor {
	if cfg.Batch <= 0 {
		cfg.Batch = 256
	}
	return &Janitor{cfg: cfg, deps: deps}
}

// Tick 处理一批死代理账号：解绑 → 立即断会话 → 经 warm:req 触发粘性重绑。
// 返回成功触发重绑的账号数。单账号错误跳过、不中断整批。
func (j *Janitor) Tick(ctx context.Context) (int, error) {
	accts, err := j.deps.ListDeadProxyAccounts(ctx, j.cfg.Batch)
	if err != nil {
		return 0, err
	}
	rebound := 0
	for _, a := range accts {
		// 1. 解绑死代理：让 StickyBindProxy.getBound 转 false，下次 warm 才会重挑。
		if err := j.deps.Release(ctx, a.JID); err != nil {
			log.Printf("control: janitor release %s: %v", a.JID, err)
			continue // 未成功解绑就补热会被粘性跳过 → 跳过本账号
		}
		// 2. linger=0 立即断掉跑在死代理上的活会话。
		j.deps.Evict(ctx, a.JID, 0)
		// 3. 经 warm:req 触发重绑+重连（BindProxy 挑新 alive 代理，ApplyProxy 注入）。
		if err := j.deps.RequestWarm(ctx, a.JID, a.CC); err != nil {
			log.Printf("control: janitor warm-req %s: %v", a.JID, err)
			continue
		}
		rebound++
		if j.deps.OnRebind != nil {
			j.deps.OnRebind()
		}
	}
	return rebound, nil
}

// Run 每 interval 调 Tick；interval<=0 表示禁用（直接返回）。Tick 错误记录并继续。
func (j *Janitor) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		<-ctx.Done()
		return ctx.Err()
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if n, err := j.Tick(ctx); err != nil {
				log.Printf("control: janitor tick error: %v", err)
			} else if n > 0 {
				log.Printf("control: janitor rebound %d dead-proxy accounts", n)
			}
		}
	}
}
