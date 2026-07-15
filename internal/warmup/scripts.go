package warmup

import (
	"context"
	"encoding/json"
	"math/rand"
	"strings"
)

type Turn struct {
	From string `json:"from"`
	Text string `json:"text"`
}

type Script struct {
	ID      int64
	Lang    string
	Turns   []Turn
	Enabled bool
}

// LoadScripts 读 enabled 脚本;lang 非空按 lang 过滤,空取全部。所有返回行
// enabled=true(WHERE 已过滤),Enabled 字段固定置 true,不必再打一次 SELECT。
func (s *Store) LoadScripts(ctx context.Context, lang string) ([]Script, error) {
	q := `SELECT id, lang, turns FROM warmup_scripts WHERE enabled`
	args := []any{}
	if lang != "" {
		q += ` AND lang=$1`
		args = append(args, lang)
	}
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Script
	for rows.Next() {
		var sc Script
		var raw []byte
		if err := rows.Scan(&sc.ID, &sc.Lang, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &sc.Turns); err != nil {
			return nil, err
		}
		sc.Enabled = true
		out = append(out, sc)
	}
	return out, rows.Err()
}

// ListAllScripts 读全部脚本(含 disabled),供后台脚本库管理列表使用。
func (s *Store) ListAllScripts(ctx context.Context) ([]Script, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, lang, turns, enabled FROM warmup_scripts ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Script
	for rows.Next() {
		var sc Script
		var raw []byte
		if err := rows.Scan(&sc.ID, &sc.Lang, &raw, &sc.Enabled); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &sc.Turns); err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// CreateScript 插入一套新脚本(默认 enabled=true),返回新行 id。
func (s *Store) CreateScript(ctx context.Context, lang string, turns []Turn) (int64, error) {
	raw, err := json.Marshal(turns)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.pool.QueryRow(ctx,
		`INSERT INTO warmup_scripts (lang, turns) VALUES ($1, $2::jsonb) RETURNING id`,
		lang, raw).Scan(&id)
	return id, err
}

// SetScriptEnabled 启停一套脚本;disabled 脚本仍在 ListAllScripts 中可见,
// 但 LoadScripts (养号引擎选脚本用) 会过滤掉它。
func (s *Store) SetScriptEnabled(ctx context.Context, id int64, enabled bool) error {
	_, err := s.pool.Exec(ctx, `UPDATE warmup_scripts SET enabled=$2 WHERE id=$1`, id, enabled)
	return err
}

// DeleteScript 硬删除一套脚本。
func (s *Store) DeleteScript(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM warmup_scripts WHERE id=$1`, id)
	return err
}

// PickScript 随机挑一套脚本。
func PickScript(scripts []Script, rng *rand.Rand) (Script, bool) {
	if len(scripts) == 0 {
		return Script{}, false
	}
	return scripts[rng.Intn(len(scripts))], true
}

var nameCandidates = []string{"", "amigo", "tudo", "man", "cara"}
var emojiCandidates = []string{"🙂", "👍", "😄", "🤝", "✌️"}

// RenderTurn 替换占位符为随机候选(空名去掉多余空格)。
func RenderTurn(t Turn, rng *rand.Rand) string {
	out := t.Text
	out = strings.ReplaceAll(out, "{name}", nameCandidates[rng.Intn(len(nameCandidates))])
	out = strings.ReplaceAll(out, "{emoji}", emojiCandidates[rng.Intn(len(emojiCandidates))])
	return strings.TrimSpace(strings.ReplaceAll(out, "  ", " "))
}
