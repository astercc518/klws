package crypto

import (
	"bytes"
	"testing"
)

func TestBlindIndex_Deterministic(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	a := BlindIndex(key, "+15550001234")
	b := BlindIndex(key, "+15550001234")
	if !bytes.Equal(a, b) {
		t.Fatal("BlindIndex not deterministic: same key+value produced different outputs")
	}
}

func TestBlindIndex_DifferentValueDiffers(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	a := BlindIndex(key, "+15550001234")
	b := BlindIndex(key, "+15550009999")
	if bytes.Equal(a, b) {
		t.Fatal("BlindIndex: different values produced same output")
	}
}

func TestBlindIndex_Len32(t *testing.T) {
	key := make([]byte, 32)
	out := BlindIndex(key, "hello")
	if len(out) != 32 {
		t.Fatalf("BlindIndex: expected 32 bytes, got %d", len(out))
	}
}
