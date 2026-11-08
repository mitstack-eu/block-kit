package observe_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/capabilities/github/landing"
	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/observe"
)

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
	observe.GitHubCommitAPI
	held    map[string][]string
	commits []observe.GitHubCommitPaths
	err     error
}

func (f *owned) holds(sub, item string) *owned {
	f.held = map[string][]string{sub: {item}, observe.HetznerSubscriptionLabel + "=" + sub: {item}, observe.GitHubSubscriptionTopic(sub): {item}}
	return f
}

func (f *owned) ResourceARNs(_ context.Context, _ contract.Desired, _, sub string) ([]string, error) {
	return f.held[sub], f.err
}

func (f *owned) RoleARNs(context.Context, contract.Desired, string, string) ([]string, error) {
	return nil, nil
}

func (f *owned) ListByLabel(_ context.Context, _ contract.Desired, sel string) (out []observe.HetznerObject, _ error) {
	for _, item := range f.held[sel] {
		kind, id, _ := strings.Cut(item, "/")
		out = append(out, observe.HetznerObject{Kind: kind, ID: id})
	}
	return out, f.err
}

func (f *owned) RepositoriesWithTopic(_ context.Context, _ contract.Desired, topic string) (out []observe.GitHubRepositoryTopics, _ error) {
	for _, r := range f.held[topic] {
		out = append(out, observe.GitHubRepositoryTopics{FullName: r, Topics: []string{topic}})
	}
	return out, f.err
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
	return f.commits, f.err
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
			ref := contract.ResourceRef{Name: o.typ, Type: o.typ, Unsubscribing: unsubscribing, Desired: refFor(o.typ, nil).Desired}
			return o.build(f).ObserveResource(context.Background(), ref)
		}
		t.Run(o.typ, func(t *testing.T) {
			got, err := observeWith((&owned{}).holds("sub-1", o.item), true)
			if err != nil || got.State != contract.Present || got.Identity[o.key] != o.id {
				t.Errorf("held: got %+v, %v; want present naming %s %q", got, err, o.key, o.id)
			}
			for label, f := range map[string]*owned{"nothing held": {}, "held by another subscription": (&owned{}).holds("sub-2", o.item)} {
				if got, err = observeWith(f, true); err != nil || got.State != contract.Absent {
					t.Errorf("%s: got %+v, %v; want absent", label, got, err)
				}
			}
			boom := errors.New("provider: throttled")
			if got, err = observeWith(&owned{err: boom}, true); !errors.Is(err, boom) || got.State == contract.Absent {
				t.Errorf("failed read: got %+v, %v; want the read's error, never absent", got, err)
			}
			if got, err = observeWith((&owned{}).holds("sub-1", o.item), false); err != nil || got.State != contract.Unknown {
				t.Errorf("subscribing: got %+v, %v; want unknown", got, err)
			}
		})
	}
}

// The repository observer reads only repositories, so a recorded contribution its API cannot read on main does not fail it.
func TestGitHubRepository_ResolvesAnEmptyIdentityBesideAContributionItsAPICannotRead(t *testing.T) {
	identity, err := landing.Record{Repository: "acme/app", Files: []landing.File{{Path: "Dockerfile", Blob: landing.BlobHash([]byte("FROM nginx:1.27\n")), Created: true}}}.Identity()
	if err != nil {
		t.Fatal(err)
	}
	ref := refFor("github.repository", nil)
	ref.Unsubscribing = true
	ref.Desired.RecordedResources = map[string]contract.RecordedResource{"contribution": {Type: "github.scaffold-contribution", Identity: identity}}

	got, err := observe.GitHubRepository{API: (&owned{}).holds("sub-1", "acme/app")}.ObserveResource(context.Background(), ref)

	if err != nil || got.State != contract.Present || got.Identity["repo"] != "acme/app" {
		t.Errorf("got %+v, %v; want present naming repo acme/app", got, err)
	}
}
