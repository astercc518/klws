// Package capacity computes node/connection/memory requirements from per-node
// limits. accountsPerNode is bounded by MaxLockConns (one pinned advisory-lock
// connection per active account). All pure functions — no I/O.
package capacity

type Inputs struct {
	Accounts         int
	MaxLockConns     int // per-node account ceiling
	MaxOpenConns     int // biz pool size (also sqlDB; ×2 more if DualRLSPools)
	AsynqConcurrency int
	HeadroomConns    int
	DualRLSPools     bool

	DailyQuotaKeyBytes int
	PacingKeyBytes     int
	AsynqQueueDepth    int
	AvgTaskBytes       int
}

type Plan struct {
	AccountsPerNode        int
	NodesNeeded            int
	ConnsPerNode           int
	RequiredMaxConnections int
	RedisMemoryMB          int
}

func ceilDiv(a, b int) int {
	if b <= 0 || a <= 0 {
		return 0
	}
	return (a + b - 1) / b
}

func computeRedisMB(accounts, dailyKey, pacingKey, queueDepth, taskBytes int) int {
	bytes := accounts*(dailyKey+pacingKey) + queueDepth*taskBytes
	if bytes <= 0 {
		return 0
	}
	// 2x overhead for Redis structures/fragmentation, then round up to MB.
	return ceilDiv(bytes*2, 1<<20)
}

func Compute(in Inputs) Plan {
	apn := in.MaxLockConns
	nodes := ceilDiv(in.Accounts, apn)
	conns := in.MaxLockConns + in.MaxOpenConns + in.MaxOpenConns // lock + biz + sqlDB
	if in.DualRLSPools {
		conns += 2 * in.MaxOpenConns
	}
	required := 0
	if nodes > 0 {
		required = conns*nodes + in.HeadroomConns
	}
	return Plan{
		AccountsPerNode:        apn,
		NodesNeeded:            nodes,
		ConnsPerNode:           conns,
		RequiredMaxConnections: required,
		RedisMemoryMB:          computeRedisMB(in.Accounts, in.DailyQuotaKeyBytes, in.PacingKeyBytes, in.AsynqQueueDepth, in.AvgTaskBytes),
	}
}
