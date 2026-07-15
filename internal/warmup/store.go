package warmup

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound 表示无该 jid 的 warmup profile。
var ErrNotFound = errors.New("warmup: profile not found")

// Profile 是 warmup_profiles 一行。
type Profile struct {
	AccountJID         string
	TenantID           int64
	Lane               Lane
	Stage              Stage
	WarmupMessagesSent int
	RepliesReceived    int
	OnlineSince        *time.Time
	MaturedAt          *time.Time
	WarmupSentToday    int
	WarmupSentDate     *time.Time
	Paused             bool
}

// Store 是 warmup 三表的持久层。pool 必须是 SystemPool(BYPASSRLS):
// warmup 表 REVOKE 了 app_tenant,worker 与 admin 跨租户操作。
type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// EnrollIfAbsent 幂等插入 stage=WARMING 的 profile;已存在则不动(不重置车道/信号)。
func (s *Store) EnrollIfAbsent(ctx context.Context, jid string, tenantID int64, lane Lane, now time.Time) error {
	_, err := s.pool.Exec(ctx, `
INSERT INTO warmup_profiles (account_jid, tenant_id, lane, stage, online_since, created_at, updated_at)
VALUES ($1, $2, $3, 'WARMING', $4, $4, $4)
ON CONFLICT (account_jid) DO NOTHING`, jid, tenantID, string(lane), now)
	return err
}

const profileCols = `account_jid, tenant_id, lane, stage, warmup_messages_sent,
	replies_received, online_since, matured_at, warmup_sent_today, warmup_sent_date, paused`

func scanProfile(row pgx.Row) (Profile, error) {
	var p Profile
	var lane, stage string
	err := row.Scan(&p.AccountJID, &p.TenantID, &lane, &stage, &p.WarmupMessagesSent,
		&p.RepliesReceived, &p.OnlineSince, &p.MaturedAt, &p.WarmupSentToday, &p.WarmupSentDate, &p.Paused)
	p.Lane = Lane(lane)
	p.Stage = Stage(stage)
	return p, err
}

func (s *Store) Get(ctx context.Context, jid string) (Profile, error) {
	p, err := scanProfile(s.pool.QueryRow(ctx,
		`SELECT `+profileCols+` FROM warmup_profiles WHERE account_jid=$1`, jid))
	if errors.Is(err, pgx.ErrNoRows) {
		return Profile{}, ErrNotFound
	}
	return p, err
}

func (s *Store) ListByStage(ctx context.Context, stage Stage, limit int) ([]Profile, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+profileCols+` FROM warmup_profiles WHERE stage=$1 ORDER BY updated_at LIMIT $2`,
		string(stage), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Profile
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Save 覆盖写全部可变字段。
func (s *Store) Save(ctx context.Context, p Profile, now time.Time) error {
	_, err := s.pool.Exec(ctx, `
UPDATE warmup_profiles SET lane=$2, stage=$3, warmup_messages_sent=$4, replies_received=$5,
	online_since=$6, matured_at=$7, warmup_sent_today=$8, warmup_sent_date=$9, paused=$10, updated_at=$11
WHERE account_jid=$1`,
		p.AccountJID, string(p.Lane), string(p.Stage), p.WarmupMessagesSent, p.RepliesReceived,
		p.OnlineSince, p.MaturedAt, p.WarmupSentToday, p.WarmupSentDate, p.Paused, now)
	return err
}

func (s *Store) PolicyFor(ctx context.Context, lane Lane) (Policy, error) {
	var p Policy
	err := s.pool.QueryRow(ctx, `
SELECT min_warmup_messages, min_replies, min_online_hours, warming_cap,
	mature_base_cap, mature_max_cap, mature_ramp_step
FROM warmup_policies WHERE lane=$1`, string(lane)).Scan(
		&p.MinWarmupMessages, &p.MinReplies, &p.MinOnlineHours, &p.WarmingCap,
		&p.MatureBaseCap, &p.MatureMaxCap, &p.MatureRampStep)
	if errors.Is(err, pgx.ErrNoRows) {
		return Policy{}, ErrNotFound
	}
	return p, err
}
