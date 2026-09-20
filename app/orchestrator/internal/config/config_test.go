package config

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// captureWarnings runs fn with a temporary slog default that records output.
func captureWarnings(fn func()) string {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(prev)
	fn()
	return buf.String()
}

// TestDefaults_TimeoutOrderingIsConsistent guards the shipped defaults against
// the combination that makes the reader's retry path unreachable: if clients
// give up before the upstream read times out, no stream anyone is watching can
// ever be recovered by a reconnect.
func TestDefaults_TimeoutOrderingIsConsistent(t *testing.T) {
	out := captureWarnings(func() { warnTimeoutOrdering(load()) })
	if out != "" {
		t.Errorf("default config must not trip the timeout ordering warnings, got:\n%s", out)
	}
}

func TestWarnTimeoutOrdering_FlagsUpstreamOutlastingClients(t *testing.T) {
	c := load()
	c.UpstreamReadTimeout = 90 * time.Second // the pre-fix default
	c.NoDataTimeoutChecks = 60
	c.NoDataCheckInterval = time.Second

	out := captureWarnings(func() { warnTimeoutOrdering(c) })
	if !strings.Contains(out, "UPSTREAM_READ_TIMEOUT_S") {
		t.Errorf("expected a warning about the upstream read timeout, got:\n%s", out)
	}
}

func TestWarnTimeoutOrdering_FlagsShortShutdownDelay(t *testing.T) {
	c := load()
	c.ChannelShutdownDelay = 5 * time.Second // the pre-fix default

	out := captureWarnings(func() { warnTimeoutOrdering(c) })
	if !strings.Contains(out, "CHANNEL_SHUTDOWN_DELAY_S") {
		t.Errorf("expected a warning about the channel shutdown delay, got:\n%s", out)
	}
}

// Settings stored in SQLite override these timeouts at runtime, so a saved
// value can reintroduce exactly what the startup check exists to catch.
func TestApplySettings_RechecksTimeoutOrdering(t *testing.T) {
	prev := C.Load()
	t.Cleanup(func() { C.Store(prev) })

	out := captureWarnings(func() {
		ApplySettings(map[string]any{"upstream_read_timeout": 300})
	})
	if !strings.Contains(out, "UPSTREAM_READ_TIMEOUT_S") {
		t.Errorf("ApplySettings must re-check the timeout ordering, got:\n%s", out)
	}
	if got := C.Load().UpstreamReadTimeout; got != 300*time.Second {
		t.Errorf("setting must still be applied: got %v", got)
	}
}
