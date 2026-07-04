package dispatch

import (
	"math"
	"testing"
)

func TestNextRate_AIMD(t *testing.T) {
	p := GovParams{SLO: 0.02, Step: 5, Factor: 0.5, MinRate: 1, MaxRate: 160}
	cases := []struct {
		name             string
		current, banRate float64
		want             float64
	}{
		{"healthy additive increase", 100, 0.00, 105},
		{"healthy capped at max", 158, 0.01, 160},
		{"over SLO multiplicative decrease", 100, 0.05, 50},
		{"decrease floored at min", 1.5, 0.9, 1}, // 1.5*0.5=0.75 → floored to 1
		{"exactly at SLO decreases", 80, 0.02, 40},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := nextRate(c.current, c.banRate, p); math.Abs(got-c.want) > 1e-9 {
				t.Fatalf("nextRate(%v,%v) = %v; want %v", c.current, c.banRate, got, c.want)
			}
		})
	}
}
