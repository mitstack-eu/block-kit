package dockerhosttest_test

// A Database served by the fake host lists and loses the apps a subscription holds, leaves nothing after a subscribe
// broken at any write, and fails a test sending another token.

import (
	"context"
	_ "embed"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/helpers/dockerhost"
	"github.com/mitstack-eu/block-kit/helpers/dockerhost/dockerhosttest"
	"github.com/mitstack-eu/block-kit/observe"
	"github.com/mitstack-eu/block-kit/verify/kittest"
)

//go:embed testdata/store-block.yaml
var storeManifest []byte

//go:embed testdata/store-types.json
var storeTypes []byte

func store() dockerhost.Database {
	return dockerhost.Database{Block: "docker-store", Type: "example.store-database", Engine: dockerhost.Engine{
		Service: "store", Image: "example.org/store:2.1@sha256:0f9e", Port: "7000", DataDir: "/data",
		Entrypoint:  []string{"sh", "-c", `exec store --password="$` + dockerhost.PasswordVariable + `"`},
		Healthcheck: []string{"CMD", "store", "ping"},
		Dump:        "store dump", Restore: "store load",
	}}
}

func TestDatabase_ListsAndHoldsTheAppsASubscriptionLandedAndLosesOne(t *testing.T) {
	ctx := context.Background()
	d := dockerhosttest.NewDatabase(t, store())
	if held := d.Holds("sub_db_shop"); len(held) != 0 {
		t.Fatalf("a fresh host holds %v; want nothing, its README aside", held)
	}
	d.Land(t, d.Desired("shop", nil))
	d.Land(t, d.Desired("blog", nil))

	tagged, err := d.ListTagged(ctx, "sub_db_shop")
	if err != nil || !slices.Equal(tagged, []observe.Tagged{{Type: "example.store-database", ID: "shop"}}) {
		t.Errorf("ListTagged(sub_db_shop) = %v, %v; want shop alone", tagged, err)
	}
	if held := d.Holds("sub_db_shop"); !slices.Contains(held, "shop/docker-store.compose.yaml") || slices.Contains(held, "README.md") {
		t.Errorf("Holds = %v; want every file landed and not the host's README", held)
	}
	if err := d.Lose(ctx, contract.CreatedResource{Identity: map[string]string{"app": "shop"}}); err != nil || len(d.Under("shop")) != 0 || len(d.Under("blog")) == 0 {
		t.Errorf("Lose(shop) = %v leaving shop %v and blog %v; want shop's files gone and blog's kept", err, d.Under("shop"), d.Under("blog"))
	}
}

func TestDatabase_ATeardownAfterASubscribeBrokenAtAnyWriteLeavesNothing(t *testing.T) {
	types, err := contract.ParseTypeSnapshot(storeTypes)
	if err != nil {
		t.Fatalf("reading testdata/store-types.json: %v", err)
	}

	kittest.AssertTeardownAfterBrokenSetup(t, kittest.BrokenSetup{
		Manifest: storeManifest, Types: types, Desired: dockerhosttest.NewDatabase(t, store()).Desired("shop", nil),
		NewProvider: func(faults *kittest.Faults) kittest.FaultProvider {
			return dockerhosttest.NewDatabase(t, store()).Breaking(faults)
		},
	})
}

type recordingTB struct {
	testing.TB
	failures []string
	cleanups []func()
}

func (r *recordingTB) Helper()          {}
func (r *recordingTB) Cleanup(f func()) { r.cleanups = append(r.cleanups, f) }
func (r *recordingTB) Errorf(format string, a ...any) {
	r.failures = append(r.failures, fmt.Sprintf(format, a...))
}

func TestNewDatabase_FailsATestWhoseRequestToTheHostCarriesAnotherToken(t *testing.T) {
	rec := &recordingTB{TB: t}
	d := dockerhosttest.NewDatabase(rec, store())
	desired := d.Desired("shop", nil)
	desired.Secrets["github"]["token"] = "ghs_another-token"

	_, _ = d.DB.EnsureResources(context.Background(), desired)
	for i := len(rec.cleanups) - 1; i >= 0; i-- {
		rec.cleanups[i]()
	}

	if !slices.ContainsFunc(rec.failures, func(f string) bool { return strings.Contains(f, "Bearer ghs_another-token") }) {
		t.Errorf("a request carrying another token failed the test with %q; want a failure naming that token", rec.failures)
	}
}
