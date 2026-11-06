package observe_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/observe"
)

type owned struct {
	observe.EC2API
	observe.HetznerServerAPI
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
	{"aws.ec2-instance", "instanceId", "i-0abc", "arn:aws:ec2:eu-west-1:1:instance/i-0abc", func(f *owned) observer { return observe.EC2Instance{API: f} }},
	{"hetzner.server", "serverId", "4711", "server/4711", func(f *owned) observer { return observe.HetznerServer{API: f} }},
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
