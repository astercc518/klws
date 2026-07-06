package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// seedDevice inserts one account_devices row.
func seedDevice(t *testing.T, ctx context.Context, s *Server, tenantID int64, jid, phone, banStatus, ownerNode string) {
	t.Helper()
	var on any
	if ownerNode != "" {
		on = ownerNode
	}
	if _, err := s.systemPool().Exec(ctx,
		`INSERT INTO account_devices (tenant_id, account_jid, phone_number, ban_status, owner_node) VALUES ($1,$2,$3,$4::ban_status_t,$5)`,
		tenantID, jid, phone, banStatus, on); err != nil {
		t.Fatalf("seed device: %v", err)
	}
}

func TestHandleAdminListDevices_pagination(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	s := &Server{sysPool: testPool(t)}
	tid, _ := seedTenantUser(t, ctx, s, "dev-admin@acme.test")

	// 3 active(online), 2 banned(offline), 1 logged_out(offline)
	for i := 0; i < 3; i++ {
		seedDevice(t, ctx, s, tid, fmt.Sprintf("a%d@wa", i), fmt.Sprintf("100%d", i), "active", "node-1")
	}
	for i := 0; i < 2; i++ {
		seedDevice(t, ctx, s, tid, fmt.Sprintf("b%d@wa", i), fmt.Sprintf("200%d", i), "banned", "")
	}
	seedDevice(t, ctx, s, tid, "c0@wa", "3000", "logged_out", "")

	call := func(q string) map[string]any {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/admin/resources/devices"+q, nil)
		s.handleAdminListDevices(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status %d body %s", w.Code, w.Body.String())
		}
		var env struct{ Data map[string]any `json:"data"` }
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return env.Data
	}

	// no filter: total 6; stats reflect full breakdown
	all := call("")
	if all["total"].(float64) != 6 {
		t.Errorf("total = %v want 6", all["total"])
	}
	stats := all["stats"].(map[string]any)
	if stats["total"].(float64) != 6 || stats["online"].(float64) != 3 || stats["banned"].(float64) != 2 || stats["logged_out"].(float64) != 1 {
		t.Errorf("stats = %v", stats)
	}
	// ban_status filter
	if call("?ban_status=banned")["total"].(float64) != 2 {
		t.Errorf("banned filter wrong")
	}
	// online filter
	if call("?online=true")["total"].(float64) != 3 {
		t.Errorf("online filter wrong")
	}
	if call("?online=false")["total"].(float64) != 3 {
		t.Errorf("offline filter wrong")
	}
	// q filter (jid prefix a)
	if call("?q=a0@wa")["total"].(float64) != 1 {
		t.Errorf("q filter wrong")
	}
	// pagination: limit 2 → 2 rows, total still 6
	pg := call("?limit=2")
	if len(pg["rows"].([]any)) != 2 || pg["total"].(float64) != 6 {
		t.Errorf("limit paging: rows=%d total=%v", len(pg["rows"].([]any)), pg["total"])
	}
	// stats are UNFILTERED even under a filter
	filteredStats := call("?ban_status=banned")["stats"].(map[string]any)
	if filteredStats["total"].(float64) != 6 {
		t.Errorf("stats must be unfiltered, got %v", filteredStats["total"])
	}
}

func seedProxy(t *testing.T, ctx context.Context, s *Server, url, country, ptype string, alive bool) {
	t.Helper()
	if _, err := s.systemPool().Exec(ctx,
		`INSERT INTO proxy_pool (proxy_url, country_code, proxy_type, is_alive) VALUES ($1,$2,$3::proxy_type_t,$4)`,
		url, country, ptype, alive); err != nil {
		t.Fatalf("seed proxy: %v", err)
	}
}

func TestHandleAdminListProxies_pagination(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	s := &Server{sysPool: testPool(t)}

	seedProxy(t, ctx, s, "socks5://1.1.1.1:1080", "US", "socks5", true)
	seedProxy(t, ctx, s, "socks5://2.2.2.2:1080", "US", "socks5", true)
	seedProxy(t, ctx, s, "http://3.3.3.3:8080", "DE", "http", false)

	call := func(q string) map[string]any {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/admin/resources/proxies"+q, nil)
		s.handleAdminListProxies(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status %d body %s", w.Code, w.Body.String())
		}
		var env struct{ Data map[string]any `json:"data"` }
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return env.Data
	}

	all := call("")
	if all["total"].(float64) != 3 {
		t.Errorf("total = %v want 3", all["total"])
	}
	stats := all["stats"].(map[string]any)
	if stats["total"].(float64) != 3 || stats["alive"].(float64) != 2 || stats["dead"].(float64) != 1 {
		t.Errorf("stats = %v", stats)
	}
	if call("?alive=true")["total"].(float64) != 2 {
		t.Errorf("alive filter wrong")
	}
	if call("?proxy_type=http")["total"].(float64) != 1 {
		t.Errorf("type filter wrong")
	}
	if call("?q=DE")["total"].(float64) != 1 {
		t.Errorf("q filter wrong")
	}
	pg := call("?limit=1")
	if len(pg["rows"].([]any)) != 1 || pg["total"].(float64) != 3 {
		t.Errorf("limit paging wrong")
	}
}
