// internal/spintax/spintax_test.go
package spintax

import (
	"math/rand"
	"strings"
	"testing"
)

func TestExpandPicksOneOption(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	got := Expand("{Hi|Hello|Hey} there", r)
	if !strings.HasSuffix(got, " there") {
		t.Fatalf("literal tail lost: %q", got)
	}
	first := strings.TrimSuffix(got, " there")
	if first != "Hi" && first != "Hello" && first != "Hey" {
		t.Fatalf("unexpected choice: %q", first)
	}
}

func TestExpandNested(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	got := Expand("{Hi {there|friend}|Hello}", r)
	ok := got == "Hello" || got == "Hi there" || got == "Hi friend"
	if !ok {
		t.Fatalf("nested expansion wrong: %q", got)
	}
}

func TestExpandNoBraces(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	if got := Expand("plain text, no spin", r); got != "plain text, no spin" {
		t.Fatalf("plain text altered: %q", got)
	}
}

func TestExpandDeterministicWithSameSeed(t *testing.T) {
	a := Expand("{a|b|c|d|e}", rand.New(rand.NewSource(42)))
	b := Expand("{a|b|c|d|e}", rand.New(rand.NewSource(42)))
	if a != b {
		t.Fatalf("same seed must yield same expansion: %q vs %q", a, b)
	}
}

func TestExpandEmptyOption(t *testing.T) {
	// "{|x}" means "" or "x" — must not panic, must be one of them.
	r := rand.New(rand.NewSource(4))
	got := Expand("a{|b}c", r)
	if got != "ac" && got != "abc" {
		t.Fatalf("empty-option expansion wrong: %q", got)
	}
}

func TestExpandPreservesTemplateVarInsideOption(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	got := Expand("{Hi {{.name}}|Hello}", r)
	if got != "Hi {{.name}}" && got != "Hello" {
		t.Fatalf("template var inside option corrupted: %q", got)
	}
}
