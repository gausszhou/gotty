package session

import "sync"

// ring is a fixed-capacity byte buffer that keeps only the most recent
// data. It backs the attach-time replay of terminal output so that a
// reconnecting client sees the tail of what the session already printed.
type ring struct {
	mu  sync.Mutex
	cap int
	b   []byte
}

// newRing creates a ring buffer holding at most cap bytes.
func newRing(cap int) *ring {
	return &ring{cap: cap}
}

// Write appends p, dropping the oldest bytes when the capacity is exceeded.
func (r *ring) Write(p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(p) >= r.cap {
		// The new data alone fills the whole ring.
		r.b = append(r.b[:0], p[len(p)-r.cap:]...)
		return
	}

	r.b = append(r.b, p...)
	if len(r.b) > r.cap {
		// Compact by copying the tail (keeps the underlying array shrinkable,
		// so a one-off burst does not pin a large allocation forever).
		n := len(r.b) - r.cap
		r.b = append(r.b[:0], r.b[n:]...)
	}
}

// Bytes returns a copy of the buffered data, oldest first.
func (r *ring) Bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]byte(nil), r.b...)
}
