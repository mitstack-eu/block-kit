package continuity

import (
	"context"
	"errors"
	"fmt"
	"io"
)

const defaultCheckQuery = "SELECT 1"

// Recovery says which cluster CNPGCluster.Recover builds: a throwaway copy for a check, or the live cluster a replacement restores.
type Recovery int

const (
	IntoScratch Recovery = iota
	IntoLive
)

type Querier interface {
	Query(ctx context.Context, sql string) ([]string, error)
}

// Recovered is a cluster CloudNativePG recovered from an archive and reported ready.
type Recovered interface {
	Querier
	Discard(ctx context.Context) error
}

// CNPGCluster is the seam to CloudNativePG: Backup returns the archive once the backup's status reads completed, and Recover returns the cluster once it reads ready.
type CNPGCluster interface {
	Backup(ctx context.Context) (io.ReadCloser, error)
	Recover(ctx context.Context, into Recovery, archive io.Reader) (Recovered, error)
}

// CloudNativePG snapshots through CloudNativePG's backup and checks a snapshot by recovering it into a scratch cluster and running Check, SELECT 1 when Check is nil.
type CloudNativePG struct {
	Cluster CNPGCluster
	Check   func(ctx context.Context, q Querier) error
}

func (c CloudNativePG) Export(ctx context.Context, w io.Writer) error {
	archive, err := c.Cluster.Backup(ctx)
	if err != nil {
		return err
	}
	defer archive.Close()
	_, err = io.Copy(w, archive)
	return err
}

// Verify discards the scratch cluster whatever the check found, on a context that outlives a cancelled one.
func (c CloudNativePG) Verify(ctx context.Context, r io.Reader) (err error) {
	scratch, err := c.Cluster.Recover(ctx, IntoScratch, r)
	if err != nil {
		return fmt.Errorf("recovering the scratch cluster: %w", err)
	}
	defer func() {
		if discardErr := scratch.Discard(context.WithoutCancel(ctx)); discardErr != nil {
			err = errors.Join(err, fmt.Errorf("discarding the scratch cluster: %w", discardErr))
		}
	}()
	check := c.Check
	if check == nil {
		check = func(ctx context.Context, q Querier) error {
			_, err := q.Query(ctx, defaultCheckQuery)
			return err
		}
	}
	return check(ctx, scratch)
}

func (c CloudNativePG) Import(ctx context.Context, r io.Reader) error {
	_, err := c.Cluster.Recover(ctx, IntoLive, r)
	if err != nil {
		return fmt.Errorf("recovering the live cluster: %w", err)
	}
	return nil
}
