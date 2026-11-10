package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/mitstack-eu/block-kit/contract"
)

// servedSnapshotRoute answers the platform's newest type snapshot, since a block's copy does not say which snapshot it was taken from.
const servedSnapshotRoute = "/v1/resource-types/snapshots/latest"

// servedSnapshotLimit bounds the read of a snapshot far above its size, so a route that never stops answering cannot fill the runner's memory.
const servedSnapshotLimit = 16 << 20

func servedSnapshot(route string) (contract.TypeSnapshot, error) {
	unread := func(answer string) error {
		return fmt.Errorf("GET %s %s, so %s could not be compared with the snapshot the platform serves", route, answer, typeSnapshotFile)
	}
	client := http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(route)
	if err != nil {
		return contract.TypeSnapshot{}, unread(fmt.Sprintf("did not answer (%v)", err))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, servedSnapshotLimit))
	switch {
	case err != nil:
		return contract.TypeSnapshot{}, unread(fmt.Sprintf("answered HTTP %d with a body that could not be read (%v)", resp.StatusCode, err))
	case resp.StatusCode != http.StatusOK:
		return contract.TypeSnapshot{}, unread(fmt.Sprintf("answered HTTP %d: %.200s", resp.StatusCode, strings.TrimSpace(string(body))))
	}
	snap, err := contract.ParseTypeSnapshot(body)
	if err != nil {
		return contract.TypeSnapshot{}, unread(fmt.Sprintf("answered a snapshot the kit cannot read (%v)", err))
	}
	return snap, nil
}

// snapshotDiffs names each field and each output, of each type spec requires or declares, that differs between the block's copy and the served snapshot.
func snapshotDiffs(spec contract.ManifestSpec, copied, served contract.TypeSnapshot) []string {
	var ids, diffs []string
	for _, q := range spec.Requires {
		ids = append(ids, q.Types...)
	}
	for _, r := range spec.Resources {
		ids = append(ids, r.Types...)
	}
	slices.Sort(ids)
	for _, id := range slices.Compact(ids) {
		mine, _ := copied.Lookup(id)
		theirs, ok := served.Lookup(id)
		if !ok {
			diffs = append(diffs, fmt.Sprintf("type %s is not in the served snapshot", id))
			continue
		}
		diffs = append(diffs, fieldDiffs(id, mine, theirs)...)
		unmatched := map[string]contract.SnapshotOutput{}
		for _, o := range theirs.Outputs {
			unmatched[o.Name] = o
		}
		for _, o := range mine.Outputs {
			s, ok := unmatched[o.Name]
			delete(unmatched, o.Name)
			switch {
			case !ok:
				diffs = append(diffs, fmt.Sprintf("type %s output %s is in %s but not served", id, o.Name, typeSnapshotFile))
			case o != s:
				diffs = append(diffs, fmt.Sprintf("type %s output %s is %s in %s and %s as served", id, o.Name, outputForm(o), typeSnapshotFile, outputForm(s)))
			}
		}
		for _, o := range theirs.Outputs {
			if _, missing := unmatched[o.Name]; missing {
				diffs = append(diffs, fmt.Sprintf("type %s output %s is served but not in %s", id, o.Name, typeSnapshotFile))
			}
		}
	}
	return diffs
}

// fieldDiffs leaves out the type's own version, which no check of the kit reads, and the outputs, which snapshotDiffs compares one by one.
func fieldDiffs(id string, mine, theirs contract.SnapshotType) []string {
	var diffs []string
	a, b := reflect.ValueOf(mine), reflect.ValueOf(theirs)
	for i := range a.NumField() {
		name, _, _ := strings.Cut(a.Type().Field(i).Tag.Get("json"), ",")
		if name == "version" || name == "outputs" {
			continue
		}
		if x, y := fieldValue(a.Field(i)), fieldValue(b.Field(i)); x != y {
			diffs = append(diffs, fmt.Sprintf("type %s field %s is %.200s in %s and %.200s as served", id, name, x, typeSnapshotFile, y))
		}
	}
	return diffs
}

// fieldValue writes v as JSON with every null, empty list and empty object dropped, so a list the copy writes as [] and the platform leaves out reads the same.
func fieldValue(v reflect.Value) string {
	// Marshal cannot fail on a field of a snapshot Unmarshal built, and the reread cannot fail on what Marshal wrote.
	raw, _ := json.Marshal(v.Interface())
	var doc any
	_ = json.Unmarshal(raw, &doc)
	raw, _ = json.Marshal(pruned(doc))
	if string(raw) == "null" {
		return "absent"
	}
	return string(raw)
}

func pruned(doc any) any {
	switch d := doc.(type) {
	case map[string]any:
		for k, v := range d {
			if d[k] = pruned(v); d[k] == nil {
				delete(d, k)
			}
		}
		if len(d) == 0 {
			return nil
		}
	case []any:
		for i, v := range d {
			d[i] = pruned(v)
		}
		if len(d) == 0 {
			return nil
		}
	}
	return doc
}

func outputForm(o contract.SnapshotOutput) string {
	return fmt.Sprintf("secret %t, required %t, schema %s", o.Secret, o.Required, cmp.Or(string(o.Schema), "none"))
}
