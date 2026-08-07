package webhook

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
)

// MaxRecentLogLines caps the in-memory ring buffer of recent operator log lines
// exposed via /logs. It is a debugging aid, not durable storage.
const MaxRecentLogLines = 500

// LogBuffer is the bounded ring buffer /logs serves from.
//
// Because every log line the operator emits is already secret-scrubbed before
// it is written, the buffer — and therefore /logs — never contains plaintext
// secrets. Scrubbing is the WRITER's job and deliberately not done here: a
// buffer that scrubbed on read would hide a leak that had already reached
// stderr, which is the opposite of what the leak tests assert.
type LogBuffer struct {
	mu    sync.Mutex
	max   int
	lines []string
}

// NewLogBuffer returns a buffer holding at most max lines (<=0 means
// MaxRecentLogLines).
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

// Clear resets the ring buffer (used by tests).
func (b *LogBuffer) Clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = nil
}

// Snapshot returns a copy of the recent lines, oldest first. The returned slice
// is always non-nil.
func (b *LogBuffer) Snapshot() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.lines))
	copy(out, b.lines)
	return out
}

// Writer returns an io.Writer that tees the standard logger's output into the
// buffer, one entry per newline-delimited line. Wire it in main via
// log.SetOutput(io.MultiWriter(os.Stderr, buf.Writer())).
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

// ServeLogs returns the operator's recent (already-scrubbed) log lines as
// {"lines": []}. It is a read-only GET that carries no request body, so it is
// authenticated by AuthenticateLogsGET (T-578) — mitstack's Ed25519 signature
// over the canonical "GET\n<path-with-query>\n<timestamp>" string, with the
// anti-replay timestamp in the X-Mitstack-Timestamp header. (The webhook POST
// /hook keeps its body-based replay guard unchanged.)
func ServeLogs(d Deps, b *LogBuffer, w http.ResponseWriter, r *http.Request) {
	if _, ok := AuthenticateLogsGET(d, w, r); !ok {
		return // AuthenticateLogsGET already wrote 401
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"lines": b.Snapshot()})
}
