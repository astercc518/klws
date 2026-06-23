// internal/sendgate/admission.go
package sendgate

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// admitLua atomically enforces a daily quota hard cap AND a minimum inter-send
// gap (human-like pacing). KEYS[1]=daily counter, KEYS[2]=last-send timestamp.
// ARGV: 1=daily_quota 2=min_gap_ms 3=now_ms 4=daily_ttl_seconds.
const admitLua = `
local sent = tonumber(redis.call('GET', KEYS[1]) or '0')
if sent >= tonumber(ARGV[1]) then return {0, 'daily_quota'} end
local last = tonumber(redis.call('GET', KEYS[2]) or '0')
if tonumber(ARGV[3]) - last < tonumber(ARGV[2]) then return {0, 'pacing'} end
redis.call('INCR', KEYS[1])
if redis.call('TTL', KEYS[1]) < 0 then redis.call('EXPIRE', KEYS[1], tonumber(ARGV[4])) end
redis.call('SET', KEYS[2], ARGV[3])
return {1, 'ok'}`

type Admission struct {
	rdb   *goredis.Client
	admit *goredis.Script
}

func NewAdmission(rdb *goredis.Client) *Admission {
	return &Admission{rdb: rdb, admit: goredis.NewScript(admitLua)}
}

// Ticket holds the consumed quota slot so a downstream failure can refund it.
type Ticket struct {
	jid    string
	dayKey string
	rdb    *goredis.Client
}

// Admit atomically checks daily quota + pacing. On success returns a Ticket and
// "ok"; on rejection returns (nil, reason) where reason is "daily_quota"/"pacing".
func (a *Admission) Admit(ctx context.Context, jid string, quota int, minGap time.Duration, now time.Time) (*Ticket, string, error) {
	u := now.UTC()
	dayKey := "q:" + jid + ":" + u.Format("20060102")
	paceKey := "p:" + jid
	ttl := int(u.Truncate(24*time.Hour).Add(24*time.Hour).Sub(u).Seconds())
	if ttl < 1 {
		ttl = 1
	}

	res, err := a.admit.Run(ctx, a.rdb,
		[]string{dayKey, paceKey},
		quota, minGap.Milliseconds(), u.UnixMilli(), ttl).Result()
	if err != nil {
		return nil, "", fmt.Errorf("admit script: %w", err)
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) != 2 {
		return nil, "", fmt.Errorf("admit script: unexpected result %v", res)
	}
	if code, _ := arr[0].(int64); code == 0 {
		reason, _ := arr[1].(string)
		return nil, reason, nil
	}
	return &Ticket{jid: jid, dayKey: dayKey, rdb: a.rdb}, "ok", nil
}

// Release refunds the consumed daily quota slot. Best-effort: a failure only
// under-counts (safer for ban avoidance). Does not affect pacing.
func (t *Ticket) Release(ctx context.Context) {
	if t == nil || t.rdb == nil {
		return
	}
	_ = t.rdb.Decr(ctx, t.dayKey).Err()
}
