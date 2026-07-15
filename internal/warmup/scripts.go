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
	ID    int64
	Lang  string
	Turns []Turn
}

// LoadScripts 读 enabled 脚本;lang 非空按 lang 过滤,空取全部。
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
		out = append(out, sc)
	}
	return out, rows.Err()
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
