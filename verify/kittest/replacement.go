package kittest

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/contract"
)

// Replacement is a block served as for AssertBlock, the spec.continuity entry to replace, and the block's own view of the data that entry keeps.
type Replacement struct {
	Block Block
	// Unit names a spec.continuity entry whose handover is snapshot.
	Unit string
	Data DrillData
}

// DrillData is how the drill writes data into a block's live state, destroys that state as a replaced server does, and reads it back.
type DrillData interface {
	Write(ctx context.Context, data map[string]string) error
	Read(ctx context.Context) (map[string]string, error)
	// Replace leaves the live state empty.
	Replace(ctx context.Context) error
}

// drillDatum is the known data the drill writes, named so that a failure points at the datum a block lost.
var drillDatum = map[string]string{"alpha": "drill-value-alpha", "beta": "drill-value-beta", "gamma": "drill-value-gamma"}

// CheckReplacement subscribes the block, writes known data, snapshots it, replaces the server, restores and observes, and returns one problem per datum lost and per resource that does not read present and healthy after the restore.
func CheckReplacement(in Replacement) []string {
	problems, _ := checkReplacement(in)
	return problems
}

// AssertReplacement runs CheckReplacement, logs which stages ran, and fails tb with one error per problem.
func AssertReplacement(tb testing.TB, in Replacement) {
	tb.Helper()
	problems, ran := checkReplacement(in)
	tb.Logf("kittest.AssertReplacement drill ran: %s", strings.Join(ran, ", "))
	for _, p := range problems {
		tb.Error(p)
	}
}

func checkReplacement(in Replacement) ([]string, []string) {
	spec, problems := blockSpec(in.Block)
	if len(problems) > 0 {
		return problems, nil
	}
	if !slices.ContainsFunc(spec.Continuity, func(u contract.ManifestContinuity) bool {
		return u.Name == in.Unit && u.Handover == contract.HandoverSnapshot
	}) {
		return []string{fmt.Sprintf("Unit %q is not a spec.continuity entry with handover %s, so the drill has no state to replace", in.Unit, contract.HandoverSnapshot)}, nil
	}
	if in.Data == nil {
		return []string{"Data is nil, so the drill cannot write, replace or read the block's state"}, nil
	}
	server, err := newBlockServer(in.Block.Config)
	if err != nil {
		return []string{err.Error()}, nil
	}
	c := &blockCheck{in: in.Block, spec: spec, server: server, desired: blockRuns(spec, in.Block)[0].desired, ran: map[string]bool{}}
	d := &drill{blockCheck: c, in: in}
	d.run(context.Background())
	return c.problems, d.stages
}

type drill struct {
	*blockCheck
	in     Replacement
	stages []string
}

func (d *drill) stage(name string) { d.stages = append(d.stages, name) }

func (d *drill) run(ctx context.Context) {
	if _, ok := d.converge("subscribe"); !ok {
		return
	}
	d.stage("subscribe")
	if err := d.in.Data.Write(ctx, maps.Clone(drillDatum)); err != nil {
		d.problem("writing the known data into the block's state failed: %v", err)
		return
	}
	d.stage("write")
	d.desired.Replacing = true
	handover, ok := d.snapshot()
	if !ok {
		return
	}
	if err := d.in.Data.Replace(ctx); err != nil {
		d.problem("replacing the block's state failed: %v", err)
		return
	}
	if !d.emptied(ctx) {
		return
	}
	d.stage("replace")
	if !d.recreate(ctx) || !d.restore(handover) {
		return
	}
	d.afterRestore = true
	if _, ok := d.converge("validate"); ok {
		d.stage("observe")
	}
	d.readBack(ctx)
}

// emptied fails each datum Data.Replace left behind, since a datum that was never lost reads back whatever the restore did.
func (d *drill) emptied(ctx context.Context) bool {
	held, err := d.in.Data.Read(ctx)
	if err != nil {
		d.problem("reading the block's state after Data.Replace failed: %v", err)
		return false
	}
	gone := true
	for _, key := range sortedKeys(drillDatum) {
		if _, left := held[key]; left {
			d.problem("Data.Replace left datum %q in the block's state, so the drill cannot tell a restore from data that was never lost", key)
			gone = false
		}
	}
	return gone
}

// snapshot sends the snapshot of the unit and returns the handover its answer publishes.
func (d *drill) snapshot() (string, bool) {
	status, resp, err := d.server.sendUnit(contract.OperationSnapshot, d.in.Unit, d.desired)
	if !d.answered(contract.OperationSnapshot, status, resp, err) {
		return "", false
	}
	key := contract.HandoverOutputKey(d.in.Unit)
	handover := resp.Outputs[key]
	if handover == "" {
		d.problem("the snapshot of %q answered no handover under the output %q", d.in.Unit, key)
		return "", false
	}
	d.stage("snapshot")
	return handover, true
}

// recreate loses each stateful resource from the provider and upgrades, so the block creates the server again as the engine's reinstall does.
func (d *drill) recreate(ctx context.Context) bool {
	for _, name := range unitNames(d.spec, d.desired.RecordedResources) {
		r := d.desired.RecordedResources[name]
		if t, ok := d.in.Block.Config.Types.Lookup(r.Type); !ok || !t.Stateful || len(r.Identity) == 0 {
			continue
		}
		if err := d.in.Block.Provider.Lose(ctx, contract.CreatedResource{Name: name, Type: r.Type, Identity: r.Identity}); err != nil {
			d.resourceProblem(name, "the provider could not lose it: %v", err)
			return false
		}
	}
	if _, ok := d.converge("upgrade"); !ok {
		return false
	}
	d.stage("upgrade")
	return true
}

func (d *drill) restore(handover string) bool {
	restoring := d.desired
	restoring.PriorOutputs = map[string]string{contract.HandoverOutputKey(d.in.Unit): handover}
	status, resp, err := d.server.sendUnit(contract.OperationRestore, d.in.Unit, restoring)
	if !d.answered(contract.OperationRestore, status, resp, err) {
		return false
	}
	d.stage("restore")
	return true
}

func (d *drill) answered(operation string, status int, resp contract.Response, err error) bool {
	switch {
	case err != nil:
		d.problem("the %s of %q could not be sent or its answer read: %v", operation, d.in.Unit, err)
	case status != 200:
		d.problem("the %s of %q answered %d: %s", operation, d.in.Unit, status, resp.Error)
	default:
		return true
	}
	return false
}

// readBack fails each datum the block no longer holds or holds changed.
func (d *drill) readBack(ctx context.Context) {
	held, err := d.in.Data.Read(ctx)
	if err != nil {
		d.problem("reading the block's state back failed: %v", err)
		return
	}
	d.stage("read-back")
	for _, key := range sortedKeys(drillDatum) {
		got, ok := held[key]
		switch {
		case !ok:
			d.problem("replacement lost datum %q", key)
		case got != drillDatum[key]:
			d.problem("replacement changed datum %q: reads %q, want %q", key, got, drillDatum[key])
		}
	}
}
