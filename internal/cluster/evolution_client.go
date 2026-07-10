package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/acme/wadist/internal/store"
)

// EvoHTTPError is a non-2xx response from Evolution, carrying the status code
// so the send path can classify it (throttle vs permanent vs transient).
type EvoHTTPError struct {
	StatusCode int
	Method     string
	Path       string
	Body       string
}

func (e *EvoHTTPError) Error() string {
	return fmt.Sprintf("evolution %s %s: %d %s", e.Method, e.Path, e.StatusCode, e.Body)
}

// IsThrottle reports whether err is a rate/overload signal (retry after backoff,
// and shed the global rate). TODO(evo-verify): confirm Evolution's throttle codes.
func IsThrottle(err error) bool {
	var he *EvoHTTPError
	return errors.As(err, &he) && (he.StatusCode == 429 || he.StatusCode == 503)
}

// IsPermanent reports whether err will not be fixed by retrying (client errors:
// bad request, unauthorized, logged-out / not-connected instance, gone).
// TODO(evo-verify): confirm which statuses Evolution returns for logged-out.
func IsPermanent(err error) bool {
	var he *EvoHTTPError
	if !errors.As(err, &he) {
		return false // network/transport errors are transient → retryable
	}
	switch he.StatusCode {
	case 400, 401, 403, 404, 422:
		return true
	}
	return false
}

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
		return &EvoHTTPError{StatusCode: resp.StatusCode, Method: method, Path: path, Body: string(msg)}
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
	if proxy != nil {
		p, ok := proxyFromBinding(proxy)
		if !ok {
			return fmt.Errorf("evolution create %s: proxy binding present but unparseable: %q", instanceName, proxy.ProxyURL)
		}
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

// SendResult carries Evolution's returned message key. RemoteID (key.id) IS the
// WhatsApp message id and MUST be persisted as campaign_recipients.message_id so
// the webhook receipt path (E3) matches on the same key the receipt carries.
type SendResult struct {
	RemoteID string
	Status   string
}

// SendText sends a plain-text message and returns the WA message id (key.id).
// TODO(evo-verify): confirm path/fields against real Evolution v2.
func (c *EvoClient) SendText(ctx context.Context, instanceName, toPhone, body string) (SendResult, error) {
	var out struct {
		Key struct {
			ID string `json:"id"`
		} `json:"key"`
		Status string `json:"status"`
	}
	err := c.doJSON(ctx, http.MethodPost, "/message/sendText/"+instanceName,
		map[string]any{"number": toPhone, "text": body}, &out)
	if err != nil {
		return SendResult{}, err
	}
	return SendResult{RemoteID: out.Key.ID, Status: out.Status}, nil
}
