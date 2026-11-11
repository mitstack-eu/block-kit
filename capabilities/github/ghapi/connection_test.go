package ghapi_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/mitstack-eu/block-kit/capabilities/github/ghapi"
	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/verify/kittest"
	"github.com/mitstack-eu/block-kit/webhook"
)

func TestOwnerAndAppID_ReadTheConnectionsConfigFirstAndTheSecretGroupOnlyWhenTheConfigLacksTheValue(t *testing.T) {
	const token = "ghs_connection-token-5e7a"
	inConfig := func(config map[string]string) map[string]contract.InstanceConfig {
		return map[string]contract.InstanceConfig{"github": {Name: "GitHub", Endpoint: "https://api.github.com", Config: config}}
	}
	inSecrets := func(owner, appID string) map[string]map[string]string {
		return map[string]map[string]string{"github": {"token": token, "owner": owner, "appId": appID}}
	}
	tokenAlone := map[string]map[string]string{"github": {"token": token}}
	for _, tc := range []struct {
		name         string
		d            contract.Desired
		owner, appID string
	}{
		{name: "a request carrying both only in the config returns the config's values",
			d:     contract.Desired{Identity: contract.Identity{Instances: inConfig(map[string]string{"owner": "acme", "appId": "1234567"})}, Secrets: tokenAlone},
			owner: "acme", appID: "1234567"},
		{name: "a request carrying both only in the secret group returns the secret group's values",
			d: contract.Desired{Secrets: inSecrets("acme", "1234567")}, owner: "acme", appID: "1234567"},
		{name: "a request carrying both in both places returns the config's values",
			d: contract.Desired{Identity: contract.Identity{Instances: inConfig(map[string]string{"owner": "acme", "appId": "1234567"})},
				Secrets: inSecrets("other-account", "7654321")},
			owner: "acme", appID: "1234567"},
		{name: "a config holding each as an empty value falls back to the secret group",
			d: contract.Desired{Identity: contract.Identity{Instances: inConfig(map[string]string{"owner": "", "appId": ""})},
				Secrets: inSecrets("acme", "1234567")},
			owner: "acme", appID: "1234567"},
		{name: "a config holding only another setting falls back to the secret group",
			d: contract.Desired{Identity: contract.Identity{Instances: inConfig(map[string]string{"visibility": "private"})},
				Secrets: inSecrets("acme", "1234567")},
			owner: "acme", appID: "1234567"},
		{name: "a github instance with no config falls back to the secret group",
			d:     contract.Desired{Identity: contract.Identity{Instances: inConfig(nil)}, Secrets: inSecrets("acme", "1234567")},
			owner: "acme", appID: "1234567"},
		{name: "a request carrying the owner in the config and the App id in the secret group returns each from where it is",
			d: contract.Desired{Identity: contract.Identity{Instances: inConfig(map[string]string{"owner": "acme"})},
				Secrets: map[string]map[string]string{"github": {"token": token, "appId": "1234567"}}},
			owner: "acme", appID: "1234567"},
		{name: "a request carrying a token and neither value returns neither, and never the token",
			d: contract.Desired{Identity: contract.Identity{Instances: inConfig(nil)}, Secrets: tokenAlone}},
		{name: "a request with no GitHub connection returns neither", d: contract.Desired{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner, appID := ghapi.Owner(tc.d), ghapi.AppID(tc.d)

			if owner != tc.owner {
				t.Errorf("Owner(instances %v) = %q; want %q", tc.d.Instances, owner, tc.owner)
			}
			if appID != tc.appID {
				t.Errorf("AppID(instances %v) = %q; want %q", tc.d.Instances, appID, tc.appID)
			}
		})
	}
}

const (
	namedOwner          = "acme-widgets"
	namedToken          = "ghs_owner-helper-leak-4d1e"
	ownerNamingManifest = "apiVersion: mitstack/v1beta\ntype: block\nmetadata:\n  id: owner-naming\n  name: owner-naming\nspec:\n  version: 1.0.0\n  resources:\n    - { name: review, type: aws.iam-role }\n" +
		"  lifecycle:\n    endpoint: https://owner-naming.example/hook\n    operations: [subscribe, unsubscribe, upgrade, validate]\n    contract: kit/v1\n"
)

type ownerNaming struct{ named *[]string }

func (b ownerNaming) names(d contract.Desired) string {
	text := "https://github.com/" + ghapi.Owner(d) + "/widget/pull/1 with " + d.Secrets["github"]["token"]
	*b.named = append(*b.named, text)
	return text
}

func (b ownerNaming) Ensure(_ context.Context, d contract.Desired) error {
	return errors.New("GitHub refused " + b.names(d))
}

func (ownerNaming) Delete(context.Context, contract.Desired) (map[string]string, error) {
	return nil, nil
}

func (b ownerNaming) ObserveResource(_ context.Context, r contract.ResourceRef) (contract.Observation, error) {
	return contract.Observation{State: contract.Unknown, WaitingOnPerson: &contract.WaitingOnPerson{Action: "approve " + b.names(r.Desired)}}, nil
}

func TestOwner_ABlockNamingTheOwnerInAWaitAndAFailedSubscribeLeaksNoValueTheSecretGroupCarries(t *testing.T) {
	const naming = "https://github.com/" + namedOwner + "/widget/pull/1 with " + namedToken
	inConfig := map[string]any{"github": map[string]any{"name": "GitHub", "endpoint": "https://api.github.com", "config": map[string]any{"owner": namedOwner}}}
	for _, tc := range []struct {
		name    string
		secrets map[string]any
		seeded  []string
	}{
		{name: "the owner in the config and in the secret group stays scrubbed beside the token",
			secrets: map[string]any{"token": namedToken, "owner": namedOwner}, seeded: []string{namedToken, namedOwner}},
		{name: "the owner in the config alone leaves the token scrubbed",
			secrets: map[string]any{"token": namedToken}, seeded: []string{namedToken}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var named []string
			body := func(operation string) map[string]any {
				return map[string]any{"operation": operation, "subscriptionId": "sub-1", "blockId": "owner-naming", "orgId": "org-1",
					"instances": inConfig, "secrets": map[string]any{"github": tc.secrets},
					"recordedResources": map[string]any{"review": map[string]any{"type": "aws.iam-role", "identity": map[string]any{"roleName": "review"}}}}
			}

			problems := kittest.CheckNoSecretLeak(kittest.SecretLeak{
				Secrets: tc.seeded,
				Serve: func(logs *webhook.LogBuffer) (http.Handler, *kittest.Signer) {
					s := kittest.NewSigner(t, "owner-naming-key", 7)
					h, err := handler.New(handler.Config{Resource: ownerNaming{&named}, Manifest: []byte(ownerNamingManifest),
						Observers: map[string]handler.Observer{"review": ownerNaming{&named}}, JWKSURL: s.JWKSServer(t), Logs: logs})
					if err != nil {
						t.Fatalf("handler.New: %v", err)
					}
					return h, s
				},
				Requests: []kittest.LeakRequest{{Name: "validate", Body: body("validate")}, {Name: "subscribe", Body: body("subscribe"), Fails: true}},
			})

			if len(problems) != 0 {
				t.Errorf("the handler's answers and logs leak: %v", problems)
			}
			if len(named) != 2 || named[0] != naming || named[1] != naming {
				t.Errorf("the block named %q; want %q in the validate's wait and in the subscribe's error, so the check reads a block that named both", named, naming)
			}
		})
	}
}
