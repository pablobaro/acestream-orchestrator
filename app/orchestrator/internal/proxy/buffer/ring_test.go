package buffer

import "testing"

// chunkOf returns targetSize bytes so a single Write stores exactly one chunk.
func chunkOf(size int) []byte {
	b := make([]byte, size)
	for i := 0; i < size; i += 188 {
		b[i] = 0x47
	}
	return b
}

func TestRingBuffer_ResetBumpsGeneration(t *testing.T) {
	rb := New(188*2, 4)

	before := rb.Generation()
	rb.Reset()
	if after := rb.Generation(); after == before {
		t.Fatalf("Reset must bump the generation, still %d", after)
	}
}

func TestRingBuffer_ResetKeepsHeadMonotonic(t *testing.T) {
	const size = 188 * 2
	rb := New(size, 4)
	for i := 0; i < 3; i++ {
		rb.Write(chunkOf(size))
	}

	head := rb.Head()
	rb.Reset()
	if got := rb.Head(); got != head {
		t.Errorf("Reset must not rewind head: got %d, want %d", got, head)
	}
}

// TestRingBuffer_StaleCursorAfterReset_StallsUntilReanchored pins down why
// consumers have to watch Generation().
//
// Reset clears the slots but leaves head where it was, so a cursor taken before
// the reset points into emptied slots. ReadAfter finds nothing there and keeps
// finding nothing while head advances — the consumer is stalled for a whole
// ring, not for one chunk. Re-anchoring to Head() is the only way out.
func TestRingBuffer_StaleCursorAfterReset_StallsUntilReanchored(t *testing.T) {
	const size = 188 * 2
	rb := New(size, 4)
	for i := 0; i < 3; i++ {
		rb.Write(chunkOf(size))
	}

	stale := int64(0) // a cursor from before the reset
	rb.Reset()
	rb.Write(chunkOf(size)) // upstream is producing again

	if chunks, _ := rb.ReadAfter(stale, 10); chunks != nil {
		t.Fatalf("stale cursor must read nothing after a reset, got %d chunks", len(chunks))
	}

	reanchored := rb.Head()
	rb.Write(chunkOf(size))

	chunks, last := rb.ReadAfter(reanchored, 10)
	if len(chunks) != 1 {
		t.Fatalf("re-anchored cursor must resume reading, got %d chunks", len(chunks))
	}
	if last <= reanchored {
		t.Errorf("cursor must advance: got %d, want > %d", last, reanchored)
	}
}
