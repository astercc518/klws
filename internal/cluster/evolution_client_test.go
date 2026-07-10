package cluster

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/acme/wadist/internal/store"
)

func TestEvoClient_FetchState_SendsApiKeyAndParses(t *testing.T) {
	var gotKey, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("apikey")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"instance":{"state":"open"}}`))
	}))
	defer srv.Close()

	c := NewEvoClient(srv.URL, "secret-key")
	state, err := c.FetchState(context.Background(), "wa_1_default_1")
	if err != nil {
		t.Fatal(err)
	}
	if gotKey != "secret-key" {
		t.Fatalf("apikey header = %q", gotKey)
	}
	if gotPath != "/instance/connectionState/wa_1_default_1" {
		t.Fatalf("path = %q", gotPath)
	}
	if state != "open" {
		t.Fatalf("state = %q", state)
	}
}

func TestEvoClient_CreateInstance_SendsProxyAndWebhook(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"instance":{"instanceName":"wa_1"}}`))
	}))
	defer srv.Close()

	c := NewEvoClient(srv.URL, "k")
	b := &store.ProxyBinding{ProxyURL: "socks5://u:p@1.2.3.4:1080", ProxyType: "socks5", Country: "US"}
	if err := c.CreateInstance(context.Background(), "wa_1", b, "https://cp/api/v1/webhook/evolution"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/instance/create" {
		t.Fatalf("path=%q", gotPath)
	}
	if gotBody["instanceName"] != "wa_1" {
		t.Fatalf("instanceName=%v", gotBody["instanceName"])
	}
	if gotBody["proxyHost"] != "1.2.3.4" || gotBody["proxyProtocol"] != "socks5" {
		t.Fatalf("proxy fields wrong: %+v", gotBody)
	}
}

func TestEvoClient_CreateInstance_NoProxyOmitsProxyFields(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := NewEvoClient(srv.URL, "k")
	if err := c.CreateInstance(context.Background(), "wa_2", nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, present := gotBody["proxyHost"]; present {
		t.Fatalf("proxyHost must be omitted when no proxy: %+v", gotBody)
	}
}
