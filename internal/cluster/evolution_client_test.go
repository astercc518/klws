package cluster

import (
	"context"
	"encoding/json"
	"errors"
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

	c := NewEvoClient(srv.URL, "secret-key", "")
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

func TestEvoClient_CreateInstance_SendsWebhookWithAuthHeader(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"instance":{"instanceName":"wa_1"}}`))
	}))
	defer srv.Close()

	c := NewEvoClient(srv.URL, "k", "wh-token")
	if err := c.CreateInstance(context.Background(), "wa_1", "https://cp/api/v1/webhook/evolution"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/instance/create" || gotBody["instanceName"] != "wa_1" {
		t.Fatalf("path=%q body=%+v", gotPath, gotBody)
	}
	// Proxy must NOT be in the create body (v2 sets it via /proxy/set).
	if _, present := gotBody["proxyHost"]; present {
		t.Fatalf("create body must not carry proxy fields: %+v", gotBody)
	}
	wh, ok := gotBody["webhook"].(map[string]any)
	if !ok {
		t.Fatalf("webhook missing: %+v", gotBody)
	}
	headers, ok := wh["headers"].(map[string]any)
	if !ok || headers["authorization"] != "wh-token" {
		t.Fatalf("webhook headers.authorization not set to echo token: %+v", wh)
	}
}

func TestEvoClient_CreateInstance_NoWebhookAuthOmitsHeaders(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := NewEvoClient(srv.URL, "k", "") // no webhook auth
	if err := c.CreateInstance(context.Background(), "wa_2", "https://cp/wh"); err != nil {
		t.Fatal(err)
	}
	wh, _ := gotBody["webhook"].(map[string]any)
	if _, present := wh["headers"]; present {
		t.Fatalf("no auth token -> no headers: %+v", wh)
	}
}

func TestEvoClient_SetProxy_UsesDedicatedEndpoint(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := NewEvoClient(srv.URL, "k", "")
	b := &store.ProxyBinding{ProxyURL: "socks5://u:p@1.2.3.4:1080", ProxyType: "socks5", Country: "US"}
	if err := c.SetProxy(context.Background(), "wa_1", b); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/proxy/set/wa_1" {
		t.Fatalf("path=%q", gotPath)
	}
	if gotBody["enabled"] != true || gotBody["host"] != "1.2.3.4" ||
		gotBody["port"] != "1080" || gotBody["protocol"] != "socks5" ||
		gotBody["username"] != "u" || gotBody["password"] != "p" {
		t.Fatalf("proxy body wrong: %+v", gotBody)
	}
}

func TestEvoClient_SetProxy_UnparseableFailsClosed(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := NewEvoClient(srv.URL, "k", "")
	// Non-nil binding whose URL has no port -> proxyFromBinding returns false.
	b := &store.ProxyBinding{ProxyURL: "http://1.2.3.4"}
	if err := c.SetProxy(context.Background(), "wa_x", b); err == nil {
		t.Fatal("expected fail-closed error for unparseable proxy binding")
	}
	if called {
		t.Fatal("must NOT call Evolution with an unparseable proxy binding")
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
	c := NewEvoClient(srv.URL, "k", "")
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
	c := NewEvoClient(srv.URL, "k", "")
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
	c := NewEvoClient(srv.URL, "k", "")
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
	c := NewEvoClient(srv.URL, "k", "")
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

	c := NewEvoClient(srv.URL, "k", "")
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

func TestEvoHTTPError_Classification(t *testing.T) {
	throttle := &EvoHTTPError{StatusCode: 429}
	perm := &EvoHTTPError{StatusCode: 400}
	other := errors.New("dial tcp: timeout")

	if !IsThrottle(throttle) || IsThrottle(perm) || IsThrottle(other) {
		t.Fatal("IsThrottle wrong")
	}
	if !IsPermanent(perm) || IsPermanent(throttle) || IsPermanent(other) {
		t.Fatal("IsPermanent wrong")
	}
	if !IsPermanent(&EvoHTTPError{StatusCode: 404}) {
		t.Fatal("404 should be permanent")
	}
	if !IsThrottle(&EvoHTTPError{StatusCode: 503}) {
		t.Fatal("503 should be throttle")
	}
}

func TestEvoClient_doJSON_ReturnsTypedHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate"}`))
	}))
	defer srv.Close()
	c := NewEvoClient(srv.URL, "k", "")
	_, err := c.SendText(context.Background(), "wa_1", "1555", "hi")
	if err == nil {
		t.Fatal("expected error on 429")
	}
	var he *EvoHTTPError
	if !errors.As(err, &he) || he.StatusCode != 429 {
		t.Fatalf("want *EvoHTTPError 429, got %T %v", err, err)
	}
	if !IsThrottle(err) {
		t.Fatal("429 send error should classify as throttle")
	}
}
