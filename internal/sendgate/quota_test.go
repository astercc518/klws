// internal/sendgate/quota_test.go
package sendgate

import (
	"testing"
	"time"
)

func TestEffectiveQuota_CurveAndHealthDiscount(t *testing.T) {
	now := time.Date(2026, 1, 30, 12, 0, 0, 0, time.UTC)
	age := func(days float64) time.Time { return now.Add(-time.Duration(days * 24 * float64(time.Hour))) }
	cases := []struct {
		name           string
		reg            time.Time
		health, want   int
	}{
		{"new-day0", age(0.5), 100, 20},
		{"day3", age(3), 100, 50},
		{"day5", age(5), 100, 100},
		{"day10", age(10), 100, 250},
		{"mature", age(30), 100, 1000},
		{"mature-health70", age(30), 70, 700},
		{"floor-at-1", age(0.5), 0, 1}, // base 20 * 0 / 100 = 0 → floored to 1
	}
	for _, c := range cases {
		if got := EffectiveQuota(c.reg, c.health, now); got != c.want {
			t.Fatalf("%s: EffectiveQuota = %d, want %d", c.name, got, c.want)
		}
	}
}
