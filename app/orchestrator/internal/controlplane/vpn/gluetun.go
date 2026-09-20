package vpn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/acestream/acestream/internal/config"
	"github.com/acestream/acestream/internal/state"
)

// controlAPIURL returns the Gluetun control API base URL for a given container.
// It prefers the stored ControlHost (IP address) over the container name so
// that the API is reachable even when the orchestrator and the Gluetun
// container are attached to different Docker networks.
func controlAPIURL(vpnContainer string) string {
	host := vpnContainer
	if n, ok := state.Global.GetVPNNode(vpnContainer); ok && n.ControlHost != "" {
		host = n.ControlHost
	}
	return fmt.Sprintf("http://%s:%d", host, config.C.Load().GluetunAPIPort)
}

// ── Forwarded-port cache ──────────────────────────────────────────────────────

type portCacheEntry struct {
	port      int
	expiresAt time.Time
}

var (
	portCacheMu sync.Mutex
	portCache   = make(map[string]portCacheEntry)
)

func cachedForwardedPort(vpnContainer string) (int, bool) {
	portCacheMu.Lock()
	defer portCacheMu.Unlock()
	e, ok := portCache[vpnContainer]
	if !ok || time.Now().After(e.expiresAt) {
		return 0, false
	}
	return e.port, true
}

func setPortCache(vpnContainer string, port int) {
	ttl := config.C.Load().GluetunPortCacheTTL
	if ttl == 0 {
		ttl = 60 * time.Second
	}
	portCacheMu.Lock()
	defer portCacheMu.Unlock()
	portCache[vpnContainer] = portCacheEntry{port: port, expiresAt: time.Now().Add(ttl)}
}

func evictPortCache(vpnContainer string) {
	portCacheMu.Lock()
	defer portCacheMu.Unlock()
	delete(portCache, vpnContainer)
}

// ── Control-API probing ───────────────────────────────────────────────────────

// Probe outcome classifications. These are stable, machine-readable values: a
// dial timeout, a refused connection and a reachable-but-disconnected tunnel
// each point at a completely different fix, so the health log has to say which
// one happened instead of just "healthy=false".
const (
	ReasonOK                 = "ok"
	ReasonDNSFailure         = "dns_failure"
	ReasonConnectionRefused  = "connection_refused"
	ReasonTimeout            = "timeout"
	ReasonNetworkUnreachable = "network_unreachable"
	ReasonTransportError     = "transport_error"
	ReasonServerError        = "server_error"
	ReasonTunnelDown         = "tunnel_down"
)

// ControlAPIProbe is the outcome of probing a Gluetun control API, including
// why it failed.
type ControlAPIProbe struct {
	// Reachable is true when the control server answered at all.
	Reachable bool
	// Connected is true when the VPN tunnel is provably carrying traffic.
	Connected bool
	// Reason classifies the outcome (one of the Reason* constants).
	Reason string
	// Detail carries the raw error or status text, for logs.
	Detail string
}

// probeClient is a dedicated HTTP client for control-API probes.
//
// It deliberately does not use http.DefaultClient. Probes run on a timer
// against containers whose network stack is reconfigured underneath us:
// Gluetun re-applies its firewall rules on every VPN (re)connection, which
// drops existing conntrack entries. A pooled keep-alive connection survives
// that as a half-open socket on our side, so the shared transport keeps handing
// the same dead connection back and every subsequent probe times out until the
// orchestrator restarts. OpenVPN renegotiates and reconnects far more often
// than WireGuard, which is why that failure mode shows up asymmetrically.
//
// One TCP handshake per probe is a cheap price for probes that are actually
// independent of each other.
var probeClient = &http.Client{
	Transport: &http.Transport{
		DisableKeepAlives: true,
		DialContext: (&net.Dialer{
			Timeout: 2 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   2 * time.Second,
		ResponseHeaderTimeout: 3 * time.Second,
	},
}

// probeURL joins a control-API base URL with a path without doubling slashes.
func probeURL(base, path string) string {
	return strings.TrimRight(base, "/") + path
}

// classifyTransportError maps a transport-level error to a Reason* constant.
// It returns "" for a nil error.
func classifyTransportError(err error) string {
	if err == nil {
		return ""
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return ReasonDNSFailure
	}
	if isConnRefused(err) {
		return ReasonConnectionRefused
	}
	if isNetUnreachable(err) {
		return ReasonNetworkUnreachable
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return ReasonTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return ReasonTimeout
	}
	return ReasonTransportError
}

// IsControlAPIReachable reports whether the Gluetun control API is up and
// (optionally) connected. Callers that need to know *why* a probe failed should
// use ProbeControlAPI instead.
func IsControlAPIReachable(vpnContainer string, requireConnected bool) bool {
	p := ProbeControlAPI(vpnContainer, requireConnected)
	if requireConnected {
		return p.Connected
	}
	return p.Reachable
}

// ProbeControlAPI probes a VPN container's Gluetun control API and reports the
// full outcome.
//
// Connectivity detection strategy:
//  1. GET /v1/publicip/ip — a non-empty public_ip proves the tunnel is carrying
//     traffic (this endpoint is always auth-free in Gluetun).
//  2. Fallback to /v1/openvpn/status, then /v1/wireguard/status. A 401 on those
//     means the API server is up and auth is configured on the endpoint (e.g.
//     via a persistent auth/config.toml in the Gluetun volume); the tunnel is
//     considered connected in that case too.
func ProbeControlAPI(vpnContainer string, requireConnected bool) ControlAPIProbe {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	return probeControlAPIAt(ctx, controlAPIURL(vpnContainer), requireConnected)
}

func probeControlAPIAt(ctx context.Context, base string, requireConnected bool) ControlAPIProbe {
	status, body, err := probeGet(ctx, probeURL(base, "/v1/publicip/ip"))
	if err != nil {
		return ControlAPIProbe{Reason: classifyTransportError(err), Detail: err.Error()}
	}
	if status >= 500 {
		return ControlAPIProbe{
			Reason: ReasonServerError,
			Detail: fmt.Sprintf("GET /v1/publicip/ip returned %d", status),
		}
	}

	if !requireConnected {
		return ControlAPIProbe{Reachable: true, Reason: ReasonOK}
	}

	// Primary connectivity signal: a non-empty public IP.
	var ipResp struct {
		IP string `json:"public_ip"`
	}
	if err := json.Unmarshal(body, &ipResp); err == nil && ipResp.IP != "" {
		return ControlAPIProbe{Reachable: true, Connected: true, Reason: ReasonOK}
	}

	// Fallbacks: the per-protocol status endpoints.
	observed := make([]string, 0, 2)
	for _, path := range []string{"/v1/openvpn/status", "/v1/wireguard/status"} {
		connected, statusText := probeTunnelStatus(ctx, base, path)
		if connected {
			return ControlAPIProbe{Reachable: true, Connected: true, Reason: ReasonOK}
		}
		observed = append(observed, path+"="+statusText)
	}

	return ControlAPIProbe{
		Reachable: true,
		Reason:    ReasonTunnelDown,
		Detail:    "control API up, no tunnel reported established (" + strings.Join(observed, ", ") + ")",
	}
}

// probeTunnelStatus queries one of Gluetun's per-protocol status endpoints.
// statusText is always a short, log-friendly description of what happened.
func probeTunnelStatus(ctx context.Context, base, path string) (connected bool, statusText string) {
	status, body, err := probeGet(ctx, probeURL(base, path))
	if err != nil {
		return false, classifyTransportError(err)
	}
	// 401 = auth required = server is up = treat as connected.
	if status == http.StatusUnauthorized {
		return true, "unauthorized"
	}
	var s struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &s); err != nil {
		return false, fmt.Sprintf("http_%d_unparseable", status)
	}
	if s.Status == "" {
		return false, fmt.Sprintf("http_%d_no_status", status)
	}
	return strings.EqualFold(s.Status, "running"), s.Status
}

func probeGet(ctx context.Context, url string) (status int, body []byte, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	resp, err := probeClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	body, _ = io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, body, nil
}

// GetForwardedPort fetches the current forwarded port from the Gluetun control
// API, with a TTL cache to avoid hammering the API on every probe.
func GetForwardedPort(vpnContainer string) int {
	if p, ok := cachedForwardedPort(vpnContainer); ok {
		return p
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Try universal portforward endpoint first
	port := fetchPort(ctx, vpnContainer, "/v1/portforward")
	if port > 0 {
		setPortCache(vpnContainer, port)
	}
	return port
}

func fetchPort(ctx context.Context, vpnContainer, path string) int {
	status, body, err := probeGet(ctx, probeURL(controlAPIURL(vpnContainer), path))
	if err != nil {
		slog.Debug("forwarded-port fetch failed",
			"vpn", vpnContainer, "reason", classifyTransportError(err), "err", err)
		return 0
	}
	if status != http.StatusOK {
		return 0
	}

	var portResp struct {
		Port int `json:"port"`
	}
	if err := json.Unmarshal(body, &portResp); err != nil {
		return 0
	}
	return portResp.Port
}

// WaitForForwardedPort polls the Gluetun control API until a forwarded port is
// available, the deadline expires, or ctx is cancelled.
func WaitForForwardedPort(ctx context.Context, vpnContainer string) int {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return 0
		default:
		}
		if p := GetForwardedPort(vpnContainer); p > 0 {
			return p
		}
		select {
		case <-ctx.Done():
			return 0
		case <-time.After(500 * time.Millisecond):
		}
	}
	slog.Warn("timed out waiting for forwarded port", "vpn", vpnContainer)
	return 0
}

// InvalidatePortCache evicts the cached forwarded port for a VPN container.
// Call this when a VPN reconnects so stale ports are not returned.
func InvalidatePortCache(vpnContainer string) {
	evictPortCache(vpnContainer)
}

// portForwardingProviders is the set of VPN providers that natively support
// port forwarding (same list as Python PORT_FORWARDING_NATIVE_PROVIDERS).
var portForwardingProviders = map[string]bool{
	"private internet access": true,
	"perfect privacy":         true,
	"privatevpn":              true,
	"protonvpn":               true,
}

// ProviderSupportsForwarding returns true if the given provider natively
// supports port forwarding.
func ProviderSupportsForwarding(provider string) bool {
	return portForwardingProviders[strings.ToLower(strings.TrimSpace(provider))]
}
