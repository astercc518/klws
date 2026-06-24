package capacity

import "testing"

func TestCompute(t *testing.T) {
	cases := []struct {
		name string
		in   Inputs
		want Plan
	}{
		{
			name: "600 accounts single-DSN two nodes",
			in: Inputs{Accounts: 600, MaxLockConns: 300, MaxOpenConns: 50,
				AsynqConcurrency: 32, HeadroomConns: 50, DualRLSPools: false,
				DailyQuotaKeyBytes: 64, PacingKeyBytes: 64, AsynqQueueDepth: 1000, AvgTaskBytes: 512},
			want: Plan{
				AccountsPerNode:        300,
				NodesNeeded:            2,
				ConnsPerNode:           300 + 50 + 50, // lock + biz + sqlDB = 400
				RequiredMaxConnections: 400*2 + 50,    // 850
				RedisMemoryMB:          computeRedisMB(600, 64, 64, 1000, 512),
			},
		},
		{
			name: "exact ceiling one node",
			in:   Inputs{Accounts: 300, MaxLockConns: 300, MaxOpenConns: 50, HeadroomConns: 50},
			want: Plan{AccountsPerNode: 300, NodesNeeded: 1, ConnsPerNode: 400, RequiredMaxConnections: 450, RedisMemoryMB: 0},
		},
		{
			name: "dual RLS pools add 2x biz",
			in:   Inputs{Accounts: 100, MaxLockConns: 300, MaxOpenConns: 50, HeadroomConns: 0, DualRLSPools: true},
			want: Plan{AccountsPerNode: 300, NodesNeeded: 1, ConnsPerNode: 300 + 50 + 50 + 100, RequiredMaxConnections: 500, RedisMemoryMB: 0},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Compute(c.in)
			if got != c.want {
				t.Fatalf("Compute(%+v)\n got=%+v\nwant=%+v", c.in, got, c.want)
			}
		})
	}
}

func TestCompute_ZeroAccounts(t *testing.T) {
	got := Compute(Inputs{Accounts: 0, MaxLockConns: 300, MaxOpenConns: 50})
	if got.NodesNeeded != 0 {
		t.Fatalf("zero accounts → 0 nodes, got %d", got.NodesNeeded)
	}
}
