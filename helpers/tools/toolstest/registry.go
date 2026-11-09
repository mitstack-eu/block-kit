// Package toolstest is a fake container registry for the tests of a script that copies a tool with copy-tool.sh. It serves images over the registry's HTTP API, in plain HTTP, to a client holding its anonymous pull token, as ghcr.io does, and records each read.
package toolstest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Setting is the variable that points copy-tool.sh at a registry, which then reads every image from it over its HTTP API.
const Setting = "COPY_TOOL_REGISTRY_URL"

// File is one file an image holds, in a layer of its own whose instruction names its path, as the kit's tools image builds it.
type File struct {
	Path, Body string
}

// Image is one image the registry serves behind an index, named by Ref with the index's digest, with one layer for each of its files in order.
type Image struct {
	Ref, Index, Manifest, Config string
	Layers                       []string
}

// Registry serves the images a test pushes, and answers a read with no token 401 with the standard Bearer challenge.
type Registry struct {
	URL         string
	mu          sync.Mutex
	blobs       map[string][]byte
	fail        map[string]int
	repos       map[string]bool
	tokenStatus int
	reads       []string
}

// New starts a registry the test closes when it ends.
func New(t testing.TB) *Registry {
	t.Helper()
	r := &Registry{blobs: map[string][]byte{}, fail: map[string]int{}, repos: map[string]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(srv.Close)
	r.URL = srv.URL
	return r
}

// Env is the environment entry that points copy-tool.sh at the registry.
func (r *Registry) Env() string { return Setting + "=" + r.URL }

func Digest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Push stores an image of files under repository for linux/amd64, beside an arm64 entry its index names and no read finds.
func (r *Registry) Push(t testing.TB, repository string, files ...File) Image {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.repos[repository] = true
	img := Image{}
	history := []map[string]any{{"created_by": "ENV PATH=/", "empty_layer": true}}
	var layers []map[string]string
	for _, f := range files {
		digest := r.put(layer(t, f))
		img.Layers = append(img.Layers, digest)
		layers = append(layers, map[string]string{"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": digest})
		history = append(history, map[string]any{"created_by": fmt.Sprintf("COPY %s %s # buildkit", f.Path, f.Path)})
	}
	img.Config = r.put(marshal(t, map[string]any{"os": "linux", "architecture": "amd64", "history": history}))
	img.Manifest = r.put(marshal(t, map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]string{"digest": img.Config}, "layers": layers}))
	img.Index = r.put(marshal(t, map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json", "manifests": []any{
		map[string]any{"digest": Digest([]byte("arm64")), "platform": map[string]string{"os": "linux", "architecture": "arm64"}},
		map[string]any{"digest": img.Manifest, "platform": map[string]string{"os": "linux", "architecture": "amd64"}},
	}}))
	img.Ref = strings.TrimPrefix(r.URL, "http://") + "/" + repository + ":test@" + img.Index
	return img
}

// Swap serves body at digest in place of what the digest names.
func (r *Registry) Swap(digest string, body []byte) {
	r.mu.Lock()
	r.blobs[digest] = body
	r.mu.Unlock()
}

// Fail answers every read of digest with status.
func (r *Registry) Fail(digest string, status int) {
	r.mu.Lock()
	r.fail[digest] = status
	r.mu.Unlock()
}

// RefuseTokens answers every pull-token request with status, as a private package does.
func (r *Registry) RefuseTokens(status int) { r.mu.Lock(); r.tokenStatus = status; r.mu.Unlock() }

// Reads lists the digests read with the token, in order.
func (r *Registry) Reads() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.reads...)
}

func (r *Registry) put(b []byte) string { d := Digest(b); r.blobs[d] = b; return d }

func (r *Registry) serve(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if req.URL.Path == "/token" {
		r.token(w, req)
		return
	}
	if req.Header.Get("Authorization") != "Bearer anonymous" {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="toolstest"`, r.URL))
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	repo, digest, ok := cutRead(req.URL.Path)
	if ok {
		r.reads = append(r.reads, digest)
	}
	blob, held := r.blobs[digest]
	switch {
	case !ok || !r.repos[repo] || !held:
		w.WriteHeader(http.StatusNotFound)
	case r.fail[digest] != 0:
		w.WriteHeader(r.fail[digest])
	default:
		_, _ = w.Write(blob)
	}
}

func (r *Registry) token(w http.ResponseWriter, req *http.Request) {
	repo := strings.TrimSuffix(strings.TrimPrefix(req.URL.Query().Get("scope"), "repository:"), ":pull")
	switch {
	case r.tokenStatus != 0:
		w.WriteHeader(r.tokenStatus)
	case req.Header.Get("Authorization") != "" || !r.repos[repo]:
		w.WriteHeader(http.StatusForbidden)
	default:
		fmt.Fprint(w, `{"token":"anonymous"}`)
	}
}

func cutRead(path string) (repo, digest string, ok bool) {
	rest, found := strings.CutPrefix(path, "/v2/")
	for _, kind := range []string{"/manifests/", "/blobs/"} {
		if repo, digest, cut := strings.Cut(rest, kind); found && cut {
			return repo, digest, true
		}
	}
	return "", "", false
}

func layer(t testing.TB, f File) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	err := tw.WriteHeader(&tar.Header{Name: strings.TrimPrefix(f.Path, "/"), Mode: 0o755, Size: int64(len(f.Body)), Typeflag: tar.TypeReg})
	if _, werr := tw.Write([]byte(f.Body)); err != nil || werr != nil || tw.Close() != nil || gz.Close() != nil {
		t.Fatalf("toolstest: writing the layer of %s: %v %v", f.Path, err, werr)
	}
	return buf.Bytes()
}

func marshal(t testing.TB, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("toolstest: %v", err)
	}
	return b
}
