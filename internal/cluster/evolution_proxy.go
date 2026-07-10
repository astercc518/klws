package cluster

import (
	"net/url"
	"strconv"

	"github.com/acme/wadist/internal/store"
)

// evoProxy is Evolution's per-instance proxy shape, derived from a
// store.ProxyBinding's URL. Evolution wants the parts split out, whereas
// whatsmeow took the whole URL string.
type evoProxy struct {
	Host     string
	Port     int
	Protocol string
	Username string
	Password string
}

// proxyFromBinding splits a ProxyBinding.ProxyURL into Evolution's proxy parts.
// Returns ok=false for a nil/empty binding or an unparseable URL (host/port
// missing) — the caller then creates the instance without a proxy rather than
// dialing direct-by-accident being masked as success.
func proxyFromBinding(b *store.ProxyBinding) (*evoProxy, bool) {
	if b == nil || b.ProxyURL == "" {
		return nil, false
	}
	u, err := url.Parse(b.ProxyURL)
	if err != nil || u.Host == "" || u.Scheme == "" {
		return nil, false
	}
	portStr := u.Port()
	if portStr == "" {
		return nil, false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, false
	}
	p := &evoProxy{
		Host:     u.Hostname(),
		Port:     port,
		Protocol: u.Scheme,
	}
	if u.User != nil {
		p.Username = u.User.Username()
		p.Password, _ = u.User.Password()
	}
	return p, true
}
