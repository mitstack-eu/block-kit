package gitops_test

// resource_stale_paths_test.go pins what an ensure does with a path the
// recorded identity names but the new render no longer has, as when an
// upgrade drops or renames a file:
//
//   - the ensure removes that path in the same commit that writes the new
//     files, and reports only the new paths;
//   - a delete after such an upgrade, given the upgrade's identity, leaves
//     no file of either version at head, and observe is absent only then;
//   - a recorded path outside the directories the render writes, such as
//     another app's, is never removed.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
	"github.com/mitstack-eu/operator-kit/gitops"
)

// renderConfigured renders the app's namespace files beside one ConfigMap
// whose file name the recorded config input names, so a later version of
// the block can rename that file.
func renderConfigured(d contract.Desired) (gitops.Rendered, error) {
	out, err := renderNamespace(d)
	if err != nil {
		return out, err
	}
	raw, ok := d.ResourceInputs["config"]
	if !ok {
		return gitops.Rendered{}, errors.New("no config input recorded")
	}
	var name string
	if err := json.Unmarshal(raw, &name); err != nil {
		return gitops.Rendered{}, fmt.Errorf("config input: %w", err)
	}
	out.Files["apps/"+d.AppID+"/"+name] = []byte(fmt.Sprintf(
		"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: %s-config\n  namespace: %s\n", d.AppID, d.AppID))
	return out, nil
}

// configuredFor is desiredFor with the config input naming file.
func configuredFor(app, file string) contract.Desired {
	d := desiredFor(app)
	d.ResourceInputs["config"] = json.RawMessage(`"` + file + `"`)
	return d
}

// joinedPaths is the identity's paths value for files: the sorted paths
// joined by commas.
func joinedPaths(files map[string][]byte) string {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return strings.Join(paths, ",")
}

func mustRender(t *testing.T, d contract.Desired) gitops.Rendered {
	t.Helper()
	out, err := renderConfigured(d)
	if err != nil {
		t.Fatalf("rendering: %v", err)
	}
	return out
}

const (
	alphaOldConfigPath = "apps/app-alpha/old.yaml"
	alphaNewConfigPath = "apps/app-alpha/new.yaml"
)

func TestResource_AnEnsureRemovesARecordedPathItsRenderNoLongerHasInTheSameCommit(t *testing.T) {
	b := newBlock(renderConfigured, "app-alpha")
	v1 := ensure(t, b, configuredFor("app-alpha", "old.yaml"))

	upgrade := configuredFor("app-alpha", "new.yaml")
	upgrade.ResourceIdentity = v1.Identity
	v2 := ensure(t, b, upgrade)

	commits := b.repo.Commits()
	if len(commits) != 2 {
		t.Fatalf("Commits = %d, want 2: the upgrade lands one commit", len(commits))
	}
	rendered := mustRender(t, upgrade)
	wantChanges := map[string][]byte{
		alphaOldConfigPath: nil,
		alphaNewConfigPath: rendered.Files[alphaNewConfigPath],
	}
	if !reflect.DeepEqual(commits[1].Changes, wantChanges) {
		t.Errorf("the upgrade commit changes %q, want it to remove %s and write %s in one commit",
			describeChanges(commits[1].Changes), alphaOldConfigPath, alphaNewConfigPath)
	}
	wantFilesAt(t, b.repo, b.head(t), map[string][]byte{
		alphaOldConfigPath: nil,
		alphaNewConfigPath: rendered.Files[alphaNewConfigPath],
	})

	wantIdentity := map[string]string{
		"app":      "app-alpha",
		"revision": commits[1].Revision,
		"paths":    joinedPaths(rendered.Files),
	}
	if !reflect.DeepEqual(v2.Identity, wantIdentity) {
		t.Errorf("the upgrade reports identity %v, want %v naming only the new paths", v2.Identity, wantIdentity)
	}
}

func TestResource_DeleteAfterAnUpgradeThatRenamedAFileLeavesNeitherVersionsFileAtHead(t *testing.T) {
	b := newBlock(renderConfigured, "app-alpha")
	v1Desired := configuredFor("app-alpha", "old.yaml")
	v1 := ensure(t, b, v1Desired)
	v2Desired := configuredFor("app-alpha", "new.yaml")
	v2Desired.ResourceIdentity = v1.Identity
	v2 := ensure(t, b, v2Desired)

	if obs := observe(t, b, v2Desired); obs.State != contract.Present {
		t.Fatalf("observe before the delete = %+v, want present", obs)
	}

	gone := configuredFor("app-alpha", "new.yaml")
	gone.ResourceIdentity = v2.Identity
	if err := b.resource.DeleteWithRequest(context.Background(), gone); err != nil {
		t.Fatalf("DeleteWithRequest: %v", err)
	}

	wantFilesAt(t, b.repo, b.head(t), map[string][]byte{
		alphaNamespacePath:      nil,
		alphaServiceAccountPath: nil,
		alphaOldConfigPath:      nil,
		alphaNewConfigPath:      nil,
	})
	for name, d := range map[string]contract.Desired{"version 1": v1Desired, "version 2": gone} {
		if obs := observe(t, b, d); obs.State != contract.Absent {
			t.Errorf("observe after the delete, rendering %s = %+v, want absent: no file of either version stands", name, obs)
		}
	}
}

func TestResource_AnEnsureNeverRemovesARecordedPathOutsideTheDirectoriesItsRenderWrites(t *testing.T) {
	betaFiles := renderApp("app-beta", "k3s-fsn1").Files
	betaPaths := joinedPaths(betaFiles)

	for name, record := range map[string]func(alpha, beta contract.Ensured) map[string]string{
		"app-alpha's record also naming app-beta's paths": func(alpha, _ contract.Ensured) map[string]string {
			return map[string]string{"app": "app-alpha", "revision": alpha.Identity["revision"], "paths": alphaPaths + "," + betaPaths}
		},
		"app-beta's own record": func(_, beta contract.Ensured) map[string]string {
			return beta.Identity
		},
	} {
		t.Run(name, func(t *testing.T) {
			b := newNamespaceBlock()
			alpha := ensure(t, b, desiredFor("app-alpha"))
			beta := ensure(t, b, desiredFor("app-beta"))

			d := desiredFor("app-alpha")
			d.ResourceIdentity = record(alpha, beta)
			ensure(t, b, d)

			for _, c := range b.repo.Commits()[2:] {
				for path := range c.Changes {
					if strings.HasPrefix(path, "apps/app-beta/") {
						t.Errorf("app-alpha's ensure commit %q touches app-beta's %s", c.Message, path)
					}
				}
			}
			wantFilesAt(t, b.repo, b.head(t), betaFiles)
			if obs := observe(t, b, desiredFor("app-beta")); obs.State != contract.Present {
				t.Errorf("app-beta observed %+v after app-alpha's ensure, want present", obs)
			}
		})
	}
}

// describeChanges names each changed path as written or removed.
func describeChanges(changes map[string][]byte) []string {
	var out []string
	for path, content := range changes {
		if content == nil {
			out = append(out, "remove "+path)
		} else {
			out = append(out, "write "+path)
		}
	}
	sort.Strings(out)
	return out
}
