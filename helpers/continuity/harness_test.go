package continuity_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/helpers/age"
	"github.com/mitstack-eu/block-kit/helpers/continuity"
)

const (
	datum     = "row-4f9a-the-customers-order"
	signature = "sig-secret-7f3"
	query     = "X-Amz-Signature=" + signature
)

var takenAt = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

// store is an object store behind presigned links: it serves an object only to a request carrying the signature.
type store struct {
	srv        *httptest.Server
	mu         sync.Mutex
	objects    map[string][]byte
	puts       [][]byte
	chunked    bool
	putStatus  int
	getStatus  int
	getCorrupt bool
}

func newStore(t *testing.T) *store {
	t.Helper()
	s := &store{objects: map[string][]byte{}}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *store) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.URL.RawQuery != query {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		s.puts = append(s.puts, body)
		s.chunked = s.chunked || len(r.TransferEncoding) > 0 || r.ContentLength != int64(len(body))
		if s.putStatus != 0 {
			w.WriteHeader(s.putStatus)
			return
		}
		s.objects[r.URL.Path] = body
	case http.MethodGet:
		if s.getStatus != 0 {
			w.WriteHeader(s.getStatus)
			return
		}
		body := bytes.Clone(s.objects[r.URL.Path])
		if s.getCorrupt && len(body) > 0 {
			body[len(body)-1] ^= 1
		}
		_, _ = w.Write(body)
	}
}

type links struct{ base string }

func (l links) Upload(_ context.Context, unit string) (continuity.Link, error) {
	return continuity.Link{URL: l.base + "/" + unit + "?" + query, Ref: unit}, nil
}

func (l links) Download(_ context.Context, _, ref string) (continuity.Link, error) {
	return continuity.Link{URL: l.base + "/" + ref + "?" + query}, nil
}

type keys struct{ id *age.Identity }

func (k keys) Recipient(context.Context, string) (string, error) {
	return k.id.Recipient().String(), nil
}
func (k keys) Identity(context.Context, string) (string, error) { return k.id.String(), nil }

func newHook(t *testing.T, state continuity.State) (continuity.Hook, *store) {
	t.Helper()
	id, err := age.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	s := newStore(t)
	return continuity.Hook{Links: links{base: s.srv.URL}, Keys: keys{id: id}, State: state, Clock: func() time.Time { return takenAt }}, s
}

// kind is one shared hook with a fixture holding datum, a way to lose it and a way to see it back.
type kind struct {
	name    string
	state   continuity.State
	lose    func()
	present func() bool
}

func volumeKind(t *testing.T) kind {
	dir := filepath.Join(t.TempDir(), "data")
	write := func(name, body string) {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	write("db/orders.txt", datum)
	write("top.txt", "unrelated")
	return kind{name: "volume", state: continuity.Volume{Dir: dir},
		lose: func() { _ = os.RemoveAll(dir) },
		present: func() bool {
			got, err := os.ReadFile(filepath.Join(dir, "db/orders.txt"))
			return err == nil && string(got) == datum
		}}
}

type fakeBao struct {
	mu          sync.Mutex
	data        []byte
	initialized bool
	restarts    int
	srv         *httptest.Server
}

func newBao(t *testing.T, data string) *fakeBao {
	b := &fakeBao{data: []byte(data), initialized: data != ""}
	b.srv = httptest.NewServer(http.HandlerFunc(b.serve))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *fakeBao) serve(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if r.Header.Get("X-Vault-Token") != "bao-token-3c1" {
		http.Error(w, "permission denied", http.StatusForbidden)
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/sys/storage/raft/snapshot":
		_, _ = w.Write(b.data)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/sys/storage/raft/snapshot-force":
		b.data, _ = io.ReadAll(r.Body)
		b.initialized = true
		w.WriteHeader(http.StatusNoContent)
	case r.URL.Path == "/v1/sys/health":
		if !b.initialized {
			w.WriteHeader(http.StatusNotImplemented)
		}
		_, _ = io.WriteString(w, `{"initialized":`+map[bool]string{true: "true", false: "false"}[b.initialized]+`}`)
	}
}

func (b *fakeBao) snapshot() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}

func openBaoKind(t *testing.T) (kind, *fakeBao, *fakeBao) {
	live, scratch := newBao(t, "kv/orders="+datum), newBao(t, "")
	state := continuity.OpenBao{
		Live:    continuity.Endpoint{Addr: live.srv.URL, Token: "bao-token-3c1"},
		Scratch: continuity.Endpoint{Addr: scratch.srv.URL, Token: "bao-token-3c1"},
		Restart: func(context.Context) error { live.restarts++; return nil },
	}
	return kind{name: "openbao", state: state,
		lose:    func() { live.data = nil },
		present: func() bool { return strings.Contains(live.snapshot(), datum) && live.restarts == 1 }}, live, scratch
}

func allKinds(t *testing.T) []kind {
	t.Helper()
	bao, _, _ := openBaoKind(t)
	return []kind{volumeKind(t), bao}
}
