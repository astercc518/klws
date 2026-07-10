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

	"github.com/acme/wadist/internal/store"
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

// CreateInstance provisions an Evolution instance bound to a proxy in one call.
// Proxy stickiness: callers invoke this only on first bind or after proxy death
// — never per-send. When webhookURL is non-empty the instance is configured to
// POST callbacks (connection/messages) back to the control plane.
// TODO(evo-verify): confirm path/fields against real Evolution v2.
func (c *EvoClient) CreateInstance(ctx context.Context, instanceName string, proxy *store.ProxyBinding, webhookURL string) error {
	body := map[string]any{
		"instanceName": instanceName,
		"integration":  "WHATSAPP-BAILEYS",
	}
	if p, ok := proxyFromBinding(proxy); ok {
		body["proxyHost"] = p.Host
		body["proxyPort"] = p.Port
		body["proxyProtocol"] = p.Protocol
		if p.Username != "" {
			body["proxyUsername"] = p.Username
			body["proxyPassword"] = p.Password
		}
	}
	if webhookURL != "" {
		body["webhook"] = map[string]any{
			"url":    webhookURL,
			"events": []string{"CONNECTION_UPDATE", "MESSAGES_UPDATE", "QRCODE_UPDATED"},
		}
	}
	return c.doJSON(ctx, http.MethodPost, "/instance/create", body, nil)
}

// ConnectInstance triggers pairing and returns a QR (base64 data URI) when the
// instance is unpaired; returns "" when already connected. QR also arrives via
// the QRCODE_UPDATED webhook.
// TODO(evo-verify): confirm path/fields against real Evolution v2.
func (c *EvoClient) ConnectInstance(ctx context.Context, instanceName string) (string, error) {
	var out struct {
		Base64 string `json:"base64"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/instance/connect/"+instanceName, nil, &out); err != nil {
		return "", err
	}
	return out.Base64, nil
}

// LogoutInstance ends the WA session (device unlinked). Terminal.
// TODO(evo-verify): confirm path against real Evolution v2.
func (c *EvoClient) LogoutInstance(ctx context.Context, instanceName string) error {
	return c.doJSON(ctx, http.MethodDelete, "/instance/logout/"+instanceName, nil, nil)
}

// DeleteInstance removes the instance and its Redis session. Idempotent.
// TODO(evo-verify): confirm path against real Evolution v2.
func (c *EvoClient) DeleteInstance(ctx context.Context, instanceName string) error {
	return c.doJSON(ctx, http.MethodDelete, "/instance/delete/"+instanceName, nil, nil)
}

// SetPresence sets the instance's global online/offline presence.
// TODO(evo-verify): confirm path/fields against real Evolution v2.
func (c *EvoClient) SetPresence(ctx context.Context, instanceName string, available bool) error {
	presence := "unavailable"
	if available {
		presence = "available"
	}
	return c.doJSON(ctx, http.MethodPost, "/instance/setPresence/"+instanceName,
		map[string]any{"presence": presence}, nil)
}

// SendTyping toggles a per-chat typing indicator (anthropomorphic dwell).
// TODO(evo-verify): confirm path/fields against real Evolution v2.
func (c *EvoClient) SendTyping(ctx context.Context, instanceName, toPhone string, composing bool) error {
	presence := "paused"
	if composing {
		presence = "composing"
	}
	return c.doJSON(ctx, http.MethodPost, "/chat/sendPresence/"+instanceName,
		map[string]any{"number": toPhone, "presence": presence}, nil)
}
