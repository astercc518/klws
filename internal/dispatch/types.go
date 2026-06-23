// Package dispatch orchestrates campaign sending: it matches recipients to
// healthy accounts, gates via sendgate, charges via billing, and enqueues sends.
package dispatch

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/acme/wadist/internal/billing"
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

// Enqueuer schedules a send (asynq adapter in prod; fake in tests).
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
}

func NewDispatcher(pool *pgxpool.Pool, b *billing.Repo, q Enqueuer, priceFor func(string) int64, baseGap time.Duration) *Dispatcher {
	return &Dispatcher{pool: pool, billing: b, queue: q, priceFor: priceFor, baseGap: baseGap}
}

// SendWorker executes a SendPayload: admit, hold, render, send, settle/refund.
type SendWorker struct {
	pool     *pgxpool.Pool
	gate     *sendgate.SendGate
	billing  *billing.Repo
	sender   Sender
	uploader Uploader
}

func NewSendWorker(pool *pgxpool.Pool, g *sendgate.SendGate, b *billing.Repo, s Sender, u Uploader) *SendWorker {
	return &SendWorker{pool: pool, gate: g, billing: b, sender: s, uploader: u}
}
