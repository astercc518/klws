// internal/store/ownership_redis.go
package store

import (
	"context"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

const hbTTL = 30 * time.Second // = NodeStaleness；心跳间隔应 = hbTTL/3

const hbKeyPrefix = "node:hb:"

func ownerKey(jid string) string  { return "owner:" + jid }
func hbKey(node string) string    { return "node:hb:" + node }
func ownedKey(node string) string { return "owned:" + node }
func fenceKey(jid string) string  { return "fence:" + jid }

// KEYS=[owner:{jid}, fence:{jid}, owned:{me}] ARGV=[me, "node:hb:", jid]
var luaAcquire = goredis.NewScript(`
local cur = redis.call('GET', KEYS[1])
if cur then
  local node = string.match(cur, "^(.-):")
  if redis.call('EXISTS', ARGV[2]..node) == 1 then return {0, cur} end
end
local fence = redis.call('INCR', KEYS[2])
redis.call('SET', KEYS[1], ARGV[1]..':'..fence)
redis.call('SADD', KEYS[3], ARGV[3])
return {1, fence}`)

// KEYS=[owner:{jid}, node:hb:{me}] ARGV=[me, fence]
var luaStillOwner = goredis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1]..':'..ARGV[2] then return 0 end
if redis.call('EXISTS', KEYS[2]) == 0 then return 0 end
return 1`)

// KEYS=[owner:{jid}, owned:{me}] ARGV=[me, fence, jid]
var luaRelease = goredis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1]..':'..ARGV[2] then
  redis.call('DEL', KEYS[1]); redis.call('SREM', KEYS[2], ARGV[3])
end
return 1`)

type redisLockHandle struct {
	rdb    *goredis.Client
	jid    string
	nodeID string
	fence  int64
}

func (h *redisLockHandle) Healthy(ctx context.Context) bool {
	v, err := luaStillOwner.Run(ctx, h.rdb,
		[]string{ownerKey(h.jid), hbKey(h.nodeID)}, h.nodeID, h.fence).Int()
	if err != nil { return false } // fail-closed
	return v == 1
}

func (h *redisLockHandle) Release(ctx context.Context) {
	_ = luaRelease.Run(ctx, h.rdb,
		[]string{ownerKey(h.jid), ownedKey(h.nodeID)}, h.nodeID, h.fence, h.jid).Err()
}

var _ LockHandle = (*redisLockHandle)(nil)

type redisOwnership struct {
	rdb    *goredis.Client
	roster func(ctx context.Context) ([]string, error) // active 账号花名册（PG）
}

func newRedisOwnership(rdb *goredis.Client) *redisOwnership { return &redisOwnership{rdb: rdb} }
func newRedisOwnershipWithRoster(rdb *goredis.Client, roster func(context.Context) ([]string, error)) *redisOwnership {
	return &redisOwnership{rdb: rdb, roster: roster}
}

func (o *redisOwnership) Acquire(ctx context.Context, jid, nodeID string) (LockHandle, error) {
	res, err := luaAcquire.Run(ctx, o.rdb,
		[]string{ownerKey(jid), fenceKey(jid), ownedKey(nodeID)}, nodeID, hbKeyPrefix, jid).Slice()
	if err != nil { return nil, err }
	ok, _ := res[0].(int64)
	if ok == 0 { return nil, ErrDeviceLocked }
	fence, _ := res[1].(int64)
	return &redisLockHandle{rdb: o.rdb, jid: jid, nodeID: nodeID, fence: fence}, nil
}

// Heartbeat 刷新本节点在 Redis 中的活性标记。
func (o *redisOwnership) Heartbeat(ctx context.Context, nodeID string) error {
	return o.rdb.Set(ctx, hbKey(nodeID), "1", hbTTL).Err()
}

// Deregister 清理本节点在 Redis 中的所有权台账。
func (o *redisOwnership) Deregister(ctx context.Context, nodeID string) error {
	jids, err := o.rdb.SMembers(ctx, ownedKey(nodeID)).Result()
	if err != nil { return err }
	pipe := o.rdb.TxPipeline()
	for _, jid := range jids {
		pipe.Del(ctx, ownerKey(jid))
	}
	pipe.Del(ctx, ownedKey(nodeID))
	pipe.Del(ctx, hbKey(nodeID))
	_, err = pipe.Exec(ctx)
	return err
}

// StaleOwned 返回归属于已过期节点的账号（接管候选）。
// 遍历所有 node 的 owned 集；其 hb 缺失即 stale。单机/少节点规模可接受。
func (o *redisOwnership) StaleOwned(ctx context.Context, _ time.Duration) ([]string, error) {
	nodes, err := o.rdb.Keys(ctx, "owned:*").Result()
	if err != nil { return nil, err }
	var out []string
	for _, ok := range nodes {
		node := strings.TrimPrefix(ok, "owned:")
		if n, _ := o.rdb.Exists(ctx, hbKey(node)).Result(); n == 1 { continue }
		jids, _ := o.rdb.SMembers(ctx, ok).Result()
		out = append(out, jids...)
	}
	return out, nil
}

// Unowned 返回 active 但无主的账号（pipeline 批量 EXISTS）。
func (o *redisOwnership) Unowned(ctx context.Context) ([]string, error) {
	if o.roster == nil { return nil, nil }
	all, err := o.roster(ctx)
	if err != nil { return nil, err }
	pipe := o.rdb.Pipeline()
	cmds := make([]*goredis.IntCmd, len(all))
	for i, jid := range all { cmds[i] = pipe.Exists(ctx, ownerKey(jid)) }
	if _, err := pipe.Exec(ctx); err != nil { return nil, err }
	var out []string
	for i, c := range cmds {
		if c.Val() == 0 { out = append(out, all[i]) }
	}
	return out, nil
}
