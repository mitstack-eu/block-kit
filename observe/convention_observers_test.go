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
}

func (f roleAWS) RoleARNs(_ context.Context, _ contract.Desired, _, role string) ([]string, error) {
	return []string{"arn:aws:ec2:eu-west-1:1:instance/i-aproxy"}, f.err
}

func (f roleAWS) AssociationIDs(_ context.Context, _ contract.Desired, document string) ([]string, error) {
	return map[string][]string{"app-env": {"b-2"}, "edge-config": {"a-1"}}[document], f.err
}

func TestAWSConventionObservers_ResolveAnEmptyIdentityWhileUnsubscribing(t *testing.T) {
	const host, proxy = "arn:aws:ec2:eu-west-1:1:instance/i-host", "arn:aws:ec2:eu-west-1:1:instance/i-aproxy"
	docs := []string{"arn:aws:ssm:eu-west-1:1:document/app-env", "arn:aws:ssm:eu-west-1:1:document/edge-config"}
	boom := errors.New("provider: throttled")
	instance := func(a roleAWS) observer { return observe.EC2Instance{API: a} }
	egress := func(a roleAWS) observer { return observe.EgressProxy{API: a} }
	association := func(a roleAWS) observer { return observe.SSMAssociation{API: a} }
	for _, tc := range []struct {
		label, typ, id string
		build          func(roleAWS) observer
		arns           []string
		err            error
	}{
		{"the host beside the proxy", "aws.ec2-instance", "i-host", instance, []string{proxy, host}, nil},
		{"only the proxy, read as the host", "aws.ec2-instance", "", instance, []string{proxy}, nil},
		{"the proxy beside the host", "aws.egress-proxy", "i-aproxy", egress, []string{host, proxy}, nil},
		{"only the host, read as the proxy", "aws.egress-proxy", "", egress, []string{host}, nil},
		{"a failed read of the host", "aws.ec2-instance", "", instance, []string{host}, boom},
		{"two documents, one association each", "aws.ssm-association", "a-1", association, docs, nil},
		{"a document whose associations are gone", "aws.ssm-association", "", association, []string{"arn:aws:ssm:eu-west-1:1:document/gone"}, nil},
		{"no document", "aws.ssm-association", "", association, nil, nil},
		{"a failed read of the associations", "aws.ssm-association", "", association, docs, boom},
	} {
		t.Run(tc.label, func(t *testing.T) {
			ref := refFor(tc.typ, nil)
			ref.Unsubscribing = true
			got, err := tc.build(roleAWS{owned: &owned{held: map[string][]string{"sub-1": tc.arns}, err: tc.err}}).ObserveResource(context.Background(), ref)
			switch {
			case tc.err != nil && (!errors.Is(err, tc.err) || got.State == contract.Absent):
				t.Errorf("got %+v, %v; want the read's error, never absent", got, err)
			case tc.err == nil && tc.id == "" && (err != nil || got.State != contract.Absent):
				t.Errorf("got %+v, %v; want absent", got, err)
			case tc.err == nil && tc.id != "" && (err != nil || got.State != contract.Present || got.Identity["instanceId"]+got.Identity["associationId"] != tc.id):
				t.Errorf("got %+v, %v; want present naming %q", got, err, tc.id)
			}
		})
	}
	t.Run("an API that cannot tell the proxy from the host stays unknown", func(t *testing.T) {
		ref := refFor("aws.ec2-instance", nil)
		ref.Unsubscribing = true
		api := struct {
			observe.EC2API
			observe.AWSTaggingAPI
		}{AWSTaggingAPI: &owned{held: map[string][]string{"sub-1": {host}}}}
		if got, err := (observe.EC2Instance{API: api}).ObserveResource(context.Background(), ref); err != nil || got.State != contract.Unknown {
			t.Errorf("got %+v, %v; want unknown", got, err)
		}
	})
}

// commander answers the container probe with out, or fails with err, and records the script it ran and where.
type commander struct {
	out, script, ranOn string
	err                error
}

func (c *commander) RunOnInstance(_ context.Context, _ contract.Desired, instanceID, script string) (string, error) {
	c.ranOn, c.script = instanceID, script
	return c.out, c.err
}

func TestEC2Container_FindsTheSubscriptionsContainerByItsLabel(t *testing.T) {
	bound := refFor("aws.ec2-container", nil)
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
			ran := tc.c.ranOn + " " + tc.c.script
			switch {
			case tc.c.err != nil && (!errors.Is(err, boom) || got.State == contract.Absent):
				t.Errorf("got %+v, %v; want the probe's error, never absent", got, err)
			case tc.c.err == nil && (err != nil || got.State != tc.state):
				t.Errorf("got %+v, %v; want %s", got, err, tc.state)
			case tc.state == contract.Present && (got.Identity["container"] != "plain-tracker" || !strings.HasPrefix(ran, "i-0abc ") || !strings.Contains(ran, "mitstack:subscription=sub-1")):
				t.Errorf("identity %v after running %q; want the bound instance and the label filter", got.Identity, ran)
			case tc.state == contract.Unknown && tc.c.ranOn != "":
				t.Errorf("ran on %q with no instance bound", tc.c.ranOn)
			}
		})
	}
}
