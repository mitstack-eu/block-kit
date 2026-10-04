package handler_test

// declared_no_name_answer_test.go pins that a block declaring its resources
// answers a request naming no resource with exactly the bytes it answered
// before a request could name one: every verb runs over every declared
// resource, in creation order or its reverse. Each expected body below was
// captured from the handler as it stood before requests carried a resource
// name, so any change to the no-name path, a new key included, turns this
// file red. It also pins that a block written against DeclaredResource's
// three calls alone still builds and serves.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
	"github.com/mitstack-eu/operator-kit/handler"
	"github.com/mitstack-eu/operator-kit/kittest"
)

func TestDeclaredResources_ARequestNamingNoResourceAnswersEveryVerbWithTheSameBytesAsBefore(t *testing.T) {
	cases := []struct {
		name       string
		operation  string
		arrange    func(b *threeBlock)
		wantStatus int
		wantBody   string
	}{
		{
			name:      "subscribe present",
			operation: "subscribe",
			arrange: func(b *threeBlock) {
				b.firewall.StatusDetail = "rules applied"
			},
			wantStatus: http.StatusOK,
			wantBody: `{"outputs":{"groupId":"sg-0abc","publicIp":"203.0.113.7","vpcId":"vpc-0abc"},"absent":false,"resources":{` +
				`"firewall":{"ensure":{},"observe":{"state":"present","outputs":{"groupId":"sg-0abc"},"statusDetail":"rules applied"}},` +
				`"instance":{"ensure":{},"observe":{"state":"present","outputs":{"publicIp":"203.0.113.7"}}},` +
				`"network":{"ensure":{},"observe":{"state":"present","outputs":{"vpcId":"vpc-0abc"}}}}}`,
		},
		{
			name:       "upgrade present",
			operation:  "upgrade",
			arrange:    func(b *threeBlock) { b.provision() },
			wantStatus: http.StatusOK,
			wantBody: `{"outputs":{"groupId":"sg-0abc","publicIp":"203.0.113.7","vpcId":"vpc-0abc"},"absent":false,"resources":{` +
				`"firewall":{"ensure":{},"observe":{"state":"present","outputs":{"groupId":"sg-0abc"}}},` +
				`"instance":{"ensure":{},"observe":{"state":"present","outputs":{"publicIp":"203.0.113.7"}}},` +
				`"network":{"ensure":{},"observe":{"state":"present","outputs":{"vpcId":"vpc-0abc"}}}}}`,
		},
		{
			name:      "subscribe pending",
			operation: "subscribe",
			arrange: func(b *threeBlock) {
				b.firewall.ObserveState = stateOf(contract.Unknown)
			},
			wantStatus: http.StatusOK,
			wantBody: `{"outputs":null,"pending":true,"statusDetail":"resource \"firewall\" still unknown after 3 attempts","resources":{` +
				`"firewall":{"ensure":{},"observe":{"state":"unknown","outputs":{"groupId":"sg-0abc"}}},` +
				`"network":{"ensure":{},"observe":{"state":"present","outputs":{"vpcId":"vpc-0abc"}}}}}`,
		},
		{
			name:      "subscribe refused",
			operation: "subscribe",
			arrange: func(b *threeBlock) {
				b.instance.EnsureErr = handler.Refuse("quota", "no capacity in %s", "eu-west-1")
			},
			wantStatus: http.StatusBadRequest,
			wantBody: `{"outputs":null,"error":"refused:quota: no capacity in eu-west-1","statusDetail":"refused:quota: no capacity in eu-west-1","resources":{` +
				`"firewall":{"ensure":{},"observe":{"state":"present","outputs":{"groupId":"sg-0abc"}}},` +
				`"instance":{"ensure":{"error":"refused:quota: no capacity in eu-west-1","statusDetail":"refused:quota: no capacity in eu-west-1"}},` +
				`"network":{"ensure":{},"observe":{"state":"present","outputs":{"vpcId":"vpc-0abc"}}}}}`,
		},
		{
			name:      "subscribe error carrying a secret",
			operation: "subscribe",
			arrange: func(b *threeBlock) {
				b.firewall.EnsureErr = errors.New("cloud API rejected " + guardSecret)
			},
			wantStatus: http.StatusInternalServerError,
			wantBody: `{"outputs":null,"error":"cloud API rejected [REDACTED]","resources":{` +
				`"firewall":{"ensure":{"error":"cloud API rejected [REDACTED]"}},` +
				`"network":{"ensure":{},"observe":{"state":"present","outputs":{"vpcId":"vpc-0abc"}}}}}`,
		},
		{
			name:       "unsubscribe absent",
			operation:  "unsubscribe",
			arrange:    func(b *threeBlock) { b.provision() },
			wantStatus: http.StatusOK,
			wantBody: `{"outputs":{"groupId":"sg-0abc","publicIp":"203.0.113.7","vpcId":"vpc-0abc"},"absent":true,"resources":{` +
				`"firewall":{"observe":{"state":"absent","outputs":{"groupId":"sg-0abc"}},"delete":{}},` +
				`"instance":{"observe":{"state":"absent","outputs":{"publicIp":"203.0.113.7"}},"delete":{}},` +
				`"network":{"observe":{"state":"absent","outputs":{"vpcId":"vpc-0abc"}},"delete":{}}}}`,
		},
		{
			name:      "unsubscribe left standing",
			operation: "unsubscribe",
			arrange: func(b *threeBlock) {
				b.provision()
				b.firewall.LeakOnDelete = true
			},
			wantStatus: http.StatusOK,
			wantBody: `{"outputs":null,"statusDetail":"resource \"firewall\" observed present after 3 attempts","absent":false,"resources":{` +
				`"firewall":{"observe":{"state":"present","outputs":{"groupId":"sg-0abc"}},"delete":{}},` +
				`"instance":{"observe":{"state":"absent","outputs":{"publicIp":"203.0.113.7"}},"delete":{}}}}`,
		},
		{
			name:       "validate present",
			operation:  "validate",
			arrange:    func(b *threeBlock) { b.provision() },
			wantStatus: http.StatusOK,
			wantBody: `{"outputs":{"groupId":"sg-0abc","publicIp":"203.0.113.7","vpcId":"vpc-0abc"},"absent":false,"resources":{` +
				`"firewall":{"observe":{"state":"present","outputs":{"groupId":"sg-0abc"}}},` +
				`"instance":{"observe":{"state":"present","outputs":{"publicIp":"203.0.113.7"}}},` +
				`"network":{"observe":{"state":"present","outputs":{"vpcId":"vpc-0abc"}}}}}`,
		},
		{
			name:       "validate absent",
			operation:  "validate",
			arrange:    func(b *threeBlock) {},
			wantStatus: http.StatusOK,
			wantBody: `{"outputs":null,"absent":true,"resources":{` +
				`"firewall":{"observe":{"state":"absent","outputs":{"groupId":"sg-0abc"}}},` +
				`"instance":{"observe":{"state":"absent","outputs":{"publicIp":"203.0.113.7"}}},` +
				`"network":{"observe":{"state":"absent","outputs":{"vpcId":"vpc-0abc"}}}}}`,
		},
		{
			name:      "validate mixed",
			operation: "validate",
			arrange: func(b *threeBlock) {
				b.network.Count, b.firewall.Count = 1, 1
			},
			wantStatus: http.StatusOK,
			wantBody: `{"outputs":null,"pending":true,"statusDetail":"resources observed \"network\" present, \"firewall\" present, \"instance\" absent","resources":{` +
				`"firewall":{"observe":{"state":"present","outputs":{"groupId":"sg-0abc"}}},` +
				`"instance":{"observe":{"state":"absent","outputs":{"publicIp":"203.0.113.7"}}},` +
				`"network":{"observe":{"state":"present","outputs":{"vpcId":"vpc-0abc"}}}}}`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newThreeBlock()
			c.arrange(b)
			s := kittest.NewSigner(t, "kid-no-name", 29)
			h := newThreeResourceHandler(t, s, b.resources())
			status, raw := postHook(t, h, s, envelope(t, s, "nonce-no-name-"+c.name, map[string]any{
				"operation": c.operation,
				"secrets":   guardSecrets(),
			}))
			if status != c.wantStatus || string(raw) != c.wantBody {
				t.Errorf("%s answered %d %s\nwant %d %s", c.operation, status, raw, c.wantStatus, c.wantBody)
			}
		})
	}
}

// threeCallResource implements exactly the three calls DeclaredResource
// has always had, and nothing more.
type threeCallResource struct {
	present bool
	calls   int
}

func (r *threeCallResource) Ensure(ctx context.Context, d contract.Desired) error {
	r.calls++
	r.present = true
	return nil
}

func (r *threeCallResource) Observe(ctx context.Context, id contract.Identity) (contract.Observation, error) {
	r.calls++
	if r.present {
		return contract.Observation{State: contract.Present}, nil
	}
	return contract.Observation{State: contract.Absent}, nil
}

func (r *threeCallResource) Delete(ctx context.Context, id contract.Identity) error {
	r.calls++
	r.present = false
	return nil
}

func TestDeclaredResources_ABlockImplementingOnlyTheThreeCallsBuildsAndServes(t *testing.T) {
	network, firewall, instance := &threeCallResource{}, &threeCallResource{}, &threeCallResource{}
	resources := map[string]handler.DeclaredResource{"network": network, "firewall": firewall, "instance": instance}

	status, raw, resp := serveThree(t, resources, "subscribe", nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", status, raw)
	}
	wantOnlyEntries(t, resp, "network", "firewall", "instance")
	for name, r := range map[string]*threeCallResource{"network": network, "firewall": firewall, "instance": instance} {
		if r.calls != 2 {
			t.Errorf("resource %q saw %d calls, want 2: one Ensure and one Observe", name, r.calls)
		}
	}
	if strings.Contains(string(raw), `"identity"`) {
		t.Errorf("body %s reports an identity, but no resource reported one", raw)
	}
}
