package wabadger

import (
	"bytes"
	"context"
	"testing"
)

func TestSession_CRUDAndMany(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if v, _ := s.GetSession(ctx, "u:1"); v != nil {
		t.Fatalf("missing session must be nil")
	}
	_ = s.PutSession(ctx, "u:1", []byte("s1"))
	_ = s.PutSession(ctx, "u:2", []byte("s2"))
	if has, _ := s.HasSession(ctx, "u:1"); !has {
		t.Fatalf("HasSession false after put")
	}
	m, err := s.GetManySessions(ctx, []string{"u:1", "u:2", "u:3"})
	if err != nil || len(m) != 2 || !bytes.Equal(m["u:1"], []byte("s1")) {
		t.Fatalf("GetManySessions = %v,%v", m, err)
	}
	_ = s.PutManySessions(ctx, map[string][]byte{"u:3": []byte("s3")})
	if v, _ := s.GetSession(ctx, "u:3"); !bytes.Equal(v, []byte("s3")) {
		t.Fatalf("PutManySessions failed")
	}
	_ = s.DeleteSession(ctx, "u:1")
	if has, _ := s.HasSession(ctx, "u:1"); has {
		t.Fatalf("DeleteSession failed")
	}
}
