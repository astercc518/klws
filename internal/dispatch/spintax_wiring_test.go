package dispatch

import "testing"

func TestRenderBodyExpandsSpintaxThenVars(t *testing.T) {
	// deterministic seed → stable choice; vars still substituted.
	out := renderBody("{Hi|Hello} {{.name}}", map[string]any{"name": "Sam"}, "msg-123")
	if out != "Hi Sam" && out != "Hello Sam" {
		t.Fatalf("unexpected render: %q", out)
	}
	// same seed (same MessageID) → identical output across calls (retry-stable).
	again := renderBody("{Hi|Hello} {{.name}}", map[string]any{"name": "Sam"}, "msg-123")
	if out != again {
		t.Fatalf("not deterministic for same seed: %q vs %q", out, again)
	}
}
