// internal/console/numbers_test.go
package console

import "testing"

func TestParseNumbers(t *testing.T) {
	raw := "+1 (555) 123-4567\n15551234567\n  \n447911123456,\n447911123456\nabc\n12\n+1-555-123-4567"
	got := parseNumbers(raw)
	// "+15551234567" appears 3 times (the +1..., the +1-555..., normalize equal) → dupes;
	// "15551234567" is a distinct number; "447911123456" twice → 1 dupe; "abc"/"12" invalid.
	// Assert the distinct valid set and that counts are non-zero where expected.
	if len(got.Valid) == 0 {
		t.Fatalf("expected some valid numbers, got %+v", got)
	}
	for _, v := range got.Valid {
		if len(v) < 8 {
			t.Fatalf("invalid number slipped through: %q", v)
		}
	}
	if got.Invalid < 2 { // "abc" and "12"
		t.Fatalf("expected >=2 invalid, got %d", got.Invalid)
	}
	if got.Duplicates < 1 {
		t.Fatalf("expected >=1 duplicate, got %d", got.Duplicates)
	}
	// no duplicates remain in Valid
	seen := map[string]bool{}
	for _, v := range got.Valid {
		if seen[v] {
			t.Fatalf("duplicate in Valid: %q", v)
		}
		seen[v] = true
	}
}

func TestParseNumbersEmpty(t *testing.T) {
	if got := parseNumbers("   \n , \n"); len(got.Valid) != 0 {
		t.Fatalf("expected no valid, got %+v", got)
	}
}
