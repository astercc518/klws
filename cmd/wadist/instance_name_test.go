package main

import "testing"

func TestInstanceNameFor_Deterministic(t *testing.T) {
	a := instanceNameFor(1, "nodeA", "123@s.whatsapp.net")
	b := instanceNameFor(1, "nodeA", "123@s.whatsapp.net")
	if a != b {
		t.Fatalf("non-deterministic: %q vs %q", a, b)
	}
	if a == instanceNameFor(2, "nodeA", "123@s.whatsapp.net") {
		t.Fatal("tenant must affect name")
	}
	if a == instanceNameFor(1, "nodeA", "999@s.whatsapp.net") {
		t.Fatal("jid must affect name")
	}
}
