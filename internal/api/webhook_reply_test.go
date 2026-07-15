package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/acme/wadist/internal/audit"
	wlog "github.com/acme/wadist/internal/log"
	"github.com/acme/wadist/internal/store"
	"github.com/acme/wadist/internal/warmup"
)

// newWarmupReplyTestServer is newWarmupTestServer PLUS a real store.Manager
// (Postgres + Redis) wired into deps.Mgr. The messages.upsert webhook path
// resolves instance -> jid via h.inst.JIDForInstance, which the
// EvolutionWebhook constructor wires from deps.Mgr — newWarmupTestServer
// (Task 7) never set deps.Mgr since none of the warmup admin-API handlers it
// backs touch instance routing. This test does, so it needs the heavier
// setup (mirrors newAdminInstanceServer in instances_db_test.go, plus the
// warmup.Service wiring from newWarmupTestServer).
func newWarmupReplyTestServer(t *testing.T) (*Server, context.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	dsn := testDSN(t)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	applyAllMigrations(t, ctx, pool)

	rdb := newTestRedis(t)
	mgr, err := store.NewManager(ctx, store.Config{DSN: dsn, Redis: rdb, NodeID: "warmup-reply-test-node", BadgerDir: t.TempDir()}, wlog.Noop)
	if err != nil {
		t.Fatalf("store.NewManager: %v", err)
	}
	t.Cleanup(mgr.Close)

	svc := warmup.NewService(warmup.NewStore(pool), func() time.Time { return time.Now().UTC() })
	return &Server{sysPool: pool, deps: Deps{Audit: audit.NewAuditWriter(pool), Warmup: svc, Mgr: mgr}}, ctx
}

// TestMessagesUpsertRecordsWarmupReply pins P0-4 Task 14: an inbound
// messages.upsert webhook (fromMe=false, nested data.key shape) addressed to
// a pool-enrolled receiver's instance must feed warmup.RecordReply for the
// receiver's jid, incrementing replies_received. Uses a real pg-backed
// warmup.Service + real store.Manager-backed instance routing
// (seedWarmupAccount) since the warmup field is a concrete *warmup.Service,
// not a fakeable interface, for this call path.
func TestMessagesUpsertRecordsWarmupReply(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, ctx := newWarmupReplyTestServer(t)
	// enroll two pool accounts; seedWarmupAccount also binds an
	// account_instances row (instance_name -> jid) so JIDForInstance can
	// resolve the receiver's instance back to its jid.
	seedWarmupAccount(t, ctx, s, "recv@s.whatsapp.net") // instance "irecv"
	seedWarmupAccount(t, ctx, s, "send@s.whatsapp.net") // pool-internal sender

	wh := NewEvolutionWebhook("", s.deps.Receipt, s.deps.Mgr, nil, s.deps.QRCache).WithWarmup(s.deps.Warmup)
	r := gin.New()
	wh.Register(r)

	// inbound: instance "irecv" receives a message from send@ (fromMe=false).
	body := `{"event":"messages.upsert","instance":"irecv","data":{"key":{"remoteJid":"send@s.whatsapp.net","fromMe":false,"id":"MID1"}}}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/evolution", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d", w.Code)
	}

	p, err := s.deps.Warmup.Store().Get(ctx, "recv@s.whatsapp.net")
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}
	if p.RepliesReceived != 1 {
		t.Fatalf("want replies_received=1 got %d", p.RepliesReceived)
	}
}

// TestMessagesUpsertFromMeIsNotRecorded pins the fromMe=true exclusion: the
// receiver's OWN outbound message (echoed back via messages.upsert, fromMe
// true) must NOT count as a reply signal.
func TestMessagesUpsertFromMeIsNotRecorded(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, ctx := newWarmupTestServer(t)
	seedWarmupAccount(t, ctx, s, "recv2@s.whatsapp.net")

	wh := NewEvolutionWebhook("", s.deps.Receipt, s.deps.Mgr, nil, s.deps.QRCache).WithWarmup(s.deps.Warmup)
	r := gin.New()
	wh.Register(r)

	body := `{"event":"messages.upsert","instance":"irecv2","data":{"key":{"remoteJid":"someone@s.whatsapp.net","fromMe":true,"id":"MID2"}}}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/evolution", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d", w.Code)
	}

	p, err := s.deps.Warmup.Store().Get(ctx, "recv2@s.whatsapp.net")
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}
	if p.RepliesReceived != 0 {
		t.Fatalf("want replies_received=0 got %d", p.RepliesReceived)
	}
}
