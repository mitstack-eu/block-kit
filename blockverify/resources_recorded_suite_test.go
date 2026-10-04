package main

// resources_recorded_suite_test.go pins the resources suite over a block
// whose declared resource can only be observed and deleted with the inputs
// recorded at subscribe and the identity its ensure stored: driven with
// both, through kittest.DeclaredResources' ResourceInputs and
// ResourceIdentity, the suite passes; driven with neither, it fails with a
// problem naming the resource.

import (
	"strings"
	"testing"
)

// recordedResourcesTestGo returns a TestDeclaredResources over network and
// instance, driving instance alone with recorded set on the
// kittest.DeclaredResources. instance's observe and delete fail unless they
// are handed the recorded inputs and stored identity, and its
// identity-only Observe and Delete always fail.
func recordedResourcesTestGo(recorded string) string {
	return `package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
	"github.com/mitstack-eu/operator-kit/handler"
	"github.com/mitstack-eu/operator-kit/kittest"
)

// instance reaches what it created only through the inputs recorded at
// subscribe and the identity its ensure stored.
type instance struct{ *kittest.FakeResource }

func handedRecorded(d contract.Desired) error {
	if string(d.ResourceInputs["network"]) != ` + "`" + `"vpc-0abc"` + "`" + ` {
		return errors.New("handed no recorded inputs")
	}
	if d.ResourceIdentity["instanceId"] != "i-0abc" {
		return errors.New("handed no stored identity")
	}
	return nil
}

func (r *instance) Observe(ctx context.Context, id contract.Identity) (contract.Observation, error) {
	return contract.Observation{}, errors.New("handed the identity alone")
}

func (r *instance) Delete(ctx context.Context, id contract.Identity) error {
	return errors.New("handed the identity alone")
}

func (r *instance) ObserveWithRequest(ctx context.Context, d contract.Desired) (contract.Observation, error) {
	if err := handedRecorded(d); err != nil {
		return contract.Observation{}, err
	}
	return r.FakeResource.Observe(ctx, d.Identity)
}

func (r *instance) DeleteWithRequest(ctx context.Context, d contract.Desired) error {
	if err := handedRecorded(d); err != nil {
		return err
	}
	return r.FakeResource.Delete(ctx, d.Identity)
}

// The fixture sets its recorded inputs as raw JSON only when driven with
// them, so this keeps encoding/json in use either way.
var _ = json.RawMessage(nil)

func TestDeclaredResources(t *testing.T) {
	manifest, err := os.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	network := &kittest.FakeResource{Outputs: map[string]string{"vpcId": "vpc-0abc"}}
	host := &instance{&kittest.FakeResource{Outputs: map[string]string{"publicIp": "203.0.113.7"}}}
	kittest.AssertDeclaredResources(t, kittest.DeclaredResources{
		Manifest:  manifest,
		Resources: map[string]handler.DeclaredResource{"network": network, "instance": host},
		Desired: contract.Desired{
			Identity: contract.Identity{SubscriptionID: "sub-fixture", BlockID: "blockverify-resources-fixture", OrgID: "org-fixture"},
		},
		Resource: "instance",` + recorded + `
	})
}
`
}

// recordedFields sets instance's recorded inputs and stored identity.
const recordedFields = `
		ResourceInputs:   map[string]json.RawMessage{"network": json.RawMessage(` + "`" + `"vpc-0abc"` + "`" + `)},
		ResourceIdentity: map[string]string{"instanceId": "i-0abc"},`

func TestResourcesSuite_PassesAgainstABlockWhoseObserveAndDeleteNeedTheRecordedInputsAndIdentity(t *testing.T) {
	dir := resourcesFixture(t, resourcesFixtureManifest, recordedResourcesTestGo(recordedFields))
	requireResourcesStatus(t, resourcesSuite(dir), "pass")
}

func TestResourcesSuite_FailsNamingTheResourceWhenDrivenWithNoRecordedInputs(t *testing.T) {
	dir := resourcesFixture(t, resourcesFixtureManifest, recordedResourcesTestGo(""))
	got := resourcesSuite(dir)
	requireResourcesFailNaming(t, got, `resource "instance"`)
	if !strings.Contains(got.Detail, "handed no recorded inputs") {
		t.Errorf("resources detail = %q, want it to say instance's observe was handed no recorded inputs", got.Detail)
	}
}
