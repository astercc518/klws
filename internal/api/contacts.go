// internal/api/contacts.go — customer-facing contact library controller:
// CRUD + paginated/filtered list + batch import (normalize/dedup/report) +
// CSV export. All reads/writes go through s.deps.Mgr.WithTenant(ctx, tid),
// i.e. the same RLS transaction pattern as tenant.go/campaign.go — Postgres
// FORCE ROW LEVEL SECURITY is the backstop.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/acme/wadist/internal/crypto"
	"github.com/acme/wadist/internal/phonenorm"
)

// atoiDefault parses s as an int, returning d on any parse error (including
// an empty/unset query param).
func atoiDefault(s string, d int) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return d
	}
	return n
}

// ---------------------------------------------------------------------------
// Import
// ---------------------------------------------------------------------------

type importContactsRequest struct {
	Country  string `json:"country" binding:"required,len=2"`
	Text     string `json:"text"`     // pasted numbers, one per line (comma also accepted)
	Filename string `json:"filename"` // optional, audit-only
}

type importReport struct {
	BatchID    int64 `json:"batch_id"`
	Total      int   `json:"total"`
	Inserted   int   `json:"inserted"`
	Duplicates int   `json:"duplicates"`
	Invalid    int   `json:"invalid"`
}

// handleImportContacts: POST /api/v1/contacts/import {country, text, filename?}.
// Normalizes every line via phonenorm, dedups in-batch, and relies on the
// (tenant_id, phone_bidx) unique constraint for cross-batch dedup via
// ON CONFLICT DO NOTHING. The batch row + audit entry are written in the same
// tx as the inserts, so the report is always consistent with what committed.
func (s *Server) handleImportContacts(c *gin.Context) {
	tid, hasTenant := tenantID(c)
	if !hasTenant {
		fail(c, http.StatusForbidden, "no tenant in session")
		return
	}
	if len(s.deps.BlindKey) != 32 {
		fail(c, http.StatusServiceUnavailable, "contacts not configured")
		return
	}
	var req importContactsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid body")
		return
	}
	lines := strings.FieldsFunc(req.Text, func(r rune) bool { return r == '\n' || r == '\r' || r == ',' })
	ctx := c.Request.Context()

	rep := importReport{Total: len(lines)}
	seen := map[string]bool{} // in-batch dedup by normalized e164
	type row struct {
		phone, cc string
		bidx      []byte
	}
	var rows []row
	for _, ln := range lines {
		e164, cc, valid := phonenorm.Normalize(ln, req.Country)
		if !valid {
			rep.Invalid++
			continue
		}
		if seen[e164] {
			rep.Duplicates++
			continue
		}
		seen[e164] = true
		rows = append(rows, row{e164, cc, crypto.BlindIndex(s.deps.BlindKey, e164)})
	}

	tx, err := s.deps.Mgr.WithTenant(ctx, tid)
	if err != nil {
		fail(c, http.StatusInternalServerError, "tx")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	for _, r := range rows {
		ct, err := tx.Exec(ctx,
			`INSERT INTO contacts (tenant_id, phone, phone_bidx, country_code, source)
			 VALUES ($1,$2,$3,$4,'import')
			 ON CONFLICT (tenant_id, phone_bidx) DO NOTHING`,
			tid, r.phone, r.bidx, r.cc)
		if err != nil {
			fail(c, http.StatusInternalServerError, "insert contact")
			return
		}
		if ct.RowsAffected() == 1 {
			rep.Inserted++
		} else {
			rep.Duplicates++
		}
	}
	if err := tx.QueryRow(ctx,
		`INSERT INTO contact_import_batches (tenant_id, filename, total, inserted, duplicates, invalid, actor_id)
		 VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
		tid, req.Filename, rep.Total, rep.Inserted, rep.Duplicates, rep.Invalid, actorID(c)).Scan(&rep.BatchID); err != nil {
		fail(c, http.StatusInternalServerError, "batch")
		return
	}
	if err := s.recordAuditTx(ctx, tx, auditEvent{
		TenantID: tid, ActorID: actorID(c), Action: "contacts.import",
		ResourceType: "contact_import_batch", ResourceID: rep.BatchID,
		Details: map[string]any{"total": rep.Total, "inserted": rep.Inserted, "duplicates": rep.Duplicates, "invalid": rep.Invalid},
	}); err != nil {
		fail(c, http.StatusInternalServerError, "audit")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		fail(c, http.StatusInternalServerError, "commit")
		return
	}
	ok(c, rep)
}

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

type contactRow struct {
	ID          int64  `json:"id"`
	Phone       string `json:"phone"`
	CountryCode string `json:"country_code"`
	DisplayName string `json:"display_name"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
}

// handleListContacts: GET /api/v1/contacts?q=&country=&status=&tag_id=&limit=&offset=.
func (s *Server) handleListContacts(c *gin.Context) {
	tid, ok2 := tenantID(c)
	if !ok2 {
		fail(c, http.StatusForbidden, "no tenant in session")
		return
	}
	f := contactFilter{
		Q:       c.Query("q"),
		Country: c.Query("country"),
		Status:  c.Query("status"),
		Limit:   clampPage(atoiDefault(c.Query("limit"), 50), 50, 500),
		Offset:  atoiDefault(c.Query("offset"), 0),
	}
	if f.Offset < 0 {
		f.Offset = 0 // ?offset=-5 would otherwise hit Postgres' OFFSET must not be negative -> 500
	}
	if t := c.Query("tag_id"); t != "" {
		f.TagID = int64(atoiDefault(t, 0))
	}
	where, args := buildContactWhere(f)
	ctx := c.Request.Context()
	tx, err := s.deps.Mgr.WithTenant(ctx, tid)
	if err != nil {
		fail(c, http.StatusInternalServerError, "tx")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var total int
	if err := tx.QueryRow(ctx,
		`SELECT count(DISTINCT c.id) FROM contacts c LEFT JOIN contact_tag_map m ON m.contact_id=c.id`+where, args...).Scan(&total); err != nil {
		fail(c, http.StatusInternalServerError, "count")
		return
	}
	args = append(args, f.Limit, f.Offset)
	q := `SELECT DISTINCT c.id, c.phone, COALESCE(c.country_code,''), c.display_name, c.status::text, c.created_at::text
	      FROM contacts c LEFT JOIN contact_tag_map m ON m.contact_id=c.id` + where +
		` ORDER BY c.id DESC LIMIT $` + strconv.Itoa(len(args)-1) + ` OFFSET $` + strconv.Itoa(len(args))
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		fail(c, http.StatusInternalServerError, "query")
		return
	}
	defer rows.Close()
	out := []contactRow{}
	for rows.Next() {
		var r contactRow
		var name *string
		if err := rows.Scan(&r.ID, &r.Phone, &r.CountryCode, &name, &r.Status, &r.CreatedAt); err != nil {
			fail(c, http.StatusInternalServerError, "scan")
			return
		}
		if name != nil {
			r.DisplayName = *name
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		fail(c, http.StatusInternalServerError, "rows")
		return
	}
	ok(c, gin.H{"rows": out, "total": total})
}

// ---------------------------------------------------------------------------
// Create (single contact)
// ---------------------------------------------------------------------------

type createContactRequest struct {
	Country     string `json:"country" binding:"required,len=2"`
	Phone       string `json:"phone" binding:"required"`
	DisplayName string `json:"display_name"`
}

// handleCreateContact: POST /api/v1/contacts {country, phone, display_name?}.
// 409s on a duplicate (tenant_id, phone_bidx).
func (s *Server) handleCreateContact(c *gin.Context) {
	tid, ok2 := tenantID(c)
	if !ok2 {
		fail(c, http.StatusForbidden, "no tenant in session")
		return
	}
	if len(s.deps.BlindKey) != 32 {
		fail(c, http.StatusServiceUnavailable, "contacts not configured")
		return
	}
	var req createContactRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid body")
		return
	}
	e164, cc, valid := phonenorm.Normalize(req.Phone, req.Country)
	if !valid {
		fail(c, http.StatusBadRequest, "invalid phone number")
		return
	}
	bidx := crypto.BlindIndex(s.deps.BlindKey, e164)
	var name *string
	if req.DisplayName != "" {
		name = &req.DisplayName
	}

	ctx := c.Request.Context()
	tx, err := s.deps.Mgr.WithTenant(ctx, tid)
	if err != nil {
		fail(c, http.StatusInternalServerError, "tx")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var r contactRow
	var dn *string
	err = tx.QueryRow(ctx,
		`INSERT INTO contacts (tenant_id, phone, phone_bidx, country_code, display_name, source)
		 VALUES ($1,$2,$3,$4,$5,'manual')
		 ON CONFLICT (tenant_id, phone_bidx) DO NOTHING
		 RETURNING id, phone, COALESCE(country_code,''), display_name, status::text, created_at::text`,
		tid, e164, bidx, cc, name).Scan(&r.ID, &r.Phone, &r.CountryCode, &dn, &r.Status, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		fail(c, http.StatusConflict, "contact already exists")
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "insert contact")
		return
	}
	if dn != nil {
		r.DisplayName = *dn
	}
	if err := s.recordAuditTx(ctx, tx, auditEvent{
		TenantID: tid, ActorID: actorID(c), Action: "contacts.create",
		ResourceType: "contact", ResourceID: r.ID, Details: map[string]any{"country": cc},
	}); err != nil {
		fail(c, http.StatusInternalServerError, "audit")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		fail(c, http.StatusInternalServerError, "commit")
		return
	}
	ok(c, r)
}

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

type updateContactRequest struct {
	DisplayName *string         `json:"display_name"`
	Status      *string         `json:"status"`
	Vars        json.RawMessage `json:"vars"`
}

// handleUpdateContact: PUT /api/v1/contacts/:id {display_name?, status?, vars?}.
// Only provided fields update.
func (s *Server) handleUpdateContact(c *gin.Context) {
	tid, ok2 := tenantID(c)
	if !ok2 {
		fail(c, http.StatusForbidden, "no tenant in session")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		fail(c, http.StatusBadRequest, "invalid contact id")
		return
	}
	var req updateContactRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Status != nil {
		switch *req.Status {
		case "active", "unsubscribed", "invalid":
		default:
			fail(c, http.StatusBadRequest, "invalid status")
			return
		}
	}

	var sets []string
	var args []any
	details := map[string]any{}
	if req.DisplayName != nil {
		args = append(args, *req.DisplayName)
		sets = append(sets, fmt.Sprintf("display_name=$%d", len(args)))
		details["display_name"] = *req.DisplayName
	}
	if req.Status != nil {
		args = append(args, *req.Status)
		sets = append(sets, fmt.Sprintf("status=$%d::contact_status_t", len(args)))
		details["status"] = *req.Status
	}
	if req.Vars != nil {
		args = append(args, []byte(req.Vars))
		sets = append(sets, fmt.Sprintf("vars=$%d::jsonb", len(args)))
		details["vars"] = true
	}
	if len(sets) == 0 {
		fail(c, http.StatusBadRequest, "no fields to update")
		return
	}

	ctx := c.Request.Context()
	tx, err := s.deps.Mgr.WithTenant(ctx, tid)
	if err != nil {
		fail(c, http.StatusInternalServerError, "tx")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	args = append(args, id)
	q := "UPDATE contacts SET " + strings.Join(sets, ", ") + ", updated_at=now() WHERE id=$" + strconv.Itoa(len(args))
	tag, err := tx.Exec(ctx, q, args...)
	if err != nil {
		fail(c, http.StatusInternalServerError, "update contact")
		return
	}
	if tag.RowsAffected() == 0 {
		fail(c, http.StatusNotFound, "contact not found")
		return
	}
	if err := s.recordAuditTx(ctx, tx, auditEvent{
		TenantID: tid, ActorID: actorID(c), Action: "contacts.update",
		ResourceType: "contact", ResourceID: id, Details: details,
	}); err != nil {
		fail(c, http.StatusInternalServerError, "audit")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		fail(c, http.StatusInternalServerError, "commit")
		return
	}
	ok(c, gin.H{"id": id})
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

// handleDeleteContact: DELETE /api/v1/contacts/:id. contact_tag_map rows for
// this contact cascade via the FK (ON DELETE CASCADE, migration 0021).
func (s *Server) handleDeleteContact(c *gin.Context) {
	tid, ok2 := tenantID(c)
	if !ok2 {
		fail(c, http.StatusForbidden, "no tenant in session")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		fail(c, http.StatusBadRequest, "invalid contact id")
		return
	}
	ctx := c.Request.Context()
	tx, err := s.deps.Mgr.WithTenant(ctx, tid)
	if err != nil {
		fail(c, http.StatusInternalServerError, "tx")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	tag, err := tx.Exec(ctx, `DELETE FROM contacts WHERE id=$1`, id)
	if err != nil {
		fail(c, http.StatusInternalServerError, "delete contact")
		return
	}
	if tag.RowsAffected() == 0 {
		fail(c, http.StatusNotFound, "contact not found")
		return
	}
	if err := s.recordAuditTx(ctx, tx, auditEvent{
		TenantID: tid, ActorID: actorID(c), Action: "contacts.delete",
		ResourceType: "contact", ResourceID: id,
	}); err != nil {
		fail(c, http.StatusInternalServerError, "audit")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		fail(c, http.StatusInternalServerError, "commit")
		return
	}
	ok(c, gin.H{"id": id})
}

// ---------------------------------------------------------------------------
// Export
// ---------------------------------------------------------------------------

// handleExportContacts: GET /api/v1/contacts/export?q=&country=&status=&tag_id=.
// Streams a CSV (bypasses the JSON envelope, matching writeCSV's contract).
func (s *Server) handleExportContacts(c *gin.Context) {
	tid, ok2 := tenantID(c)
	if !ok2 {
		fail(c, http.StatusForbidden, "no tenant in session")
		return
	}
	f := contactFilter{
		Q:       c.Query("q"),
		Country: c.Query("country"),
		Status:  c.Query("status"),
	}
	if t := c.Query("tag_id"); t != "" {
		f.TagID = int64(atoiDefault(t, 0))
	}
	where, args := buildContactWhere(f)
	ctx := c.Request.Context()
	tx, err := s.deps.Mgr.WithTenant(ctx, tid)
	if err != nil {
		fail(c, http.StatusInternalServerError, "tx")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	q := `SELECT DISTINCT c.phone, COALESCE(c.country_code,''), COALESCE(c.display_name,''), c.status::text, c.created_at::text
	      FROM contacts c LEFT JOIN contact_tag_map m ON m.contact_id=c.id` + where + ` ORDER BY c.phone`
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		fail(c, http.StatusInternalServerError, "query")
		return
	}
	defer rows.Close()
	var out [][]string
	for rows.Next() {
		var phone, cc, name, status, createdAt string
		if err := rows.Scan(&phone, &cc, &name, &status, &createdAt); err != nil {
			fail(c, http.StatusInternalServerError, "scan")
			return
		}
		out = append(out, []string{phone, cc, name, status, createdAt})
	}
	if err := rows.Err(); err != nil {
		fail(c, http.StatusInternalServerError, "rows")
		return
	}
	s.recordAudit(ctx, auditEvent{TenantID: tid, ActorID: actorID(c), Action: "contacts.export",
		ResourceType: "contact", Details: map[string]any{"rows": len(out)}})
	writeCSV(c, "contacts.csv", []string{"phone", "country_code", "display_name", "status", "created_at"}, out)
}

// ---------------------------------------------------------------------------
// Tags
// ---------------------------------------------------------------------------

type tagRow struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

// handleListTags: GET /api/v1/contacts/tags.
func (s *Server) handleListTags(c *gin.Context) {
	tid, ok2 := tenantID(c)
	if !ok2 {
		fail(c, http.StatusForbidden, "no tenant in session")
		return
	}
	ctx := c.Request.Context()
	tx, err := s.deps.Mgr.WithTenant(ctx, tid)
	if err != nil {
		fail(c, http.StatusInternalServerError, "tx")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	rows, err := tx.Query(ctx, `SELECT id, name, created_at::text FROM contact_tags ORDER BY name`)
	if err != nil {
		fail(c, http.StatusInternalServerError, "query")
		return
	}
	defer rows.Close()
	out := []tagRow{}
	for rows.Next() {
		var r tagRow
		if err := rows.Scan(&r.ID, &r.Name, &r.CreatedAt); err != nil {
			fail(c, http.StatusInternalServerError, "scan")
			return
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		fail(c, http.StatusInternalServerError, "rows")
		return
	}
	ok(c, out)
}

type createTagRequest struct {
	Name string `json:"name" binding:"required"`
}

// handleCreateTag: POST /api/v1/contacts/tags {name}. 409s on a duplicate
// (tenant_id, name).
func (s *Server) handleCreateTag(c *gin.Context) {
	tid, ok2 := tenantID(c)
	if !ok2 {
		fail(c, http.StatusForbidden, "no tenant in session")
		return
	}
	var req createTagRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid body")
		return
	}
	ctx := c.Request.Context()
	tx, err := s.deps.Mgr.WithTenant(ctx, tid)
	if err != nil {
		fail(c, http.StatusInternalServerError, "tx")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var r tagRow
	err = tx.QueryRow(ctx,
		`INSERT INTO contact_tags (tenant_id, name) VALUES ($1,$2)
		 ON CONFLICT (tenant_id, name) DO NOTHING
		 RETURNING id, name, created_at::text`,
		tid, req.Name).Scan(&r.ID, &r.Name, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		fail(c, http.StatusConflict, "tag already exists")
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "insert tag")
		return
	}
	if err := s.recordAuditTx(ctx, tx, auditEvent{
		TenantID: tid, ActorID: actorID(c), Action: "contacts.tag_create",
		ResourceType: "contact_tag", ResourceID: r.ID, Details: map[string]any{"name": r.Name},
	}); err != nil {
		fail(c, http.StatusInternalServerError, "audit")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		fail(c, http.StatusInternalServerError, "commit")
		return
	}
	ok(c, r)
}

// handleDeleteTag: DELETE /api/v1/contacts/tags/:id. contact_tag_map rows for
// this tag cascade via the FK (ON DELETE CASCADE, migration 0021).
func (s *Server) handleDeleteTag(c *gin.Context) {
	tid, ok2 := tenantID(c)
	if !ok2 {
		fail(c, http.StatusForbidden, "no tenant in session")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		fail(c, http.StatusBadRequest, "invalid tag id")
		return
	}
	ctx := c.Request.Context()
	tx, err := s.deps.Mgr.WithTenant(ctx, tid)
	if err != nil {
		fail(c, http.StatusInternalServerError, "tx")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	tag, err := tx.Exec(ctx, `DELETE FROM contact_tags WHERE id=$1`, id)
	if err != nil {
		fail(c, http.StatusInternalServerError, "delete tag")
		return
	}
	if tag.RowsAffected() == 0 {
		fail(c, http.StatusNotFound, "tag not found")
		return
	}
	if err := s.recordAuditTx(ctx, tx, auditEvent{
		TenantID: tid, ActorID: actorID(c), Action: "contacts.tag_delete",
		ResourceType: "contact_tag", ResourceID: id,
	}); err != nil {
		fail(c, http.StatusInternalServerError, "audit")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		fail(c, http.StatusInternalServerError, "commit")
		return
	}
	ok(c, gin.H{"id": id})
}

type applyTagRequest struct {
	ContactIDs []int64 `json:"contact_ids" binding:"required"`
	Remove     bool    `json:"remove"`
}

// handleApplyTag: POST /api/v1/contacts/tags/:id/apply {contact_ids, remove?}.
// Bulk-tags (or, with remove:true, un-tags) the given contacts with tag :id.
func (s *Server) handleApplyTag(c *gin.Context) {
	tid, ok2 := tenantID(c)
	if !ok2 {
		fail(c, http.StatusForbidden, "no tenant in session")
		return
	}
	tagID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "bad tag id")
		return
	}
	var req applyTagRequest
	if err := c.ShouldBindJSON(&req); err != nil || len(req.ContactIDs) == 0 {
		fail(c, http.StatusBadRequest, "no contacts")
		return
	}
	ctx := c.Request.Context()
	tx, err := s.deps.Mgr.WithTenant(ctx, tid)
	if err != nil {
		fail(c, http.StatusInternalServerError, "tx")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if req.Remove {
		if _, err := tx.Exec(ctx, `DELETE FROM contact_tag_map WHERE tag_id=$1 AND contact_id = ANY($2)`, tagID, req.ContactIDs); err != nil {
			fail(c, http.StatusInternalServerError, "untag")
			return
		}
	} else {
		if _, err := tx.Exec(ctx,
			`INSERT INTO contact_tag_map (tenant_id, contact_id, tag_id)
			 SELECT $1, unnest($2::bigint[]), $3 ON CONFLICT DO NOTHING`,
			tid, req.ContactIDs, tagID); err != nil {
			fail(c, http.StatusInternalServerError, "tag")
			return
		}
	}
	if err := s.recordAuditTx(ctx, tx, auditEvent{
		TenantID: tid, ActorID: actorID(c), Action: "contacts.tag_apply",
		ResourceType: "contact_tag", ResourceID: tagID,
		Details: map[string]any{"n": len(req.ContactIDs), "remove": req.Remove},
	}); err != nil {
		fail(c, http.StatusInternalServerError, "audit")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		fail(c, http.StatusInternalServerError, "commit")
		return
	}
	ok(c, gin.H{"ok": true})
}

// ---------------------------------------------------------------------------
// Segments
// ---------------------------------------------------------------------------

type segmentRow struct {
	ID        int64           `json:"id"`
	Name      string          `json:"name"`
	Filter    json.RawMessage `json:"filter"`
	CreatedAt string          `json:"created_at"`
}

// handleListSegments: GET /api/v1/contacts/segments.
func (s *Server) handleListSegments(c *gin.Context) {
	tid, ok2 := tenantID(c)
	if !ok2 {
		fail(c, http.StatusForbidden, "no tenant in session")
		return
	}
	ctx := c.Request.Context()
	tx, err := s.deps.Mgr.WithTenant(ctx, tid)
	if err != nil {
		fail(c, http.StatusInternalServerError, "tx")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	rows, err := tx.Query(ctx, `SELECT id, name, filter, created_at::text FROM contact_segments ORDER BY name`)
	if err != nil {
		fail(c, http.StatusInternalServerError, "query")
		return
	}
	defer rows.Close()
	out := []segmentRow{}
	for rows.Next() {
		var r segmentRow
		var raw []byte
		if err := rows.Scan(&r.ID, &r.Name, &raw, &r.CreatedAt); err != nil {
			fail(c, http.StatusInternalServerError, "scan")
			return
		}
		r.Filter = raw
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		fail(c, http.StatusInternalServerError, "rows")
		return
	}
	ok(c, out)
}

type createSegmentRequest struct {
	Name   string          `json:"name" binding:"required"`
	Filter json.RawMessage `json:"filter"`
}

// handleCreateSegment: POST /api/v1/contacts/segments {name, filter?}. 409s on
// a duplicate (tenant_id, name); filter defaults to {} (matches the column
// default) and must unmarshal into segmentFilter.
func (s *Server) handleCreateSegment(c *gin.Context) {
	tid, ok2 := tenantID(c)
	if !ok2 {
		fail(c, http.StatusForbidden, "no tenant in session")
		return
	}
	var req createSegmentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid body")
		return
	}
	filter := []byte(req.Filter)
	if len(filter) == 0 {
		filter = []byte(`{}`)
	}
	var f segmentFilter
	if err := json.Unmarshal(filter, &f); err != nil {
		fail(c, http.StatusBadRequest, "invalid filter")
		return
	}
	ctx := c.Request.Context()
	tx, err := s.deps.Mgr.WithTenant(ctx, tid)
	if err != nil {
		fail(c, http.StatusInternalServerError, "tx")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var r segmentRow
	var raw []byte
	err = tx.QueryRow(ctx,
		`INSERT INTO contact_segments (tenant_id, name, filter) VALUES ($1,$2,$3)
		 ON CONFLICT (tenant_id, name) DO NOTHING
		 RETURNING id, name, filter, created_at::text`,
		tid, req.Name, filter).Scan(&r.ID, &r.Name, &raw, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		fail(c, http.StatusConflict, "segment already exists")
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "insert segment")
		return
	}
	r.Filter = raw
	if err := s.recordAuditTx(ctx, tx, auditEvent{
		TenantID: tid, ActorID: actorID(c), Action: "contacts.segment_create",
		ResourceType: "contact_segment", ResourceID: r.ID, Details: map[string]any{"name": r.Name},
	}); err != nil {
		fail(c, http.StatusInternalServerError, "audit")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		fail(c, http.StatusInternalServerError, "commit")
		return
	}
	ok(c, r)
}

// handleDeleteSegment: DELETE /api/v1/contacts/segments/:id.
func (s *Server) handleDeleteSegment(c *gin.Context) {
	tid, ok2 := tenantID(c)
	if !ok2 {
		fail(c, http.StatusForbidden, "no tenant in session")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		fail(c, http.StatusBadRequest, "invalid segment id")
		return
	}
	ctx := c.Request.Context()
	tx, err := s.deps.Mgr.WithTenant(ctx, tid)
	if err != nil {
		fail(c, http.StatusInternalServerError, "tx")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	tag, err := tx.Exec(ctx, `DELETE FROM contact_segments WHERE id=$1`, id)
	if err != nil {
		fail(c, http.StatusInternalServerError, "delete segment")
		return
	}
	if tag.RowsAffected() == 0 {
		fail(c, http.StatusNotFound, "segment not found")
		return
	}
	if err := s.recordAuditTx(ctx, tx, auditEvent{
		TenantID: tid, ActorID: actorID(c), Action: "contacts.segment_delete",
		ResourceType: "contact_segment", ResourceID: id,
	}); err != nil {
		fail(c, http.StatusInternalServerError, "audit")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		fail(c, http.StatusInternalServerError, "commit")
		return
	}
	ok(c, gin.H{"id": id})
}

// ---------------------------------------------------------------------------
// Suppression (manual blacklist / opt-out)
// ---------------------------------------------------------------------------
//
// suppression_list (migration 0008) stores ONLY (tenant_id, phone_bidx,
// reason, added_at) — the phone itself is never persisted, so nothing here
// can ever display it back. Matching against it (both here and in the
// dispatcher) is by blind index: crypto.BlindIndex(BlindKey, e164).
//
// The table is also intentionally append-only at the DB grant level:
// migration 0008 grants app_tenant/app_system only SELECT+INSERT on it and
// explicitly REVOKEs UPDATE/DELETE from both roles ("compliance: tenants
// must not be able to un-suppress opt-outs"). handleDeleteSuppression below
// respects that by construction — see its doc comment.

type suppressionRow struct {
	ID      int64  `json:"id"`
	Reason  string `json:"reason"`
	AddedAt string `json:"added_at"`
}

type suppressionReport struct {
	Total      int `json:"total"`
	Inserted   int `json:"inserted"`
	Duplicates int `json:"duplicates"`
	Invalid    int `json:"invalid"`
}

// normalizeSuppressionPhones normalizes each raw phone via phonenorm, dedups
// in-batch by normalized e164, and blind-indexes the survivors. Un-normalizable
// entries count as Invalid; in-batch repeats count as Duplicates (cross-batch
// repeats are counted separately by the caller via ON CONFLICT). Shared by
// handleAddSuppression (explicit phones[]) and handleImportSuppression
// (pasted text) — mirrors handleImportContacts' normalize/dedup pattern.
func (s *Server) normalizeSuppressionPhones(country string, raws []string) (suppressionReport, [][]byte) {
	rep := suppressionReport{Total: len(raws)}
	seen := map[string]bool{}
	var bidxs [][]byte
	for _, raw := range raws {
		e164, _, valid := phonenorm.Normalize(raw, country)
		if !valid {
			rep.Invalid++
			continue
		}
		if seen[e164] {
			rep.Duplicates++
			continue
		}
		seen[e164] = true
		bidxs = append(bidxs, crypto.BlindIndex(s.deps.BlindKey, e164))
	}
	return rep, bidxs
}

// insertSuppressionRows inserts each bidx via ON CONFLICT (tenant_id,
// phone_bidx) DO NOTHING, folding cross-batch conflicts into rep.Duplicates.
func insertSuppressionRows(ctx context.Context, tx pgx.Tx, tid int64, bidxs [][]byte, reason string, rep *suppressionReport) error {
	for _, bidx := range bidxs {
		ct, err := tx.Exec(ctx,
			`INSERT INTO suppression_list (tenant_id, phone_bidx, reason)
			 VALUES ($1,$2,$3)
			 ON CONFLICT (tenant_id, phone_bidx) DO NOTHING`,
			tid, bidx, reason)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 1 {
			rep.Inserted++
		} else {
			rep.Duplicates++
		}
	}
	return nil
}

// handleListSuppression: GET /api/v1/suppression?limit=&offset=.
// Returns only id/reason/added_at — suppression_list has no phone column to
// show (it stores the blind index only, which is one-way).
//
// IMPORTANT: suppression_list is NOT RLS-protected — migration 0008 only
// GRANT/REVOKEs it and never runs it through an ENABLE/FORCE ROW LEVEL
// SECURITY loop (0008's loop covers other tables; 0021's covers the contact
// tables). So WithTenant's app.current_tenant_id has no effect here and both
// queries MUST filter tenant_id explicitly, or one tenant sees every tenant's
// rows. The add/import path is safe because it sets tenant_id on INSERT.
func (s *Server) handleListSuppression(c *gin.Context) {
	tid, hasTenant := tenantID(c)
	if !hasTenant {
		fail(c, http.StatusForbidden, "no tenant in session")
		return
	}
	limit := clampPage(atoiDefault(c.Query("limit"), 50), 50, 500)
	offset := atoiDefault(c.Query("offset"), 0)
	if offset < 0 {
		offset = 0 // ?offset=-5 would otherwise hit Postgres' OFFSET must not be negative -> 500
	}

	ctx := c.Request.Context()
	tx, err := s.deps.Mgr.WithTenant(ctx, tid)
	if err != nil {
		fail(c, http.StatusInternalServerError, "tx")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var total int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM suppression_list WHERE tenant_id = $1`, tid).Scan(&total); err != nil {
		fail(c, http.StatusInternalServerError, "count")
		return
	}
	rows, err := tx.Query(ctx,
		`SELECT id, COALESCE(reason,''), added_at::text FROM suppression_list
		 WHERE tenant_id = $1 ORDER BY id DESC LIMIT $2 OFFSET $3`, tid, limit, offset)
	if err != nil {
		fail(c, http.StatusInternalServerError, "query")
		return
	}
	defer rows.Close()
	out := []suppressionRow{}
	for rows.Next() {
		var r suppressionRow
		if err := rows.Scan(&r.ID, &r.Reason, &r.AddedAt); err != nil {
			fail(c, http.StatusInternalServerError, "scan")
			return
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		fail(c, http.StatusInternalServerError, "rows")
		return
	}
	ok(c, gin.H{"rows": out, "total": total})
}

type addSuppressionRequest struct {
	Country string   `json:"country" binding:"required,len=2"`
	Phones  []string `json:"phones" binding:"required"`
	Reason  string   `json:"reason"`
}

// handleAddSuppression: POST /api/v1/suppression {country, phones[], reason?}.
func (s *Server) handleAddSuppression(c *gin.Context) {
	tid, hasTenant := tenantID(c)
	if !hasTenant {
		fail(c, http.StatusForbidden, "no tenant in session")
		return
	}
	if len(s.deps.BlindKey) != 32 {
		fail(c, http.StatusServiceUnavailable, "contacts not configured")
		return
	}
	var req addSuppressionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid body")
		return
	}
	rep, bidxs := s.normalizeSuppressionPhones(req.Country, req.Phones)

	ctx := c.Request.Context()
	tx, err := s.deps.Mgr.WithTenant(ctx, tid)
	if err != nil {
		fail(c, http.StatusInternalServerError, "tx")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := insertSuppressionRows(ctx, tx, tid, bidxs, req.Reason, &rep); err != nil {
		fail(c, http.StatusInternalServerError, "insert suppression")
		return
	}
	if err := s.recordAuditTx(ctx, tx, auditEvent{
		TenantID: tid, ActorID: actorID(c), Action: "suppression.add",
		ResourceType: "suppression_list",
		Details:      map[string]any{"total": rep.Total, "inserted": rep.Inserted, "duplicates": rep.Duplicates, "invalid": rep.Invalid},
	}); err != nil {
		fail(c, http.StatusInternalServerError, "audit")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		fail(c, http.StatusInternalServerError, "commit")
		return
	}
	ok(c, rep)
}

type importSuppressionRequest struct {
	Country string `json:"country" binding:"required,len=2"`
	Text    string `json:"text"` // pasted numbers, one per line (comma also accepted)
	Reason  string `json:"reason"`
}

// handleImportSuppression: POST /api/v1/suppression/import {country, text, reason?}.
// Same normalize/dedup/report shape as handleImportContacts.
func (s *Server) handleImportSuppression(c *gin.Context) {
	tid, hasTenant := tenantID(c)
	if !hasTenant {
		fail(c, http.StatusForbidden, "no tenant in session")
		return
	}
	if len(s.deps.BlindKey) != 32 {
		fail(c, http.StatusServiceUnavailable, "contacts not configured")
		return
	}
	var req importSuppressionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid body")
		return
	}
	lines := strings.FieldsFunc(req.Text, func(r rune) bool { return r == '\n' || r == '\r' || r == ',' })
	rep, bidxs := s.normalizeSuppressionPhones(req.Country, lines)

	ctx := c.Request.Context()
	tx, err := s.deps.Mgr.WithTenant(ctx, tid)
	if err != nil {
		fail(c, http.StatusInternalServerError, "tx")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := insertSuppressionRows(ctx, tx, tid, bidxs, req.Reason, &rep); err != nil {
		fail(c, http.StatusInternalServerError, "insert suppression")
		return
	}
	if err := s.recordAuditTx(ctx, tx, auditEvent{
		TenantID: tid, ActorID: actorID(c), Action: "suppression.import",
		ResourceType: "suppression_list",
		Details:      map[string]any{"total": rep.Total, "inserted": rep.Inserted, "duplicates": rep.Duplicates, "invalid": rep.Invalid},
	}); err != nil {
		fail(c, http.StatusInternalServerError, "audit")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		fail(c, http.StatusInternalServerError, "commit")
		return
	}
	ok(c, rep)
}

// Note: there is deliberately NO handleDeleteSuppression / un-suppress
// endpoint. suppression_list is append-only by compliance design — migration
// 0008 grants app_tenant/app_system only SELECT+INSERT and explicitly REVOKEs
// UPDATE/DELETE from both roles ("tenants must not be able to un-suppress
// opt-outs").

// ---------------------------------------------------------------------------
// Admin (cross-tenant, read-only god view)
// ---------------------------------------------------------------------------

type adminContactRow struct {
	ID          int64  `json:"id"`
	TenantID    int64  `json:"tenant_id"`
	Phone       string `json:"phone"`
	CountryCode string `json:"country_code"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
}

// handleAdminListContacts: GET /api/v1/admin/contacts?q=&country=&status=&tag_id=&tenant_id=&limit=&offset=.
// Cross-tenant contact list for the admin console — uses s.systemPool()
// (BYPASSRLS) rather than s.deps.Mgr.WithTenant, so it intentionally sees
// every tenant's contacts, same as handleAdminListRecipients /
// handleAdminListCampaigns. tenant_id is an optional filter, NOT a session
// scope (there is no WithTenant here). Read-only: no writes, no audit —
// admins cannot edit customer contacts via this endpoint (governance
// boundary), they can only view them.
func (s *Server) handleAdminListContacts(c *gin.Context) {
	ctx := c.Request.Context()
	pool := s.systemPool()
	f := contactFilter{
		Q:       c.Query("q"),
		Country: c.Query("country"),
		Status:  c.Query("status"),
		Limit:   clampPage(atoiDefault(c.Query("limit"), 50), 50, 500),
		Offset:  atoiDefault(c.Query("offset"), 0),
	}
	if f.Offset < 0 {
		f.Offset = 0 // ?offset=-5 would otherwise hit Postgres' OFFSET must not be negative -> 500
	}
	if t := c.Query("tag_id"); t != "" {
		f.TagID = int64(atoiDefault(t, 0))
	}
	where, args := buildContactWhere(f)
	if tidStr := c.Query("tenant_id"); tidStr != "" {
		if tid, err := strconv.ParseInt(tidStr, 10, 64); err == nil && tid > 0 {
			args = append(args, tid)
			if where == "" {
				where = fmt.Sprintf(" WHERE c.tenant_id = $%d", len(args))
			} else {
				where += fmt.Sprintf(" AND c.tenant_id = $%d", len(args))
			}
		}
	}

	listArgs := append(append([]any{}, args...), f.Limit, f.Offset)
	list := `SELECT DISTINCT c.id, c.tenant_id, c.phone, COALESCE(c.country_code,''), c.status::text, c.created_at::text
	           FROM contacts c LEFT JOIN contact_tag_map m ON m.contact_id=c.id` + where +
		fmt.Sprintf(" ORDER BY c.id DESC LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
	rows, err := pool.Query(ctx, list, listArgs...)
	if err != nil {
		fail(c, http.StatusInternalServerError, "query")
		return
	}
	defer rows.Close()
	out := []adminContactRow{}
	for rows.Next() {
		var r adminContactRow
		if err := rows.Scan(&r.ID, &r.TenantID, &r.Phone, &r.CountryCode, &r.Status, &r.CreatedAt); err != nil {
			fail(c, http.StatusInternalServerError, "scan")
			return
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		fail(c, http.StatusInternalServerError, "rows")
		return
	}

	var total int
	if err := pool.QueryRow(ctx,
		`SELECT count(DISTINCT c.id) FROM contacts c LEFT JOIN contact_tag_map m ON m.contact_id=c.id`+where, args...).Scan(&total); err != nil {
		fail(c, http.StatusInternalServerError, "count")
		return
	}
	ok(c, gin.H{"rows": out, "total": total})
}

// segmentSendWhere builds the WHERE clause (and matching args) for the set of
// contacts a segment would ACTUALLY send to: status defaults to "active" when
// the filter leaves it unset, and anyone on the suppression list is excluded.
// This mirrors resolveSegmentPhones (campaign_from_segment.go) rule-for-rule —
// that function is the source of truth for what a campaign actually sends to.
// It isn't called from here directly (resolveSegmentPhones lives in a file
// this change doesn't touch), so keep the two in sync by hand if either
// changes.
func segmentSendWhere(f segmentFilter) (string, []any) {
	if f.Status == "" {
		f.Status = "active" // a campaign only ever sends to active contacts
	}
	where, args := segmentToWhere(f)
	const suppressionGuard = `NOT EXISTS (
		SELECT 1 FROM suppression_list sl
		 WHERE sl.tenant_id = c.tenant_id AND sl.phone_bidx = c.phone_bidx
	)`
	if where == "" {
		where = " WHERE " + suppressionGuard
	} else {
		where += " AND " + suppressionGuard
	}
	return where, args
}

// handleSegmentPreview: GET /api/v1/contacts/segments/:id/preview -> {count}.
// Resolves the saved filter with the SAME rules resolveSegmentPhones applies
// when a campaign is actually built from this segment (status defaults to
// "active", suppressed contacts excluded) via segmentSendWhere, so the
// preview count matches what a subsequent campaign-from-segment would send —
// no rows are materialized/returned, just a live count.
func (s *Server) handleSegmentPreview(c *gin.Context) {
	tid, ok2 := tenantID(c)
	if !ok2 {
		fail(c, http.StatusForbidden, "no tenant in session")
		return
	}
	segID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "bad segment id")
		return
	}
	ctx := c.Request.Context()
	tx, err := s.deps.Mgr.WithTenant(ctx, tid)
	if err != nil {
		fail(c, http.StatusInternalServerError, "tx")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT filter FROM contact_segments WHERE id=$1`, segID).Scan(&raw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			fail(c, http.StatusNotFound, "segment not found")
		} else {
			fail(c, http.StatusInternalServerError, "segment lookup")
		}
		return
	}
	var f segmentFilter
	_ = json.Unmarshal(raw, &f)
	where, args := segmentSendWhere(f)
	var count int
	if err := tx.QueryRow(ctx,
		`SELECT count(DISTINCT c.id) FROM contacts c LEFT JOIN contact_tag_map m ON m.contact_id=c.id`+where, args...).Scan(&count); err != nil {
		fail(c, http.StatusInternalServerError, "count")
		return
	}
	ok(c, gin.H{"count": count})
}
