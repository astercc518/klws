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
// Use a high replica count (>=50) for even distribution; a low count skews.
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
// NOTE: counts is a point-in-time snapshot and AssignNode is pure, so under
// concurrent instance-creates the per-node cap is ADVISORY, not a hard guarantee
// — the caller (E6) must serialize creates or do the assignment atomically
// DB-side if the cap must be strict.
func AssignNode(r *Ring, counts map[string]int, key string, capPerNode int) (string, bool) {
	for _, node := range r.Successors(key) {
		if counts[node] < capPerNode {
			return node, true
		}
	}
	return "", false
}
