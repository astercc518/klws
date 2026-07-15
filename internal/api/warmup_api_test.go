package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/acme/wadist/internal/audit"
	"github.com/acme/wadist/internal/warmup"
)

// newWarmupTestServer wires a Server backed by a migrated pg testcontainer
// pool plus a real warmup.Service (fixed clock), mirroring newCrudServer's
// sysPool + Audit wiring so recordAudit/systemPool work in handlers under test.
func newWarmupTestServer(t *testing.T) (*Server, context.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	pool := testPool(t)
	svc := warmup.NewService(warmup.NewStore(pool), func() time.Time { return time.Now().UTC() })
	return &Server{sysPool: pool, deps: Deps{Audit: audit.NewAuditWriter(pool), Warmup: svc}}, context.Background()
}

// seedWarmupAccount inserts an account_devices row + enrolls it into warmup
// (mirrors the account_devices + EnrollIfAbsent seeding in
// internal/warmup/list_test.go's TestStoreListJoinsDevice).
func seedWarmupAccount(t *testing.T, ctx context.Context, s *Server, jid string) {
	t.Helper()
	var proxyID int64
	if err := s.systemPool().QueryRow(ctx, `INSERT INTO proxy_pool
		(proxy_url,proxy_type,country_code,max_bindings,current_bindings)
		VALUES ('socks5://w','socks5','US',1,1) RETURNING id`).Scan(&proxyID); err != nil {
		t.Fatalf("seed proxy: %v", err)
	}
	if _, err := s.systemPool().Exec(ctx, `INSERT INTO account_devices
		(tenant_id,account_jid,phone_number,ban_status,proxy_id,registered_at,health_score,sent_today)
		VALUES (1,$1,'15550001111','active',$2,now()-interval '10 days',80,3)`, jid, proxyID); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	if err := s.deps.Warmup.Enroll(ctx, jid, 1, warmup.LaneStandard); err != nil {
		t.Fatalf("enroll: %v", err)
	}
}

func TestWarmupListAndPauseAction(t *testing.T) {
	s, ctx := newWarmupTestServer(t)
	seedWarmupAccount(t, ctx, s, "wa@s.whatsapp.net")

	// list
	w := doJSON(t, s, s.handleAdminWarmupList, http.MethodGet, "/admin/warmup", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("list want 200 got %d: %s", w.Code, w.Body.String())
	}
	var listResp struct {
		Data struct {
			Rows  []warmup.ListRow `json:"rows"`
			Total int              `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list resp: %v", err)
	}
	if listResp.Data.Total != 1 || len(listResp.Data.Rows) != 1 {
		t.Fatalf("want 1 row/total, got %+v", listResp.Data)
	}
	if listResp.Data.Rows[0].AccountJID != "wa@s.whatsapp.net" {
		t.Fatalf("unexpected row: %+v", listResp.Data.Rows[0])
	}

	// pause action
	w2 := doJSON(t, s, s.handleAdminWarmupAction, http.MethodPost,
		"/admin/warmup/action?jid=wa@s.whatsapp.net", "", `{"action":"pause"}`)
	if w2.Code != http.StatusOK {
		t.Fatalf("pause want 200 got %d: %s", w2.Code, w2.Body.String())
	}

	// 验库
	p, err := s.deps.Warmup.Store().Get(ctx, "wa@s.whatsapp.net")
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}
	if !p.Paused {
		t.Fatalf("expected paused after action")
	}
}

func TestWarmupOverview(t *testing.T) {
	s, ctx := newWarmupTestServer(t)
	seedWarmupAccount(t, ctx, s, "ov@s.whatsapp.net")

	w := doJSON(t, s, s.handleAdminWarmupOverview, http.MethodGet, "/admin/warmup/overview", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("overview want 200 got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data warmup.Overview `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode overview resp: %v", err)
	}
	if resp.Data.Warming != 1 {
		t.Fatalf("want 1 warming, got %+v", resp.Data)
	}
}

// doJSONLane is doJSON but keys the route param "lane" instead of "id" —
// handleAdminWarmupSetPolicy reads c.Param("lane"), which doJSON (keyed "id")
// doesn't cover.
func doJSONLane(t *testing.T, s *Server, method, target, lane, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "lane", Value: lane}}
	s.handleAdminWarmupSetPolicy(c)
	return w
}

func TestWarmupPolicyRoundTrip(t *testing.T) {
	s, _ := newWarmupTestServer(t)

	w2 := doJSONLane(t, s, http.MethodPut, "/admin/warmup/policies/STANDARD", "STANDARD",
		`{"min_warmup_messages":20,"min_replies":5,"min_online_hours":36,"warming_cap":77,"mature_base_cap":20,"mature_max_cap":40,"mature_ramp_step":5}`)
	if w2.Code != http.StatusOK {
		t.Fatalf("set policy want 200 got %d: %s", w2.Code, w2.Body.String())
	}

	wg := doJSON(t, s, s.handleAdminWarmupGetPolicies, http.MethodGet, "/admin/warmup/policies", "", "")
	if wg.Code != http.StatusOK {
		t.Fatalf("get policies want 200 got %d: %s", wg.Code, wg.Body.String())
	}
	var resp struct {
		Data map[warmup.Lane]warmup.Policy `json:"data"`
	}
	if err := json.Unmarshal(wg.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode policies resp: %v", err)
	}
	got, ok := resp.Data[warmup.LaneStandard]
	if !ok {
		t.Fatalf("STANDARD lane missing from response: %+v", resp.Data)
	}
	if got.WarmingCap != 77 {
		t.Fatalf("want warming_cap=77 reflected in GET, got %+v", got)
	}
}
