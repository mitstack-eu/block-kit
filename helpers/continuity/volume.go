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
)

const (
	volumeManifestName = ".continuity-manifest.json"
	maxManifestBytes   = 64 << 20
	dirMode            = 0o755
	ownerRWX           = 0o700
	ownerRW            = 0o600
)

type volumeFile struct {
	Path   string      `json:"path"`
	Mode   fs.FileMode `json:"mode"`
	SHA256 string      `json:"sha256"`
}

type volumeManifest struct {
	Files []volumeFile `json:"files"`
}

// Volume snapshots the directories and regular files under Dir as a tar that ends with the digest of every file, and refuses any other kind of entry.
type Volume struct {
	Dir string
}

func (v Volume) Export(ctx context.Context, w io.Writer) error {
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
		return nil, tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: name + "/", Mode: int64(info.Mode().Perm())})
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
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: int64(info.Mode().Perm()), Size: info.Size()}); err != nil {
		return nil, err
	}
	digest := sha256.New()
	if _, err := io.CopyN(tw, io.TeeReader(f, digest), info.Size()); err != nil {
		return nil, fmt.Errorf("%s changed while it was read: %w", name, err)
	}
	return &volumeFile{Path: name, Mode: info.Mode().Perm(), SHA256: hex.EncodeToString(digest.Sum(nil))}, nil
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

func (v Volume) Verify(ctx context.Context, r io.Reader) error {
	scratch, err := os.MkdirTemp("", "continuity-scratch-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	return extract(ctx, r, scratch)
}

// extract writes the archive under dst through an os.Root, which refuses a name or a link that leaves dst, then rereads every file from disk against the archive's digest list.
func extract(ctx context.Context, r io.Reader, dst string) error {
	root, err := os.OpenRoot(dst)
	if err != nil {
		return err
	}
	defer root.Close()
	tr := tar.NewReader(r)
	var manifest *volumeManifest
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
		if err := extractEntry(root, tr, hdr); err != nil {
			return fmt.Errorf("extracting %q: %w", hdr.Name, err)
		}
		if hdr.Typeflag == tar.TypeReg {
			written[hdr.Name] = true
		}
	}
	if manifest == nil {
		return errors.New("the archive ends with no digest list")
	}
	return checkFiles(root, *manifest, written)
}

func readManifest(r io.Reader) (*volumeManifest, error) {
	var m volumeManifest
	if err := json.NewDecoder(io.LimitReader(r, maxManifestBytes)).Decode(&m); err != nil {
		return nil, fmt.Errorf("reading the digest list: %w", err)
	}
	return &m, nil
}

func extractEntry(root *os.Root, tr *tar.Reader, hdr *tar.Header) error {
	name := strings.TrimSuffix(hdr.Name, "/")
	perm := fs.FileMode(hdr.Mode).Perm()
	switch hdr.Typeflag {
	case tar.TypeDir:
		return root.MkdirAll(name, perm|ownerRWX)
	case tar.TypeReg:
		return writeExtracted(root, tr, name, perm)
	}
	return fmt.Errorf("an entry of type %q, which a volume snapshot does not carry", hdr.Typeflag)
}

func writeExtracted(root *os.Root, r io.Reader, name string, perm fs.FileMode) error {
	if dir := filepath.Dir(name); dir != "." {
		if err := root.MkdirAll(dir, dirMode); err != nil {
			return err
		}
	}
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm|ownerRW)
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
