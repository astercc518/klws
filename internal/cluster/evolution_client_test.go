package cluster

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
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
