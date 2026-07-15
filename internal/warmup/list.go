package warmup

import (
	"context"
	"strings"
	"time"
)

// ListRow 是养号中心列表一行(warmup_profiles LEFT JOIN account_devices)。
type ListRow struct {
	AccountJID             string     `json:"account_jid"`
	TenantID               int64      `json:"tenant_id"`
	Lane                   Lane       `json:"lane"`
	Stage                  Stage      `json:"stage"`
	WarmupMessagesSent     int        `json:"warmup_messages_sent"`
	RepliesReceived        int        `json:"replies_received"`
	OnlineHours            float64    `json:"online_hours"`
	Health                 int        `json:"health"`
	RegisteredAt           *time.Time `json:"registered_at"`
	BusinessQuotaRemaining int        `json:"business_quota_remaining"`
	Paused                 bool       `json:"paused"`
}

type ListFilter struct {
	Stage  string
	Lane   string
	Q      string
	Limit  int
	Offset int
}

// List 返回分页养号行 + 总数。BusinessQuotaRemaining 复用 SQL effective_quota。
func (s *Store) List(ctx context.Context, f ListFilter) ([]ListRow, int, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	var where []string
	var args []any
	add := func(cond string, v any) { args = append(args, v); where = append(where, cond) }
	if f.Stage != "" {
		add("wp.stage=$"+itoa(len(args)+1), f.Stage)
	}
	if f.Lane != "" {
		add("wp.lane=$"+itoa(len(args)+1), f.Lane)
	}
	if f.Q != "" {
		add("wp.account_jid ILIKE $"+itoa(len(args)+1), "%"+f.Q+"%")
	}
	cond := ""
	if len(where) > 0 {
		cond = " WHERE " + strings.Join(where, " AND ")
	}

	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM warmup_profiles wp`+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	q := `
SELECT wp.account_jid, wp.tenant_id, wp.lane, wp.stage, wp.warmup_messages_sent,
       wp.replies_received,
       COALESCE(EXTRACT(EPOCH FROM (now()-wp.online_since))/3600, 0) AS online_hours,
       COALESCE(d.health_score, 0), d.registered_at,
       GREATEST(COALESCE(effective_quota(d.registered_at, d.health_score) - d.sent_today, 0), 0),
       wp.paused
  FROM warmup_profiles wp
  LEFT JOIN account_devices d ON d.account_jid = wp.account_jid` + cond +
		` ORDER BY wp.updated_at DESC LIMIT $` + itoa(len(args)+1) + ` OFFSET $` + itoa(len(args)+2)
	args = append(args, f.Limit, f.Offset)

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []ListRow
	for rows.Next() {
		var r ListRow
		var lane, stage string
		if err := rows.Scan(&r.AccountJID, &r.TenantID, &lane, &stage, &r.WarmupMessagesSent,
			&r.RepliesReceived, &r.OnlineHours, &r.Health, &r.RegisteredAt, &r.BusinessQuotaRemaining, &r.Paused); err != nil {
			return nil, 0, err
		}
		r.Lane, r.Stage = Lane(lane), Stage(stage)
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// Overview 是养号总览卡数据。
type Overview struct {
	New     int `json:"new"`
	Warming int `json:"warming"`
	Mature  int `json:"mature"`
	Paused  int `json:"paused"`
}

func (s *Store) Overview(ctx context.Context) (Overview, error) {
	var o Overview
	err := s.pool.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE stage='NEW'),
       count(*) FILTER (WHERE stage='WARMING'),
       count(*) FILTER (WHERE stage='MATURE'),
       count(*) FILTER (WHERE paused)
  FROM warmup_profiles`).Scan(&o.New, &o.Warming, &o.Mature, &o.Paused)
	return o, err
}

// UpsertPolicy 写一条车道策略(后台热改)。
func (s *Store) UpsertPolicy(ctx context.Context, lane Lane, p Policy, now time.Time) error {
	_, err := s.pool.Exec(ctx, `
INSERT INTO warmup_policies (lane, min_warmup_messages, min_replies, min_online_hours,
	warming_cap, mature_base_cap, mature_max_cap, mature_ramp_step, updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
ON CONFLICT (lane) DO UPDATE SET min_warmup_messages=EXCLUDED.min_warmup_messages,
	min_replies=EXCLUDED.min_replies, min_online_hours=EXCLUDED.min_online_hours,
	warming_cap=EXCLUDED.warming_cap, mature_base_cap=EXCLUDED.mature_base_cap,
	mature_max_cap=EXCLUDED.mature_max_cap, mature_ramp_step=EXCLUDED.mature_ramp_step,
	updated_at=EXCLUDED.updated_at`,
		string(lane), p.MinWarmupMessages, p.MinReplies, p.MinOnlineHours,
		p.WarmingCap, p.MatureBaseCap, p.MatureMaxCap, p.MatureRampStep, now)
	return err
}

func (s *Store) ListPolicies(ctx context.Context) (map[Lane]Policy, error) {
	rows, err := s.pool.Query(ctx, `
SELECT lane, min_warmup_messages, min_replies, min_online_hours, warming_cap,
	mature_base_cap, mature_max_cap, mature_ramp_step FROM warmup_policies`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[Lane]Policy{}
	for rows.Next() {
		var lane string
		var p Policy
		if err := rows.Scan(&lane, &p.MinWarmupMessages, &p.MinReplies, &p.MinOnlineHours,
			&p.WarmingCap, &p.MatureBaseCap, &p.MatureMaxCap, &p.MatureRampStep); err != nil {
			return nil, err
		}
		out[Lane(lane)] = p
	}
	return out, rows.Err()
}

// itoa 是最小无依赖整数转字符串(避免 import strconv 仅为拼参数号)。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
