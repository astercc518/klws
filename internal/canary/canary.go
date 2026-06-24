// Package canary assigns accounts to a rollout cohort deterministically so a new
// release/behavior can be staged to a percentage of accounts. Uses the same
// fnv64a hash as the advisory-lock key (stable across processes).
package canary

import "hash/fnv"

func bucket(jid string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(jid))
	return h.Sum64() % 100
}

// InCohort reports whether jid is in the canary cohort for the given percent
// (0..100). pct=0 → never; pct>=100 → always.
func InCohort(jid string, pct uint8) bool {
	if pct == 0 {
		return false
	}
	if pct >= 100 {
		return true
	}
	return bucket(jid) < uint64(pct)
}

// Cohort returns the bounded metric label for jid.
func Cohort(jid string, pct uint8) string {
	if InCohort(jid, pct) {
		return "canary"
	}
	return "stable"
}
