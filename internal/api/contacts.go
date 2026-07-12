// internal/api/contacts.go — customer-facing contact library controller:
// CRUD + paginated/filtered list + batch import (normalize/dedup/report) +
// CSV export. All reads/writes go through s.deps.Mgr.WithTenant(ctx, tid),
// i.e. the same RLS transaction pattern as tenant.go/campaign.go — Postgres
// FORCE ROW LEVEL SECURITY is the backstop.
package api

import (
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
