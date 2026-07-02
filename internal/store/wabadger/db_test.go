package wabadger

import (
	"bytes"
	"testing"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestKV_PutGetDelScan(t *testing.T) {
	db := openTestDB(t)

	if v, err := db.get(kb("idt", "j1", "a")); err != nil || v != nil {
		t.Fatalf("get missing = %v,%v; want nil,nil", v, err)
	}
	if err := db.put(kb("idt", "j1", "a"), []byte("x"), true); err != nil {
		t.Fatalf("put: %v", err)
	}
	v, err := db.get(kb("idt", "j1", "a"))
	if err != nil || !bytes.Equal(v, []byte("x")) {
		t.Fatalf("get = %q,%v; want x,nil", v, err)
	}
	_ = db.put(kb("idt", "j1", "b"), []byte("y"), false)
	_ = db.put(kb("idt", "j2", "a"), []byte("z"), false)  // different jid, must not match
	_ = db.put(kb("idt", "j1x", "a"), []byte("w"), false) // jid "j1x" shares the "j1" byte-prefix but is a different segment; kp("idt","j1")'s trailing NUL must exclude it

	var got int
	if err := db.scanPrefix(kp("idt", "j1"), func(_, _ []byte) error { got++; return nil }); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got != 2 {
		t.Fatalf("scanPrefix count = %d; want 2 (must exclude jid j1x)", got)
	}
	if err := db.dropPrefix(kp("idt", "j1")); err != nil {
		t.Fatalf("dropPrefix: %v", err)
	}
	if v, _ := db.get(kb("idt", "j1", "a")); v != nil {
		t.Fatalf("after dropPrefix get j1/a = %q; want nil", v)
	}
	if v, _ := db.get(kb("idt", "j2", "a")); !bytes.Equal(v, []byte("z")) {
		t.Fatalf("dropPrefix leaked to j2")
	}
	if v, _ := db.get(kb("idt", "j1x", "a")); !bytes.Equal(v, []byte("w")) {
		t.Fatalf("dropPrefix(kp idt/j1) wrongly deleted jid j1x: got %q; want w", v)
	}

	// del() removes a single key.
	if err := db.put(kb("idt", "j3", "a"), []byte("d"), false); err != nil {
		t.Fatalf("put j3/a: %v", err)
	}
	if err := db.del(kb("idt", "j3", "a")); err != nil {
		t.Fatalf("del: %v", err)
	}
	if v, _ := db.get(kb("idt", "j3", "a")); v != nil {
		t.Fatalf("after del get j3/a = %q; want nil", v)
	}
}
