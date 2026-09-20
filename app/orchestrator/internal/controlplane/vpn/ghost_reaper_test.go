package vpn

import (
	"testing"
	"time"
)

func TestClassifyMissingNode(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 8, 23, 14, 40, 0, 0, time.UTC)
	const grace = 30 * time.Second

	tests := []struct {
		name        string
		lifecycle   string
		firstMissed time.Time
		now         time.Time
		want        missingNodeAction
	}{
		{
			// The drain path owns draining nodes end-to-end; the reaper must
			// not race it.
			name:        "draining nodes are left alone",
			lifecycle:   "draining",
			firstMissed: base,
			now:         base.Add(10 * time.Minute),
			want:        missingNodeKeep,
		},
		{
			name:        "first miss only marks the node down",
			lifecycle:   "active",
			firstMissed: base,
			now:         base,
			want:        missingNodeMarkDown,
		},
		{
			name:        "still inside the grace window",
			lifecycle:   "active",
			firstMissed: base,
			now:         base.Add(grace - time.Second),
			want:        missingNodeMarkDown,
		},
		{
			// This is the bug that kept the badge DOWN: a container removed
			// from Docker stayed in the store forever and monitorHealth kept
			// dialing its dead IP, timing out on every tick.
			name:        "past the grace window the node is reaped",
			lifecycle:   "active",
			firstMissed: base,
			now:         base.Add(grace),
			want:        missingNodeReap,
		},
		{
			name:        "well past the grace window",
			lifecycle:   "active",
			firstMissed: base,
			now:         base.Add(10 * time.Minute),
			want:        missingNodeReap,
		},
		{
			// Clock skew must never reap early.
			name:        "clock going backwards does not reap",
			lifecycle:   "active",
			firstMissed: base,
			now:         base.Add(-time.Hour),
			want:        missingNodeMarkDown,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := classifyMissingNode(tc.lifecycle, tc.firstMissed, tc.now, grace)
			if got != tc.want {
				t.Fatalf("classifyMissingNode(%q, +%v) = %v, want %v",
					tc.lifecycle, tc.now.Sub(tc.firstMissed), got, tc.want)
			}
		})
	}
}

func TestMissingSinceTracker(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 8, 23, 14, 40, 0, 0, time.UTC)
	var tr missingSinceTracker

	// First miss records the timestamp.
	if got := tr.firstMissed("node-a", base); !got.Equal(base) {
		t.Fatalf("firstMissed = %v, want %v", got, base)
	}

	// Subsequent misses keep the original timestamp, so the grace window is
	// measured from when the node actually disappeared.
	later := base.Add(20 * time.Second)
	if got := tr.firstMissed("node-a", later); !got.Equal(base) {
		t.Fatalf("firstMissed on second miss = %v, want %v (the original)", got, base)
	}

	// Observing the node again clears it, so a later disappearance gets a
	// fresh full grace window rather than being reaped instantly.
	tr.observed("node-a")
	reappeared := base.Add(time.Hour)
	if got := tr.firstMissed("node-a", reappeared); !got.Equal(reappeared) {
		t.Fatalf("firstMissed after observed = %v, want %v", got, reappeared)
	}

	// Independent keys do not interfere.
	if got := tr.firstMissed("node-b", later); !got.Equal(later) {
		t.Fatalf("firstMissed(node-b) = %v, want %v", got, later)
	}
}
