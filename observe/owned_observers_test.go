package observe_test

import (
	"context"
	"errors"
	"fmt"
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

// MainFile fails every read, so an observer that read main for a contribution would fail with it.
func (f *owned) MainFile(_ context.Context, _ contract.Desired, repo, path string) ([]byte, bool, error) {
	return nil, false, fmt.Errorf("owned: read %s on main of %s, which no repository observer needs", path, repo)
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

// The repository observer reads only repositories, so a recorded contribution whose files main cannot be read for does not fail it.
func TestGitHubRepository_ResolvesAnEmptyIdentityBesideAContributionItCannotRead(t *testing.T) {
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

// listsWithoutMainFile lists topic repositories and trailer commits but reads no file on main, as a repository or commit observer's API may.
type listsWithoutMainFile struct {
	observe.GitHubRepositoryAPI
	observe.GitHubCommitAPI
	f *owned
}

func (w listsWithoutMainFile) RepositoriesWithTopic(ctx context.Context, d contract.Desired, topic string) ([]observe.GitHubRepositoryTopics, error) {
	return w.f.RepositoriesWithTopic(ctx, d, topic)
}
func (w listsWithoutMainFile) Webhooks(ctx context.Context, d contract.Desired, repo string) ([]observe.GitHubWebhook, error) {
	return w.f.Webhooks(ctx, d, repo)
}
func (w listsWithoutMainFile) ActionsSecretNames(ctx context.Context, d contract.Desired, repo string) ([]string, error) {
	return w.f.ActionsSecretNames(ctx, d, repo)
}
func (w listsWithoutMainFile) ActionsVariableNames(ctx context.Context, d contract.Desired, repo string) ([]string, error) {
	return w.f.ActionsVariableNames(ctx, d, repo)
}
func (w listsWithoutMainFile) Commits(ctx context.Context, d contract.Desired, repo string) ([]observe.GitHubCommitPaths, error) {
	return w.f.Commits(ctx, d, repo)
}

// The repository and commit observers resolve an empty identity while unsubscribing from listing alone, so an API that cannot read main keeps that.
func TestGitHubRepositoryAndCommit_ResolveAnEmptyIdentityWithAnAPIThatReadsNoFile(t *testing.T) {
	trailer := "\n\n" + observe.GitHubSubscriptionTrailer("sub-1")
	api := listsWithoutMainFile{f: &owned{
		held:    map[string][]string{observe.GitHubSubscriptionTopic("sub-1"): {"acme/app"}},
		commits: []observe.GitHubCommitPaths{{SHA: "new1", Message: "Add" + trailer}},
	}}
	for _, tc := range []struct {
		typ   string
		build observer
		key   string
		want  string
	}{
		{"github.repository", observe.GitHubRepository{API: api}, "repo", "acme/app"},
		{"github.commit", observe.GitHubCommit{API: api}, "sha", "new1"},
	} {
		ref := refFor(tc.typ, nil)
		ref.Unsubscribing = true

		got, err := tc.build.ObserveResource(context.Background(), ref)

		if err != nil || got.State != contract.Present || got.Identity[tc.key] != tc.want {
			t.Errorf("%s: got %+v, %v; want present naming %s %q", tc.typ, got, err, tc.key, tc.want)
		}
	}
}
