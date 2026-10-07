package contract

import (
	"cmp"
	"fmt"
	"slices"
)

// KindOutput is one output a kind names, with its secret mark.
type KindOutput struct {
	Name   string `json:"name"`
	Secret bool   `json:"secret"`
}

// Kind is one released kind, the only owner of outputs: a type produces its kind's required outputs and may produce its optional ones.
type Kind struct {
	ID string
	// Required and Optional are never nil.
	Required []KindOutput
	Optional []KindOutput
}

// clone keeps an empty list empty rather than nil.
func (k Kind) clone() Kind {
	k.Required = append([]KindOutput{}, k.Required...)
	k.Optional = append([]KindOutput{}, k.Optional...)
	return k
}

func (k Kind) outputs() []KindOutput {
	return append(append([]KindOutput{}, k.Required...), k.Optional...)
}

func (k Kind) names(name string) bool {
	return slices.ContainsFunc(k.outputs(), func(o KindOutput) bool { return o.Name == name })
}

// Kinds returns fresh copies of every released kind, sorted by ID.
func Kinds() []Kind {
	out := make([]Kind, len(kindCatalogue))
	for i, k := range kindCatalogue {
		out[i] = k.clone()
	}
	slices.SortFunc(out, func(a, b Kind) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

// LookupKind returns a copy of the released kind whose ID is id, matched case-sensitively.
func LookupKind(id string) (Kind, bool) {
	for _, k := range kindCatalogue {
		if k.ID == id {
			return k.clone(), true
		}
	}
	return Kind{}, false
}

// ValidateKinds returns the first defect it finds in ks, or nil; one output name carries one secret mark in every kind that names it.
func ValidateKinds(ks []Kind) error {
	seen := make(map[string]bool, len(ks))
	type mark struct {
		secret bool
		kind   string
	}
	secretMark := map[string]mark{}
	for _, k := range ks {
		if err := validateKind(k); err != nil {
			return fmt.Errorf("kind %q: %w", k.ID, err)
		}
		if seen[k.ID] {
			return fmt.Errorf("kind %q: listed more than once", k.ID)
		}
		seen[k.ID] = true
		for _, o := range k.outputs() {
			first, ok := secretMark[o.Name]
			if !ok {
				secretMark[o.Name] = mark{o.Secret, k.ID}
				continue
			}
			if first.secret != o.Secret {
				return fmt.Errorf("kind %q: output %q is marked secret=%v, but kind %q marks it secret=%v; an output carries one secret mark",
					k.ID, o.Name, o.Secret, first.kind, first.secret)
			}
		}
	}
	return nil
}

func validateKind(k Kind) error {
	if !isKebab(k.ID) {
		return fmt.Errorf("ID is not lowercase kebab case")
	}
	if k.Required == nil {
		return fmt.Errorf("required outputs list is nil, want an empty list when the kind requires none")
	}
	if k.Optional == nil {
		return fmt.Errorf("optional outputs list is nil, want an empty list when the kind names none")
	}
	required := make(map[string]bool, len(k.Required))
	for _, o := range k.Required {
		if !isCanonicalResourceName(o.Name) {
			return fmt.Errorf("output %q is not a canonical resource registry name", o.Name)
		}
		if required[o.Name] {
			return fmt.Errorf("output %q listed more than once", o.Name)
		}
		required[o.Name] = true
	}
	optional := make(map[string]bool, len(k.Optional))
	for _, o := range k.Optional {
		if !isCanonicalResourceName(o.Name) {
			return fmt.Errorf("output %q is not a canonical resource registry name", o.Name)
		}
		if required[o.Name] {
			return fmt.Errorf("output %q is both required and optional", o.Name)
		}
		if optional[o.Name] {
			return fmt.Errorf("output %q listed more than once", o.Name)
		}
		optional[o.Name] = true
	}
	return nil
}

func plain(name string) KindOutput  { return KindOutput{Name: name} }
func secret(name string) KindOutput { return KindOutput{Name: name, Secret: true} }

// noOutputs is shared by every kind, so clone must copy it.
var noOutputs = []KindOutput{}

var kindCatalogue = []Kind{
	{ID: "api-token", Required: noOutputs, Optional: noOutputs},
	{ID: "app-address", Required: []KindOutput{plain("appHostname"), plain("appUrl")}, Optional: noOutputs},
	{ID: "block-volume", Required: noOutputs, Optional: []KindOutput{plain("dataSnapshotId")}},
	{ID: "change-request", Required: []KindOutput{plain("pullRequestUrl")}, Optional: noOutputs},
	{ID: "ci-runner", Required: []KindOutput{plain("runsOn")}, Optional: []KindOutput{plain("awsCredential"), plain("runnerLabel")}},
	{ID: "ci-variable", Required: noOutputs, Optional: noOutputs},
	{ID: "ci-workflow", Required: []KindOutput{plain("workflowPath")}, Optional: []KindOutput{plain("ciCheckContribution"), plain("workflowUrl")}},
	{ID: "container", Required: noOutputs, Optional: []KindOutput{plain("url")}},
	{ID: "container-image", Required: []KindOutput{plain("imageRepo")}, Optional: noOutputs},
	{ID: "container-repository", Required: []KindOutput{plain("registryHost")}, Optional: noOutputs},
	{ID: "egress-proxy", Required: noOutputs, Optional: noOutputs},
	{ID: "firewall", Required: noOutputs, Optional: noOutputs},
	{ID: "git-repository", Required: []KindOutput{plain("repo")}, Optional: noOutputs},
	{ID: "host",
		Required: []KindOutput{plain("execKind"), plain("execTarget"), plain("hostId")},
		Optional: []KindOutput{plain("cloud.region"), plain("host.type"), plain("publicIp")}},
	{ID: "host-configuration", Required: noOutputs, Optional: noOutputs},
	{ID: "iam-role", Required: []KindOutput{secret("roleArn")}, Optional: noOutputs},
	{ID: "ip-address", Required: []KindOutput{plain("publicIp")}, Optional: noOutputs},
	{ID: "kubernetes-cluster",
		Required: []KindOutput{plain("apiEndpoint"), plain("caCertificate"), secret("clusterCredential")},
		Optional: []KindOutput{plain("cloud.region"), plain("issuerUrl"), plain("privateEndpoint")}},
	{ID: "kubernetes-namespace", Required: []KindOutput{plain("namespace"), plain("serviceAccount")}, Optional: noOutputs},
	{ID: "llm-endpoint", Required: []KindOutput{plain("llmEndpoint")}, Optional: []KindOutput{plain("model")}},
	{ID: "network", Required: noOutputs, Optional: noOutputs},
	{ID: "oidc-provider", Required: []KindOutput{secret("oidcProviderArn")}, Optional: noOutputs},
	{ID: "postgres-database",
		Required: []KindOutput{plain("dbName"), plain("host"), secret("password"), plain("port"), plain("username")},
		Optional: noOutputs},
	{ID: "reconcile-claim",
		Required: []KindOutput{plain("claim")},
		Optional: []KindOutput{plain("driftPolicy"), plain("driftedResources"), plain("reconciledResources")}},
	{ID: "runner-group", Required: noOutputs, Optional: noOutputs},
	{ID: "scaffold-contribution", Required: []KindOutput{plain("scaffoldContribution")}, Optional: noOutputs},
	{ID: "secret", Required: noOutputs, Optional: noOutputs},
	{ID: "source-revision", Required: []KindOutput{plain("sourceSha")}, Optional: []KindOutput{plain("sourceBranch")}},
	{ID: "ssh-key", Required: noOutputs, Optional: noOutputs},
	{ID: "webhook", Required: noOutputs, Optional: noOutputs},
}
