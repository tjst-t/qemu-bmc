package serial

// ring keeps the last size bytes written to it.
type ring struct {
	buf  []byte
	next int  // where the next byte goes
	full bool // buf has wrapped at least once
}

func newRing(size int) *ring {
	if size < 0 {
		size = 0
	}
	return &ring{buf: make([]byte, size)}
}

func (r *ring) Write(p []byte) {
	size := len(r.buf)
	if size == 0 {
		return
	}
	if len(p) >= size {
		copy(r.buf, p[len(p)-size:])
		r.next, r.full = 0, true
		return
	}
	n := copy(r.buf[r.next:], p)
	if n < len(p) {
		copy(r.buf, p[n:])
		r.full = true
	}
	r.next += len(p)
	if r.next >= size {
		r.next -= size
		r.full = true
	}
}

// Bytes returns a copy of the buffered bytes, oldest first.
func (r *ring) Bytes() []byte {
	if !r.full {
		return append([]byte(nil), r.buf[:r.next]...)
	}
	out := make([]byte, 0, len(r.buf))
	out = append(out, r.buf[r.next:]...)
	return append(out, r.buf[:r.next]...)
}
