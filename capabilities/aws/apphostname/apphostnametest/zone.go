// Package apphostnametest, an optional helper, holds an in-memory apps zone that applies a change batch whole or not at all, as Route 53 does.
package apphostnametest

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/mitstack-eu/block-kit/capabilities/aws/apphostname"
)

// Batch is one change batch the zone applied.
type Batch struct {
	ZoneID  string
	Changes []apphostname.Change
}

// Zone holds each hostname's A record and TXT value.
type Zone struct {
	mu           sync.Mutex
	addresses    map[string][]string
	owners       map[string]string
	batches      []Batch
	beforeChange func(name string) error
}

func New() *Zone {
	return &Zone{addresses: map[string][]string{}, owners: map[string]string{}}
}

// Set gives name the A record addresses and the TXT value owner, a nil or "" leaving it without that record.
func (z *Zone) Set(name string, addresses []string, owner string) {
	z.mu.Lock()
	defer z.mu.Unlock()
	delete(z.addresses, name)
	delete(z.owners, name)
	if len(addresses) > 0 {
		z.addresses[name] = slices.Clone(addresses)
	}
	if owner != "" {
		z.owners[name] = owner
	}
}

// BeforeChange runs fn ahead of every batch, as another writer would between a read and a write; an error from it rejects the batch.
func (z *Zone) BeforeChange(fn func(name string) error) {
	z.mu.Lock()
	defer z.mu.Unlock()
	z.beforeChange = fn
}

func (z *Zone) Addresses(name string) []string {
	z.mu.Lock()
	defer z.mu.Unlock()
	return slices.Clone(z.addresses[name])
}

func (z *Zone) Owner(name string) string {
	z.mu.Lock()
	defer z.mu.Unlock()
	return z.owners[name]
}

func (z *Zone) Batches() []Batch {
	z.mu.Lock()
	defer z.mu.Unlock()
	return slices.Clone(z.batches)
}

// HeldBy names, sorted, each hostname whose TXT names subscription, which is how a block's tests find what a subscription left in the zone.
func (z *Zone) HeldBy(subscription string) []string {
	z.mu.Lock()
	defer z.mu.Unlock()
	var names []string
	for name, owner := range z.owners {
		if owner == apphostname.OwnerText(subscription) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

func (z *Zone) LookupHost(_ context.Context, _, name string) (apphostname.Held, error) {
	z.mu.Lock()
	defer z.mu.Unlock()
	return apphostname.Held{Addresses: slices.Clone(z.addresses[name]), Owner: z.owners[name]}, nil
}

// ChangeRecords rejects the whole batch when a CREATE names a record that exists, or a DELETE's values are not the record's.
func (z *Zone) ChangeRecords(_ context.Context, zoneID string, changes []apphostname.Change) error {
	z.mu.Lock()
	before := z.beforeChange
	z.mu.Unlock()
	if before != nil && len(changes) > 0 {
		if err := before(changes[0].Name); err != nil {
			return err
		}
	}
	z.mu.Lock()
	defer z.mu.Unlock()
	sets := map[string]map[string][]string{apphostname.TypeA: maps.Clone(z.addresses), apphostname.TypeTXT: {}}
	for name, owner := range z.owners {
		sets[apphostname.TypeTXT][name] = []string{owner}
	}
	for _, c := range changes {
		records := sets[c.Type]
		current, exists := records[c.Name]
		switch {
		case c.Action == apphostname.Create && exists:
			return fmt.Errorf("creating %s %s, which exists: %w", c.Type, c.Name, apphostname.ErrConflict)
		case c.Action == apphostname.Delete && (!exists || !slices.Equal(current, c.Values)):
			return fmt.Errorf("deleting %s %s at %q, which holds %q: %w", c.Type, c.Name, c.Values, current, apphostname.ErrConflict)
		case c.Action == apphostname.Create:
			records[c.Name] = slices.Clone(c.Values)
		default:
			delete(records, c.Name)
		}
	}
	z.addresses, z.owners = sets[apphostname.TypeA], map[string]string{}
	for name, values := range sets[apphostname.TypeTXT] {
		z.owners[name] = values[0]
	}
	z.batches = append(z.batches, Batch{ZoneID: zoneID, Changes: slices.Clone(changes)})
	return nil
}
