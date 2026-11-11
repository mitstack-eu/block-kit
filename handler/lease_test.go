// A lease asks a block for one short-lived credential on /hook, and the handler answers it only for a block that lists the operation, within a day, without caching it or repeating a secret.
package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
)

var leaseManifest = strings.Replace(blockLevelManifest, "operations: [subscribe, unsubscribe, upgrade, validate]", "operations: [subscribe, unsubscribe, upgrade, validate, lease]", 1)

type leasingBlock struct {
	*blockResource
	lease contract.Lease
	err   error
	// retireErr fails a request that only retires.
	retireErr error
	requests  []contract.LeaseRequest
	desired   []contract.Desired
}

func (b *leasingBlock) Lease(ctx context.Context, d contract.Desired, l contract.LeaseRequest) (contract.Lease, error) {
	b.requests = append(b.requests, l)
	b.desired = append(b.desired, d)
	if l.ID == "" && b.retireErr != nil {
		return contract.Lease{}, b.retireErr
	}
	return b.lease, b.err
}

func newLeasingBlock(credential string, expiresAt time.Time) *leasingBlock {
	l := contract.Lease{ExpiresAt: expiresAt, ID: "tok_1"}
	if credential != "" {
		l.Credential = json.RawMessage(credential)
	}
	return &leasingBlock{blockResource: &blockResource{cloud: newFakeCloud()}, lease: l}
}

func leaseFields(extra map[string]any) map[string]any {
	fields := map[string]any{
		"resource": "hetzner.backup-bucket", "leaseId": "lease_0001", "idempotencyKey": "lease-1",
		"want":   map[string]any{"signals": "metrics,logs"},
		"caller": map[string]any{"subscriptionId": "sub_host", "placed": []map[string]any{{"app": "shop", "blockId": "docker-mariadb", "subscriptionId": "sub_db"}}},
	}
	for k, v := range extra {
		fields[k] = v
	}
	return fields
}

func serveLease(t *testing.T, r handler.Resource, manifest string, fields map[string]any) (int, contract.Response) {
	t.Helper()
	return serveBlock(t, blockCall{r: r, obs: asObservers(blockObservers(newFakeCloud())), op: "lease", fields: fields,
		config: []func(*handler.Config){withManifest(manifest)}})
}

func TestLease_AnswersEachCallWithTheCredentialTheBlockMintsAndReachesNoOtherVerb(t *testing.T) {
	expiresAt := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	block := newLeasingBlock(`{"uploads":{"shop":{"url":"https://store.example/shop/a.age?X-Amz-Credential=AKIA123%2F20260715","object":"shop/a.age"}}}`, expiresAt)
	x := newHarness(t, block, withManifest(leaseManifest), func(c *handler.Config) { c.Observers = asObservers(blockObservers(block.cloud)) })

	for i := 1; i <= 2; i++ {
		status, resp := x.answer(leaseFields(map[string]any{"operation": "lease", "secrets": map[string]any{"hetzner": map[string]any{"accessKey": "AKIA123", "secretKey": "s3cr3t-9f2"}}}))

		l := resp.Lease
		if status != http.StatusOK || l == nil || string(l.Credential) != string(block.lease.Credential) || !l.ExpiresAt.Equal(expiresAt) || l.ID != "tok_1" {
			t.Fatalf("lease %d: status %d, lease %+v; want 200 and %s expiring %s under id tok_1", i, status, l, block.lease.Credential, expiresAt)
		}
	}

	want := contract.LeaseRequest{
		ID: "lease_0001", Resource: "hetzner.backup-bucket", Want: json.RawMessage(`{"signals":"metrics,logs"}`),
		Caller: contract.LeaseCaller{SubscriptionID: "sub_host", Placed: []contract.Placed{{App: "shop", BlockID: "docker-mariadb", SubscriptionID: "sub_db"}}},
	}
	if len(block.requests) != 2 || !reflect.DeepEqual(block.requests[0], want) {
		t.Fatalf("the block saw %d lease requests, the first %+v; want one per call, the first %+v: a credential replayed from the idempotency cache could have expired", len(block.requests), block.requests, want)
	}
	if got := block.desired[0].Secrets["hetzner"]["secretKey"]; got != "s3cr3t-9f2" {
		t.Errorf("the block's Desired.Secrets[hetzner][secretKey] = %q, want the request's secret, since the block signs with it", got)
	}
	if block.ensureCalls+block.deleteCalls != 0 {
		t.Errorf("a lease ran %d Ensure and %d Delete calls, want neither verb", block.ensureCalls, block.deleteCalls)
	}
}

func TestLease_AcceptsACredentialThatOnlyHoldsASecretInsideALongerValue(t *testing.T) {
	block := newLeasingBlock(`{"link":"https://store.example/o?X-Amz-Credential=AKIA123%2F20260715&X-Amz-Signature=ab12"}`, time.Now().Add(time.Hour))
	fields := leaseFields(map[string]any{"secrets": map[string]any{"hetzner": map[string]any{"accessKey": "AKIA123"}}})

	status, resp := serveLease(t, block, leaseManifest, fields)

	if status != http.StatusOK || resp.Lease == nil {
		t.Errorf("status = %d, lease = %+v; want 200 and the lease: a signed link names the key id that signed it, and only a value equal to a secret is the secret", status, resp.Lease)
	}
}

func TestLease_RefusesALeaseThatIsNoLeaseWithoutRevealingASecret(t *testing.T) {
	soon, token := time.Now().Add(time.Hour), `{"token":"push-token-not-real"}`
	withSecret := leaseFields(map[string]any{"secrets": map[string]any{"scaleway": map[string]any{"secretKey": "scw-secret-4d1"}}})
	failing := func(err error) *leasingBlock {
		return &leasingBlock{blockResource: &blockResource{cloud: newFakeCloud()}, err: err}
	}
	cases := []struct {
		name, manifest string
		block          *leasingBlock
		fields         map[string]any
		status         int
		want           string
	}{
		{"a block whose manifest does not list lease", blockLevelManifest, newLeasingBlock(token, soon), leaseFields(nil), http.StatusBadRequest, "refused:lease-not-listed"},
		{"a credential with no expiry", leaseManifest, newLeasingBlock(token, time.Time{}), leaseFields(nil), http.StatusInternalServerError, "no expiry"},
		{"a credential whose expiry has passed", leaseManifest, newLeasingBlock(token, time.Now().Add(-time.Minute)), leaseFields(nil), http.StatusInternalServerError, "already passed"},
		{"a credential that outlives a day", leaseManifest, newLeasingBlock(token, time.Now().Add(contract.MaxLeaseLife+time.Hour)), leaseFields(nil), http.StatusInternalServerError, "ahead"},
		{"a lease holding no credential", leaseManifest, newLeasingBlock("", soon), leaseFields(nil), http.StatusInternalServerError, "no credential"},
		{"a credential that is null", leaseManifest, newLeasingBlock("null", soon), leaseFields(nil), http.StatusInternalServerError, "JSON object"},
		{"a credential that is an empty object", leaseManifest, newLeasingBlock("{}", soon), leaseFields(nil), http.StatusInternalServerError, "JSON object"},
		{"a credential that is a string", leaseManifest, newLeasingBlock(`"abc"`, soon), leaseFields(nil), http.StatusInternalServerError, "JSON object"},
		{"a credential repeating a secret the request carried", leaseManifest, newLeasingBlock(`{"nested":{"key":"scw-secret-4d1"}}`, soon), withSecret, http.StatusInternalServerError, "repeats a secret"},
		{"a credential whose only string is empty", leaseManifest, newLeasingBlock(`{"token":""}`, soon), leaseFields(nil), http.StatusInternalServerError, "no value"},
		{"a credential of empty shells", leaseManifest, newLeasingBlock(`{"a":{},"b":null,"c":[]}`, soon), leaseFields(nil), http.StatusInternalServerError, "no value"},
		{"a handle repeating a secret the request carried", leaseManifest, &leasingBlock{blockResource: &blockResource{cloud: newFakeCloud()},
			lease: contract.Lease{Credential: json.RawMessage(token), ExpiresAt: soon, ID: "scw-secret-4d1"}}, withSecret, http.StatusInternalServerError, "repeats a secret"},
		{"a lease the block fails to mint", leaseManifest, failing(errors.New("the store refused scw-secret-4d1")), withSecret, http.StatusInternalServerError, "the store refused"},
		{"a lease the block refuses", leaseManifest, failing(handler.Refuse("no-bucket", "the host keeps no bucket")), leaseFields(nil), http.StatusBadRequest, "refused:no-bucket"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, resp := serveLease(t, tc.block, tc.manifest, tc.fields)

			if status != tc.status || resp.Lease != nil {
				t.Fatalf("status = %d, lease = %+v; want %d and no lease", status, resp.Lease, tc.status)
			}
			if got := resp.Error + " " + resp.StatusDetail; !strings.Contains(got, tc.want) || strings.Contains(got, "scw-secret-4d1") {
				t.Errorf("error and statusDetail = %q, want them to carry %q and no secret", got, tc.want)
			}
		})
	}
}

func TestLease_RetiresACredentialItRefusesSoNoneIsLeftLiveAtTheProvider(t *testing.T) {
	cases := []struct {
		name      string
		lease     contract.Lease
		retireErr error
		want      string
	}{
		{"a credential with no expiry", contract.Lease{Credential: json.RawMessage(`{"token":"t"}`), ID: "tok_9"}, nil, "no expiry"},
		{"a credential that outlives a day", contract.Lease{Credential: json.RawMessage(`{"token":"t"}`), ExpiresAt: time.Now().Add(48 * time.Hour), ID: "tok_9"}, nil, "ahead"},
		{"a refused credential whose retire also fails", contract.Lease{Credential: json.RawMessage(`{"token":"t"}`), ID: "tok_9"}, errors.New("the provider is down"), "the provider is down"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			block := &leasingBlock{blockResource: &blockResource{cloud: newFakeCloud()}, lease: tc.lease, retireErr: tc.retireErr}

			status, resp := serveLease(t, block, leaseManifest, leaseFields(nil))

			if status != http.StatusInternalServerError || resp.Lease != nil || !strings.Contains(resp.Error, tc.want) {
				t.Fatalf("status = %d, lease = %+v, error = %q; want 500, no lease and an error carrying %q", status, resp.Lease, resp.Error, tc.want)
			}
			if len(block.requests) != 2 || block.requests[1].ID != "" || !reflect.DeepEqual(block.requests[1].Retire, []string{"tok_9"}) {
				t.Errorf("the block saw %+v, want the mint and then one request with no lease id retiring tok_9", block.requests)
			}
		})
	}
}

func TestLease_RetiresTheCredentialsItNamesAndAnswersNoLease(t *testing.T) {
	block := newLeasingBlock(`{"token":"ignored-on-a-retire"}`, time.Now().Add(time.Hour))
	fields := map[string]any{"resource": "scaleway.cockpit", "retire": []string{"tok_old_1", "tok_old_2"}}

	status, resp := serveLease(t, block, leaseManifest, fields)

	if status != http.StatusOK || resp.Lease != nil {
		t.Errorf("status = %d, lease = %+v; want 200 and no lease, since a retire mints nothing", status, resp.Lease)
	}
	if len(block.requests) != 1 || block.requests[0].ID != "" || !reflect.DeepEqual(block.requests[0].Retire, []string{"tok_old_1", "tok_old_2"}) {
		t.Errorf("the block saw %+v, want one request with no lease id and the two credentials to retire", block.requests)
	}
}
