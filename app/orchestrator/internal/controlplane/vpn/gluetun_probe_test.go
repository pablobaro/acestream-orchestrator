package vpn

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestClassifyTransportError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "nil error is not classified",
			err:  nil,
			want: "",
		},
		{
			name: "dns failure",
			err:  &net.DNSError{Err: "no such host", Name: "gluetun-x", IsNotFound: true},
			want: ReasonDNSFailure,
		},
		{
			name: "context deadline exceeded is a timeout",
			err:  context.DeadlineExceeded,
			want: ReasonTimeout,
		},
		{
			name: "net timeout",
			err:  &net.OpError{Op: "dial", Err: &timeoutErr{}},
			want: ReasonTimeout,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := classifyTransportError(tc.err)
			if got != tc.want {
				t.Fatalf("classifyTransportError(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// timeoutErr is a minimal net.Error that reports a timeout.
type timeoutErr struct{}

func (e *timeoutErr) Error() string   { return "i/o timeout" }
func (e *timeoutErr) Timeout() bool   { return true }
func (e *timeoutErr) Temporary() bool { return true }

func TestClassifyTransportErrorConnectionRefused(t *testing.T) {
	t.Parallel()

	// Bind then immediately close so the port is (almost certainly) refused.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var d net.Dialer
	_, dialErr := d.DialContext(ctx, "tcp", addr)
	if dialErr == nil {
		t.Skip("port unexpectedly accepted a connection")
	}

	if got := classifyTransportError(dialErr); got != ReasonConnectionRefused {
		t.Fatalf("classifyTransportError(%v) = %q, want %q", dialErr, got, ReasonConnectionRefused)
	}
}

func TestProbeControlAPIAt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		requireConnected bool
		handler          http.HandlerFunc
		wantReachable    bool
		wantConnected    bool
		wantReason       string
	}{
		{
			name:             "public ip present means connected",
			requireConnected: true,
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/publicip/ip" {
					_, _ = w.Write([]byte(`{"public_ip":"1.2.3.4"}`))
					return
				}
				w.WriteHeader(http.StatusNotFound)
			},
			wantReachable: true,
			wantConnected: true,
			wantReason:    ReasonOK,
		},
		{
			name:             "reachable is enough when connection is not required",
			requireConnected: false,
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"public_ip":""}`))
			},
			wantReachable: true,
			wantConnected: false,
			wantReason:    ReasonOK,
		},
		{
			name:             "empty public ip falls back to openvpn status",
			requireConnected: true,
			handler: func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/publicip/ip":
					_, _ = w.Write([]byte(`{"public_ip":""}`))
				case "/v1/openvpn/status":
					_, _ = w.Write([]byte(`{"status":"running"}`))
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			},
			wantReachable: true,
			wantConnected: true,
			wantReason:    ReasonOK,
		},
		{
			name:             "401 on status endpoint counts as connected",
			requireConnected: true,
			handler: func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/publicip/ip":
					_, _ = w.Write([]byte(`{"public_ip":""}`))
				default:
					w.WriteHeader(http.StatusUnauthorized)
				}
			},
			wantReachable: true,
			wantConnected: true,
			wantReason:    ReasonOK,
		},
		{
			name:             "falls back to wireguard status",
			requireConnected: true,
			handler: func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/publicip/ip":
					_, _ = w.Write([]byte(`{"public_ip":""}`))
				case "/v1/openvpn/status":
					_, _ = w.Write([]byte(`{"status":"stopped"}`))
				case "/v1/wireguard/status":
					_, _ = w.Write([]byte(`{"status":"running"}`))
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			},
			wantReachable: true,
			wantConnected: true,
			wantReason:    ReasonOK,
		},
		{
			name:             "server up but tunnel reports down",
			requireConnected: true,
			handler: func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/publicip/ip":
					_, _ = w.Write([]byte(`{"public_ip":""}`))
				default:
					_, _ = w.Write([]byte(`{"status":"stopped"}`))
				}
			},
			wantReachable: true,
			wantConnected: false,
			wantReason:    ReasonTunnelDown,
		},
		{
			name:             "5xx is a server error",
			requireConnected: true,
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			wantReachable: false,
			wantConnected: false,
			wantReason:    ReasonServerError,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(tc.handler)
			defer srv.Close()

			got := probeControlAPIAt(context.Background(), srv.URL, tc.requireConnected)

			if got.Reachable != tc.wantReachable {
				t.Errorf("Reachable = %v, want %v (detail=%q)", got.Reachable, tc.wantReachable, got.Detail)
			}
			if got.Connected != tc.wantConnected {
				t.Errorf("Connected = %v, want %v (detail=%q)", got.Connected, tc.wantConnected, got.Detail)
			}
			if got.Reason != tc.wantReason {
				t.Errorf("Reason = %q, want %q (detail=%q)", got.Reason, tc.wantReason, got.Detail)
			}
		})
	}
}

func TestProbeControlAPIAtUnreachable(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening anymore

	got := probeControlAPIAt(context.Background(), url, true)

	if got.Reachable {
		t.Fatalf("Reachable = true, want false")
	}
	if got.Reason == ReasonOK || got.Reason == "" {
		t.Fatalf("Reason = %q, want a transport failure classification", got.Reason)
	}
	if got.Detail == "" {
		t.Error("Detail is empty; the probe must surface the underlying error for logs")
	}
}

// TestProbeClientDisablesKeepAlives pins the reason the probe path does not use
// http.DefaultClient: Gluetun re-applies its firewall on every VPN
// (re)connection, which drops existing conntrack entries. A pooled keep-alive
// connection survives that as a half-open socket, so every later probe reuses a
// dead connection and times out until the process restarts.
func TestProbeClientDisablesKeepAlives(t *testing.T) {
	t.Parallel()

	tr, ok := probeClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("probeClient.Transport is %T, want *http.Transport", probeClient.Transport)
	}
	if !tr.DisableKeepAlives {
		t.Error("probeClient must disable keep-alives so each probe dials fresh")
	}
	if probeClient == http.DefaultClient {
		t.Error("probeClient must not be http.DefaultClient")
	}
}

func TestProbeControlAPIAtSeparateConnectionsPerProbe(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	conns := make(map[string]bool)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		conns[r.RemoteAddr] = true
		mu.Unlock()
		_, _ = w.Write([]byte(`{"public_ip":"1.2.3.4"}`))
	}))
	defer srv.Close()

	for i := 0; i < 3; i++ {
		probeControlAPIAt(context.Background(), srv.URL, true)
	}

	mu.Lock()
	n := len(conns)
	mu.Unlock()

	if n < 3 {
		t.Errorf("probes shared %d connection(s) across 3 calls; want a fresh connection each time", n)
	}
}

func TestControlAPIURLTrimsTrailingSlash(t *testing.T) {
	t.Parallel()

	got := probeURL("http://1.2.3.4:8001/", "/v1/publicip/ip")
	want := "http://1.2.3.4:8001/v1/publicip/ip"
	if got != want {
		t.Fatalf("probeURL = %q, want %q", got, want)
	}
	if strings.Contains(got, "//v1") {
		t.Errorf("probeURL produced a doubled slash: %q", got)
	}
}
