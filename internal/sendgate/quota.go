// internal/sendgate/quota.go
package sendgate

import "time"

// warmupQuota maps account age to a base daily send cap (the warmup curve).
// MUST stay in sync with the SQL effective_quota() function in migration 0005.
func warmupQuota(age time.Duration) int {
	d := age.Hours() / 24
	switch {
	case d < 2:
		return 20
	case d < 4:
		return 50
	case d < 8:
		return 100
	case d < 15:
		return 250
	default:
		return 1000
	}
}

// EffectiveQuota = warmup base × health discount, floored at 1.
func EffectiveQuota(registeredAt time.Time, health int, now time.Time) int {
	base := warmupQuota(now.Sub(registeredAt))
	q := base * health / 100
	if q < 1 {
		q = 1
	}
	return q
}
