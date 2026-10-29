package kittest_test

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/mitstack-eu/block-kit/capabilities/github/ghapi"
	"github.com/mitstack-eu/block-kit/capabilities/github/landing"
	"github.com/mitstack-eu/block-kit/capabilities/github/landing/landingtest"
	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/observe"
	"github.com/mitstack-eu/block-kit/verify/kittest"
)

var offeredFiles = map[string]string{"CLAUDE.md": "# weather\n", "Dockerfile": "FROM nginx:1.27\n"}

// offerBlock offers two files as a scaffold contribution that another block commits, records them through landing.Offer, and removes them through landing.Remove unless teardown names a fault.
type offerBlock struct {
	repo     *landingtest.Fake
	teardown string
	lost     bool
}

func (b *offerBlock) client(context.Context, contract.Desired) (*ghapi.Client, error) {
	if b.lost {
		return nil, errors.New("the installation no longer reaches the repository")
	}
	return ghapi.NewClient(b.repo.URL, "ghs_kittest", nil), nil
}

func (b *offerBlock) EnsureResources(ctx context.Context, d contract.Desired) ([]contract.CreatedResource, error) {
	b.lost = false
	c, _ := b.client(ctx, d)
	files := offeredFiles
	if b.teardown == "records one file" {
		files = map[string]string{"CLAUDE.md": offeredFiles["CLAUDE.md"]}
	}
	prior, _ := landing.RecordFromIdentity(d.RecordedResources[landingType].Identity)
	rec, err := landing.Offer(ctx, c, landing.Landing{Repository: b.repo.Repo, Files: files, Prior: prior})
	if err != nil {
		return nil, err
	}
	identity, err := rec.Identity()
	return []contract.CreatedResource{{Name: landingType, Type: landingType, Identity: identity}}, err
}

func (b *offerBlock) Ensure(ctx context.Context, d contract.Desired) error {
	_, err := b.EnsureResources(ctx, d)
	return err
}

func (b *offerBlock) Delete(ctx context.Context, d contract.Desired) (map[string]string, error) {
	_, _, err := b.DeleteKeeping(ctx, d)
	return nil, err
}

func (b *offerBlock) DeleteKeeping(ctx context.Context, d contract.Desired) (map[string]string, map[string][]contract.Kept, error) {
	rec, err := landing.RecordFromIdentity(d.RecordedResources[landingType].Identity)
	if err != nil || b.teardown == "leaves its files" {
		return nil, nil, nil
	}
	c, err := b.client(ctx, d)
	if err != nil {
		return nil, nil, err
	}
	res, err := landing.Remove(ctx, c, landing.Removal{Subscription: d.SubscriptionID, Records: []landing.Record{rec}})
	return nil, map[string][]contract.Kept{landingType: res.Kept}, err
}

type offerObserver struct{ b *offerBlock }

func (o offerObserver) ObserveResource(ctx context.Context, r contract.ResourceRef) (contract.Observation, error) {
	obs, err := landing.OfferObserver{Client: o.b.client}.ObserveResource(ctx, r)
	if err != nil || obs.State != contract.Present {
		return obs, err
	}
	fragment, err := json.Marshal(map[string]any{"files": offeredFiles})
	obs.Outputs = map[string]string{"scaffoldContribution": string(fragment)}
	return obs, err
}

type offerProvider struct{ b *offerBlock }

func (p offerProvider) Repository() *landingtest.Fake { return p.b.repo }

func (p offerProvider) ListTagged(context.Context, string) ([]observe.Tagged, error) {
	return []observe.Tagged{{Type: landingType, ID: p.b.repo.Repo}}, nil
}

func (p offerProvider) Lose(context.Context, contract.CreatedResource) error {
	p.b.lost = true
	return nil
}

func offerBlockThat(t *testing.T, teardown string) kittest.Block {
	t.Helper()
	b := &offerBlock{repo: landingtest.New(t, "acme/weather"), teardown: teardown}
	types := typeSnapshot(t)
	if teardown == "leaves its files" {
		types = markedSnapshot(t, landingType)
	}
	return kittest.Block{
		Config: handler.Config{
			Resource:  b,
			Manifest:  []byte(landingBlockManifest),
			Types:     types,
			Observers: map[string]handler.Observer{landingType: offerObserver{b: b}},
		},
		Provider: offerProvider{b: b},
		Desired:  blockDesired(""),
	}
}

// The check commits the offered files onto main as app-scaffold would, then changes CLAUDE.md, which leaves Dockerfile unchanged.
func TestAssertBlock_CommitsAContributionsOfferedFilesBeforeReadingMainAfterUnsubscribe(t *testing.T) {
	for _, tc := range []struct {
		teardown string
		want     []string
	}{
		{"", nil},
		{"leaves its files", []string{"Dockerfile", "unchanged"}},
		{"records one file", []string{"Dockerfile", "landing record"}},
	} {
		t.Run("a contribution that "+cmp.Or(tc.teardown, "removes through the landing package"), func(t *testing.T) {
			in := offerBlockThat(t, tc.teardown)

			got := runAndCollect(func(tb testing.TB) { kittest.AssertBlock(tb, in) })

			if got.failed != (tc.want != nil) {
				t.Fatalf("AssertBlock failed = %v; want %v, naming %q: %q", got.failed, tc.want != nil, tc.want, got.logs)
			}
			wantProblemNaming(t, got.logs, tc.want...)
		})
	}
}
