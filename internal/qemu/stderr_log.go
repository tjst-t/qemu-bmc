package qemu

import (
	"bytes"
	"sync"
)

// lineLogger is an io.Writer that logs every complete line written to it
// with a "qemu: " prefix, so QEMU's stderr (e.g. "could not open
// /dev/net/tun") shows up in the qemu-bmc log.
type lineLogger struct {
	logf func(format string, v ...any)
	mu   sync.Mutex
	buf  []byte
}

func (l *lineLogger) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		l.emit(l.buf[:i])
		l.buf = l.buf[i+1:]
	}
	return len(p), nil
}

// Flush logs a trailing line that was not terminated by a newline.
func (l *lineLogger) Flush() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.buf) > 0 {
		l.emit(l.buf)
		l.buf = nil
	}
}

func (l *lineLogger) emit(line []byte) {
	line = bytes.TrimRight(line, "\r")
	if len(line) == 0 {
		return
	}
	l.logf("qemu: %s", line)
}
