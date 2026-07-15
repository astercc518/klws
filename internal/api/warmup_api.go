// internal/api/warmup_api.go — admin 养号中心 API:分页列表(join account_devices)、
// 总览卡、单号动作(pause/resume/promote/demote/lane)、车道策略读写。
package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/acme/wadist/internal/warmup"
)

// handleAdminWarmupList: GET /admin/warmup — 分页养号行(join account_devices)。
// nil Deps.Warmup (未接线环境) 返回空列表而不是 500,与其它可选依赖(QRCache 等)一致。
func (s *Server) handleAdminWarmupList(c *gin.Context) {
	if s.deps.Warmup == nil {
		ok(c, gin.H{"rows": []any{}, "total": 0})
		return
	}
	f := warmup.ListFilter{
		Stage: c.Query("stage"),
		Lane:  c.Query("lane"),
		Q:     c.Query("q"),
	}
	f.Limit = atoiDefault(c.Query("limit"), 50)
	f.Offset = atoiDefault(c.Query("offset"), 0)
	rows, total, err := s.deps.Warmup.Store().List(c.Request.Context(), f)
	if err != nil {
		fail(c, http.StatusInternalServerError, "list warmup accounts failed")
		return
	}
	ok(c, gin.H{"rows": rows, "total": total})
}

// handleAdminWarmupOverview: GET /admin/warmup/overview — 阶段计数卡。
func (s *Server) handleAdminWarmupOverview(c *gin.Context) {
	if s.deps.Warmup == nil {
		ok(c, warmup.Overview{})
		return
	}
	o, err := s.deps.Warmup.Store().Overview(c.Request.Context())
	if err != nil {
		fail(c, http.StatusInternalServerError, "load warmup overview failed")
		return
	}
	ok(c, o)
}

// handleAdminWarmupAction: POST /admin/warmup/action?jid=... body
// {"action":"pause|resume|promote|demote|lane","lane":"FAST|STANDARD"}.
// jid rides as a query param (not a path segment) because account JIDs
// contain '@' and '.', which don't round-trip cleanly through gin's
// wildcard route params.
func (s *Server) handleAdminWarmupAction(c *gin.Context) {
	if s.deps.Warmup == nil {
		fail(c, http.StatusServiceUnavailable, "warmup disabled")
		return
	}
	jid := c.Query("jid")
	if jid == "" {
		fail(c, http.StatusBadRequest, "jid is required")
		return
	}
	var body struct {
		Action string `json:"action"`
		Lane   string `json:"lane"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		fail(c, http.StatusBadRequest, "bad body")
		return
	}
	ctx := c.Request.Context()
	var err error
	switch body.Action {
	case "pause":
		err = s.deps.Warmup.SetPaused(ctx, jid, true)
	case "resume":
		err = s.deps.Warmup.SetPaused(ctx, jid, false)
	case "promote":
		err = s.deps.Warmup.ForcePromote(ctx, jid)
	case "demote":
		err = s.deps.Warmup.Demote(ctx, jid, "manual")
	case "lane":
		err = s.deps.Warmup.SetLane(ctx, jid, warmup.Lane(body.Lane))
	default:
		fail(c, http.StatusBadRequest, "unknown action")
		return
	}
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	s.recordAudit(ctx, auditEvent{
		ActorID:      actorID(c),
		Action:       "warmup." + body.Action,
		ResourceType: "warmup_profile",
		Details:      gin.H{"jid": jid, "lane": body.Lane},
	})
	ok(c, gin.H{"ok": true})
}

// handleAdminWarmupGetPolicies: GET /admin/warmup/policies — 全部车道策略,keyed by lane。
func (s *Server) handleAdminWarmupGetPolicies(c *gin.Context) {
	if s.deps.Warmup == nil {
		ok(c, gin.H{})
		return
	}
	m, err := s.deps.Warmup.Store().ListPolicies(c.Request.Context())
	if err != nil {
		fail(c, http.StatusInternalServerError, "load warmup policies failed")
		return
	}
	ok(c, m)
}

// handleAdminWarmupSetPolicy: PUT /admin/warmup/policies/:lane — 后台热改一条车道策略。
func (s *Server) handleAdminWarmupSetPolicy(c *gin.Context) {
	if s.deps.Warmup == nil {
		fail(c, http.StatusServiceUnavailable, "warmup disabled")
		return
	}
	lane := warmup.Lane(c.Param("lane"))
	var p warmup.Policy
	if err := c.ShouldBindJSON(&p); err != nil {
		fail(c, http.StatusBadRequest, "bad body")
		return
	}
	ctx := c.Request.Context()
	if err := s.deps.Warmup.Store().UpsertPolicy(ctx, lane, p, timeNow()); err != nil {
		fail(c, http.StatusInternalServerError, "save warmup policy failed")
		return
	}
	s.recordAudit(ctx, auditEvent{
		ActorID:      actorID(c),
		Action:       "warmup.policy",
		ResourceType: "warmup_policy",
		Details:      gin.H{"lane": string(lane), "policy": p},
	})
	ok(c, gin.H{"ok": true})
}

// timeNow returns the current time (UTC). No equivalent helper exists
// elsewhere in package api; kept trivial since no test needs to freeze it —
// the policy round-trip test only asserts the updated value, not updated_at.
func timeNow() time.Time { return time.Now().UTC() }
