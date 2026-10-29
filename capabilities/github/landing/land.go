package landing

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/mitstack-eu/block-kit/capabilities/github/ghapi"
)

// Landing is one commit of files onto a repository's main.
type Landing struct {
	Repository string
	Message    string
	Files      map[string]string
	// Prior is the record an earlier landing of the same resource returned, so a path it created stays recorded created.
	Prior Record
}

// Land commits l.Files onto main in one commit and records each one, a path main already held recorded not created unless l.Prior created it.
func Land(ctx context.Context, c *ghapi.Client, l Landing) (Record, error) {
	head, err := c.HeadCommit(ctx, l.Repository)
	if err != nil {
		return Record{}, fmt.Errorf("landing: read main of %s: %w", l.Repository, err)
	}
	rec := Record{Repository: l.Repository}
	changes := make(map[string]*string, len(l.Files))
	for _, path := range slices.Sorted(maps.Keys(l.Files)) {
		content := l.Files[path]
		held, err := heldAt(ctx, c, l.Repository, head, path)
		if err != nil {
			return Record{}, err
		}
		rec.Files = append(rec.Files, File{Path: path, Blob: BlobHash([]byte(content)), Created: !held || l.Prior.created(l.Repository, path)})
		changes[path] = &content
	}
	if _, err := c.CommitChanges(ctx, l.Repository, head, l.Message, changes); err != nil {
		return Record{}, fmt.Errorf("landing: commit %d files onto main of %s: %w", len(changes), l.Repository, err)
	}
	return rec, nil
}

func heldAt(ctx context.Context, c *ghapi.Client, repo, head, path string) (bool, error) {
	if head == "" {
		return false, nil
	}
	_, held, err := c.ReadFileAt(ctx, repo, head, path)
	if err != nil {
		return false, fmt.Errorf("landing: read %s on main of %s: %w", path, repo, err)
	}
	return held, nil
}
