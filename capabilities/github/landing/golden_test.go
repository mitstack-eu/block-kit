package landing_test

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mitstack-eu/block-kit/capabilities/github/ghapi"
	"github.com/mitstack-eu/block-kit/capabilities/github/landing"
	"github.com/mitstack-eu/block-kit/verify/kittest"
)

const goldens = "../../../verify/kittest/testdata/github-landing"

var goldenRecord = landing.Record{Repository: repo, Files: []landing.File{
	{Path: "Dockerfile", Blob: "c901703072104919a085c3bba881ebf634c4728c", Created: true},
	{Path: "README.md", Blob: "c3242822052ae35a13313988cc45e585460c95cd", Created: false},
	{Path: "mitstack-app.json", Blob: "645ee200d9fec76b9ee5e4d165f2eadf6b733402", Created: true},
}}

// replay serves the exchange recorded in dir and fails unless the calls ran are exactly its fixtures, in order.
func replay(t *testing.T, dir string, run func(c *ghapi.Client)) {
	t.Helper()
	fixtures := kittest.LoadSaaSFixtures(t, filepath.Join(goldens, dir))
	saas := kittest.NewFakeSaaS(t, fixtures)
	run(ghapi.NewClient(saas.URL, token, nil))

	var got, want []string
	for _, c := range saas.Calls() {
		got = append(got, c.Method+" "+c.Path)
	}
	for _, f := range fixtures {
		want = append(want, f.Request.Method+" "+f.Request.Path)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("calls = %q, want the recorded exchange %q", got, want)
	}
}

func TestGoldens_ALandingReplaysItsGitHubExchange(t *testing.T) {
	replay(t, "land", func(c *ghapi.Client) {
		rec, err := landing.Land(context.Background(), c, landing.Landing{Repository: repo, Message: "mitstack: land the starter", Files: map[string]string{
			"Dockerfile": "FROM nginx:1.27\n", "README.md": "# weather\n", "mitstack-app.json": "{\"name\":\"weather\"}\n",
		}})
		if err != nil || !reflect.DeepEqual(rec, goldenRecord) {
			t.Errorf("Land = %+v, %v, want %+v", rec, err, goldenRecord)
		}
	})
}

func TestGoldens_ARemovalOpensItsPullRequestThenMergesItOnTheRequiredCheck(t *testing.T) {
	replay(t, "remove-open", func(c *ghapi.Client) {
		_, err := remove(c, goldenRecord)
		waiting(t, err, "managed-ci")
	})
	replay(t, "remove-merge", func(c *ghapi.Client) {
		got, err := remove(c, goldenRecord)
		want := []landing.Kept{{Repository: repo, Path: "mitstack-app.json", Reason: "changed"}}
		if err != nil || !reflect.DeepEqual(got.Kept, want) {
			t.Errorf("Remove = %+v, %v, want kept %+v", got, err, want)
		}
	})
}
