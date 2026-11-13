package gitops_test

// The test block is one kubernetes.namespace whose Namespace and ServiceAccount a block-level handler commits and judges through Check.

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"testing"

	"github.com/mitstack-eu/block-kit/capabilities/github/gitops"
	"github.com/mitstack-eu/block-kit/capabilities/github/gitops/gitopstest"
	"github.com/mitstack-eu/block-kit/contract"
)

const (
	resourceName = "app-namespace"
	resourceType = "kubernetes.namespace"
	clusterParam = "k3s-fsn1"

	githubToken   = "ghs_platformMintedInstallationToken0123"
	secretInput   = "secret-input-value-4567"
	appSecret     = "app-secret-value-89ab"
	hetznerSecret = "hetzner-api-token-cdef"

	alphaNamespacePath      = "apps/app-alpha/namespace.yaml"
	alphaServiceAccountPath = "apps/app-alpha/serviceaccount.yaml"
)

var seededSecrets = []string{githubToken, secretInput, appSecret, hetznerSecret}

func readNamespaceManifest(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/kubernetes-namespace/manifest.yaml")
	if err != nil {
		t.Fatalf("reading the test block's manifest: %v", err)
	}
	return raw
}

func desiredFor(app string) contract.Desired {
	return contract.Desired{
		Identity: contract.Identity{
			SubscriptionID: "sub-" + app,
			BlockID:        "gitops-app-namespace",
			BlockVersion:   "1.0.0",
			OrgID:          "org-1",
			AppID:          app,
		},
		Params:       map[string]string{"appName": app, "cluster": clusterParam},
		SecretInputs: map[string]string{"dbPassword": secretInput},
		AppSecrets:   map[string]string{"STRIPE_KEY": appSecret},
		Secrets: map[string]map[string]string{
			"github":  {"token": githubToken},
			"hetzner": {"apiToken": hetznerSecret},
		},
	}
}

type rendered struct {
	App     string
	Files   map[string][]byte
	Outputs map[string]string
}

func renderNamespace(d contract.Desired) (rendered, error) {
	cluster := d.Params["cluster"]
	if cluster == "" {
		return rendered{}, errors.New("no cluster param")
	}
	if d.AppID == "" {
		return rendered{}, errors.New("no app id")
	}
	return renderApp(d.AppID, cluster), nil
}

func renderApp(app, cluster string) rendered {
	sa := app + "-runtime"
	return rendered{
		App: app,
		Files: map[string][]byte{
			"apps/" + app + "/namespace.yaml": []byte(fmt.Sprintf(
				"apiVersion: v1\nkind: Namespace\nmetadata:\n  name: %s\n  annotations:\n    mitstack.dev/cluster: %s\n", app, cluster)),
			"apps/" + app + "/serviceaccount.yaml": []byte(fmt.Sprintf(
				"apiVersion: v1\nkind: ServiceAccount\nmetadata:\n  name: %s\n  namespace: %s\n", sa, app)),
		},
		Outputs: map[string]string{"namespace": app, "serviceAccount": sa},
	}
}

func alphaOutputs() map[string]string {
	return map[string]string{"namespace": "app-alpha", "serviceAccount": "app-alpha-runtime"}
}

// gitopsBlock is the test block's sources: the fakes, and the repository and notifications it reaches, which a test may swap.
type gitopsBlock struct {
	repo          *gitopstest.Repository
	notes         *gitopstest.Notifications
	reports       *gitopstest.NodeReports
	repository    func(ctx context.Context, d contract.Desired) (gitops.Repository, error)
	notifications gitops.Notifications
}

func newNamespaceBlock() *gitopsBlock {
	b := &gitopsBlock{repo: gitopstest.NewRepository(), notes: gitopstest.NewNotifications(), reports: gitopstest.NewNodeReports()}
	for _, app := range []string{"app-alpha", "app-beta"} {
		b.notes.SucceedCommits(app, b.repo)
		b.reports.AppliesHead(alphaCluster, app, b.repo)
	}
	b.notifications = b.notes
	b.repository = func(context.Context, contract.Desired) (gitops.Repository, error) { return b.repo, nil }
	return b
}

func (b *gitopsBlock) deployment(ctx context.Context, d contract.Desired, app string, files map[string][]byte) (gitops.Deployment, error) {
	repo, err := b.repository(ctx, d)
	if err != nil {
		return gitops.Deployment{}, err
	}
	return gitops.Deployment{
		Repository: repo, Notifications: b.notifications, NodeReports: b.reports,
		Desired: d, Application: app, Cluster: alphaCluster, Paths: slices.Sorted(maps.Keys(files)),
	}, nil
}

func (b *gitopsBlock) head(t *testing.T) string {
	t.Helper()
	head, err := b.repo.Head(context.Background())
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	return head
}
