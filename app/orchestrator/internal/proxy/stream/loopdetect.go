package stream

import (
	"sort"
	"sync"
	"time"
)

// ── Looping-stream detection ──────────────────────────────────────────────────
//
// A live AceStream engine reports live_last: the wall-clock timestamp of the
// newest data it holds. When the broadcast stops feeding it, the engine does
// not fail — it keeps serving what it already has, and live_last stops tracking
// the present. Playback continues, but it is replaying the past, which viewers
// experience as a channel that is stuck and running backwards in time.
//
// docs/STREAM_LOOP_DETECTION.md describes this behaviour as shipped. The Go
// rewrite kept the live_last plumbing — parsed in proxy/aceapi, carried through
// proxy/stream, exposed in state — but not the detector that consumed it, so
// the value reached the dashboard and stopped there.

// isLiveLagExceeded reports whether an engine's live_last has fallen further
// behind now than threshold allows.
//
// A non-positive live_last means the engine has not reported a live position
// yet, which is absence of evidence, not evidence of a loop. A non-positive
// threshold disables the check.
func isLiveLagExceeded(liveLast int64, now time.Time, threshold time.Duration) bool {
	if liveLast <= 0 || threshold <= 0 {
		return false
	}
	return now.Sub(time.Unix(liveLast, 0)) > threshold
}

// LoopTracker records the streams detected as looping.
//
// Detection alone is not enough: a stopped stream is restarted by the next
// client request, straight back into the same loop. The tracker is what lets
// the proxy refuse that request until the source is producing again.
type LoopTracker struct {
	mu sync.Mutex
	at map[string]time.Time
}

// Loops is the process-wide tracker consulted before a stream is started.
var Loops = &LoopTracker{}

// Mark records contentID as looping, keeping the earliest detection time so
// retention is measured from when the loop started, not from the last check.
func (t *LoopTracker) Mark(contentID string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.at == nil {
		t.at = make(map[string]time.Time)
	}
	if _, ok := t.at[contentID]; !ok {
		t.at[contentID] = now
	}
}

// IsLooping reports whether contentID is currently marked. A positive
// retention expires the mark; zero keeps it until removed by hand.
func (t *LoopTracker) IsLooping(contentID string, now time.Time, retention time.Duration) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	markedAt, ok := t.at[contentID]
	if !ok {
		return false
	}
	if retention > 0 && now.Sub(markedAt) >= retention {
		delete(t.at, contentID)
		return false
	}
	return true
}

// Remove drops a mark, reporting whether one was present.
func (t *LoopTracker) Remove(contentID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.at[contentID]
	delete(t.at, contentID)
	return ok
}

// Clear drops every mark and reports how many there were.
func (t *LoopTracker) Clear() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(t.at)
	t.at = nil
	return n
}

// List returns the live marks, dropping any the retention window has expired.
// IDs come back sorted so the API response is stable between calls.
func (t *LoopTracker) List(now time.Time, retention time.Duration) ([]string, map[string]time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()

	ids := make([]string, 0, len(t.at))
	out := make(map[string]time.Time, len(t.at))
	for id, markedAt := range t.at {
		if retention > 0 && now.Sub(markedAt) >= retention {
			delete(t.at, id)
			continue
		}
		ids = append(ids, id)
		out[id] = markedAt
	}
	sort.Strings(ids)
	return ids, out
}
