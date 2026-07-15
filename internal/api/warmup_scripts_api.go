// internal/api/warmup_scripts_api.go — admin 养号脚本库管理 API:
// 列表(含 disabled)/创建/启停/删除。镜像 warmup_api.go 的 ok/fail 信封、
// nil-Warmup 兜底、recordAudit 用法。
package api

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/acme/wadist/internal/warmup"
)

// handleAdminWarmupListScripts: GET /admin/warmup/scripts — 全部脚本(含 disabled)。
func (s *Server) handleAdminWarmupListScripts(c *gin.Context) {
	if s.deps.Warmup == nil {
		ok(c, gin.H{"rows": []any{}})
		return
	}
	rows, err := s.deps.Warmup.Store().ListAllScripts(c.Request.Context())
	if err != nil {
		fail(c, http.StatusInternalServerError, "list warmup scripts failed")
		return
	}
	ok(c, gin.H{"rows": rows})
}

// handleAdminWarmupCreateScript: POST /admin/warmup/scripts body
// {"lang":"pt","turns":[{"from":"A","text":"..."},{"from":"B","text":"..."}]}.
// 至少 2 轮,否则养号引擎没法演出一段像样的对话。
func (s *Server) handleAdminWarmupCreateScript(c *gin.Context) {
	if s.deps.Warmup == nil {
		fail(c, http.StatusServiceUnavailable, "warmup disabled")
		return
	}
	var body struct {
		Lang  string        `json:"lang"`
		Turns []warmup.Turn `json:"turns"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		fail(c, http.StatusBadRequest, "bad body")
		return
	}
	if body.Lang == "" {
		fail(c, http.StatusBadRequest, "lang is required")
		return
	}
	if len(body.Turns) < 2 {
		fail(c, http.StatusBadRequest, "turns must have at least 2 entries")
		return
	}
	ctx := c.Request.Context()
	id, err := s.deps.Warmup.Store().CreateScript(ctx, body.Lang, body.Turns)
	if err != nil {
		fail(c, http.StatusInternalServerError, "create warmup script failed")
		return
	}
	s.recordAudit(ctx, auditEvent{
		ActorID:      actorID(c),
		Action:       "warmup.script.create",
		ResourceType: "warmup_script",
		ResourceID:   id,
		Details:      gin.H{"lang": body.Lang, "turns": len(body.Turns)},
	})
	ok(c, gin.H{"id": id})
}

// handleAdminWarmupSetScriptEnabled: PUT /admin/warmup/scripts/:id body {"enabled":true}.
func (s *Server) handleAdminWarmupSetScriptEnabled(c *gin.Context) {
	if s.deps.Warmup == nil {
		fail(c, http.StatusServiceUnavailable, "warmup disabled")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "bad id")
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		fail(c, http.StatusBadRequest, "bad body")
		return
	}
	ctx := c.Request.Context()
	if err := s.deps.Warmup.Store().SetScriptEnabled(ctx, id, body.Enabled); err != nil {
		fail(c, http.StatusInternalServerError, "update warmup script failed")
		return
	}
	s.recordAudit(ctx, auditEvent{
		ActorID:      actorID(c),
		Action:       "warmup.script.setEnabled",
		ResourceType: "warmup_script",
		ResourceID:   id,
		Details:      gin.H{"enabled": body.Enabled},
	})
	ok(c, gin.H{"ok": true})
}

// handleAdminWarmupDeleteScript: DELETE /admin/warmup/scripts/:id.
func (s *Server) handleAdminWarmupDeleteScript(c *gin.Context) {
	if s.deps.Warmup == nil {
		fail(c, http.StatusServiceUnavailable, "warmup disabled")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "bad id")
		return
	}
	ctx := c.Request.Context()
	if err := s.deps.Warmup.Store().DeleteScript(ctx, id); err != nil {
		fail(c, http.StatusInternalServerError, "delete warmup script failed")
		return
	}
	s.recordAudit(ctx, auditEvent{
		ActorID:      actorID(c),
		Action:       "warmup.script.delete",
		ResourceType: "warmup_script",
		ResourceID:   id,
	})
	ok(c, gin.H{"ok": true})
}
