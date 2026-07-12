package api

import (
	"strings"
	"testing"
)

func TestBuildContactWhere(t *testing.T) {
	where, args := buildContactWhere(contactFilter{Q: "138", Country: "CN", Status: "active", TagID: 7})
	for _, want := range []string{"c.phone ILIKE", "c.country_code = $", "c.status::text = $", "m.tag_id = $"} {
		if !strings.Contains(where, want) {
			t.Fatalf("where %q missing %q", where, want)
		}
	}
	if len(args) != 4 {
		t.Fatalf("want 4 args, got %d (%v)", len(args), args)
	}
}

func TestBuildContactWhere_Empty(t *testing.T) {
	where, args := buildContactWhere(contactFilter{})
	if where != "" || len(args) != 0 {
		t.Fatalf("empty filter -> %q %v", where, args)
	}
}

func TestSegmentToWhere_Tags(t *testing.T) {
	where, args := segmentToWhere(segmentFilter{Tags: []int64{1, 2}, Status: "active"})
	if !strings.Contains(where, "m.tag_id = ANY($1)") || !strings.Contains(where, "c.status::text = $2") {
		t.Fatalf("where=%q", where)
	}
	if len(args) != 2 {
		t.Fatalf("args=%v", args)
	}
}
