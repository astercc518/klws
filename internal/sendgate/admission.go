// internal/sendgate/admission.go
package sendgate

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// admitLuaBackoff atomically enforces a daily quota hard cap AND a minimum
// inter-send gap (human-like pacing), with the min_gap widened by the max of
// two segment backoff multipliers before the pacing check. KEYS[1]=daily counter,
// KEYS[2]=last-send timestamp, KEYS[3]=cc backoff key, KEYS[4]=net backoff key.
// ARGV: 1=daily_quota 2=min_gap_ms 3=now_ms 4=daily_ttl_seconds.
const admitLuaBackoff = `
local sent = tonumber(redis.call('GET', KEYS[1]) or '0')
if sent >= tonumber(ARGV[1]) then return {0, 'daily_quota'} end
local mult = math.max(1, tonumber(redis.call('GET', KEYS[3]) or '1'), tonumber(redis.call('GET', KEYS[4]) or '1'))
local gap = tonumber(ARGV[2]) * mult
local last = tonumber(redis.call('GET', KEYS[2]) or '0')
if tonumber(ARGV[3]) - last < gap then return {0, 'pacing'} end
redis.call('INCR', KEYS[1])
if redis.call('TTL', KEYS[1]) < 0 then redis.call('EXPIRE', KEYS[1], tonumber(ARGV[4])) end
redis.call('SET', KEYS[2], ARGV[3])
return {1, 'ok'}`

// backoffLua atomically multiplies a segment's backoff key by Factor (capped
// at Max) and refreshes its TTL. The stored value is an integer multiplier
// that admission can later use to widen that segment's min_gap.
// KEYS[1]=segment key. ARGV: 1=factor 2=max 3=ttl_ms.
const backoffLua = `
local cur = tonumber(redis.call('GET', KEYS[1]) or '1')
local nv = cur * tonumber(ARGV[1])
if nv > tonumber(ARGV[2]) then nv = tonumber(ARGV[2]) end
redis.call('SET', KEYS[1], nv, 'PX', tonumber(ARGV[3]))
return nv`

// BackoffParams controls per-segment exponential backoff on warnings. Backoff
// is always active; these are tuning knobs only.
type BackoffParams struct {
	Factor int
	Max    int
	TTL    time.Duration
}

type Admission struct {
	rdb     *goredis.Client
	admit   *goredis.Script
	backoff *goredis.Script
	bp      BackoffParams
}

func NewAdmission(rdb *goredis.Client, bp BackoffParams) *Admission {
	return &Admission{
		rdb:     rdb,
		admit:   goredis.NewScript(admitLuaBackoff),
		backoff: goredis.NewScript(backoffLua),
		bp:      bp,
	}
}

// RecordWarning multiplicatively increases each segment's backoff multiplier
// (capped at bp.Max) with a fresh TTL, so admission widens that segment's
// min_gap.
func (a *Admission) RecordWarning(ctx context.Context, segKeys ...string) error {
	for _, k := range segKeys {
		if err := a.backoff.Run(ctx, a.rdb, []string{k},
			a.bp.Factor, a.bp.Max, a.bp.TTL.Milliseconds()).Err(); err != nil {
			return fmt.Errorf("record warning %s: %w", k, err)
		}
	}
	return nil
}

// Ticket holds the consumed quota slot so a downstream failure can refund it.
type Ticket struct {
	jid    string
	dayKey string
	rdb    *goredis.Client
}

// Admit atomically checks daily quota + pacing. On success returns a Ticket and
// "ok"; on rejection returns (nil, reason) where reason is "daily_quota"/"pacing".
// ccKey/netKey are per-segment backoff multiplier keys (always passed, 4 KEYS)
// that widen the pacing gap when either segment has an active backoff
// multiplier.
func (a *Admission) Admit(ctx context.Context, jid string, quota int, minGap time.Duration, now time.Time, ccKey, netKey string) (*Ticket, string, error) {
	u := now.UTC()
	dayKey := "q:" + jid + ":" + u.Format("20060102")
	paceKey := "p:" + jid
	ttl := int(u.Truncate(24*time.Hour).Add(24*time.Hour).Sub(u).Seconds())
	if ttl < 1 {
		ttl = 1
	}

	res, err := a.admit.Run(ctx, a.rdb,
		[]string{dayKey, paceKey, ccKey, netKey},
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
