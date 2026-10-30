package kittest

import (
	"fmt"
	"maps"
	"slices"

	"github.com/mitstack-eu/block-kit/contract"
)

// ReadCounter is a Provider that records every provider read the block's code makes, so the check can hold a probe to its type's maxReads.
type ReadCounter interface {
	Reads() int
}

func entryTypes(entry contract.ManifestResource) []string {
	if len(entry.Types) == 0 {
		return []string{entry.Type}
	}
	return entry.Types
}

// unservedKitTypes refuses each declared type the kit observer reads but cannot serve, such as one naming no usable read, before handler.New refuses the block for it.
func unservedKitTypes(in Block, spec contract.ManifestSpec) []string {
	var problems []string
	for _, entry := range spec.Resources {
		for _, typ := range entryTypes(entry) {
			if t, ok := in.Config.Types.Lookup(typ); ok && t.KitObserved() {
				if err := in.Config.KitObserver.Serves(t); err != nil {
					problems = append(problems, fmt.Sprintf("resource %q: the kit observer reads its type %s but cannot serve it: %v", entry.Name, typ, err))
				}
			}
		}
	}
	return problems
}

// ownObservers refuses a block's own observer for a resource or a type the kit observer reads, since the kit observer would never call it.
func ownObservers(in Block, spec contract.ManifestSpec, ran map[string]bool) []string {
	var problems []string
	for _, entry := range spec.Resources {
		types := entryTypes(entry)
		kitObserved := len(types) > 0
		for _, typ := range types {
			t, ok := in.Config.Types.Lookup(typ)
			kitObserved = kitObserved && ok && t.KitObserved()
		}
		if !kitObserved {
			continue
		}
		ran[guardKitObserver] = true
		if in.Config.Observers[entry.Name] != nil {
			problems = append(problems, fmt.Sprintf("resource %q: its type %s carries an observe spec, so the kit observer reads it, but Config.Observers registers the block's own observer for it; remove that observer", entry.Name, types[0]))
		}
	}
	for _, typ := range slices.Sorted(maps.Keys(in.Config.TypeObservers)) {
		if t, ok := in.Config.Types.Lookup(typ); ok && t.KitObserved() {
			ran[guardKitObserver] = true
			problems = append(problems, fmt.Sprintf("type %s carries an observe spec, so the kit observer reads it, but Config.TypeObservers registers the block's own observer for it; remove that observer", typ))
		}
	}
	return problems
}

// probeReads validates each kit-observed resource twice, since the first may be a full read, and counts the provider reads of the second, a probe, against its type's maxReads.
func (c *blockCheck) probeReads() {
	for _, entry := range c.spec.Resources {
		rec := c.desired.RecordedResources[entry.Name]
		t, ok := c.in.Config.Types.Lookup(rec.Type)
		if !ok || !t.KitObserved() {
			continue
		}
		maxReads := t.ObserveSpec.Probe.MaxReads
		counter, ok := c.in.Provider.(ReadCounter)
		if !ok {
			c.resourceProblem(entry.Name, "its type %s carries an observe spec, but Provider is no kittest.ReadCounter, so whether its probe stays within maxReads %d is unproven", rec.Type, maxReads)
			continue
		}
		c.ran[guardProbeReads] = true
		d := c.desired
		d.Resource, d.ResourceType, d.ResourceIdentity = entry.Name, rec.Type, rec.Identity
		if _, _, ok := c.sendOK("validate", d); !ok {
			continue
		}
		before := counter.Reads()
		if _, _, ok := c.sendOK("validate", d); !ok {
			continue
		}
		if reads := counter.Reads() - before; reads > maxReads {
			c.resourceProblem(entry.Name, "a probe of its type %s made %d provider reads, above the type's maxReads of %d", rec.Type, reads, maxReads)
		}
	}
}
