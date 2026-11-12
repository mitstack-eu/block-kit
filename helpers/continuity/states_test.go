// The volume and OpenBao states refuse what a scratch restore must not accept and leak no token.
package continuity_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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

func archive(t *testing.T, digests map[string]string, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	type file struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	}
	var files []file
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Typeflag: e.typeflag, Mode: 0o644, Size: int64(len(e.body)), Uid: os.Getuid(), Gid: os.Getgid()}
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
			files = append(files, file{e.name, sum})
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

func TestVolumeExport_RefusesADirThatIsALinkOrAFile(t *testing.T) {
	volume := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(volume, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(volume, "orders.txt")
	if err := os.WriteFile(file, []byte(datum), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(volume, link); err != nil {
		t.Fatal(err)
	}
	for label, dir := range map[string]string{"a link to the volume": link, "a regular file": file} {
		t.Run(label, func(t *testing.T) {
			var out bytes.Buffer

			err := continuity.Volume{Dir: dir}.Export(context.Background(), &out)

			if err == nil || !strings.Contains(err.Error(), "not a directory") {
				t.Errorf("Export of %s returned %v and wrote %d bytes; want an error saying it is not a directory", label, err, out.Len())
			}
		})
	}
}

// keptBits are the mode bits a restore must bring back.
const keptBits = fs.ModePerm | fs.ModeSticky | fs.ModeSetuid | fs.ModeSetgid

func ownerOf(t *testing.T, path string) (uid, gid int) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	return int(stat.Uid), int(stat.Gid)
}

func TestVolumeRestore_KeepsEachEntrysModeAndOwner(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	modes := map[string]fs.FileMode{"shared": fs.ModeSticky | 0o777, "shared/notes.txt": 0o664, "secret.key": 0o400}
	if err := os.MkdirAll(filepath.Join(dir, "shared"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"shared/notes.txt", "secret.key"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(datum), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, mode := range modes {
		if err := os.Chmod(filepath.Join(dir, name), mode); err != nil {
			t.Fatal(err)
		}
	}
	uid, gid := ownerOf(t, filepath.Join(dir, "secret.key"))
	var archive bytes.Buffer
	if err := (continuity.Volume{Dir: dir}).Export(context.Background(), &archive); err != nil {
		t.Fatalf("Export: %v", err)
	}
	for tr := tar.NewReader(bytes.NewReader(archive.Bytes())); ; {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Name != ".continuity-manifest.json" && (hdr.Uid != uid || hdr.Gid != gid) {
			t.Errorf("the archive records %s as owned by %d:%d; want %d:%d, the owner on disk", hdr.Name, hdr.Uid, hdr.Gid, uid, gid)
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	err := continuity.Volume{Dir: dir}.Import(context.Background(), &archive)

	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	for name, want := range modes {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode() & keptBits; got != want {
			t.Errorf("%s came back with mode %v; want %v", name, got, want)
		}
		if gotUID, gotGID := ownerOf(t, filepath.Join(dir, name)); gotUID != uid || gotGID != gid {
			t.Errorf("%s came back owned by %d:%d; want %d:%d", name, gotUID, gotGID, uid, gid)
		}
	}
}
