package warmup

import "testing"

func TestTransition(t *testing.T) {
	cases := []struct {
		s    Stage
		e    Event
		want Stage
		ok   bool
	}{
		{StageNew, EventEnroll, StageWarming, true},
		{StageWarming, EventPromote, StageMature, true},
		{StageMature, EventDemote, StageWarming, true},
		{StageNew, EventPromote, "", false},
		{StageMature, EventEnroll, "", false},
		{StageWarming, EventDemote, "", false},
	}
	for _, c := range cases {
		got, err := Transition(c.s, c.e)
		if c.ok && (err != nil || got != c.want) {
			t.Fatalf("%s+%s: want %s ok, got %s err=%v", c.s, c.e, c.want, got, err)
		}
		if !c.ok && err == nil {
			t.Fatalf("%s+%s: want error, got %s", c.s, c.e, got)
		}
	}
}
