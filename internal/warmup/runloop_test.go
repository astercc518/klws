package warmup

import (
	"math/rand"
	"testing"
	"time"
)

func TestTickOncePromotesAndPairs(t *testing.T) {
	pool, ctx := pgPool(t)
	st := NewStore(pool)
	now := time.Now().UTC()
	// A ripe account (enough signal to graduate) + a pair that can message each other.
	_ = st.EnrollIfAbsent(ctx, "ripe@s.whatsapp.net", 1, LaneFast, now.Add(-3*time.Hour))
	rp, _ := st.Get(ctx, "ripe@s.whatsapp.net")
	rp.WarmupMessagesSent = 2 // FAST minMessages=2, minReplies=0, minOnlineHours=2
	_ = st.Save(ctx, rp, now)

	accts := fakeAccounts{"ripe@s.whatsapp.net": {JID: "ripe@s.whatsapp.net", InstanceName: "i", PhoneNumber: "1", Lang: "pt", Online: true}}
	svc := NewService(st, fixedClock(now)).WithSender(&fakeSender{}).WithAccounts(accts)

	if err := svc.TickOnce(ctx, rand.New(rand.NewSource(3)), func(time.Duration) {}); err != nil {
		t.Fatalf("tick: %v", err)
	}
	p, _ := st.Get(ctx, "ripe@s.whatsapp.net")
	if p.Stage != StageMature {
		t.Fatalf("ripe account should have matured, got %s", p.Stage)
	}
}
