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

func TestEvoClient_CreateInstance_UnparseableProxyFailsClosed(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := NewEvoClient(srv.URL, "k")
	// Non-nil binding whose URL has no port -> proxyFromBinding returns false.
	b := &store.ProxyBinding{ProxyURL: "http://1.2.3.4"}
	err := c.CreateInstance(context.Background(), "wa_x", b, "")
	if err == nil {
		t.Fatal("expected fail-closed error for unparseable proxy binding, got nil")
	}
	if called {
		t.Fatal("must NOT create a proxyless instance when a proxy binding was intended")
	}
}

func TestEvoClient_ConnectInstance_ReturnsQR(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/instance/connect/wa_1" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"base64":"data:image/png;base64,QRDATA"}`))
	}))
	defer srv.Close()
	c := NewEvoClient(srv.URL, "k")
	qr, err := c.ConnectInstance(context.Background(), "wa_1")
	if err != nil {
		t.Fatal(err)
	}
	if qr != "data:image/png;base64,QRDATA" {
		t.Fatalf("qr=%q", qr)
	}
}

func TestEvoClient_LogoutAndDelete(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method=%s", r.Method)
		}
		paths = append(paths, r.URL.Path)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := NewEvoClient(srv.URL, "k")
	if err := c.LogoutInstance(context.Background(), "wa_1"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteInstance(context.Background(), "wa_1"); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0] != "/instance/logout/wa_1" || paths[1] != "/instance/delete/wa_1" {
		t.Fatalf("paths=%v", paths)
	}
}

func TestEvoClient_SetPresence(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := NewEvoClient(srv.URL, "k")
	if err := c.SetPresence(context.Background(), "wa_1", true); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/instance/setPresence/wa_1" || gotBody["presence"] != "available" {
		t.Fatalf("path=%q body=%+v", gotPath, gotBody)
	}
}

func TestEvoClient_SendTyping(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := NewEvoClient(srv.URL, "k")
	if err := c.SendTyping(context.Background(), "wa_1", "15551234", true); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/chat/sendPresence/wa_1" || gotBody["number"] != "15551234" || gotBody["presence"] != "composing" {
		t.Fatalf("path=%q body=%+v", gotPath, gotBody)
	}
}

func TestEvoClient_SendText_ReturnsKeyID(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"key":{"id":"WAMID123","fromMe":true},"status":"PENDING"}`))
	}))
	defer srv.Close()

	c := NewEvoClient(srv.URL, "k")
	res, err := c.SendText(context.Background(), "wa_1", "15551234", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/message/sendText/wa_1" {
		t.Fatalf("path=%q", gotPath)
	}
	if gotBody["number"] != "15551234" || gotBody["text"] != "hello" {
		t.Fatalf("body=%+v", gotBody)
	}
	if res.RemoteID != "WAMID123" || res.Status != "PENDING" {
		t.Fatalf("res=%+v", res)
	}
}
