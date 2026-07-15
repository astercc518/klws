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

// Save 覆盖写全部可变字段。仅供测试灌数据用;线上写路径一律走下面的
// 字段级原子 UPDATE(见 IncReplies/BumpWarmupSent/SetPausedCol/SetLaneCol/
// PromoteCol/DemoteCol),避免跨进程(CONSOLE webhook 写手 vs WORKER tick)用
// 陈旧内存快照整行覆盖、丢掉并发写入的字段(lost update)。
func (s *Store) Save(ctx context.Context, p Profile, now time.Time) error {
	_, err := s.pool.Exec(ctx, `
UPDATE warmup_profiles SET lane=$2, stage=$3, warmup_messages_sent=$4, replies_received=$5,
	online_since=$6, matured_at=$7, warmup_sent_today=$8, warmup_sent_date=$9, paused=$10, updated_at=$11
WHERE account_jid=$1`,
		p.AccountJID, string(p.Lane), string(p.Stage), p.WarmupMessagesSent, p.RepliesReceived,
		p.OnlineSince, p.MaturedAt, p.WarmupSentToday, p.WarmupSentDate, p.Paused, now)
	return err
}

// IncReplies 原子累加 replies_received(仅动这一列)。非池内 jid(无 profile)
// UPDATE 影响 0 行,天然 no-op,不返回错误 —— 与 RecordReply 现有静默跳过语义一致。
func (s *Store) IncReplies(ctx context.Context, jid string) error {
	_, err := s.pool.Exec(ctx, `
UPDATE warmup_profiles SET replies_received = replies_received + 1, updated_at = now()
WHERE account_jid = $1`, jid)
	return err
}

// BumpWarmupSent 原子累加 warmup_messages_sent + 当日 warmup_sent_today(跨日自动清零),
// 仅动这几列。today 须是按日截断的 time.Time(列类型是 DATE)。
func (s *Store) BumpWarmupSent(ctx context.Context, jid string, today time.Time) error {
	_, err := s.pool.Exec(ctx, `
UPDATE warmup_profiles SET warmup_messages_sent = warmup_messages_sent + 1,
	warmup_sent_today = (CASE WHEN warmup_sent_date = $2 THEN warmup_sent_today ELSE 0 END) + 1,
	warmup_sent_date = $2, updated_at = now()
WHERE account_jid = $1`, jid, today)
	return err
}

// SetPausedCol 原子写 paused(仅动这一列)。
func (s *Store) SetPausedCol(ctx context.Context, jid string, paused bool) error {
	_, err := s.pool.Exec(ctx, `
UPDATE warmup_profiles SET paused = $2, updated_at = now() WHERE account_jid = $1`, jid, paused)
	return err
}

// SetLaneCol 原子写 lane(仅动这一列)。
func (s *Store) SetLaneCol(ctx context.Context, jid string, lane Lane) error {
	_, err := s.pool.Exec(ctx, `
UPDATE warmup_profiles SET lane = $2, updated_at = now() WHERE account_jid = $1`, jid, string(lane))
	return err
}

// PromoteCol 原子条件 UPDATE:仅当当前 stage=WARMING 才升 MATURE(仅动
// stage/matured_at 两列)。返回是否真的改动了一行(false = 未处于 WARMING,
// 含已被并发升级/未 enroll 的情况)。
func (s *Store) PromoteCol(ctx context.Context, jid string, maturedAt time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
UPDATE warmup_profiles SET stage = 'MATURE', matured_at = $2, updated_at = now()
WHERE account_jid = $1 AND stage = 'WARMING'`, jid, maturedAt)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// DemoteCol 原子条件 UPDATE:仅当当前 stage=MATURE 才退回 WARMING(仅动
// stage/matured_at/online_since 三列,重置在线基准重新计时)。返回是否真的
// 改动了一行(false = 未处于 MATURE)。
func (s *Store) DemoteCol(ctx context.Context, jid string, onlineSince time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
UPDATE warmup_profiles SET stage = 'WARMING', matured_at = NULL, online_since = $2, updated_at = now()
WHERE account_jid = $1 AND stage = 'MATURE'`, jid, onlineSince)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
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
