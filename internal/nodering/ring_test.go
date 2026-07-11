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
