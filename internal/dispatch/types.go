// Package dispatch orchestrates campaign sending: it matches recipients to
// healthy accounts, gates via sendgate, charges via billing, and enqueues sends.
package dispatch

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/acme/wadist/internal/billing"
	"github.com/acme/wadist/internal/metrics"
	"github.com/acme/wadist/internal/sendgate"
)

var ErrNoCapacity = errors.New("dispatch: no account with remaining quota")

// MediaHandle is a reusable WhatsApp media upload result (per uploading account).
type MediaHandle struct {
	URL, DirectPath                      string
	MediaKey, FileSHA256, FileEncSHA256  []byte
	FileLength                           int64
}

// SendPayload is the unit of work handed to the send queue.
type SendPayload struct {
	TenantID, CampaignID, RecipientID int64
	JID, Phone, Country, MessageID    string
	Vars                              map[string]any
}

// Sender performs the actual WhatsApp send (faked in tests; real impl wraps whatsmeow).
type Sender interface {
	Send(ctx context.Context, jid, phone, body string, media *MediaHandle) (msgID string, err error)
}

// Uploader uploads media for an account (faked in tests).
type Uploader interface {
	Upload(ctx context.Context, jid string, data []byte, mediaSha, mime string) (*MediaHandle, error)
}

// Enqueuer schedules a send (pump adapter in prod; fake in tests).
type Enqueuer interface {
	EnqueueSend(ctx context.Context, p SendPayload, delay time.Duration) error
}

// Dispatcher pulls pending recipients and assigns them to accounts.
type Dispatcher struct {
	pool     *pgxpool.Pool
	billing  *billing.Repo
	queue    Enqueuer
	priceFor func(country string) int64
	baseGap  time.Duration
	m        *metrics.Metrics
}

func NewDispatcher(pool *pgxpool.Pool, b *billing.Repo, q Enqueuer, priceFor func(string) int64, baseGap time.Duration) *Dispatcher {
	return &Dispatcher{pool: pool, billing: b, queue: q, priceFor: priceFor, baseGap: baseGap}
}

func (d *Dispatcher) WithMetrics(m *metrics.Metrics) *Dispatcher { d.m = m; return d }

// SendWorker executes a SendPayload: admit, hold, render, send, settle/refund.
type SendWorker struct {
	pool      *pgxpool.Pool
	gate      *sendgate.SendGate
	billing   *billing.Repo
	sender    Sender
	uploader  Uploader
	m         *metrics.Metrics
	canaryPct uint8
	priceFor  func(ctx context.Context, tenantID int64, country string) int64
}

func NewSendWorker(pool *pgxpool.Pool, g *sendgate.SendGate, b *billing.Repo, s Sender, u Uploader) *SendWorker {
	return &SendWorker{pool: pool, gate: g, billing: b, sender: s, uploader: u}
}

func (w *SendWorker) WithMetrics(m *metrics.Metrics) *SendWorker { w.m = m; return w }

// WithCanary sets the canary rollout percentage (0..100) for cohort metric labelling.
// Default 0 means all sends are labelled "stable". Does not change NewSendWorker signature.
func (w *SendWorker) WithCanary(pct uint8) *SendWorker { w.canaryPct = pct; return w }

// WithPricing injects the per-tenant×country unit-price lookup used at the charge
// point. When unset, amountFor returns 1 (the pre-pricing default).
func (w *SendWorker) WithPricing(fn func(ctx context.Context, tenantID int64, country string) int64) *SendWorker {
	w.priceFor = fn
	return w
}

// amountFor resolves the charge amount for a message; defaults to 1 minor unit.
func (w *SendWorker) amountFor(ctx context.Context, tenantID int64, country string) int64 {
	if w.priceFor == nil {
		return 1
	}
	return w.priceFor(ctx, tenantID, country)
}

// SendBodyResolver resolves a campaign's message body/media at send time.
// Consumed by the pump send loop.
type SendBodyResolver func(ctx context.Context, campaignID int64) (body, mediaSha, mime string, raw []byte, err error)
