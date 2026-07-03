// internal/store/proxy_redis.go
package store

import (
	"context"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

func availKey(cc string) string { return "proxy:avail:" + cc }

const (
	proxyFreeKey = "proxy:free" // hash: field=proxyID, val=free slot count
	proxyMetaKey = "proxy:meta" // hash: field=proxyID, val="url|type|cc"
)

func itoa(id int64) string { return strconv.FormatInt(id, 10) }

// pickLua atomically selects the lowest-score eligible proxy in avail:{cc}
// (score<=now), decrements its free-slot counter, and either re-adds it with a
// fresh cooldown score (if slots remain) or removes it (if it was the last slot).
// KEYS=[avail:{cc}, proxy:free, proxy:meta] ARGV=[nowMs, cooldownMs]
// returns {proxyID, meta} or {} on miss.
var pickLua = goredis.NewScript(`
local ids = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', ARGV[1], 'LIMIT', 0, 1)
if #ids == 0 then return {} end
local id = ids[1]
local free = redis.call('HINCRBY', KEYS[2], id, -1)
if free <= 0 then
  redis.call('ZREM', KEYS[1], id)
else
  redis.call('ZADD', KEYS[1], tonumber(ARGV[1]) + tonumber(ARGV[2]), id)
end
local meta = redis.call('HGET', KEYS[3], id)
return {id, meta}`)

type redisProxyAllocator struct {
	rdb      *goredis.Client
	cooldown time.Duration
}

func newRedisProxyAllocator(rdb *goredis.Client, cooldown time.Duration) *redisProxyAllocator {
	return &redisProxyAllocator{rdb: rdb, cooldown: cooldown}
}

func (a *redisProxyAllocator) pick(ctx context.Context, cc string, nowMs int64) (int64, string, error) {
	res, err := pickLua.Run(ctx, a.rdb,
		[]string{availKey(cc), proxyFreeKey, proxyMetaKey},
		nowMs, a.cooldown.Milliseconds()).Slice()
	if err != nil {
		return 0, "", err
	}
	if len(res) < 2 {
		return 0, "", ErrNoProxyAvailable
	}
	idStr, _ := res[0].(string)
	meta, _ := res[1].(string)
	id, perr := strconv.ParseInt(idStr, 10, 64)
	if perr != nil {
		return 0, "", perr
	}
	return id, meta, nil
}

// release returns a slot to proxyID and re-arms its cooldown in avail:{cc}
// (score=nowMs+cooldown), so it becomes eligible again only after cooldown.
func (a *redisProxyAllocator) release(ctx context.Context, proxyID int64, cc string, nowMs int64) error {
	member := itoa(proxyID)
	pipe := a.rdb.TxPipeline()
	pipe.HIncrBy(ctx, proxyFreeKey, member, 1)
	pipe.ZAdd(ctx, availKey(cc), goredis.Z{Score: float64(nowMs + a.cooldown.Milliseconds()), Member: member})
	_, err := pipe.Exec(ctx)
	return err
}

// markDead fully excludes proxyID from the hot index: removed from the
// avail:{cc} ring and its free/meta hash entries deleted.
func (a *redisProxyAllocator) markDead(ctx context.Context, proxyID int64, cc string) error {
	member := itoa(proxyID)
	pipe := a.rdb.TxPipeline()
	pipe.ZRem(ctx, availKey(cc), member)
	pipe.HDel(ctx, proxyFreeKey, member)
	pipe.HDel(ctx, proxyMetaKey, member)
	_, err := pipe.Exec(ctx)
	return err
}

// markAlive (re)admits proxyID into the hot index: sets its free-slot count
// and meta, and adds it to avail:{cc} with score=nowMs (eligible immediately).
func (a *redisProxyAllocator) markAlive(ctx context.Context, proxyID int64, cc, meta string, freeSlots int, nowMs int64) error {
	member := itoa(proxyID)
	pipe := a.rdb.TxPipeline()
	pipe.HSet(ctx, proxyFreeKey, member, freeSlots)
	pipe.HSet(ctx, proxyMetaKey, member, meta)
	pipe.ZAdd(ctx, availKey(cc), goredis.Z{Score: float64(nowMs), Member: member})
	_, err := pipe.Exec(ctx)
	return err
}
