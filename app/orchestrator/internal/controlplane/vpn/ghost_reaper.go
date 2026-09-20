package vpn

import (
	"sync"
	"time"
)

// missingNodeReapGrace is how long a dynamic VPN node may stay in the state
// store after Docker stops reporting its container.
//
// ListManagedNodes is called with All=true, so a container drops out of the
// observed set only when it is genuinely removed from Docker — not when it is
// merely stopped or restarting. The window therefore exists only to absorb a
// momentarily empty container listing (e.g. while the Docker daemon restarts),
// not to wait out a container that might come back. At the default 5s
// controller interval this is roughly six consecutive misses.
const missingNodeReapGrace = 30 * time.Second

// missingNodeAction is what to do with a dynamic VPN node that Docker no longer
// reports.
type missingNodeAction int

const (
	// missingNodeKeep leaves the node untouched.
	missingNodeKeep missingNodeAction = iota
	// missingNodeMarkDown records the node as down but keeps it in the store.
	missingNodeMarkDown
	// missingNodeReap removes the node from the store entirely.
	missingNodeReap
)

func (a missingNodeAction) String() string {
	switch a {
	case missingNodeKeep:
		return "keep"
	case missingNodeMarkDown:
		return "mark_down"
	case missingNodeReap:
		return "reap"
	default:
		return "unknown"
	}
}

// classifyMissingNode decides the fate of a node whose container Docker no
// longer reports.
//
// Nodes that are draining are left to the drain path, which owns their teardown
// end to end. Everything else is marked down first and removed once it has been
// missing for longer than grace — a node that is gone from Docker has no
// control API to probe, and leaving it in the store makes monitorHealth dial a
// dead IP forever, which is what pinned the health badge to DOWN.
func classifyMissingNode(lifecycle string, firstMissed, now time.Time, grace time.Duration) missingNodeAction {
	if lifecycle == "draining" {
		return missingNodeKeep
	}
	if now.Sub(firstMissed) >= grace {
		return missingNodeReap
	}
	return missingNodeMarkDown
}

// missingSinceTracker remembers when each container was first missing from
// Docker's listing, so the grace window is measured from the disappearance
// rather than from the current tick.
//
// It is deliberately not persisted: on restart the state store is rebuilt from
// Docker anyway, so a fresh tracker is always correct.
type missingSinceTracker struct {
	mu sync.Mutex
	at map[string]time.Time
}

// firstMissed records now as the container's first miss if it has none yet, and
// returns the effective first-miss timestamp.
func (t *missingSinceTracker) firstMissed(name string, now time.Time) time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.at == nil {
		t.at = make(map[string]time.Time)
	}
	if existing, ok := t.at[name]; ok {
		return existing
	}
	t.at[name] = now
	return now
}

// observed clears any recorded miss for a container that is present again, so a
// later disappearance gets a full grace window.
func (t *missingSinceTracker) observed(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.at, name)
}
