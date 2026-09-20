package stream

import "testing"

func TestChunksLapped(t *testing.T) {
	tests := []struct {
		name       string
		localIndex int64
		oldest     int64
		want       int64
	}{
		{"ring has not wrapped yet", 0, 0, 0},
		{"cursor sits exactly at the oldest chunk", 4, 5, 0},
		{"cursor is comfortably inside the ring", 40, 20, 0},
		{"cursor is one chunk off the back", 3, 5, 1},
		{"cursor was lapped hard", 0, 16, 15},
		{"fresh client at the live edge", 99, 84, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := chunksLapped(tc.localIndex, tc.oldest); got != tc.want {
				t.Errorf("chunksLapped(%d, %d) = %d, want %d",
					tc.localIndex, tc.oldest, got, tc.want)
			}
		})
	}
}
