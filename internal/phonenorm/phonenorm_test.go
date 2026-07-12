package phonenorm

import "testing"

func TestNormalize(t *testing.T) {
	cases := []struct {
		raw, def string
		wantE164 string
		wantCC   string
		ok       bool
	}{
		{"+1 (415) 555-2671", "", "+14155552671", "US", true},
		{"4155552671", "US", "+14155552671", "US", true},       // 补美国码
		{"08613800138000", "CN", "+8613800138000", "CN", true}, // 去前导0/国际前缀
		{"13800138000", "CN", "+8613800138000", "CN", true},
		{"555", "US", "", "", false}, // 太短 -> 非法
		{"not-a-number", "US", "", "", false},
		{"", "US", "", "", false},
		// US wins the shared +1 code by explicit rule (even if raw input looks Canadian).
		{"+1 (250) 555-0123", "", "+12505550123", "US", true},
	}
	for _, c := range cases {
		e, cc, ok := Normalize(c.raw, c.def)
		if ok != c.ok || (ok && (e != c.wantE164 || cc != c.wantCC)) {
			t.Fatalf("Normalize(%q,%q)=(%q,%q,%v) want (%q,%q,%v)", c.raw, c.def, e, cc, ok, c.wantE164, c.wantCC, c.ok)
		}
	}
}
