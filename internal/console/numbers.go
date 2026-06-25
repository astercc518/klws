// internal/console/numbers.go
package console

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/acme/wadist/internal/crypto"
)

var ErrSendNotConfigured = errors.New("console: send flow requires WADIST_BLIND_INDEX_KEY")

// ParsedNumbers is the outcome of parsing an uploaded/pasted number pack.
type ParsedNumbers struct {
	Valid      []string
	Duplicates int
	Invalid    int
}

// parseNumbers splits a raw pack (newline/comma/space separated), normalizes each
// entry to leading-'+'? + digits, validates 8–15 digits, dedupes (first-seen order).
func parseNumbers(raw string) ParsedNumbers {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ',' || r == ';' || r == '\t' || r == ' '
	})
	var out ParsedNumbers
	seen := make(map[string]bool)
	for _, f := range fields {
		n := normalizePhone(f)
		if !validPhone(n) {
			out.Invalid++
			continue
		}
		if seen[n] {
			out.Duplicates++
			continue
		}
		seen[n] = true
		out.Valid = append(out.Valid, n)
	}
	return out
}

func normalizePhone(s string) string {
	var b strings.Builder
	for i, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' && i == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func validPhone(n string) bool {
	digits := strings.TrimPrefix(n, "+")
	if len(digits) < 8 || len(digits) > 15 {
		return false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// WithBlindKey injects the HMAC blind-index key used for suppression + dedup.
func (s *Server) WithBlindKey(key []byte) *Server { s.blindKey = key; return s }

// filterSuppressed removes phones present in the tenant's suppression_list.
// Returns ErrSendNotConfigured when no blind key is configured (fail-closed:
// we must never send to an opted-out number).
func (s *Server) filterSuppressed(ctx context.Context, phones []string) (kept []string, filtered int, err error) {
	if len(s.blindKey) != 32 {
		return nil, 0, ErrSendNotConfigured
	}
	tx, err := s.withTenantTx(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback(ctx)
	for _, p := range phones {
		bidx := crypto.BlindIndex(s.blindKey, p)
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM suppression_list WHERE phone_bidx=$1)`, bidx).Scan(&exists); err != nil {
			return nil, 0, fmt.Errorf("suppression check: %w", err)
		}
		if exists {
			filtered++
			continue
		}
		kept = append(kept, p)
	}
	return kept, filtered, nil
}
