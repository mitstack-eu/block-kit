package gitops_test

// Objects reads the object each manifest document declares, by API group, kind, namespace and name, from JSON as blocks render it and from YAML.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/capabilities/github/gitops"
)

func TestObjects(t *testing.T) {
	namespace := gitops.Object{Kind: "Namespace", Name: "app-alpha"}
	account := gitops.Object{Kind: "ServiceAccount", Namespace: "app-alpha", Name: "app-alpha-runtime"}
	cases := []struct {
		name    string
		content string
		want    []gitops.Object
		wantErr string
	}{
		{"a JSON object as a block renders it, its group read from apiVersion", string(manifest(dbObj)), []gitops.Object{dbObj}, ""},
		{"a JSON array of objects", "[" + string(manifest(dbObj)) + ",\n" + string(manifest(cert)) + "]", []gitops.Object{dbObj, cert}, ""},
		{"YAML documents, a core kind with no namespace read as cluster-scoped", "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: app-alpha\n---\napiVersion: v1\nkind: ServiceAccount\nmetadata:\n  name: app-alpha-runtime\n  namespace: app-alpha\n",
			[]gitops.Object{namespace, account}, ""},
		{"comments, quotes and keys nested under metadata's own", "# the runtime account\n---\napiVersion: \"v1\"\nkind: ServiceAccount # the account\nmetadata:\n  labels:\n    name: not-the-name\n  name: 'app-alpha-runtime'\n  namespace: app-alpha\nspec:\n  name: nor-this\n",
			[]gitops.Object{account}, ""},
		{"a YAML document naming no metadata.name fails naming the document", "apiVersion: v1\nkind: Namespace\n---\nkind: ServiceAccount\n", nil, "YAML document 1"},
		{"a YAML document whose metadata is a flow mapping fails", "apiVersion: v1\nkind: Namespace\nmetadata: {name: app-alpha}\n", nil, "block mapping"},
		{"JSON that does not decode fails", `{"apiVersion": "v1",`, nil, "JSON manifest"},
		{"a JSON object naming no kind fails", `{"apiVersion": "v1", "metadata": {"name": "app-alpha"}}`, nil, "kind"},
		{"a workflow, naming neither apiVersion nor kind, declares no object", "name: scan\non: [push]\njobs:\n  scan:\n    runs-on: ubuntu-latest\n", nil, ""},
		{"a JSON object naming neither apiVersion nor kind declares no object", `{"name": "settings", "metadata": {"name": "app-alpha"}}`, nil, ""},
		{"a manifest beside a document declaring no object", "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: app-alpha\n---\nimages:\n  - nginx\n", []gitops.Object{namespace}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := gitops.Objects([]byte(tc.content))

			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("Objects(%q) = %v, %v; want an error naming %q", tc.content, got, err, tc.wantErr)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Objects(%q) = %v, %v; want %v", tc.content, got, err, tc.want)
			}
		})
	}
}

func TestObject_StringNamesItAsKubectlDoes(t *testing.T) {
	cases := map[gitops.Object]string{
		dbObj:                                  "Cluster.postgresql.cnpg.io app-home-db/postgres",
		{Kind: "Namespace", Name: "app-alpha"}: "Namespace app-alpha",
	}
	for o, want := range cases {
		if got := o.String(); got != want {
			t.Errorf("%#v.String() = %q, want %q", o, got, want)
		}
	}
}
