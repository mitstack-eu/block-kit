package continuity

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	volumeManifestName = ".continuity-manifest.json"
	maxManifestBytes   = 64 << 20
	dirMode            = 0o755
	ownerRWX           = 0o700
	ownerRW            = 0o600
)

// keptModeBits are the mode bits a snapshot records and a restore gives back.
const keptModeBits = fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky

type volumeFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type volumeManifest struct {
	Files []volumeFile `json:"files"`
}

// Volume snapshots the directories and regular files under Dir, each with its mode and owner, as a tar that ends with the digest of every file, and refuses any other kind of entry.
type Volume struct {
	Dir string
}

func (v Volume) Export(ctx context.Context, w io.Writer) error {
	if err := requireDirectory(v.Dir); err != nil {
		return err
	}
	tw := tar.NewWriter(w)
	var manifest volumeManifest
	err := filepath.WalkDir(v.Dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(v.Dir, path)
		if err != nil || rel == "." {
			return err
		}
		file, err := writeEntry(tw, path, filepath.ToSlash(rel), d)
		if file != nil {
			manifest.Files = append(manifest.Files, *file)
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("reading the volume: %w", err)
	}
	if err := writeManifest(tw, manifest); err != nil {
		return err
	}
	return tw.Close()
}

// requireDirectory exists because walking a link or a file visits nothing, and the empty archive that follows would pass every check.
func requireDirectory(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("reading the volume: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("the volume %s is not a directory itself, so name the directory and never a link to it or a file", dir)
	}
	return nil
}

// entryHeader records an entry's mode and owner, and leaves out the names and times a restore does not give back.
func entryHeader(info fs.FileInfo, name string) (*tar.Header, error) {
	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return nil, err
	}
	hdr.Name, hdr.Uname, hdr.Gname = name, "", ""
	hdr.AccessTime, hdr.ChangeTime = time.Time{}, time.Time{}
	return hdr, nil
}

func writeEntry(tw *tar.Writer, path, name string, d fs.DirEntry) (*volumeFile, error) {
	if name == volumeManifestName {
		return nil, fmt.Errorf("%s is the name the snapshot keeps for its digest list", name)
	}
	info, err := d.Info()
	if err != nil {
		return nil, err
	}
	switch {
	case d.IsDir():
		hdr, err := entryHeader(info, name+"/")
		if err != nil {
			return nil, err
		}
		return nil, tw.WriteHeader(hdr)
	case info.Mode().IsRegular():
		return writeFile(tw, path, name, info)
	}
	return nil, fmt.Errorf("%s is a %s, and a volume snapshot carries only directories and regular files", name, info.Mode().Type())
}

func writeFile(tw *tar.Writer, path, name string, info fs.FileInfo) (*volumeFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	hdr, err := entryHeader(info, name)
	if err != nil {
		return nil, err
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return nil, err
	}
	digest := sha256.New()
	if _, err := io.CopyN(tw, io.TeeReader(f, digest), info.Size()); err != nil {
		return nil, fmt.Errorf("%s changed while it was read: %w", name, err)
	}
	return &volumeFile{Path: name, SHA256: hex.EncodeToString(digest.Sum(nil))}, nil
}

func writeManifest(tw *tar.Writer, manifest volumeManifest) error {
	raw, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: volumeManifestName, Mode: ownerRW, Size: int64(len(raw))}); err != nil {
		return err
	}
	_, err = tw.Write(raw)
	return err
}

func (v Volume) Import(ctx context.Context, r io.Reader) error {
	if err := os.MkdirAll(v.Dir, dirMode); err != nil {
		return err
	}
	return extract(ctx, r, v.Dir)
}

func (v Volume) Verify(ctx context.Context, r io.Reader) (err error) {
	scratch, err := os.MkdirTemp("", "continuity-scratch-*")
	if err != nil {
		return err
	}
	defer func() {
		if removeErr := removeScratch(scratch); removeErr != nil {
			err = errors.Join(err, fmt.Errorf("removing the scratch copy: %w", removeErr))
		}
	}()
	return extract(ctx, r, scratch)
}

// removeScratch makes each directory writable first, since one restored read-only refuses the removal of what it holds.
func removeScratch(dir string) error {
	walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		return os.Chmod(path, ownerRWX)
	})
	return errors.Join(walkErr, os.RemoveAll(dir))
}

// restoredEntry is the mode and owner a restore gives one entry back.
type restoredEntry struct {
	name     string
	mode     fs.FileMode
	uid, gid int
}

// extract writes the archive under dst through an os.Root, which refuses a name or a link that leaves dst, rereads every file from disk against the archive's digest list, and only then gives each entry its mode and owner back.
func extract(ctx context.Context, r io.Reader, dst string) error {
	root, err := os.OpenRoot(dst)
	if err != nil {
		return err
	}
	defer root.Close()
	tr := tar.NewReader(r)
	var manifest *volumeManifest
	var entries []restoredEntry
	written := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("reading the archive: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if hdr.Name == volumeManifestName {
			if manifest, err = readManifest(tr); err != nil {
				return err
			}
			continue
		}
		entry, err := extractEntry(root, tr, hdr)
		if err != nil {
			return fmt.Errorf("extracting %q: %w", hdr.Name, err)
		}
		entries = append(entries, entry)
		if hdr.Typeflag == tar.TypeReg {
			written[hdr.Name] = true
		}
	}
	if manifest == nil {
		return errors.New("the archive ends with no digest list")
	}
	if err := checkFiles(root, *manifest, written); err != nil {
		return err
	}
	return restoreModesAndOwners(root, entries)
}

// restoreModesAndOwners runs from the last entry back, since a directory given a read-only mode back refuses a change to what it holds.
func restoreModesAndOwners(root *os.Root, entries []restoredEntry) error {
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if err := root.Chown(e.name, e.uid, e.gid); err != nil {
			return fmt.Errorf("giving %s back to its owner %d:%d: %w", e.name, e.uid, e.gid, err)
		}
		if err := root.Chmod(e.name, e.mode); err != nil {
			return fmt.Errorf("giving %s its mode %v back: %w", e.name, e.mode, err)
		}
	}
	return nil
}

func readManifest(r io.Reader) (*volumeManifest, error) {
	var m volumeManifest
	if err := json.NewDecoder(io.LimitReader(r, maxManifestBytes)).Decode(&m); err != nil {
		return nil, fmt.Errorf("reading the digest list: %w", err)
	}
	return &m, nil
}

func extractEntry(root *os.Root, tr *tar.Reader, hdr *tar.Header) (restoredEntry, error) {
	entry := restoredEntry{name: strings.TrimSuffix(hdr.Name, "/"), mode: hdr.FileInfo().Mode() & keptModeBits, uid: hdr.Uid, gid: hdr.Gid}
	switch hdr.Typeflag {
	case tar.TypeDir:
		return entry, makeOwnerWritableDir(root, entry.name)
	case tar.TypeReg:
		return entry, writeExtracted(root, tr, entry.name)
	}
	return entry, fmt.Errorf("an entry of type %q, which a volume snapshot does not carry", hdr.Typeflag)
}

// makeOwnerWritableDir keeps a directory writable until every entry is in place, as one an earlier restore left read-only would refuse its own files.
func makeOwnerWritableDir(root *os.Root, name string) error {
	if err := root.MkdirAll(name, ownerRWX); err != nil {
		return err
	}
	return root.Chmod(name, ownerRWX)
}

// writeExtracted opens a file an earlier restore left read-only by making it writable first.
func writeExtracted(root *os.Root, r io.Reader, name string) error {
	if dir := filepath.Dir(name); dir != "." {
		if err := root.MkdirAll(dir, dirMode); err != nil {
			return err
		}
	}
	if err := root.Chmod(name, ownerRW); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, ownerRW)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func checkFiles(root *os.Root, manifest volumeManifest, written map[string]bool) error {
	if len(manifest.Files) != len(written) {
		return fmt.Errorf("the digest list names %d files and the archive holds %d", len(manifest.Files), len(written))
	}
	for _, want := range manifest.Files {
		if !written[want.Path] {
			return fmt.Errorf("the digest list names %s, which the archive does not hold", want.Path)
		}
		got, err := hashFile(root, want.Path)
		if err != nil {
			return err
		}
		if got != want.SHA256 {
			return fmt.Errorf("%s read back from the scratch copy with a different digest", want.Path)
		}
	}
	return nil
}

func hashFile(root *os.Root, name string) (string, error) {
	f, err := root.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
