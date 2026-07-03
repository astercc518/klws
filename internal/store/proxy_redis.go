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
local free = tonumber(redis.call('HGET', KEYS[2], id) or '0') - 1
redis.call('HSET', KEYS[2], id, free)
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
