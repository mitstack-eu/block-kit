package webhook

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
)

// MaxRecentLogLines caps the in-memory ring buffer of recent log lines exposed via /logs.
const MaxRecentLogLines = 500

// LogBuffer is the bounded ring buffer /logs serves from, and relies on writers scrubbing secrets because scrubbing on read would hide a leak that already reached stderr.
type LogBuffer struct {
	mu    sync.Mutex
	max   int
	lines []string
}

// NewLogBuffer returns a buffer holding at most max lines, or MaxRecentLogLines when max is not positive.
func NewLogBuffer(max int) *LogBuffer {
	if max <= 0 {
		max = MaxRecentLogLines
	}
	return &LogBuffer{max: max}
}

// Append adds one line to the ring buffer.
func (b *LogBuffer) Append(line string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.max <= 0 {
		b.max = MaxRecentLogLines
	}
	b.lines = append(b.lines, line)
	if len(b.lines) > b.max {
		b.lines = b.lines[len(b.lines)-b.max:]
	}
}

// Clear resets the ring buffer.
func (b *LogBuffer) Clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = nil
}

// Snapshot returns a non-nil copy of the recent lines, oldest first.
func (b *LogBuffer) Snapshot() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.lines))
	copy(out, b.lines)
	return out
}

// Writer returns an io.Writer that tees the standard logger's output into the buffer, one entry per line.
func (b *LogBuffer) Writer() io.Writer { return logBufferWriter{b} }

type logBufferWriter struct{ b *LogBuffer }

func (w logBufferWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line != "" {
			w.b.Append(line)
		}
	}
	return len(p), nil
}

// ServeLogs returns the operator's recent log lines as {"lines": []}, authenticated by AuthenticateLogsGET.
func ServeLogs(d Deps, b *LogBuffer, w http.ResponseWriter, r *http.Request) {
	if _, ok := AuthenticateLogsGET(d, w, r); !ok {
		return // AuthenticateLogsGET already wrote 401
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"lines": b.Snapshot()})
}
