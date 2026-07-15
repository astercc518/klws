package warmup

import (
	"math/rand"
	"strings"
	"testing"
)

func TestLoadAndPickScript(t *testing.T) {
	pool, ctx := pgPool(t)
	st := NewStore(pool)
	scripts, err := st.LoadScripts(ctx, "pt")
	if err != nil || len(scripts) == 0 {
		t.Fatalf("load pt scripts: n=%d err=%v", len(scripts), err)
	}
	if len(scripts[0].Turns) < 2 {
		t.Fatalf("seeded pt script should be multi-turn, got %d turns", len(scripts[0].Turns))
	}
	rng := rand.New(rand.NewSource(42))
	sc, ok := PickScript(scripts, rng)
	if !ok || len(sc.Turns) == 0 {
		t.Fatalf("pick failed")
	}
}

func TestRenderTurnSubstitutes(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	out := RenderTurn(Turn{From: "A", Text: "oi {name} {emoji}"}, rng)
	if strings.Contains(out, "{name}") || strings.Contains(out, "{emoji}") {
		t.Fatalf("placeholders not substituted: %q", out)
	}
}
