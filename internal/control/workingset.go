package control

import (
	"context"
	"log"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

type Config struct {
	Target          int
	WarmReqBatch    int
	KeepWarmHorizon time.Duration
	Linger          time.Duration
}

type Deps struct {
	Warm      func(ctx context.Context, jid string) (bool, error)
	Evict     func(ctx context.Context, jid string, linger time.Duration)
	IsWarm    func(jid string) bool
	BindProxy func(ctx context.Context, jid, cc string) error
}

type WorkingSet struct {
	rdb   *goredis.Client
	sched *Scheduler
	ccs   []string
	cfg   Config
	deps  Deps
}

func NewWorkingSet(rdb *goredis.Client, sched *Scheduler, ccs []string, cfg Config, deps Deps) *WorkingSet {
	return &WorkingSet{rdb: rdb, sched: sched, ccs: ccs, cfg: cfg, deps: deps}
}

const residentKey = "ctl:resident"
const warmReqKey = "warm:req"

func splitReq(v string) (jid, cc string) {
	for i := 0; i < len(v); i++ {
		if v[i] == '|' {
			return v[:i], v[i+1:]
		}
	}
	return v, ""
}

func (w *WorkingSet) residentCount(ctx context.Context) int {
	n, _ := w.rdb.SCard(ctx, residentKey).Result()
	return int(n)
}

func (w *WorkingSet) warm(ctx context.Context, jid, cc string) {
	_ = w.deps.BindProxy(ctx, jid, cc)
	ok, err := w.deps.Warm(ctx, jid)
	if err == nil && ok {
		w.rdb.SAdd(ctx, residentKey, jid)
	}
}

// Run starts a ticker loop that calls Tick on every interval. Tick errors are
// logged and the loop continues. Run returns ctx.Err() when the context is
// cancelled.
func (w *WorkingSet) Run(ctx context.Context, interval time.Duration) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if err := w.Tick(ctx, time.Now().UnixMilli()); err != nil {
				log.Printf("control: WorkingSet.Tick error: %v", err)
				// continue — transient error; next tick retries
			}
		}
	}
}

func (w *WorkingSet) Tick(ctx context.Context, nowMs int64) error {
	// 1. PASSIVE warm: pop warm:req batch
	reqs, _ := w.rdb.LPopCount(ctx, warmReqKey, w.cfg.WarmReqBatch).Result()
	for _, item := range reqs {
		jid, cc := splitReq(item)
		w.warm(ctx, jid, cc)
	}

	// 2. ACTIVE warm: for each cc, fill resident set up to Target
	for _, cc := range w.ccs {
		if w.residentCount(ctx) >= w.cfg.Target {
			break
		}
		due, err := w.sched.PopDue(ctx, cc, nowMs, w.cfg.Target)
		if err != nil {
			return err
		}
		for _, jid := range due {
			if w.residentCount(ctx) >= w.cfg.Target {
				break
			}
			w.warm(ctx, jid, cc)
		}
	}

	// 3. EVICT: remove non-warm members from resident set
	members, _ := w.rdb.SMembers(ctx, residentKey).Result()
	for _, jid := range members {
		if !w.deps.IsWarm(jid) {
			w.rdb.SRem(ctx, residentKey, jid)
			w.deps.Evict(ctx, jid, w.cfg.Linger)
			continue
		}
	}
	return nil
}
