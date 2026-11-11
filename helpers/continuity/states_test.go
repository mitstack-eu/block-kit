// The volume, OpenBao and CloudNativePG states refuse what a scratch restore must not accept and leak neither a token nor a scratch cluster.
package continuity_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/helpers/continuity"
)

type entry struct {
	name, body string
	typeflag   byte
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// archive writes a volume archive whose digest list names each regular entry, with its digest taken from digests when it holds the name.
func archive(t *testing.T, digests map[string]string, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	type file struct {
		Path   string `json:"path"`
		Mode   int    `json:"mode"`
		SHA256 string `json:"sha256"`
	}
	var files []file
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Typeflag: e.typeflag, Mode: 0o644, Size: int64(len(e.body))}
		if e.typeflag == tar.TypeSymlink {
			hdr.Linkname, hdr.Size = "/etc/passwd", 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(e.body))
		if e.typeflag == tar.TypeReg {
			sum, ok := digests[e.name]
			if !ok {
				sum = digest(e.body)
			}
			files = append(files, file{e.name, 0o644, sum})
		}
	}
	manifest, _ := json.Marshal(map[string]any{"files": files})
	_ = tw.WriteHeader(&tar.Header{Name: ".continuity-manifest.json", Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(manifest))})
	_, _ = tw.Write(manifest)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestVolumeImport_RefusesAnArchiveThatEscapesItsDirectoryOrHoldsWhatItCannotCarry(t *testing.T) {
	good := entry{"orders.txt", datum, tar.TypeReg}
	cases := []struct {
		label string
		raw   []byte
	}{
		{"a name climbing out of the directory", archive(t, nil, good, entry{"../escape.txt", "x", tar.TypeReg})},
		{"an absolute name", archive(t, nil, good, entry{"/tmp/escape.txt", "x", tar.TypeReg})},
		{"a symbolic link", archive(t, nil, good, entry{"link", "", tar.TypeSymlink})},
		{"a file whose digest is not the listed one", archive(t, map[string]string{"orders.txt": digest("another")}, good)},
		{"an empty archive, which lists no digests", nil},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			base := t.TempDir()
			v := continuity.Volume{Dir: filepath.Join(base, "data")}

			err := v.Import(context.Background(), bytes.NewReader(tc.raw))

			if err == nil {
				t.Errorf("Import accepted %s", tc.label)
			}
			if _, statErr := os.Stat(filepath.Join(base, "escape.txt")); statErr == nil {
				t.Errorf("Import of %s wrote a file outside its directory", tc.label)
			}
		})
	}
}

func TestVolumeVerify_AcceptsAnArchiveWhoseFilesReadBackWithTheirListedDigests(t *testing.T) {
	raw := archive(t, nil, entry{"db/", "", tar.TypeDir}, entry{"db/orders.txt", datum, tar.TypeReg})

	err := continuity.Volume{Dir: t.TempDir()}.Verify(context.Background(), bytes.NewReader(raw))

	if err != nil {
		t.Errorf("Verify of a consistent archive: %v", err)
	}
}

func TestVolumeExport_RefusesALinkInsteadOfSnapshottingItsTarget(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink("/etc/hostname", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}

	err := continuity.Volume{Dir: dir}.Export(context.Background(), &bytes.Buffer{})

	if err == nil || !strings.Contains(err.Error(), "link") {
		t.Errorf("Export of a directory holding a link = %v; want an error naming it", err)
	}
}

func TestOpenBao_NamesNoTokenWhenTheServerRefusesIt(t *testing.T) {
	live := newBao(t, "kv/orders="+datum)
	state := continuity.OpenBao{Live: continuity.Endpoint{Addr: live.srv.URL, Token: "bao-token-WRONG-9d2"}}

	err := state.Export(context.Background(), &bytes.Buffer{})

	if err == nil || !strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), "WRONG-9d2") {
		t.Errorf("Export with a refused token = %v; want an error naming the 403 and not the token", err)
	}
}

func TestOpenBaoImport_RefusesToRestoreWithoutARestartOfEveryMember(t *testing.T) {
	live := newBao(t, "kv/orders=old")
	state := continuity.OpenBao{Live: continuity.Endpoint{Addr: live.srv.URL, Token: "bao-token-3c1"}}

	err := state.Import(context.Background(), strings.NewReader("kv/orders=new"))

	if err == nil || live.snapshot() != "kv/orders=old" {
		t.Errorf("Import with no Restart = %v, live data %q; want an error and the live data untouched", err, live.snapshot())
	}
}

func TestOpenBaoVerify_FailsWhenTheScratchServerReportsItselfUninitialized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = w.Write([]byte(`{"initialized":false}`))
	}))
	t.Cleanup(srv.Close)
	state := continuity.OpenBao{Scratch: continuity.Endpoint{Addr: srv.URL, Token: "bao-token-3c1"}}

	err := state.Verify(context.Background(), strings.NewReader("kv/orders="+datum))

	if err == nil || !strings.Contains(err.Error(), "uninitialized") {
		t.Errorf("Verify against an uninitialized scratch server = %v; want an error saying so", err)
	}
}

func TestCloudNativePGVerify_DiscardsTheScratchClusterWhateverTheCheckFound(t *testing.T) {
	cases := []struct {
		label string
		live  []string
		check func(context.Context, continuity.Querier) error
		fails bool
	}{
		{"a check that passes", []string{datum}, hasDatum, false},
		{"a check that finds the row lost", []string{"first-row"}, hasDatum, true},
		{"no check, which runs a plain query", []string{"first-row"}, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			cluster := &fakeCluster{live: tc.live}
			hook, _ := newHook(t, continuity.CloudNativePG{Cluster: cluster, Check: tc.check})

			_, err := hook.Snapshot(context.Background(), "app-data")

			if (err != nil) != tc.fails || cluster.scratch == nil || !cluster.scratch.discarded {
				t.Errorf("Snapshot with %s = %v, scratch discarded = %v; want failure %v and the scratch cluster discarded", tc.label, err, cluster.scratch != nil && cluster.scratch.discarded, tc.fails)
			}
		})
	}
}

func TestCloudNativePGVerify_NamesTheRecoveryThatFailed(t *testing.T) {
	cluster := &fakeCluster{}
	hook, _ := newHook(t, continuity.CloudNativePG{Cluster: cluster})

	handover, err := hook.Snapshot(context.Background(), "app-data")

	if handover != "" || err == nil || !strings.Contains(err.Error(), "recovering the scratch cluster") {
		t.Errorf("Snapshot of an empty backup = %q, %v; want no handover and an error naming the recovery", handover, err)
	}
}
