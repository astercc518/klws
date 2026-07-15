package warmup

import (
	"context"
	"math/rand"
	"testing"
	"time"
)

type fakeSender struct {
	sent   []string
	typing int
}

func (f *fakeSender) SendText(_ context.Context, inst, to, text string) error {
	f.sent = append(f.sent, inst+"->"+to+":"+text)
	return nil
}
func (f *fakeSender) SendTyping(_ context.Context, _, _ string, _ bool) error {
	f.typing++
	return nil
}

type fakeAccounts map[string]Account

func (m fakeAccounts) WarmupAccount(_ context.Context, jid string) (Account, error) {
	a, ok := m[jid]
	if !ok {
		return Account{}, ErrNotFound
	}
	return a, nil
}

func TestPairAndWarm(t *testing.T) {
	pool, ctx := pgPool(t)
	st := NewStore(pool)
	now := time.Now().UTC()
	for _, jid := range []string{"pa@s.whatsapp.net", "pb@s.whatsapp.net"} {
		_ = st.EnrollIfAbsent(ctx, jid, 1, LaneStandard, now)
	}
	accts := fakeAccounts{
		"pa@s.whatsapp.net": {JID: "pa@s.whatsapp.net", InstanceName: "ia", PhoneNumber: "551100", Lang: "pt", Online: true},
		"pb@s.whatsapp.net": {JID: "pb@s.whatsapp.net", InstanceName: "ib", PhoneNumber: "551101", Lang: "pt", Online: true},
	}
	fs := &fakeSender{}
	svc := NewService(st, fixedClock(now)).WithSender(fs).WithAccounts(accts)

	pairs, msgs, err := svc.PairAndWarm(ctx, 10, rand.New(rand.NewSource(7)), func(time.Duration) {})
	if err != nil || pairs != 1 || msgs < 2 {
		t.Fatalf("pair want 1 pair >=2 msgs got %d/%d err=%v", pairs, msgs, err)
	}
	if len(fs.sent) < 2 {
		t.Fatalf("expected >=2 sends, got %d", len(fs.sent))
	}
	if fs.typing == 0 {
		t.Fatalf("expected typing presence before sends")
	}
	// 计数已 bump
	p, _ := st.Get(ctx, "pa@s.whatsapp.net")
	if p.WarmupMessagesSent == 0 || p.WarmupSentToday == 0 {
		t.Fatalf("counters not bumped: %+v", p)
	}
}

func TestPairAndWarmSkipsOffline(t *testing.T) {
	pool, ctx := pgPool(t)
	st := NewStore(pool)
	now := time.Now().UTC()
	_ = st.EnrollIfAbsent(ctx, "on@s.whatsapp.net", 1, LaneStandard, now)
	_ = st.EnrollIfAbsent(ctx, "off@s.whatsapp.net", 1, LaneStandard, now)
	accts := fakeAccounts{
		"on@s.whatsapp.net":  {JID: "on@s.whatsapp.net", InstanceName: "i1", PhoneNumber: "1", Lang: "pt", Online: true},
		"off@s.whatsapp.net": {JID: "off@s.whatsapp.net", InstanceName: "i2", PhoneNumber: "2", Lang: "pt", Online: false},
	}
	svc := NewService(st, fixedClock(now)).WithSender(&fakeSender{}).WithAccounts(accts)
	pairs, _, err := svc.PairAndWarm(ctx, 10, rand.New(rand.NewSource(1)), func(time.Duration) {})
	if err != nil || pairs != 0 {
		t.Fatalf("offline account must not pair: pairs=%d err=%v", pairs, err)
	}
}
