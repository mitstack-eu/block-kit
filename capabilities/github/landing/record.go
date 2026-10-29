// Package landing, an optional helper, records each file a block lands in an app's repository and, at unsubscribe, removes every one main still carries unchanged through one pull request per repository.
package landing

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/mitstack-eu/block-kit/capabilities/github/ghapi"
)

// File is one path a landing wrote, the git blob hash of what it wrote, and whether main lacked the path before.
type File struct {
	Path    string `json:"path"`
	Blob    string `json:"blob"`
	Created bool   `json:"created"`
}

// Record is every file one landing wrote into one repository, kept in the identity of the resource that landed them.
type Record struct {
	Repository string
	Files      []File
}

const (
	identityRepository = "repository"
	identityFiles      = "files"
)

var blobHash = regexp.MustCompile(`^[0-9a-f]{40}$`)

// LandsFiles reports whether a resource of type typ keeps a Record in its identity and reads absent from main, as the file-landing types of the type catalogue do.
func LandsFiles(typ string) bool {
	switch typ {
	case "github.scaffold-commit", "github.scaffold-contribution", "github.workflow-file":
		return true
	}
	return false
}

// Identity encodes r as a resource identity, its files as one JSON list, since an identity holds strings alone.
func (r Record) Identity() (map[string]string, error) {
	files, err := json.Marshal(r.Files)
	if err != nil {
		return nil, fmt.Errorf("landing: encode the files landed in %s: %w", r.Repository, err)
	}
	return map[string]string{identityRepository: r.Repository, identityFiles: string(files)}, nil
}

func RecordFromIdentity(identity map[string]string) (Record, error) {
	repo, err := ghapi.SafeRepo(identity[identityRepository])
	if err != nil {
		return Record{}, fmt.Errorf("landing: identity names no repository: %w", err)
	}
	var files []File
	if err := json.Unmarshal([]byte(identity[identityFiles]), &files); err != nil {
		return Record{}, fmt.Errorf("landing: identity of %s holds no list of files: %w", repo, err)
	}
	for _, f := range files {
		if f.Path == "" || !blobHash.MatchString(f.Blob) {
			return Record{}, fmt.Errorf("landing: identity of %s records path %q with blob %q, want a path and a 40-character blob hash", repo, f.Path, f.Blob)
		}
	}
	return Record{Repository: repo, Files: files}, nil
}

// BlobHash is the hash git gives content as a blob, which GitHub's contents API reports as a file's sha.
func BlobHash(content []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(content))
	h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}

func (r Record) created(repo, path string) bool {
	if r.Repository != repo {
		return false
	}
	for _, f := range r.Files {
		if f.Path == path {
			return f.Created
		}
	}
	return false
}
