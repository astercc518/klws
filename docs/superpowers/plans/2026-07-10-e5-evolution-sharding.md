# E5 多 Evolution 节点分片 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 落地把 Evolution 实例分片到多节点的原语——一致性哈希环 + 容量感知节点分配（`internal/nodering`）、每节点实例计数（`store.NodeCounts`）、节点→客户端注册表（`cluster.EvoCluster`）——**全部休眠**（不接 live 创建/发送路径，默认仍 whatsmeow），零行为变更。填 10 万账号单机密度落差（spec §1a：Evolution 单机现实百~低千实例，需横向铺节点）。

**Architecture:** 一致性哈希环放**新 leaf 包 `internal/nodering`**（只依赖 stdlib，无循环——`store` 被 `cluster` import，不能反向，故 ring 不能放 cluster）。分配决策 `AssignNode` 是纯函数（吃 ring + counts），DB 部分 `NodeCounts` 在 store。`EvoCluster` 是 `evo_node→*EvoClient` 的普通 map（cutover 时 evoInstance 按其 `account_instances.evo_node` 取对应节点客户端）。live 组合（创建实例时 `NodeCounts`→`AssignNode`→落 `evo_node`→用该节点 `EvoClient`）属 E6 cutover。

**Tech Stack:** Go、hash/crc32（环哈希）、sort（环有序键 + 二分）、testcontainers（NodeCounts）、纯单测（ring/registry）。

## Global Constraints

逐字遵守：

- **零行为变更**：E5 新符号无 live 调用者（whatsmeow 路径不动；无实例创建路径被接线）。不改 dispatch/cmd/orchestrator。
- **不制造 import 环**：`nodering` 是 leaf（仅 stdlib）；`store` 可 import `nodering`；`cluster` 可 import `nodering` 与 `store`；`store` 绝不 import `cluster`。
- **确定性**：环用固定哈希（crc32 IEEE），同一 (nodes, key) 恒定映射；`AssignNode` 是纯函数、可测。
- **不引入 whatsmeow 依赖**到新文件。
- **`make gate` 全程绿**（除预存在 `GO-2026-5856`）；gofmt 干净；提交带 `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>` trailer。

**执行前置**：从 `main`（现 `4b88fd0`）新建分支 `feat/evolution-e5-sharding`，先确认基线编译。

---

## Task 1: `internal/nodering` — 一致性哈希环 + `AssignNode`（纯）

**Files:**
- Create: `internal/nodering/ring.go`
- Test: `internal/nodering/ring_test.go`

**Interfaces:**
- Produces（package `nodering`）：
  - `type Ring struct { ... }`（内部：`replicas int`、有序 `keys []uint32`、`hashMap map[uint32]string`）
  - `func New(replicas int, nodes ...string) *Ring` —— `replicas<1`→`1`；对每个 node 放 `replicas` 个虚拟点（key=`crc32("node#i")`）；`keys` 排序。空 nodes 合法（空环）。
  - `func (r *Ring) Get(key string) (string, bool)` —— 顺时针最近节点；空环→`("",false)`。
  - `func (r *Ring) Successors(key string) []string` —— 从 key 位置起顺时针的**去重**节点序（长度=不同节点数）；空环→`nil`。用于容量回退。
  - `func AssignNode(r *Ring, counts map[string]int, key string, capPerNode int) (string, bool)` —— 纯：按 `Successors(key)` 顺序取第一个 `counts[node] < capPerNode` 的节点；全满或空环→`("",false)`。`capPerNode<1` 视为无容量（总是满）。

- [ ] **Step 1: 写失败测试**

`internal/nodering/ring_test.go`：
```go
package nodering

import "testing"

func TestRing_GetDeterministicAndNonEmpty(t *testing.T) {
	r := New(50, "n1", "n2", "n3")
	got, ok := r.Get("instance-abc")
	if !ok {
		t.Fatal("non-empty ring must resolve")
	}
	// deterministic
	got2, _ := r.Get("instance-abc")
	if got != got2 {
		t.Fatalf("non-deterministic: %q vs %q", got, got2)
	}
	if got != "n1" && got != "n2" && got != "n3" {
		t.Fatalf("resolved to unknown node %q", got)
	}
}

func TestRing_EmptyRing(t *testing.T) {
	r := New(10)
	if _, ok := r.Get("x"); ok {
		t.Fatal("empty ring must return ok=false")
	}
	if s := r.Successors("x"); s != nil {
		t.Fatalf("empty ring successors=%v want nil", s)
	}
}

func TestRing_DistributionRoughlyEven(t *testing.T) {
	r := New(200, "a", "b", "c", "d")
	counts := map[string]int{}
	for i := 0; i < 8000; i++ {
		n, _ := r.Get(string(rune('A'+i%26)) + string(rune('0'+i%10)) + itoa(i))
		counts[n]++
	}
	// each of 4 nodes should get a non-trivial share (>10% of 8000).
	for _, n := range []string{"a", "b", "c", "d"} {
		if counts[n] < 800 {
			t.Fatalf("node %q got %d (<800) — distribution too skewed: %v", n, counts[n], counts)
		}
	}
}

func TestRing_SuccessorsDistinctAndComplete(t *testing.T) {
	r := New(50, "n1", "n2", "n3")
	s := r.Successors("some-key")
	if len(s) != 3 {
		t.Fatalf("successors=%v want 3 distinct", s)
	}
	seen := map[string]bool{}
	for _, n := range s {
		if seen[n] {
			t.Fatalf("successors has duplicate: %v", s)
		}
		seen[n] = true
	}
	// first successor == Get
	g, _ := r.Get("some-key")
	if s[0] != g {
		t.Fatalf("successors[0]=%q != Get=%q", s[0], g)
	}
}

func TestAssignNode_CapacityFallback(t *testing.T) {
	r := New(50, "n1", "n2", "n3")
	key := "acct-42"
	order := r.Successors(key) // e.g. [nX, nY, nZ]
	// preferred full, second has room → assign second.
	counts := map[string]int{order[0]: 100}
	got, ok := AssignNode(r, counts, key, 100)
	if !ok || got != order[1] {
		t.Fatalf("cap fallback: got %q ok=%v want %q", got, ok, order[1])
	}
}

func TestAssignNode_AllFull(t *testing.T) {
	r := New(50, "n1", "n2", "n3")
	counts := map[string]int{"n1": 5, "n2": 5, "n3": 5}
	if _, ok := AssignNode(r, counts, "k", 5); ok {
		t.Fatal("all nodes at cap must return ok=false")
	}
}

func TestAssignNode_EmptyRing(t *testing.T) {
	if _, ok := AssignNode(New(10), map[string]int{}, "k", 100); ok {
		t.Fatal("empty ring must return ok=false")
	}
}

// itoa avoids strconv import churn in the test.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/nodering/ -v`
Expected: FAIL（包/符号未定义）

- [ ] **Step 3: 实现**

`internal/nodering/ring.go`：
```go
// Package nodering is a leaf package (stdlib only) providing a consistent-hash
// ring for sharding Evolution instances across nodes, plus capacity-aware
// assignment. Kept dependency-free so both internal/store and internal/cluster
// can import it without a cycle.
package nodering

import (
	"hash/crc32"
	"sort"
	"strconv"
)

// Ring is a consistent-hash ring with virtual nodes for even distribution.
type Ring struct {
	replicas int
	keys     []uint32          // sorted virtual-node hashes
	hashMap  map[uint32]string // hash → real node
}

// New builds a ring placing `replicas` virtual points per node. replicas<1 → 1.
// Zero nodes is valid (an empty ring that resolves nothing).
func New(replicas int, nodes ...string) *Ring {
	if replicas < 1 {
		replicas = 1
	}
	r := &Ring{replicas: replicas, hashMap: map[uint32]string{}}
	for _, n := range nodes {
		for i := 0; i < replicas; i++ {
			h := crc32.ChecksumIEEE([]byte(n + "#" + strconv.Itoa(i)))
			r.keys = append(r.keys, h)
			r.hashMap[h] = n
		}
	}
	sort.Slice(r.keys, func(i, j int) bool { return r.keys[i] < r.keys[j] })
	return r
}

// Get returns the node owning key (clockwise-nearest virtual node). ok=false on
// an empty ring.
func (r *Ring) Get(key string) (string, bool) {
	if len(r.keys) == 0 {
		return "", false
	}
	h := crc32.ChecksumIEEE([]byte(key))
	i := sort.Search(len(r.keys), func(i int) bool { return r.keys[i] >= h })
	if i == len(r.keys) {
		i = 0 // wrap
	}
	return r.hashMap[r.keys[i]], true
}

// Successors returns the distinct nodes in clockwise order starting at key's
// position (length == number of distinct nodes). Empty ring → nil. Used for
// capacity fallback: try the preferred node, then the next, and so on.
func (r *Ring) Successors(key string) []string {
	if len(r.keys) == 0 {
		return nil
	}
	h := crc32.ChecksumIEEE([]byte(key))
	start := sort.Search(len(r.keys), func(i int) bool { return r.keys[i] >= h })
	if start == len(r.keys) {
		start = 0
	}
	var out []string
	seen := map[string]bool{}
	for i := 0; i < len(r.keys); i++ {
		node := r.hashMap[r.keys[(start+i)%len(r.keys)]]
		if !seen[node] {
			seen[node] = true
			out = append(out, node)
		}
	}
	return out
}

// AssignNode picks the first node (in ring successor order from key) whose
// current instance count is below capPerNode. ok=false if all are at capacity
// or the ring is empty. Pure: the caller supplies counts (e.g. store.NodeCounts).
func AssignNode(r *Ring, counts map[string]int, key string, capPerNode int) (string, bool) {
	for _, node := range r.Successors(key) {
		if counts[node] < capPerNode {
			return node, true
		}
	}
	return "", false
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/nodering/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/nodering/ring.go internal/nodering/ring_test.go
git commit -m "feat(nodering): consistent-hash ring + capacity-aware AssignNode (leaf pkg)"
```

---

## Task 2: `store.NodeCounts`（每节点实例计数）

**Files:**
- Create: `internal/store/node_counts.go`
- Test: `internal/store/instances_db_test.go`（追加到 E0 建的文件）

**Interfaces:**
- Consumes: `account_instances`（E0 表，含 `evo_node`）。
- Produces:
  - `func (m *Manager) NodeCounts(ctx context.Context) (map[string]int, error)` —— `SELECT evo_node, count(*) FROM account_instances GROUP BY evo_node`，走 `SystemPool()`（分片计数无租户上下文，跨租户全量）。空表→空 map（非 nil）。

- [ ] **Step 1: 写失败测试**

`internal/store/instances_db_test.go` 追加：
```go
func TestNodeCounts(t *testing.T) {
	ctx := context.Background()
	m := newTestManager(t)
	// seed: 2 on nodeA, 1 on nodeB
	mustUpsert := func(name, node string) {
		if err := m.UpsertInstance(ctx, store.InstanceRow{
			InstanceName: name, TenantID: 1, EvoNode: node, State: "created",
		}); err != nil {
			t.Fatal(err)
		}
	}
	mustUpsert("i1", "nodeA")
	mustUpsert("i2", "nodeA")
	mustUpsert("i3", "nodeB")

	counts, err := m.NodeCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts["nodeA"] != 2 || counts["nodeB"] != 1 {
		t.Fatalf("counts=%v want nodeA:2 nodeB:1", counts)
	}
}

func TestNodeCounts_EmptyIsNonNil(t *testing.T) {
	counts, err := newTestManager(t).NodeCounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if counts == nil {
		t.Fatal("empty table must return non-nil empty map")
	}
}
```
（若 `instances_db_test.go` 用的是 `package store`，`store.InstanceRow` 应写 `InstanceRow`——**与该文件既有 E0 测试的写法保持一致**：grep 文件里 `UpsertInstance` 现有调用，照抄其 `InstanceRow`/`store.InstanceRow` 前缀。）

- [ ] **Step 2: 跑测试确认失败**

Run: `TESTCONTAINERS_RYUK_DISABLED=true go test ./internal/store/ -run TestNodeCounts -v`
Expected: FAIL（`NodeCounts` 未定义）

- [ ] **Step 3: 实现**

`internal/store/node_counts.go`：
```go
package store

import "context"

// NodeCounts returns the number of account_instances rows per evo_node. Used by
// nodering.AssignNode for capacity-aware sharding. Uses SystemPool (sharding is
// fleet-wide, no tenant context). Empty table → empty (non-nil) map.
func (m *Manager) NodeCounts(ctx context.Context) (map[string]int, error) {
	rows, err := m.SystemPool().Query(ctx,
		`SELECT evo_node, count(*) FROM account_instances GROUP BY evo_node`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var node string
		var n int
		if err := rows.Scan(&node, &n); err != nil {
			return nil, err
		}
		out[node] = n
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `TESTCONTAINERS_RYUK_DISABLED=true go test ./internal/store/ -run TestNodeCounts -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/store/node_counts.go internal/store/instances_db_test.go
git commit -m "feat(store): NodeCounts per evo_node (capacity input for sharding)"
```

---

## Task 3: `cluster.EvoCluster`（节点→客户端注册表）

**Files:**
- Create: `internal/cluster/evolution_cluster.go`
- Test: `internal/cluster/evolution_cluster_test.go`

**Interfaces:**
- Consumes: `*EvoClient`（E0/E1）。
- Produces:
  - `type EvoCluster struct { clients map[string]*EvoClient }`
  - `func NewEvoCluster(baseURLs map[string]string, apiKey string) *EvoCluster` —— 每个 `node→baseURL` 建一个 `NewEvoClient(baseURL, apiKey)`。
  - `func (c *EvoCluster) For(node string) (*EvoClient, bool)` —— 取该节点客户端；未知节点→`(nil,false)`。
  - `func (c *EvoCluster) Nodes() []string` —— 已配置节点名（排序，稳定）。

- [ ] **Step 1: 写失败测试**

`internal/cluster/evolution_cluster_test.go`：
```go
package cluster

import (
	"reflect"
	"testing"
)

func TestEvoCluster_ForAndNodes(t *testing.T) {
	c := NewEvoCluster(map[string]string{
		"nodeA": "http://a:8080",
		"nodeB": "http://b:8080",
	}, "key")

	if got := c.Nodes(); !reflect.DeepEqual(got, []string{"nodeA", "nodeB"}) {
		t.Fatalf("Nodes()=%v want sorted [nodeA nodeB]", got)
	}
	cl, ok := c.For("nodeA")
	if !ok || cl == nil {
		t.Fatal("For(nodeA) must resolve")
	}
	if _, ok := c.For("ghost"); ok {
		t.Fatal("For(unknown) must be ok=false")
	}
}

func TestEvoCluster_Empty(t *testing.T) {
	c := NewEvoCluster(nil, "k")
	if len(c.Nodes()) != 0 {
		t.Fatal("empty cluster has no nodes")
	}
	if _, ok := c.For("x"); ok {
		t.Fatal("empty cluster resolves nothing")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/cluster/ -run TestEvoCluster -v`
Expected: FAIL（未定义）

- [ ] **Step 3: 实现**

`internal/cluster/evolution_cluster.go`：
```go
package cluster

import "sort"

// EvoCluster is a registry of per-node Evolution HTTP clients. An instance's
// evo_node (account_instances) selects which client its lifecycle/send calls
// go to. DORMANT: composed into the live path only at E6 cutover.
type EvoCluster struct {
	clients map[string]*EvoClient
}

// NewEvoCluster builds one EvoClient per node from a node→baseURL map. All nodes
// share the same apiKey (Evolution's global key).
func NewEvoCluster(baseURLs map[string]string, apiKey string) *EvoCluster {
	c := &EvoCluster{clients: map[string]*EvoClient{}}
	for node, url := range baseURLs {
		c.clients[node] = NewEvoClient(url, apiKey)
	}
	return c
}

// For returns the client for a node, or (nil,false) if the node is unknown.
func (c *EvoCluster) For(node string) (*EvoClient, bool) {
	cl, ok := c.clients[node]
	return cl, ok
}

// Nodes returns the configured node names, sorted (stable).
func (c *EvoCluster) Nodes() []string {
	out := make([]string, 0, len(c.clients))
	for n := range c.clients {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
```

- [ ] **Step 4: 跑测试通过 + 全包编译**

Run: `go test ./internal/cluster/ -run TestEvoCluster -v && go build ./... && gofmt -l internal/cluster/evolution_cluster.go internal/cluster/evolution_cluster_test.go`
Expected: PASS + 编译 + gofmt 空

- [ ] **Step 5: Commit**

```bash
git add internal/cluster/evolution_cluster.go internal/cluster/evolution_cluster_test.go
git commit -m "feat(cluster): EvoCluster per-node client registry (dormant)"
```

---

## E5 收尾：门禁 + 零回归自检

- [ ] **门禁**

Run: `make gate`
Expected: `tidy vet test-race labels` 绿；`vuln` 仅 `GO-2026-5856`。

- [ ] **零回归自检**

Run: `grep -rn "nodering\.\|NodeCounts\|NewEvoCluster\|EvoCluster" internal/ cmd/ --include=*.go | grep -v _test.go | grep -vE "internal/nodering/|internal/store/node_counts.go|internal/cluster/evolution_cluster.go"`
Expected: **空**——E5 新符号在定义文件外无调用者（创建/发送路径未接线），零行为变更。

- [ ] **无环自检**

Run: `grep -rn "internal/cluster" internal/store/ internal/nodering/ --include=*.go | grep -v _test.go || echo "clean: store/nodering 不 import cluster"`
Expected: `clean`——`nodering` leaf 无 cluster 依赖，`store` 不 import cluster（防环）。

---

## Self-Review（对 spec §7 E5）

- **一致性哈希 instance→evo_node** → Task 1（`nodering.Ring.Get`/`Successors`）。✅
- **每节点容量上限** → Task 1（`AssignNode` 容量回退）+ Task 2（`NodeCounts` 提供当前计数）。✅
- **instanceResolver 据此路由** → Task 3（`EvoCluster.For(node)` 节点→客户端）+ E0 的 `account_instances.evo_node` 存储。live 组合（创建时 NodeCounts→AssignNode→落 evo_node→For(node) 取客户端）属 E6 cutover。
- **填密度落差（§1a）** → 分片原语齐备，E6 接线后单机瓶颈转为「多节点横向铺」。
- **门控/live 接线** → 本模块休眠，收尾自检证明零调用者。
- **占位符扫描**：无 TBD；每步含实际代码/命令/期望。
- **类型一致性**：`AssignNode(r *Ring, counts map[string]int, key string, cap int)` 的 `counts` 正是 `store.NodeCounts` 的返回类型；`EvoCluster.For` 返回 `*EvoClient`（E1 `evoInstance` 的 `evoAPI` 由 `*EvoClient` 结构满足，cutover 时按节点取）。
- **无 evo-verify 依赖**：E5 不碰 Evolution REST 字段/协议——纯分片路由，是继 E4 之后又一个不受「真机未验证」风险影响的模块。

---

**下一步**：**E6 cutover**（唯一改变运行行为、不可逆的一步）——live 组合 `evoSender→retrySender(WithPermanent=cluster.IsPermanent)→perInstanceLimiter→circuitBreakerSender` 塞进 SendWorker + 创建路径 NodeCounts→AssignNode→evo_node→EvoCluster.For + throttle→governor + healthSink→真 sendgate + 门控 `WADIST_SENDER`/`WADIST_CONN` 翻默认 + 删 whatsmeow/wabadger + go.mod 去依赖 + 全量重扫码 + E3 carry-forwards。**E6 前必须完成真机回环把所有 `TODO(evo-verify)` 钉死**（spec §9，全局阻塞项）。
