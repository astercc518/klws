// internal/store/proxy_redis.go
package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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

// releaseLua returns a slot to proxyID and re-arms its cooldown in avail:{cc},
// but ONLY if the proxy is still known-alive (its proxy:meta field exists). If
// the proxy was markDead'd (meta HDEL'd) while an account was bound, release is
// a no-op — it must NOT resurrect a dead proxy into the ring with empty meta.
// markAlive (ReportProxySuccess) / boot-rebuild are the only revival paths.
// KEYS=[avail:{cc}, proxy:free, proxy:meta] ARGV=[proxyID, scoreMs]
var releaseLua = goredis.NewScript(`
if redis.call('HEXISTS', KEYS[3], ARGV[1]) == 1 then
  redis.call('HINCRBY', KEYS[2], ARGV[1], 1)
  redis.call('ZADD', KEYS[1], ARGV[2], ARGV[1])
end
return 1`)

// release returns a slot to proxyID and re-arms its cooldown in avail:{cc}
// (score=nowMs+cooldown) — but only for a still-alive proxy (see releaseLua).
func (a *redisProxyAllocator) release(ctx context.Context, proxyID int64, cc string, nowMs int64) error {
	score := nowMs + a.cooldown.Milliseconds()
	return releaseLua.Run(ctx, a.rdb,
		[]string{availKey(cc), proxyFreeKey, proxyMetaKey},
		itoa(proxyID), score).Err()
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

// rebuildFromPG rebuilds the Redis hot-index (proxy:avail:*, proxy:free,
// proxy:meta) from the durable PG proxy_pool at boot. It first clears any
// stale hot-index state (idempotent: safe to re-run), then seeds one entry
// per alive proxy that still has free capacity (max_bindings >
// current_bindings), eligible immediately (score=nowMs). Returns the count
// of proxies seeded.
func (a *redisProxyAllocator) rebuildFromPG(ctx context.Context, pool *pgxpool.Pool, nowMs int64) (int, error) {
	// clear stale hot index (idempotent boot).
	iter := a.rdb.Scan(ctx, 0, "proxy:avail:*", 0).Iterator()
	var availKeys []string
	for iter.Next(ctx) {
		availKeys = append(availKeys, iter.Val())
	}
	if err := iter.Err(); err != nil {
		return 0, err
	}
	pipe := a.rdb.TxPipeline()
	if len(availKeys) > 0 {
		pipe.Del(ctx, availKeys...)
	}
	pipe.Del(ctx, proxyFreeKey, proxyMetaKey)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, err
	}

	rows, err := pool.Query(ctx, `
SELECT id, proxy_url, proxy_type::text, country_code, (max_bindings - current_bindings) AS free
  FROM proxy_pool
 WHERE is_alive = TRUE AND max_bindings > current_bindings`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	seed := a.rdb.TxPipeline()
	n := 0
	for rows.Next() {
		var id int64
		var url, ptype, cc string
		var free int
		if err := rows.Scan(&id, &url, &ptype, &cc, &free); err != nil {
			return 0, err
		}
		member := itoa(id)
		seed.HSet(ctx, proxyFreeKey, member, free)
		seed.HSet(ctx, proxyMetaKey, member, url+"|"+ptype+"|"+cc)
		seed.ZAdd(ctx, availKey(cc), goredis.Z{Score: float64(nowMs), Member: member})
		n++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if _, err := seed.Exec(ctx); err != nil {
		return 0, err
	}
	return n, nil
}

// parseMeta splits a "url|type|cc" proxy:meta value into its parts.
func parseMeta(meta string) (url, ptype, cc string, ok bool) {
	parts := strings.SplitN(meta, "|", 3)
	if len(parts) != 3 {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

// bind picks an alive proxy with free capacity via the Redis hot index, then
// durably writes the acquisition to PG in one tx: proxy_pool counters
// (current_bindings+1, usage_count+1) and the account_devices binding. If the
// PG write fails for any reason (including the account row not existing), the
// Redis pick is released so its capacity isn't leaked, and the error is
// returned. accountJID is assumed to be currently unbound (callers guard this
// upstream, e.g. StickyBindProxy); this only performs the acquire side.
func (a *redisProxyAllocator) bind(ctx context.Context, pool *pgxpool.Pool, accountJID, cc string, nowMs int64) (*ProxyBinding, error) {
	id, meta, err := a.pick(ctx, cc, nowMs)
	if err != nil {
		return nil, err // ErrNoProxyAvailable on miss
	}
	url, ptype, mcc, ok := parseMeta(meta)
	if !ok {
		_ = a.release(ctx, id, cc, nowMs) // return the slot; corrupt meta
		return nil, fmt.Errorf("proxy meta corrupt for id %d: %q", id, meta)
	}
	err = pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `UPDATE proxy_pool SET current_bindings=current_bindings+1, usage_count=usage_count+1 WHERE id=$1`, id); e != nil {
			return e
		}
		ct, e := tx.Exec(ctx, `UPDATE account_devices SET proxy_id=$1, proxy_url_cache=$2 WHERE account_jid=$3`, id, url, accountJID)
		if e != nil {
			return e
		}
		if ct.RowsAffected() == 0 {
			return ErrAccountMissing
		}
		return nil
	})
	if err != nil {
		_ = a.release(ctx, id, cc, nowMs) // PG failed → don't leak capacity in Redis
		return nil, err
	}
	return &ProxyBinding{ProxyID: id, ProxyURL: url, ProxyType: ptype, Country: mcc}, nil
}

// releaseBinding reads the account's currently bound proxy (and its country)
// from PG, then durably decrements the proxy's counter and clears the
// account's binding in one tx, and finally returns the slot to the Redis hot
// index. Idempotent: a no-op (nil error) when the account is already
// unbound, so callers may release-then-bind unconditionally.
func (a *redisProxyAllocator) releaseBinding(ctx context.Context, pool *pgxpool.Pool, accountJID string, nowMs int64) error {
	var oldID *int64
	var cc string
	err := pool.QueryRow(ctx, `
SELECT a.proxy_id, COALESCE(p.country_code,'')
  FROM account_devices a LEFT JOIN proxy_pool p ON p.id=a.proxy_id
 WHERE a.account_jid=$1`, accountJID).Scan(&oldID, &cc)
	if err != nil {
		if err == pgx.ErrNoRows {
			return ErrAccountMissing
		}
		return err
	}
	if oldID == nil {
		return nil // already unbound (idempotent)
	}
	err = pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `UPDATE proxy_pool SET current_bindings=GREATEST(current_bindings-1,0) WHERE id=$1`, *oldID); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, `UPDATE account_devices SET proxy_id=NULL, proxy_url_cache=NULL WHERE account_jid=$1`, accountJID)
		return e
	})
	if err != nil {
		return err
	}
	return a.release(ctx, *oldID, cc, nowMs)
}
