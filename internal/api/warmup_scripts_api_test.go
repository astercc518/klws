package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/acme/wadist/internal/warmup"
)

// TestWarmupScriptsCreateAndList covers Task 18's script-library CRUD wiring:
// create via API -> list reflects it (including Enabled=true) -> disable via
// API -> list still shows it with Enabled=false -> delete via API -> gone.
func TestWarmupScriptsCreateAndList(t *testing.T) {
	s, _ := newWarmupTestServer(t)

	create := doJSON(t, s, s.handleAdminWarmupCreateScript, http.MethodPost, "/admin/warmup/scripts", "",
		`{"lang":"xx-api-test","turns":[{"from":"A","text":"oi"},{"from":"B","text":"tudo bem"}]}`)
	if create.Code != http.StatusOK {
		t.Fatalf("create want 200 got %d: %s", create.Code, create.Body.String())
	}
	var createResp struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("decode create resp: %v", err)
	}
	if createResp.Data.ID == 0 {
		t.Fatalf("expected nonzero id, got %+v", createResp.Data)
	}
	id := createResp.Data.ID

	list := doJSON(t, s, s.handleAdminWarmupListScripts, http.MethodGet, "/admin/warmup/scripts", "", "")
	if list.Code != http.StatusOK {
		t.Fatalf("list want 200 got %d: %s", list.Code, list.Body.String())
	}
	var listResp struct {
		Data struct {
			Rows []warmup.Script `json:"rows"`
		} `json:"data"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list resp: %v", err)
	}
	var found *warmup.Script
	for i := range listResp.Data.Rows {
		if listResp.Data.Rows[i].ID == id {
			found = &listResp.Data.Rows[i]
		}
	}
	if found == nil {
		t.Fatalf("created script id=%d not in list: %+v", id, listResp.Data.Rows)
	}
	if !found.Enabled || len(found.Turns) != 2 {
		t.Fatalf("unexpected script row: %+v", found)
	}

	// disable it
	setEnabled := doJSON(t, s, s.handleAdminWarmupSetScriptEnabled, http.MethodPut,
		"/admin/warmup/scripts/"+itoa(id), itoa(id), `{"enabled":false}`)
	if setEnabled.Code != http.StatusOK {
		t.Fatalf("set enabled want 200 got %d: %s", setEnabled.Code, setEnabled.Body.String())
	}
	list2 := doJSON(t, s, s.handleAdminWarmupListScripts, http.MethodGet, "/admin/warmup/scripts", "", "")
	var listResp2 struct {
		Data struct {
			Rows []warmup.Script `json:"rows"`
		} `json:"data"`
	}
	if err := json.Unmarshal(list2.Body.Bytes(), &listResp2); err != nil {
		t.Fatalf("decode list2 resp: %v", err)
	}
	found2 := false
	for _, r := range listResp2.Data.Rows {
		if r.ID == id {
			found2 = true
			if r.Enabled {
				t.Fatalf("expected disabled after setEnabled(false), got %+v", r)
			}
		}
	}
	if !found2 {
		t.Fatalf("disabled script id=%d should still be listed", id)
	}

	// delete it
	del := doJSON(t, s, s.handleAdminWarmupDeleteScript, http.MethodDelete,
		"/admin/warmup/scripts/"+itoa(id), itoa(id), "")
	if del.Code != http.StatusOK {
		t.Fatalf("delete want 200 got %d: %s", del.Code, del.Body.String())
	}
	list3 := doJSON(t, s, s.handleAdminWarmupListScripts, http.MethodGet, "/admin/warmup/scripts", "", "")
	var listResp3 struct {
		Data struct {
			Rows []warmup.Script `json:"rows"`
		} `json:"data"`
	}
	if err := json.Unmarshal(list3.Body.Bytes(), &listResp3); err != nil {
		t.Fatalf("decode list3 resp: %v", err)
	}
	for _, r := range listResp3.Data.Rows {
		if r.ID == id {
			t.Fatalf("deleted script id=%d still listed: %+v", id, listResp3.Data.Rows)
		}
	}
}
