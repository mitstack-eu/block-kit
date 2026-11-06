package observe_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/observe"
)

// roleAWS adds the role and association reads to owned's tag listing.
type roleAWS struct {
	*owned
	observe.SSMAssociationAPI
	proxies []string
	assoc   map[string][]string
}

func (f roleAWS) RoleARNs(_ context.Context, _ contract.Desired, _, role string) ([]string, error) {
	if role != "egress-proxy" {
		return nil, nil
	}
	return f.proxies, f.err
}

func (f roleAWS) AssociationIDs(_ context.Context, _ contract.Desired, document string) ([]string, error) {
	return f.assoc[document], f.err
}

func TestAWSConventionObservers_ResolveAnEmptyIdentityWhileUnsubscribing(t *testing.T) {
	const host, proxy = "arn:aws:ec2:eu-west-1:1:instance/i-host", "arn:aws:ec2:eu-west-1:1:instance/i-aproxy"
	const docA, docB = "arn:aws:ssm:eu-west-1:1:document/app-env", "arn:aws:ssm:eu-west-1:1:document/edge-config"
	boom := errors.New("provider: throttled")
	instance := func(a roleAWS) observer { return observe.EC2Instance{API: a} }
	egress := func(a roleAWS) observer { return observe.EgressProxy{API: a} }
	association := func(a roleAWS) observer { return observe.SSMAssociation{API: a} }
	for _, tc := range []struct {
		label, typ, key string
		build           func(roleAWS) observer
		arns            []string
		err             error
		state           contract.State
		id              string
	}{
		{"the host beside the proxy", "aws.ec2-instance", "instanceId", instance, []string{proxy, host}, nil, contract.Present, "i-host"},
		{"only the proxy, read as the host", "aws.ec2-instance", "instanceId", instance, []string{proxy}, nil, contract.Absent, ""},
		{"the proxy beside the host", "aws.egress-proxy", "instanceId", egress, []string{host, proxy}, nil, contract.Present, "i-aproxy"},
		{"only the host, read as the proxy", "aws.egress-proxy", "instanceId", egress, []string{host}, nil, contract.Absent, ""},
		{"a failed read of the host", "aws.ec2-instance", "instanceId", instance, []string{host}, boom, contract.Absent, ""},
		{"two documents, one association each", "aws.ssm-association", "associationId", association, []string{docA, docB}, nil, contract.Present, "a-1"},
		{"documents whose associations are gone", "aws.ssm-association", "associationId", association, []string{docA}, nil, contract.Absent, ""},
		{"no document", "aws.ssm-association", "associationId", association, nil, nil, contract.Absent, ""},
		{"a failed read of the associations", "aws.ssm-association", "associationId", association, []string{docA}, boom, contract.Absent, ""},
	} {
		t.Run(tc.label, func(t *testing.T) {
			api := roleAWS{owned: &owned{held: map[string][]string{"sub-1": tc.arns}, err: tc.err}, proxies: []string{proxy}}
			if tc.typ == "aws.ssm-association" && tc.state == contract.Present {
				api.assoc = map[string][]string{"app-env": {"b-2"}, "edge-config": {"a-1"}}
			}
			ref := refFor(tc.typ, nil)
			ref.Unsubscribing = true
			got, err := tc.build(api).ObserveResource(context.Background(), ref)
			switch {
			case tc.err != nil && (!errors.Is(err, tc.err) || got.State == contract.Absent):
				t.Errorf("got %+v, %v; want the read's error, never absent", got, err)
			case tc.err == nil && (err != nil || got.State != tc.state || got.Identity[tc.key] != tc.id):
				t.Errorf("got %+v, %v; want %s naming %q", got, err, tc.state, tc.id)
			}
		})
	}
	t.Run("an API that cannot tell the proxy from the host stays unknown", func(t *testing.T) {
		ref := refFor("aws.ec2-instance", nil)
		ref.Unsubscribing = true
		got, err := observe.EC2Instance{API: &owned{held: map[string][]string{"sub-1": {host}}}}.ObserveResource(context.Background(), ref)
		if err != nil || got.State != contract.Unknown {
			t.Errorf("got %+v, %v; want unknown", got, err)
		}
	})
}

// commander answers the container probe with out, or fails with err, and records the script it ran and where.
type commander struct {
	out    string
	err    error
	script string
	ranOn  string
}

func (c *commander) RunOnInstance(_ context.Context, _ contract.Desired, instanceID, script string) (string, error) {
	c.ranOn, c.script = instanceID, script
	return c.out, c.err
}

func TestEC2Container_FindsTheSubscriptionsContainerByItsLabel(t *testing.T) {
	bound := refFor("aws.ec2-container", nil)
	bound.Unsubscribing = true
	bound.Desired.RequirementTypes = map[string]string{"host": "aws.ec2-instance"}
	bound.Desired.Bindings = map[string]map[string]string{"host": {"instanceId": "i-0abc"}}
	boom := errors.New("ssm: command exited 125")
	for _, tc := range []struct {
		label string
		c     *commander
		ref   contract.ResourceRef
		state contract.State
	}{
		{"a container carries the label", &commander{out: "plain-tracker\n"}, bound, contract.Present},
		{"no container carries it", &commander{}, bound, contract.Absent},
		{"the probe failed", &commander{err: boom}, bound, contract.Absent},
		{"no instance is bound", &commander{out: "plain-tracker\n"}, refFor("aws.ec2-container", nil), contract.Unknown},
	} {
		t.Run(tc.label, func(t *testing.T) {
			tc.ref.Unsubscribing = true
			got, err := observe.EC2Container{API: tc.c}.ObserveResource(context.Background(), tc.ref)
			switch {
			case tc.c.err != nil && (!errors.Is(err, boom) || got.State == contract.Absent):
				t.Errorf("got %+v, %v; want the probe's error, never absent", got, err)
			case tc.c.err == nil && (err != nil || got.State != tc.state):
				t.Errorf("got %+v, %v; want %s", got, err, tc.state)
			case tc.state == contract.Present && (got.Identity["instanceId"] != "i-0abc" || got.Identity["container"] != "plain-tracker" || tc.c.ranOn != "i-0abc" || !strings.Contains(tc.c.script, "mitstack:subscription=sub-1")):
				t.Errorf("identity %v after running %q on %q; want the bound instance and the label filter", got.Identity, tc.c.script, tc.c.ranOn)
			case tc.state == contract.Unknown && tc.c.ranOn != "":
				t.Errorf("ran on %q with no instance bound", tc.c.ranOn)
			}
		})
	}
}
