package api

import (
	"fmt"
	"strings"
)

type contactFilter struct {
	Q       string
	Country string
	Status  string
	TagID   int64
	Limit   int
	Offset  int
}

// buildContactWhere builds the WHERE clause for a query shaped as
//
//	FROM contacts c LEFT JOIN contact_tag_map m ON m.contact_id = c.id
func buildContactWhere(f contactFilter) (string, []any) {
	var conds []string
	var args []any
	if f.Q != "" {
		args = append(args, "%"+f.Q+"%")
		n := len(args)
		conds = append(conds, fmt.Sprintf("(c.phone ILIKE $%d OR c.display_name ILIKE $%d)", n, n))
	}
	if f.Country != "" {
		args = append(args, f.Country)
		conds = append(conds, fmt.Sprintf("c.country_code = $%d", len(args)))
	}
	if f.Status != "" {
		args = append(args, f.Status)
		conds = append(conds, fmt.Sprintf("c.status::text = $%d", len(args)))
	}
	if f.TagID != 0 {
		args = append(args, f.TagID)
		conds = append(conds, fmt.Sprintf("m.tag_id = $%d", len(args)))
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

type segmentFilter struct {
	Tags    []int64 `json:"tags"`
	Country string  `json:"country"`
	Status  string  `json:"status"`
}

// segmentToWhere resolves a saved segment filter into a WHERE clause over the
// same (contacts c LEFT JOIN contact_tag_map m) shape.
func segmentToWhere(f segmentFilter) (string, []any) {
	var conds []string
	var args []any
	if len(f.Tags) > 0 {
		args = append(args, f.Tags)
		conds = append(conds, fmt.Sprintf("m.tag_id = ANY($%d)", len(args)))
	}
	if f.Country != "" {
		args = append(args, f.Country)
		conds = append(conds, fmt.Sprintf("c.country_code = $%d", len(args)))
	}
	if f.Status != "" {
		args = append(args, f.Status)
		conds = append(conds, fmt.Sprintf("c.status::text = $%d", len(args)))
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}
