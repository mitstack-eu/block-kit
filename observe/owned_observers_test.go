package observe_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/observe"
)

// owned lists, by subscription, what the subscription created; its embedded APIs are nil, so a read by an identity panics.
type owned struct {
	observe.EC2API
	observe.S3BucketAPI
	observe.VPCAPI
	observe.SecurityGroupAPI
	observe.EBSVolumeAPI
	observe.IAMRoleAPI
	observe.HetznerServerAPI
	observe.HetznerPrimaryIPAPI
	observe.HetznerNetworkAPI
	observe.HetznerFirewallAPI
	observe.GitHubRepositoryAPI
	held   map[string][]string
	err    error
	listed int
}

func (f *owned) list(key string) ([]string, error) {
	f.listed++
	return f.held[key], f.err
}

// holds files item under the key its provider lists by: an ARN under the tag value, kind/id under the label selector, a repository under the topic.
func (f *owned) holds(sub, typ, item string) *owned {
	key := sub
	switch {
	case strings.HasPrefix(typ, "hetzner"):
		key = observe.HetznerSubscriptionLabel + "=" + sub
	case strings.HasPrefix(typ, "github"):
		key = observe.GitHubSubscriptionTopic(sub)
	}
	f.held = map[string][]string{key: {item}}
	return f
}

func (f *owned) ResourceARNs(_ context.Context, _ contract.Desired, _, sub string) ([]string, error) {
	return f.list(sub)
}

func (f *owned) ListByLabel(_ context.Context, _ contract.Desired, sel string) (out []observe.HetznerObject, err error) {
	items, err := f.list(sel)
	for _, item := range items {
		kind, id, _ := strings.Cut(item, "/")
		out = append(out, observe.HetznerObject{Kind: kind, ID: id})
	}
	return out, err
}

func (f *owned) RepositoriesWithTopic(_ context.Context, _ contract.Desired, topic string) (out []observe.GitHubRepositoryTopics, err error) {
	repos, err := f.list(topic)
	for _, r := range repos {
		out = append(out, observe.GitHubRepositoryTopics{FullName: r, Topics: []string{topic}})
	}
	return out, err
}

func (f *owned) Webhooks(context.Context, contract.Desired, string) ([]observe.GitHubWebhook, error) {
	return nil, nil
}
func (f *owned) ActionsSecretNames(context.Context, contract.Desired, string) ([]string, error) {
	return nil, nil
}
func (f *owned) ActionsVariableNames(context.Context, contract.Desired, string) ([]string, error) {
	return nil, nil
}
func (f *owned) Commits(context.Context, contract.Desired, string) ([]observe.GitHubCommitPaths, error) {
	return nil, nil
}

var ownedObservers = []struct {
	typ, key, id, item string
	build              func(f *owned) observer
}{
	{"aws.s3-bucket", "bucket", "app-state", "arn:aws:s3:::app-state", func(f *owned) observer { return observe.S3Bucket{API: f} }},
	{"aws.vpc", "vpcId", "vpc-0abc", "arn:aws:ec2:eu-west-1:1:vpc/vpc-0abc", func(f *owned) observer { return observe.VPC{API: f} }},
	{"aws.security-group", "groupId", "sg-0abc", "arn:aws:ec2:eu-west-1:1:security-group/sg-0abc", func(f *owned) observer { return observe.SecurityGroup{API: f} }},
	{"aws.ec2-instance", "instanceId", "i-0abc", "arn:aws:ec2:eu-west-1:1:instance/i-0abc", func(f *owned) observer { return observe.EC2Instance{API: f} }},
	{"aws.ebs-volume", "volumeId", "vol-0abc", "arn:aws:ec2:eu-west-1:1:volume/vol-0abc", func(f *owned) observer { return observe.EBSVolume{API: f} }},
	{"aws.iam-role", "roleName", "deploy", "arn:aws:iam::1:role/ci/deploy", func(f *owned) observer { return observe.IAMRole{API: f} }},
	{"hetzner.server", "serverId", "4711", "server/4711", func(f *owned) observer { return observe.HetznerServer{API: f} }},
	{"hetzner.primary-ip", "primaryIpId", "99", "primary_ip/99", func(f *owned) observer { return observe.HetznerPrimaryIP{API: f} }},
	{"hetzner.network", "networkId", "77", "network/77", func(f *owned) observer { return observe.HetznerNetwork{API: f} }},
	{"hetzner.firewall", "firewallId", "4711", "firewall/4711", func(f *owned) observer { return observe.HetznerFirewall{API: f} }},
	{"github.repository", "repo", "acme/app", "acme/app", func(f *owned) observer { return observe.GitHubRepository{API: f} }},
}

func TestOwnedObservers_ResolveAnEmptyIdentityWhileUnsubscribing(t *testing.T) {
	for _, o := range ownedObservers {
		observeWith := func(f *owned, unsubscribing bool) (contract.Observation, error) {
			ref := refFor(o.typ, nil)
			ref.Unsubscribing = unsubscribing
			return o.build(f).ObserveResource(context.Background(), ref)
		}
		t.Run(o.typ, func(t *testing.T) {
			got, err := observeWith((&owned{}).holds("sub-1", o.typ, o.item), true)
			if err != nil || got.State != contract.Present || got.Identity[o.key] != o.id {
				t.Errorf("held: got %+v, %v; want present naming %s %q", got, err, o.key, o.id)
			}
			for label, f := range map[string]*owned{"nothing held": {}, "held by another subscription": (&owned{}).holds("sub-2", o.typ, o.item)} {
				if got, err = observeWith(f, true); err != nil || got.State != contract.Absent {
					t.Errorf("%s: got %+v, %v; want absent", label, got, err)
				}
			}
			boom := errors.New("provider: throttled")
			if got, err = observeWith(&owned{err: boom}, true); !errors.Is(err, boom) || got.State == contract.Absent {
				t.Errorf("failed read: got %+v, %v; want the read's error, never absent", got, err)
			}
			f := (&owned{}).holds("sub-1", o.typ, o.item)
			if got, err = observeWith(f, false); err != nil || got.State != contract.Unknown || f.listed != 0 {
				t.Errorf("subscribing: got %+v, %v after %d lists; want unknown without a list", got, err, f.listed)
			}
		})
	}
}
