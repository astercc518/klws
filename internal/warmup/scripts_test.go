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

// TestScriptCRUD 覆盖 Task 18 的脚本库管理:创建→ListAllScripts 可见(含未
// enabled 之前也应可见,这里先验证 enabled 创建默认可见)→SetScriptEnabled(false)
// 后 LoadScripts(lang) 不再返回但 ListAllScripts 仍返回→DeleteScript 后彻底消失。
func TestScriptCRUD(t *testing.T) {
	pool, ctx := pgPool(t)
	st := NewStore(pool)

	turns := []Turn{
		{From: "A", Text: "oi {name}"},
		{From: "B", Text: "tudo bem {emoji}"},
	}
	id, err := st.CreateScript(ctx, "xx-crud-test", turns)
	if err != nil || id == 0 {
		t.Fatalf("create script: id=%d err=%v", id, err)
	}

	all, err := st.ListAllScripts(ctx)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	found := false
	for _, sc := range all {
		if sc.ID == id {
			found = true
			if !sc.Enabled {
				t.Fatalf("newly created script should be enabled by default, got %+v", sc)
			}
			if len(sc.Turns) != 2 {
				t.Fatalf("want 2 turns, got %+v", sc.Turns)
			}
		}
	}
	if !found {
		t.Fatalf("created script id=%d not found in ListAllScripts", id)
	}

	// disable it: LoadScripts(lang) 应不再返回,ListAllScripts 仍返回但 Enabled=false。
	if err := st.SetScriptEnabled(ctx, id, false); err != nil {
		t.Fatalf("set disabled: %v", err)
	}
	loaded, err := st.LoadScripts(ctx, "xx-crud-test")
	if err != nil {
		t.Fatalf("load after disable: %v", err)
	}
	if len(loaded) != 0 {
		t.Fatalf("disabled script should not be returned by LoadScripts, got %+v", loaded)
	}
	all2, err := st.ListAllScripts(ctx)
	if err != nil {
		t.Fatalf("list all after disable: %v", err)
	}
	stillThere := false
	for _, sc := range all2 {
		if sc.ID == id {
			stillThere = true
			if sc.Enabled {
				t.Fatalf("script should show Enabled=false after SetScriptEnabled(false), got %+v", sc)
			}
		}
	}
	if !stillThere {
		t.Fatalf("disabled script should still appear in ListAllScripts, id=%d missing from %+v", id, all2)
	}

	// delete: gone from ListAllScripts entirely.
	if err := st.DeleteScript(ctx, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	all3, err := st.ListAllScripts(ctx)
	if err != nil {
		t.Fatalf("list all after delete: %v", err)
	}
	for _, sc := range all3 {
		if sc.ID == id {
			t.Fatalf("deleted script id=%d still present: %+v", id, all3)
		}
	}
}
