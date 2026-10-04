package gitops_test

// fixture_test.go is the test block every Resource test drives: one
// kubernetes.namespace, declared in testdata/kubernetes-namespace, served by
// a gitops.Resource that renders a Namespace and a ServiceAccount for the
// app into the cluster's GitOps repository and publishes the namespace and
// serviceAccount outputs the catalogue requires.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
	"github.com/mitstack-eu/operator-kit/gitops"
	"github.com/mitstack-eu/operator-kit/gitops/gitopstest"
)

const (
	resourceName = "app-namespace"
	clusterInput = `"k3s-fsn1"`

	githubToken   = "ghs_platformMintedInstallationToken0123"
	secretInput   = "secret-input-value-4567"
	appSecret     = "app-secret-value-89ab"
	hetznerSecret = "hetzner-api-token-cdef"

	alphaNamespacePath      = "apps/app-alpha/namespace.yaml"
	alphaServiceAccountPath = "apps/app-alpha/serviceaccount.yaml"
	alphaPaths              = alphaNamespacePath + "," + alphaServiceAccountPath
)

// seededSecrets is every secret value a Desired from desiredFor carries.
var seededSecrets = []string{githubToken, secretInput, appSecret, hetznerSecret}

func readNamespaceManifest(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/kubernetes-namespace/manifest.yaml")
	if err != nil {
		t.Fatalf("reading the test block's manifest: %v", err)
	}
	return raw
}

// desiredFor is the Desired a request naming the app's namespace carries:
// the subscription, the recorded cluster input and every kind of secret.
func desiredFor(app string) contract.Desired {
	return contract.Desired{
		Identity: contract.Identity{
			SubscriptionID: "sub-" + app,
			BlockID:        "gitops-app-namespace",
			BlockVersion:   "1.0.0",
			OrgID:          "org-1",
			AppID:          app,
		},
		Params:       map[string]string{"appName": app},
		SecretInputs: map[string]string{"dbPassword": secretInput},
		AppSecrets:   map[string]string{"STRIPE_KEY": appSecret},
		Secrets: map[string]map[string]string{
			"github":  {"token": githubToken},
			"hetzner": {"apiToken": hetznerSecret},
		},
		Resource:       resourceName,
		ResourceInputs: map[string]json.RawMessage{"cluster": json.RawMessage(clusterInput)},
	}
}

// renderNamespace renders the app's Namespace and ServiceAccount from the
// recorded cluster input and the app id alone, so a Desired rebuilt from
// the subscription's record renders the same files.
func renderNamespace(d contract.Desired) (gitops.Rendered, error) {
	raw, ok := d.ResourceInputs["cluster"]
	if !ok {
		return gitops.Rendered{}, errors.New("no cluster input recorded")
	}
	var cluster string
	if err := json.Unmarshal(raw, &cluster); err != nil {
		return gitops.Rendered{}, fmt.Errorf("cluster input: %w", err)
	}
	if d.AppID == "" {
		return gitops.Rendered{}, errors.New("no app id")
	}
	return renderApp(d.AppID, cluster), nil
}

func renderApp(app, cluster string) gitops.Rendered {
	sa := app + "-runtime"
	return gitops.Rendered{
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

// gitopsBlock is the test block's resource over one fake repository and one
// fake notification source.
type gitopsBlock struct {
	repo     *gitopstest.Repository
	notes    *gitopstest.Notifications
	resource *gitops.Resource
}

// newBlock builds the block with render, over a notification source that
// answers Succeeded and Healthy for every revision of each autoApps app.
func newBlock(render func(contract.Desired) (gitops.Rendered, error), autoApps ...string) *gitopsBlock {
	b := &gitopsBlock{repo: gitopstest.NewRepository(), notes: gitopstest.NewNotifications()}
	for _, app := range autoApps {
		b.notes.SucceedCommits(app, b.repo)
	}
	b.resource = &gitops.Resource{
		Repository: func(context.Context, contract.Desired) (gitops.Repository, error) {
			return b.repo, nil
		},
		Notifications: func(context.Context, contract.Desired) (gitops.Notifications, error) {
			return b.notes, nil
		},
		Render: render,
	}
	return b
}

// newNamespaceBlock is the namespace block whose deploys of app-alpha and
// app-beta all succeed.
func newNamespaceBlock() *gitopsBlock {
	return newBlock(renderNamespace, "app-alpha", "app-beta")
}

func (b *gitopsBlock) head(t *testing.T) string {
	t.Helper()
	head, err := b.repo.Head(context.Background())
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	return head
}
