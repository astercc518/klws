package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// EvoClient is the low-level HTTP client to one Evolution API node. It caps
// connections per host so a burst of sends cannot exhaust the (single-process,
// V8) Evolution node — the density guardrail called out in the migration spec.
type EvoClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func NewEvoClient(baseURL, apiKey string) *EvoClient {
	tr := &http.Transport{
		MaxConnsPerHost:     64,
		MaxIdleConnsPerHost: 32,
		IdleConnTimeout:     90 * time.Second,
	}
	return &EvoClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Transport: tr, Timeout: 30 * time.Second},
	}
}

// doJSON issues a request with the apikey header and decodes a JSON response.
func (c *EvoClient) doJSON(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("apikey", c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("evolution %s %s: %d %s", method, path, resp.StatusCode, msg)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// FetchState returns the connection state of an instance (E0 round-trip proof).
func (c *EvoClient) FetchState(ctx context.Context, instanceName string) (string, error) {
	var out struct {
		Instance struct {
			State string `json:"state"`
		} `json:"instance"`
	}
	if err := c.doJSON(ctx, http.MethodGet,
		"/instance/connectionState/"+instanceName, nil, &out); err != nil {
		return "", err
	}
	return out.Instance.State, nil
}
