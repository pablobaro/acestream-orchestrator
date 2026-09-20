package stream

import (
	"reflect"
	"testing"
	"time"
)

func TestIsLiveLagExceeded(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	threshold := time.Hour

	tests := []struct {
		name      string
		liveLast  int64
		threshold time.Duration
		want      bool
	}{
		{"tracking the live edge", now.Add(-5 * time.Second).Unix(), threshold, false},
		{"lagging but inside the threshold", now.Add(-59 * time.Minute).Unix(), threshold, false},
		{"exactly at the threshold", now.Add(-time.Hour).Unix(), threshold, false},
		{"replaying hours-old data", now.Add(-3 * time.Hour).Unix(), threshold, true},
		{"engine has not reported yet", 0, threshold, false},
		{"negative live_last", -1, threshold, false},
		{"detection disabled by threshold", now.Add(-3 * time.Hour).Unix(), 0, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isLiveLagExceeded(tc.liveLast, now, tc.threshold); got != tc.want {
				t.Errorf("isLiveLagExceeded = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLoopTracker_MarkAndQuery(t *testing.T) {
	tr := &LoopTracker{}
	now := time.Now()

	if tr.IsLooping("a", now, 0) {
		t.Error("an unmarked stream must not report as looping")
	}

	tr.Mark("a", now)
	if !tr.IsLooping("a", now, 0) {
		t.Error("a marked stream must report as looping")
	}
}

// Retention of zero is the documented "keep until cleared by hand" setting.
func TestLoopTracker_ZeroRetentionNeverExpires(t *testing.T) {
	tr := &LoopTracker{}
	now := time.Now()
	tr.Mark("a", now)

	if !tr.IsLooping("a", now.Add(30*24*time.Hour), 0) {
		t.Error("zero retention must keep the mark indefinitely")
	}
}

func TestLoopTracker_RetentionExpiresMark(t *testing.T) {
	tr := &LoopTracker{}
	now := time.Now()
	tr.Mark("a", now)

	if !tr.IsLooping("a", now.Add(4*time.Minute), 5*time.Minute) {
		t.Error("mark must survive inside the retention window")
	}
	if tr.IsLooping("a", now.Add(5*time.Minute), 5*time.Minute) {
		t.Error("mark must expire once the retention window has passed")
	}
	if tr.IsLooping("a", now, 0) {
		t.Error("an expired mark must be dropped, not just hidden")
	}
}

// The detector re-checks on every stat tick; retention has to run from when the
// loop was first seen, not from the most recent confirmation of it.
func TestLoopTracker_MarkKeepsFirstDetectionTime(t *testing.T) {
	tr := &LoopTracker{}
	first := time.Now()
	tr.Mark("a", first)
	tr.Mark("a", first.Add(4*time.Minute))

	if tr.IsLooping("a", first.Add(5*time.Minute), 5*time.Minute) {
		t.Error("re-marking must not extend the retention window")
	}
}

func TestLoopTracker_RemoveAndClear(t *testing.T) {
	tr := &LoopTracker{}
	now := time.Now()
	tr.Mark("a", now)
	tr.Mark("b", now)

	if !tr.Remove("a") {
		t.Error("Remove must report that a mark was present")
	}
	if tr.Remove("a") {
		t.Error("Remove must report false the second time")
	}
	if n := tr.Clear(); n != 1 {
		t.Errorf("Clear removed %d marks, want 1", n)
	}
	if ids, _ := tr.List(now, 0); len(ids) != 0 {
		t.Errorf("list must be empty after Clear, got %v", ids)
	}
}

func TestLoopTracker_ListIsSortedAndDropsExpired(t *testing.T) {
	tr := &LoopTracker{}
	now := time.Now()
	tr.Mark("charlie", now)
	tr.Mark("alpha", now)
	tr.Mark("bravo", now.Add(-10*time.Minute)) // older than the window below

	ids, marks := tr.List(now, 5*time.Minute)
	if want := []string{"alpha", "charlie"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("ids = %v, want %v", ids, want)
	}
	if _, ok := marks["bravo"]; ok {
		t.Error("expired mark must not appear in the listing")
	}
}
