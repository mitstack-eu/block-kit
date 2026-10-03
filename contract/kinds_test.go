package contract_test

// kinds_test.go pins the kinds the catalogue releases. A kind is an interface
// of outputs: it names required and optional outputs and marks each one secret
// or not. Outputs belong to kinds alone, so a type produces its kind's required
// outputs, may produce its optional ones and names no other output.

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/mitstack-eu/operator-kit/contract"
)

// out and secretOut build one kind output, so the pinned table reads as a list.
func out(name string) contract.KindOutput       { return contract.KindOutput{Name: name} }
func secretOut(name string) contract.KindOutput { return contract.KindOutput{Name: name, Secret: true} }

// kindRow is one released kind: its id, its required outputs and its optional
// outputs, each with its secret mark. A nil list means the kind has none.
type kindRow struct {
	id                 string
	required, optional []contract.KindOutput
}

// kindTable pins every kind the kit releases, so adding, removing or changing
// a kind means changing this table.
var kindTable = []kindRow{
	{"api-token", nil, nil},
	{"app-address", []contract.KindOutput{out("appHostname"), out("appUrl")}, nil},
	{"block-volume", nil, []contract.KindOutput{out("dataSnapshotId")}},
	{"change-request", []contract.KindOutput{out("pullRequestUrl")}, nil},
	{"ci-runner", []contract.KindOutput{out("runsOn")}, []contract.KindOutput{out("awsCredential"), out("runnerLabel")}},
	{"ci-variable", nil, nil},
	{"ci-workflow", []contract.KindOutput{out("workflowPath")}, []contract.KindOutput{out("ciCheckContribution"), out("workflowUrl")}},
	{"container", nil, []contract.KindOutput{out("url")}},
	{"container-image", []contract.KindOutput{out("imageRepo")}, nil},
	{"container-repository", []contract.KindOutput{out("registryHost")}, nil},
	{"egress-proxy", nil, nil},
	{"firewall", nil, nil},
	{"git-repository", []contract.KindOutput{out("repo")}, nil},
	{"host", []contract.KindOutput{out("execKind"), out("execTarget"), out("hostId")}, []contract.KindOutput{out("cloud.region"), out("host.type"), out("publicIp")}},
	{"host-configuration", nil, nil},
	{"iam-role", []contract.KindOutput{secretOut("roleArn")}, nil},
	{"ip-address", []contract.KindOutput{out("publicIp")}, nil},
	{"kubernetes-cluster", []contract.KindOutput{out("apiEndpoint"), out("caCertificate"), secretOut("clusterCredential")}, []contract.KindOutput{out("cloud.region"), out("issuerUrl"), out("privateEndpoint")}},
	{"llm-endpoint", []contract.KindOutput{out("llmEndpoint")}, []contract.KindOutput{out("model")}},
	{"network", nil, nil},
	{"oidc-provider", []contract.KindOutput{secretOut("oidcProviderArn")}, nil},
	{"postgres-database", []contract.KindOutput{out("dbName"), out("host"), secretOut("password"), out("port"), out("username")}, nil},
	{"reconcile-claim", []contract.KindOutput{out("claim")}, []contract.KindOutput{out("driftPolicy"), out("driftedResources"), out("reconciledResources")}},
	{"runner-group", nil, nil},
	{"scaffold-contribution", []contract.KindOutput{out("scaffoldContribution")}, nil},
	{"secret", nil, nil},
	{"source-revision", []contract.KindOutput{out("sourceSha")}, []contract.KindOutput{out("sourceBranch")}},
	{"ssh-key", nil, nil},
	{"webhook", nil, nil},
}

// sortedOutputs returns ks sorted by name as a non-nil list, so two lists
// compare as sets.
func sortedOutputs(ks []contract.KindOutput) []contract.KindOutput {
	s := append([]contract.KindOutput{}, ks...)
	slices.SortFunc(s, func(a, b contract.KindOutput) int { return strings.Compare(a.Name, b.Name) })
	return s
}

// kinds returns contract.Kinds(), failing the test when it is empty so a check
// over every kind cannot pass by having none to check.
func kinds(t *testing.T) []contract.Kind {
	t.Helper()
	ks := contract.Kinds()
	if len(ks) == 0 {
		t.Fatal("contract.Kinds() is empty")
	}
	return ks
}

func TestKinds_HoldExactlyThePinnedKinds(t *testing.T) {
	if got := len(kinds(t)); got != len(kindTable) {
		t.Errorf("Kinds() holds %d kinds, want %d: a kind the table does not pin was added, or a pinned one is missing", got, len(kindTable))
	}
	for _, row := range kindTable {
		t.Run(row.id, func(t *testing.T) {
			k, ok := contract.LookupKind(row.id)
			if !ok {
				t.Fatalf("Kinds() has no %q", row.id)
			}
			if k.ID != row.id {
				t.Errorf("ID = %q, want %q", k.ID, row.id)
			}
			if k.Required == nil || k.Optional == nil {
				t.Errorf("Required, Optional = %#v, %#v; want empty lists, never nil", k.Required, k.Optional)
			}
			if got, want := sortedOutputs(k.Required), sortedOutputs(row.required); !reflect.DeepEqual(got, want) {
				t.Errorf("Required = %+v, want %+v", k.Required, want)
			}
			if got, want := sortedOutputs(k.Optional), sortedOutputs(row.optional); !reflect.DeepEqual(got, want) {
				t.Errorf("Optional = %+v, want %+v", k.Optional, want)
			}
		})
	}
}

func TestKinds_HaveNoDeploymentKind(t *testing.T) {
	if _, ok := contract.LookupKind("deployment"); ok {
		t.Error(`LookupKind("deployment") found a kind; a CD registration is its operator's bookkeeping, not a kind`)
	}
}

func TestKinds_AreSortedByIDWithEachIDOnce(t *testing.T) {
	ks := kinds(t)
	for i := 1; i < len(ks); i++ {
		if ks[i-1].ID >= ks[i].ID {
			t.Errorf("kind %d (%q) must sort strictly before kind %d (%q)", i-1, ks[i-1].ID, i, ks[i].ID)
		}
	}
}

func TestKinds_ReturnsCopiesACallerCanMutate(t *testing.T) {
	before := contract.Kinds()
	snapshot := make([]contract.Kind, len(before))
	for i, k := range before {
		k.Required = append([]contract.KindOutput{}, k.Required...)
		k.Optional = append([]contract.KindOutput{}, k.Optional...)
		snapshot[i] = k
	}
	got := kinds(t)
	for i := range got {
		for j := range got[i].Required {
			got[i].Required[j] = contract.KindOutput{Name: "mutated", Secret: true}
		}
		for j := range got[i].Optional {
			got[i].Optional[j] = contract.KindOutput{Name: "mutated", Secret: true}
		}
		got[i].ID = "mutated"
	}
	if after := contract.Kinds(); !reflect.DeepEqual(after, snapshot) {
		t.Error("mutating what Kinds returned changed a later call's result")
	}
}

func TestLookupKind_FindsEveryKindAndReturnsACopy(t *testing.T) {
	for _, k := range kinds(t) {
		got, ok := contract.LookupKind(k.ID)
		if !ok || !reflect.DeepEqual(got, k) {
			t.Errorf("LookupKind(%q) = %+v, %v; want %+v", k.ID, got, ok, k)
		}
	}
	host, _ := contract.LookupKind("host")
	host.Required[0] = contract.KindOutput{Name: "mutated"}
	if again, _ := contract.LookupKind("host"); again.Required[0].Name == "mutated" {
		t.Error("mutating a looked-up kind changed the released kind")
	}
}

func TestLookupKind_UnknownOrMiscasedIDIsNotFound(t *testing.T) {
	for _, id := range []string{"", "Host", "hosts", "app-host", "kube-cluster", "deployment"} {
		if k, ok := contract.LookupKind(id); ok {
			t.Errorf("LookupKind(%q) found %q, want not found", id, k.ID)
		}
	}
}

func TestValidateKinds_AcceptsTheReleasedKinds(t *testing.T) {
	if err := contract.ValidateKinds(kinds(t)); err != nil {
		t.Errorf("ValidateKinds(Kinds()) = %v, want nil", err)
	}
}

// validKind returns a fresh kind every rule accepts, for a test to mutate.
func validKind() contract.Kind {
	return contract.Kind{
		ID:       "thing",
		Required: []contract.KindOutput{out("hostId")},
		Optional: []contract.KindOutput{out("publicIp")},
	}
}

func TestValidateKinds_RefusesADefectNamingTheKind(t *testing.T) {
	cases := []struct {
		name, rule string
		mutate     func(k *contract.Kind)
	}{
		{"kind id not in lowercase kebab case", "ID", func(k *contract.Kind) { k.ID = "Thing_Kind" }},
		{"nil required outputs", "required", func(k *contract.Kind) { k.Required = nil }},
		{"nil optional outputs", "optional", func(k *contract.Kind) { k.Optional = nil }},
		{"output the registry does not hold", "noSuchOutput", func(k *contract.Kind) {
			k.Optional = []contract.KindOutput{out("noSuchOutput")}
		}},
		{"output named by a registry alias", "region", func(k *contract.Kind) {
			k.Optional = []contract.KindOutput{out("region")}
		}},
		{"output listed twice in required", "hostId", func(k *contract.Kind) {
			k.Required = []contract.KindOutput{out("hostId"), out("hostId")}
		}},
		{"output both required and optional", "publicIp", func(k *contract.Kind) {
			k.Required = []contract.KindOutput{out("hostId"), out("publicIp")}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k := validKind()
			c.mutate(&k)
			err := contract.ValidateKinds([]contract.Kind{k})
			if err == nil {
				t.Fatalf("ValidateKinds(%+v) = nil, want an error", k)
			}
			if !strings.Contains(err.Error(), k.ID) {
				t.Errorf("error %q does not name the kind %q", err, k.ID)
			}
			if rest := strings.ReplaceAll(err.Error(), k.ID, ""); !strings.Contains(rest, c.rule) {
				t.Errorf("error %q does not carry %q", err, c.rule)
			}
		})
	}
}

func TestValidateKinds_RefusesAKindListedTwice(t *testing.T) {
	err := contract.ValidateKinds([]contract.Kind{validKind(), validKind()})
	if err == nil || !strings.Contains(err.Error(), "thing") {
		t.Errorf("ValidateKinds with kind thing twice = %v, want an error naming %q", err, "thing")
	}
}

// One output name carries one secret mark, whichever kind names it, so the
// mark a reader finds never depends on which kind it looked at.
func TestValidateKinds_RefusesAnOutputMarkedSecretInOneKindAndNotInAnother(t *testing.T) {
	a := validKind()
	b := validKind()
	b.ID = "other-thing"
	b.Required = []contract.KindOutput{secretOut("hostId")}
	b.Optional = []contract.KindOutput{}
	err := contract.ValidateKinds([]contract.Kind{a, b})
	if err == nil {
		t.Fatal("ValidateKinds with hostId secret in other-thing and not in thing = nil, want an error")
	}
	for _, want := range []string{"hostId", "thing", "other-thing", "secret"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// Every output a kind names is a canonical entry of the resource registry, so
// its meaning, shape and example are written once.
func TestKinds_EveryOutputIsACanonicalRegistryEntry(t *testing.T) {
	for _, k := range kinds(t) {
		for _, o := range append(append([]contract.KindOutput{}, k.Required...), k.Optional...) {
			r, ok := contract.LookupResource(o.Name)
			if !ok || r.Name != o.Name {
				t.Errorf("kind %q names output %q, which is not a canonical registry entry", k.ID, o.Name)
			}
		}
	}
}

// The secret mark of an output sits on its kind. The registry entry of a name
// a kind declares carries the same mark, so no reader of either finds another.
func TestKinds_EverySecretMarkSitsOnTheKindAndTheRegistryAgrees(t *testing.T) {
	sawSecret := false
	for _, k := range kinds(t) {
		for _, o := range append(append([]contract.KindOutput{}, k.Required...), k.Optional...) {
			sawSecret = sawSecret || o.Secret
			r, ok := contract.LookupResource(o.Name)
			if !ok {
				continue
			}
			if r.Secret != o.Secret {
				t.Errorf("kind %q marks %q secret=%v, but its registry entry carries secret=%v", k.ID, o.Name, o.Secret, r.Secret)
			}
			if o.Secret && r.Example != "" {
				t.Errorf("kind %q marks %q secret, but its registry entry carries the example %q", k.ID, o.Name, r.Example)
			}
		}
	}
	if !sawSecret {
		t.Error("no kind marks any output secret; the iam-role, oidc-provider, postgres-database and kubernetes-cluster kinds each mark one")
	}
}

func TestKinds_TheKubernetesClusterKindPublishesItsEndpointCAAndASecretCredential(t *testing.T) {
	k, ok := contract.LookupKind("kubernetes-cluster")
	if !ok {
		t.Fatal(`LookupKind("kubernetes-cluster") found nothing`)
	}
	want := map[string]bool{"apiEndpoint": false, "caCertificate": false, "clusterCredential": true}
	for name, secret := range want {
		i := slices.IndexFunc(k.Required, func(o contract.KindOutput) bool { return o.Name == name })
		if i < 0 {
			t.Errorf("kubernetes-cluster does not require %q", name)
			continue
		}
		if k.Required[i].Secret != secret {
			t.Errorf("kubernetes-cluster marks %q secret=%v, want %v", name, k.Required[i].Secret, secret)
		}
	}
}

func TestFailureClasses_AreExactlyPermanentAndTransient(t *testing.T) {
	assertVocabulary(t, "FailureClasses", contract.FailureClasses, []string{"permanent", "transient"})
}

func TestKindOutput_MarshalsNameAndSecret(t *testing.T) {
	m := marshalToMap(t, contract.KindOutput{Name: "roleArn", Secret: true})
	if want := map[string]any{"name": "roleArn", "secret": true}; !reflect.DeepEqual(m, want) {
		t.Errorf("KindOutput marshals to %v, want %v", m, want)
	}
	m = marshalToMap(t, contract.KindOutput{Name: "repo"})
	if want := map[string]any{"name": "repo", "secret": false}; !reflect.DeepEqual(m, want) {
		t.Errorf("KindOutput marshals to %v, want %v: the secret mark is always written", m, want)
	}
}
