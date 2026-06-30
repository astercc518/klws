package control

import (
	"context"
	"math/rand"

	goredis "github.com/redis/go-redis/v9"
)

type Scheduler struct{ rdb *goredis.Client }

func NewScheduler(rdb *goredis.Client) *Scheduler { return &Scheduler{rdb: rdb} }

func dueKey(cc string) string { return "due:" + cc }

func (s *Scheduler) Enqueue(ctx context.Context, jid, cc string, nextMs int64) error {
	return s.rdb.ZAdd(ctx, dueKey(cc), goredis.Z{Score: float64(nextMs), Member: jid}).Err()
}

// PopDue 原子弹出 score<=nowMs 的最多 max 个成员（按 score 升序）。
func (s *Scheduler) PopDue(ctx context.Context, cc string, nowMs int64, max int) ([]string, error) {
	res, err := popDueLua.Run(ctx, s.rdb, []string{dueKey(cc)}, nowMs, max).StringSlice()
	if err != nil {
		return nil, err
	}
	return res, nil
}

// KEYS=[due:{cc}] ARGV=[nowMs, max]
var popDueLua = goredis.NewScript(`
local due = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', ARGV[1], 'LIMIT', 0, tonumber(ARGV[2]))
if #due > 0 then redis.call('ZREM', KEYS[1], unpack(due)) end
return due`)

// SeedDue 启动播种：把账号按打散相位写入各自国家队列。
type AccountCC struct {
	JID, CC string
	NextMs  int64
}

func (s *Scheduler) SeedDue(ctx context.Context, accts []AccountCC) error {
	pipe := s.rdb.Pipeline()
	for _, a := range accts {
		pipe.ZAdd(ctx, dueKey(a.CC), goredis.Z{Score: float64(a.NextMs), Member: a.JID})
	}
	_, err := pipe.Exec(ctx)
	return err
}

type Window struct{ StartHour, EndHour int }

func (w Window) SecondsActive() int64 { return int64((w.EndHour - w.StartHour)) * 3600 }

// NextEligibleMs 均匀基距 ± 40% 抖动，硬下限 60s。窗外顺延逻辑由调用方处理。
func NextEligibleMs(nowMs int64, quotaToday int, w Window, rng *rand.Rand) int64 {
	if quotaToday < 1 {
		quotaToday = 1
	}
	base := w.SecondsActive() / int64(quotaToday)
	jitter := int64(float64(base) * (rng.Float64()*0.8 - 0.4))
	delta := base + jitter
	if delta < 60 {
		delta = 60
	}
	return nowMs + delta*1000
}
