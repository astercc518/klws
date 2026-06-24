package canary

import (
	"strconv"
	"testing"
)

func TestInCohort_Deterministic(t *testing.T) {
	jid := "1234567890.0:0@s.whatsapp.net"
	if InCohort(jid, 0) {
		t.Fatal("pct=0 → never in cohort")
	}
	if !InCohort(jid, 100) {
		t.Fatal("pct=100 → always in cohort")
	}
	// deterministic
	a := InCohort(jid, 50)
	b := InCohort(jid, 50)
	if a != b {
		t.Fatal("must be deterministic for same jid/pct")
	}
}

func TestCohort_Label(t *testing.T) {
	if Cohort("x", 100) != "canary" || Cohort("x", 0) != "stable" {
		t.Fatal("Cohort label wrong")
	}
}

func TestInCohort_Distribution(t *testing.T) {
	in := 0
	const total = 10000
	for i := 0; i < total; i++ {
		if InCohort(jidN(i), 10) {
			in++
		}
	}
	// ~10% ± 2%
	if in < total*8/100 || in > total*12/100 {
		t.Fatalf("10%% cohort distribution off: %d/%d", in, total)
	}
}

func jidN(i int) string { return "jid-" + strconv.Itoa(i) + "@s.whatsapp.net" }
