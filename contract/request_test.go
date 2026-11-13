package contract_test

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/verify/kittest"
)

func TestDecodeRequest_DecodesEveryValidGoldenViaKittest(t *testing.T) {
	kittest.AssertDecodesEveryValidGolden(t, func(raw []byte) error {
		_, err := contract.DecodeRequest(raw)
		return err
	}, "operation", "nonce", "createdAt", "subscriptionId", "blockId", "orgId")
}

func strOf(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func strMap(v any) map[string]string {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, e := range m {
		out[k], _ = e.(string)
	}
	return out
}

func strMaps(v any) map[string]map[string]string {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]map[string]string, len(m))
	for k, e := range m {
		out[k] = strMap(e)
	}
	return out
}

// requestFromGolden builds a Request from the golden's raw keys by hand, so a field that fails to reach the typed Request shows as a difference.
func requestFromGolden(m map[string]any) contract.Request {
	r := contract.Request{
		Operation: strOf(m, "operation"), SubscriptionID: strOf(m, "subscriptionId"), IdempotencyKey: strOf(m, "idempotencyKey"),
		BlockID: strOf(m, "blockId"), BlockVersion: strOf(m, "blockVersion"), OrgID: strOf(m, "orgId"), AppID: strOf(m, "appId"),
		OBO: strOf(m, "obo"), Nonce: strOf(m, "nonce"),
		RunID: strOf(m, "runId"), Command: strOf(m, "command"), Process: strOf(m, "process"),
		Unit: strOf(m, "unit"), Replacing: m["replacing"] == true,
		Params: strMap(m["params"]), SecretInputs: strMap(m["secretInputs"]), AppSecrets: strMap(m["appSecrets"]),
		PriorOutputs: strMap(m["priorOutputs"]), Secrets: strMaps(m["secrets"]), Bindings: strMaps(m["bindings"]),
	}
	if instances, ok := m["instances"].(map[string]any); ok {
		r.Instances = map[string]contract.InstanceConfig{}
		for k, v := range instances {
			o, _ := v.(map[string]any)
			r.Instances[k] = contract.InstanceConfig{Name: strOf(o, "name"), Endpoint: strOf(o, "endpoint"), Config: strMap(o["config"])}
		}
	}
	if all, ok := m["bindingsAll"].(map[string]any); ok {
		r.BindingsAll = map[string][]map[string]string{}
		for k, v := range all {
			list, _ := v.([]any)
			r.BindingsAll[k] = []map[string]string{}
			for _, e := range list {
				r.BindingsAll[k] = append(r.BindingsAll[k], strMap(e))
			}
		}
	}
	if ci, ok := m["cloudIdentity"].(map[string]any); ok {
		r.CloudIdentity = &contract.CloudIdentity{Provider: strOf(ci, "provider"), RoleARN: strOf(ci, "roleArn"),
			WebIdentityToken: strOf(ci, "webIdentityToken"), AccountRef: strOf(ci, "accountRef")}
	}
	if abs, ok := m["absences"].(map[string]any); ok {
		r.Absences = map[string]contract.Absence{}
		for k, v := range abs {
			o, _ := v.(map[string]any)
			at, _ := time.Parse(time.RFC3339, strOf(o, "readAbsentAt"))
			r.Absences[k] = contract.Absence{Observer: strOf(o, "observer"), Resource: strOf(o, "resource"), ReadAbsentAt: at, ProvesAbsence: o["provesAbsence"] == true}
		}
	}
	if cb, ok := m["eventsCallback"].(map[string]any); ok {
		r.EventsCallback = &contract.EventsCallback{URL: strOf(cb, "url"), Token: strOf(cb, "token")}
	}
	return r
}

func TestDecodeRequest_CarriesEveryGoldenFieldOntoTheRequest(t *testing.T) {
	valid, err := contract.ValidGoldens()
	if err != nil {
		t.Fatalf("contract.ValidGoldens(): %v", err)
	}
	for name, raw := range valid {
		t.Run(name, func(t *testing.T) {
			m := jsonObject(t, raw)
			wantCreated, err := time.Parse(time.RFC3339, strOf(m, "createdAt"))
			if err != nil {
				t.Fatalf("golden createdAt %q is not RFC 3339: %v", m["createdAt"], err)
			}

			got, err := contract.DecodeRequest(raw)

			if err != nil {
				t.Fatalf("DecodeRequest(%s): %v", name, err)
			}
			if !got.CreatedAt.Equal(wantCreated) {
				t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, wantCreated)
			}
			got.CreatedAt = time.Time{}
			if want := requestFromGolden(m); !reflect.DeepEqual(got, want) {
				t.Errorf("DecodeRequest(%s) =\n%+v\nwant the golden's fields\n%+v", name, got, want)
			}
		})
	}
}

// The encodings live in a golden file because a field added later is optional, so it must leave every golden's encoding as pinned.
func TestDecodeRequest_EveryRequestGoldenDecodesOrIsRefusedExactlyAsPinned(t *testing.T) {
	refusals := map[string]string{
		"invalid-missing-nonce": `contract: envelope field "nonce" is required`,
		"invalid-unknown-verb":  `contract: envelope field "operation" must be one of subscribe, unsubscribe, upgrade, validate, issue, logs, verify-connection, lease, run, snapshot, restore, got "reticulate"`,
	}
	pinned, err := os.ReadFile("testdata/request-golden-encodings.json")
	if err != nil {
		t.Fatal(err)
	}
	var encodings map[string]json.RawMessage
	if err := json.Unmarshal(pinned, &encodings); err != nil {
		t.Fatalf("testdata/request-golden-encodings.json: %v", err)
	}
	valid, verr := contract.ValidGoldens()
	invalid, ierr := contract.InvalidGoldens()
	if verr != nil || ierr != nil {
		t.Fatalf("contract.ValidGoldens(), InvalidGoldens(): %v, %v", verr, ierr)
	}
	all := map[string][]byte{}
	for _, set := range []map[string][]byte{valid, invalid} {
		for name, raw := range set {
			all[name] = raw
			_, encoded := encodings[name]
			if _, refused := refusals[name]; !encoded && !refused {
				t.Errorf("request golden %q has no pinned outcome", name)
			}
		}
	}
	for _, name := range append(sortedKeys(encodings), sortedKeys(refusals)...) {
		t.Run(name, func(t *testing.T) {
			raw, ok := all[name]
			if !ok {
				t.Fatalf("request golden %q is gone, so its pinned outcome can no longer be compared", name)
			}

			req, err := contract.DecodeRequest(raw)

			if want, refused := refusals[name]; refused {
				if err == nil || err.Error() != want {
					t.Errorf("DecodeRequest(%s) error = %v, want %q", name, err, want)
				}
				return
			}
			if err != nil {
				t.Fatalf("DecodeRequest(%s) refused with %q, want it decoded", name, err)
			}
			got, err := json.Marshal(req)
			var want bytes.Buffer
			if err == nil {
				err = json.Compact(&want, encodings[name])
			}
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want.Bytes()) {
				t.Errorf("%s decodes to a Request encoding as\n  %s\nwant\n  %s", name, got, want.Bytes())
			}
		})
	}
}

func TestDecodeRequest_RefusesAMalformedFieldNamingIt(t *testing.T) {
	identity := map[string]string{"groupId": "sg-0abc", "vpcId": "vpc-0abc"}
	without := func(key string) func(m map[string]any) {
		return func(m map[string]any) { delete(m["cloudIdentity"].(map[string]any), key) }
	}
	cases := []struct {
		label  string
		mutate func(m map[string]any)
		wants  []string
	}{
		{"createdAt a number", func(m map[string]any) { m["createdAt"] = 12345 }, []string{"createdAt"}},
		{"createdAt a bool", func(m map[string]any) { m["createdAt"] = true }, []string{"createdAt"}},
		{"createdAt an object", func(m map[string]any) { m["createdAt"] = map[string]any{"x": 1} }, []string{"createdAt"}},
		{"createdAt not RFC 3339", func(m map[string]any) { m["createdAt"] = "not-a-timestamp" }, []string{"createdAt"}},
		{"settlesAt not a time", func(m map[string]any) { m["settlesAt"] = "yesterday" }, []string{`"settlesAt"`}},
		{"settlesAt a number", func(m map[string]any) { m["settlesAt"] = 1752613200 }, []string{`"settlesAt"`}},
		{"neverActive a string", func(m map[string]any) { m["neverActive"] = "true" }, []string{`"neverActive"`}},
		{"settling a string", func(m map[string]any) { m["settling"] = "true" }, []string{`"settling"`}},
		{"own secrets not strings", func(m map[string]any) { m["ownSecrets"] = map[string]any{"gatewayCaKey": 7} }, []string{`"ownSecrets`}},
		{"cloudIdentity without provider", without("provider"), []string{"cloudIdentity.provider"}},
		{"cloudIdentity without roleArn", without("roleArn"), []string{"cloudIdentity.roleArn"}},
		{"cloudIdentity without webIdentityToken", without("webIdentityToken"), []string{"cloudIdentity.webIdentityToken"}},
		{"a resource identity with no resource", func(m map[string]any) { m["resourceIdentity"] = identity },
			[]string{`"resourceIdentity"`, `"resource"`}},
		{"a resource identity with an empty resource", func(m map[string]any) { m["resource"] = ""; m["resourceIdentity"] = identity },
			[]string{`"resourceIdentity"`, `"resource"`}},
		{"a resource type with no resource", func(m map[string]any) { m["resourceType"] = "aws.ec2-instance" },
			[]string{`"resourceType"`, `"resource"`}},
		{"a resource type with an empty resource", func(m map[string]any) { m["resource"] = ""; m["resourceType"] = "aws.ec2-instance" },
			[]string{`"resourceType"`, `"resource"`}},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			raw := mutateSubscribeGolden(t, tc.mutate)

			_, err := contract.DecodeRequest(raw)

			requireErrorNaming(t, "DecodeRequest", err, tc.wants...)
		})
	}
}

func TestDecodeRequest_CarriesWhatTheRequestNamesOntoDesired(t *testing.T) {
	const settles = "2026-07-15T20:00:00Z"
	settlesAt, _ := time.Parse(time.RFC3339, settles)
	identity := map[string]string{"groupId": "sg-0abc", "vpcId": "vpc-0abc"}
	recorded := map[string]contract.RecordedResource{
		"aws.ec2-instance": {Type: "aws.ec2-instance", Identity: map[string]string{"instanceId": "i-0abc"}},
		"app-container":    {Type: "hetzner.server-container", Identity: map[string]string{"serverId": "4711", "container": "app"}},
		"database":         {Type: "aws.ec2-postgres-database", Identity: map[string]string{"instanceId": "i-0abc"}, State: contract.Absent},
	}
	requirementTypes := map[string]string{"host": "hetzner.server", "github.repository": "github.repository"}
	cases := []struct {
		label  string
		mutate func(m map[string]any)
		check  func(t *testing.T, req contract.Request, d contract.Desired)
	}{
		{"an unmodified golden decodes", func(map[string]any) {}, func(t *testing.T, req contract.Request, d contract.Desired) {}},
		{"a property the contract does not declare changes nothing", func(m map[string]any) {
			m["undeclared"] = map[string]any{"id": "b1", "branch": "main"}
		}, func(t *testing.T, req contract.Request, d contract.Desired) {
			wantField(t, "DecodeRequest", req, decodeGolden(t, "subscribe"))
		}},
		{"a named resource is decoded", func(m map[string]any) { m["operation"] = "validate"; m["resource"] = "firewall" },
			func(t *testing.T, req contract.Request, d contract.Desired) {
				wantField(t, "Resource", req.Resource, "firewall")
			}},
		{"a named resource reaches Desired", func(m map[string]any) { m["operation"] = "validate"; m["resource"] = "firewall" },
			func(t *testing.T, req contract.Request, d contract.Desired) {
				wantField(t, "Desired().Resource", d.Resource, "firewall")
			}},
		{"a named resource's stored identity reaches Desired", func(m map[string]any) {
			m["operation"], m["resource"], m["resourceIdentity"] = "validate", "firewall", identity
		}, func(t *testing.T, req contract.Request, d contract.Desired) {
			wantField(t, "ResourceIdentity", req.ResourceIdentity, identity)
			wantField(t, "Desired().Resource", d.Resource, "firewall")
			wantField(t, "Desired().ResourceIdentity", d.ResourceIdentity, identity)
		}},
		{"a named resource's type reaches Desired", func(m map[string]any) {
			m["operation"], m["resource"], m["resourceType"] = "validate", "app-container", "hetzner.server-container"
			m["resourceIdentity"] = map[string]any{"serverId": "4711"}
		}, func(t *testing.T, req contract.Request, d contract.Desired) {
			wantField(t, "ResourceType", req.ResourceType, "hetzner.server-container")
			wantField(t, "Desired() resource, type", []string{d.Resource, d.ResourceType}, []string{"app-container", "hetzner.server-container"})
		}},
		{"a named resource's unsubscribing marker reaches Desired", func(m map[string]any) {
			m["operation"], m["resource"], m["resourceUnsubscribing"] = "validate", "firewall", true
		}, func(t *testing.T, req contract.Request, d contract.Desired) {
			wantField(t, "ResourceUnsubscribing, Desired().ResourceUnsubscribing", []bool{req.ResourceUnsubscribing, d.ResourceUnsubscribing}, []bool{true, true})
		}},
		{"the app's delete marker reaches Desired", func(m map[string]any) {
			m["operation"], m["appDeleting"] = "unsubscribe", true
		}, func(t *testing.T, req contract.Request, d contract.Desired) {
			wantField(t, "AppDeleting, Desired().AppDeleting", []bool{req.AppDeleting, d.AppDeleting}, []bool{true, true})
		}},
		{"recorded resources and requirement types reach Desired, at the request's time", func(m map[string]any) {
			m["operation"], m["recordedResources"], m["requirementTypes"] = "unsubscribe", recorded, requirementTypes
		}, func(t *testing.T, req contract.Request, d contract.Desired) {
			wantField(t, "RecordedResources", req.RecordedResources, recorded)
			wantField(t, "RequirementTypes", req.RequirementTypes, requirementTypes)
			wantField(t, "Desired().RecordedResources", d.RecordedResources, recorded)
			wantField(t, "Desired().RequirementTypes", d.RequirementTypes, requirementTypes)
			if d.VerbAt.IsZero() || !d.VerbAt.Equal(req.CreatedAt) {
				t.Errorf("Desired().VerbAt = %v, want the request's createdAt %v", d.VerbAt, req.CreatedAt)
			}
		}},
		{"a validate without settlesAt settles at its createdAt", func(m map[string]any) { m["operation"] = "validate" },
			func(t *testing.T, req contract.Request, d contract.Desired) {
				if d.VerbAt.IsZero() || !d.VerbAt.Equal(req.CreatedAt) {
					t.Errorf("Desired().VerbAt = %v, want createdAt %v", d.VerbAt, req.CreatedAt)
				}
			}},
		{"a subscription the engine proved never active reaches Desired as never active", func(m map[string]any) { m["neverActive"] = true },
			func(t *testing.T, req contract.Request, d contract.Desired) {
				wantField(t, "NeverActive, Desired().NeverActive", []bool{req.NeverActive, d.NeverActive}, []bool{true, true})
			}},
		{"a subscription's own secrets reach Desired under their exact names", func(m map[string]any) {
			m["operation"], m["ownSecrets"] = "upgrade", map[string]any{"gatewayCaKey": "own-secret-not-a-real-key"}
		}, func(t *testing.T, req contract.Request, d contract.Desired) {
			want := map[string]string{"gatewayCaKey": "own-secret-not-a-real-key"}
			wantField(t, "OwnSecrets, Desired().OwnSecrets", []map[string]string{req.OwnSecrets, d.OwnSecrets}, []map[string]string{want, want})
		}},
		{"a validate the engine marks as settling a verb is decoded as settling", func(m map[string]any) { m["operation"], m["settling"] = "validate", true },
			func(t *testing.T, req contract.Request, d contract.Desired) {
				wantField(t, "Settling", req.Settling, true)
			}},
		{"a validate without the marker settles no verb", func(m map[string]any) { m["operation"], m["settlesAt"] = "validate", settles },
			func(t *testing.T, req contract.Request, d contract.Desired) {
				wantField(t, "Settling", req.Settling, false)
			}},
		{"a validate settles at its settlesAt", func(m map[string]any) { m["operation"], m["settlesAt"] = "validate", settles },
			func(t *testing.T, req contract.Request, d contract.Desired) {
				if !d.VerbAt.Equal(settlesAt) {
					t.Errorf("Desired().VerbAt = %v, want settlesAt %v rather than createdAt %v", d.VerbAt, settlesAt, req.CreatedAt)
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			raw := mutateSubscribeGolden(t, tc.mutate)

			req, err := contract.DecodeRequest(raw)

			if err != nil {
				t.Fatalf("DecodeRequest: %v", err)
			}
			d := req.Desired()
			if d.SubscriptionID != req.SubscriptionID || !reflect.DeepEqual(d.Params, req.Params) {
				t.Errorf("Desired() lost the subscription or params: %+v", d)
			}
			tc.check(t, req, d)
		})
	}
}

func TestRequestDesired_IsReadOnlyOnlyOnAValidateThatSettlesNoVerb(t *testing.T) {
	for _, tc := range []struct {
		label    string
		req      contract.Request
		readOnly bool
	}{
		{"a validate without the settling marker", contract.Request{Operation: "validate"}, true},
		{"a validate naming one resource without the marker", contract.Request{Operation: "validate", Resource: "firewall"}, true},
		{"the validate that settles a verb", contract.Request{Operation: "validate", Settling: true}, false},
		{"a subscribe", contract.Request{Operation: "subscribe"}, false},
		{"an upgrade", contract.Request{Operation: "upgrade"}, false},
		{"an unsubscribe", contract.Request{Operation: "unsubscribe"}, false},
		{"a run", contract.Request{Operation: contract.OperationRun}, false},
		{"a validate naming one resource that carries settling", contract.Request{Operation: "validate", Resource: "firewall", Settling: true}, true},
		{"a subscribe that carries settling", contract.Request{Operation: "subscribe", Settling: true}, false},
		{"an unsubscribe that carries settling", contract.Request{Operation: "unsubscribe", Settling: true}, false},
		{"a snapshot", contract.Request{Operation: contract.OperationSnapshot}, false},
		{"a restore", contract.Request{Operation: contract.OperationRestore}, false},
		{"a snapshot that carries settling", contract.Request{Operation: contract.OperationSnapshot, Settling: true}, false},
		{"a restore that carries settling", contract.Request{Operation: contract.OperationRestore, Settling: true}, false},
	} {
		t.Run(tc.label, func(t *testing.T) {
			wantField(t, "Desired().ReadOnly", tc.req.Desired().ReadOnly, tc.readOnly)
		})
	}
}

var requestGoldenBehaviours = []struct {
	behaviour string
	check     func(t *testing.T, req contract.Request)
}{
	{"names no resource on the request or Desired", func(t *testing.T, req contract.Request) {
		wantField(t, "Resource, Desired().Resource", []string{req.Resource, req.Desired().Resource}, []string{"", ""})
	}},
	{"decodes no resource identity", func(t *testing.T, req contract.Request) {
		if req.ResourceIdentity != nil || req.Desired().ResourceIdentity != nil {
			t.Errorf("ResourceIdentity, Desired().ResourceIdentity = %v, %v; want nil", req.ResourceIdentity, req.Desired().ResourceIdentity)
		}
	}},
	{"decodes no resource type, recorded resources or requirement types", func(t *testing.T, req contract.Request) {
		if req.ResourceType != "" || req.RecordedResources != nil || req.RequirementTypes != nil {
			t.Errorf("resourceType %q, recordedResources %v, requirementTypes %v; want none", req.ResourceType, req.RecordedResources, req.RequirementTypes)
		}
	}},
	{"reads as a subscription that may have been active, on the request and Desired", func(t *testing.T, req contract.Request) {
		wantField(t, "NeverActive, Desired().NeverActive", []bool{req.NeverActive, req.Desired().NeverActive}, []bool{false, false})
	}},
	{"hands Identity the request's identity fields", func(t *testing.T, req contract.Request) {
		wantField(t, "Identity()", req.Identity(), identityOf(req))
	}},
	{"embeds that Identity in Desired, promoting its instances", func(t *testing.T, req contract.Request) {
		d := req.Desired()
		wantField(t, "Desired().Identity", d.Identity, identityOf(req))
		wantField(t, "Desired().Instances", d.Instances, req.Instances)
	}},
	{"carries no own secrets, on the request or Desired", func(t *testing.T, req contract.Request) {
		if req.OwnSecrets != nil || req.Desired().OwnSecrets != nil {
			t.Errorf("OwnSecrets, Desired().OwnSecrets = %v, %v; want nil", req.OwnSecrets, req.Desired().OwnSecrets)
		}
	}},
	{"hands Desired the request's params and secret inputs", func(t *testing.T, req contract.Request) {
		d := req.Desired()
		wantField(t, "Desired() params, secret inputs", []any{d.Params, d.SecretInputs}, []any{req.Params, req.SecretInputs})
	}},
}

func identityOf(req contract.Request) contract.Identity {
	return contract.Identity{SubscriptionID: req.SubscriptionID, BlockID: req.BlockID, BlockVersion: req.BlockVersion,
		OrgID: req.OrgID, AppID: req.AppID, Instances: req.Instances, PriorOutputs: req.PriorOutputs, CloudIdentity: req.CloudIdentity}
}

func TestRequest_EveryValidGoldenDecodesToItsIdentityAndDesired(t *testing.T) {
	for _, name := range []string{"subscribe", "unsubscribe", "upgrade", "validate"} {
		for _, tc := range requestGoldenBehaviours {
			t.Run(name+"/"+tc.behaviour, func(t *testing.T) {
				req := decodeGolden(t, name)
				if len(req.Instances) == 0 {
					t.Fatalf("the %s golden carries no instances, so this proves nothing about what Identity carries", name)
				}

				tc.check(t, req)
			})
		}
	}
}

func TestRequest_IdentityCarriesWhatEachGoldenHoldsForItsResource(t *testing.T) {
	cases := []struct {
		label, golden string
		got           func(r contract.Request) any
		want          any
	}{
		{"subscribe carries no prior outputs", "subscribe", func(r contract.Request) any { return len(r.Identity().PriorOutputs) }, 0},
		{"subscribe carries its cloud identity", "subscribe", func(r contract.Request) any { return r.Identity().CloudIdentity != nil }, true},
		{"unsubscribe carries its prior outputs", "unsubscribe",
			func(r contract.Request) any { return r.Identity().PriorOutputs["instanceId"] }, "i-0a1b2c3d4e5f67890"},
		{"Desired carries unsubscribe's prior outputs", "unsubscribe",
			func(r contract.Request) any { return r.Desired().PriorOutputs["instanceId"] }, "i-0a1b2c3d4e5f67890"},
		{"unsubscribe carries its instance region", "unsubscribe",
			func(r contract.Request) any { return r.Identity().Instances["aws"].Config["region"] }, "eu-west-1"},
		{"Desired carries subscribe's instance region", "subscribe",
			func(r contract.Request) any { return r.Desired().Instances["aws"].Config["region"] }, "eu-west-1"},
		{"Desired carries unsubscribe's instance region", "unsubscribe",
			func(r contract.Request) any { return r.Desired().Instances["aws"].Config["region"] }, "eu-west-1"},
		{"Desired carries a logs request's binding", "logs",
			func(r contract.Request) any { return r.Desired().Bindings["docker-host"]["subscriptionId"] }, "sub_h0st5e1f2a"},
		{"Desired carries every binding of a logs request's requirement", "logs",
			func(r contract.Request) any { return len(r.Desired().BindingsAll["docker-host"]) }, 1},
		{"a logs request carries the events callback", "logs",
			func(r contract.Request) any { return r.EventsCallback.URL }, "https://api.mitstack.dev/v1/webhooks/operator-events"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			req := decodeGolden(t, tc.golden)

			got := tc.got(req)

			wantField(t, tc.label, got, tc.want)
		})
	}
}

func TestRequest_NoInstancesLeavesTheMapNil(t *testing.T) {
	req := contract.Request{Operation: "unsubscribe", SubscriptionID: "sub-1", BlockID: "block-1", OrgID: "org-1"}

	id, d := req.Identity(), req.Desired()

	if id.Instances != nil || d.Instances != nil {
		t.Errorf("Identity().Instances, Desired().Instances = %v, %v; want nil when the request carries none", id.Instances, d.Instances)
	}
	if got := id.Instances["aws"].Config["region"]; got != "" {
		t.Errorf("Identity().Instances[aws].Config[region] = %q, want an empty string", got)
	}
}

func TestRequestMarshal_EncodesEachResourceFieldUnderItsWireNameOnlyWhenSet(t *testing.T) {
	blockLevel := contract.Request{
		Resource:          "aws.ec2-instance",
		ResourceType:      "aws.ec2-instance",
		RecordedResources: map[string]contract.RecordedResource{"aws.vpc": {Type: "aws.vpc", Identity: map[string]string{"vpcId": "vpc-0abc"}}},
		RequirementTypes:  map[string]string{"host": "aws.ec2-instance"},
	}
	cases := []struct {
		label string
		req   contract.Request
		key   string
		want  any
	}{
		{"a resource identity", contract.Request{Resource: "firewall", ResourceIdentity: map[string]string{"groupId": "sg-0abc"}},
			"resourceIdentity", map[string]any{"groupId": "sg-0abc"}},
		{"a resource type", blockLevel, "resourceType", "aws.ec2-instance"},
		{"recorded resources", blockLevel, "recordedResources",
			map[string]any{"aws.vpc": map[string]any{"type": "aws.vpc", "identity": map[string]any{"vpcId": "vpc-0abc"}}}},
		{"requirement types", blockLevel, "requirementTypes", map[string]any{"host": "aws.ec2-instance"}},
		{"a recorded resource's state, when the engine recorded it absent", contract.Request{RecordedResources: map[string]contract.RecordedResource{
			"database": {Type: "aws.ec2-postgres-database", State: contract.Absent}}}, "recordedResources",
			map[string]any{"database": map[string]any{"type": "aws.ec2-postgres-database", "state": "absent"}}},
		{"never active, when the engine proved it", contract.Request{NeverActive: true}, "neverActive", true},
		{"own secrets", contract.Request{OwnSecrets: map[string]string{"gatewayCaKey": "own-secret-not-a-real-key"}},
			"ownSecrets", map[string]any{"gatewayCaKey": "own-secret-not-a-real-key"}},
		{"no own secrets key when the subscription keeps none", contract.Request{}, "ownSecrets", nil},
		{"a resource's unsubscribing marker", contract.Request{Resource: "firewall", ResourceUnsubscribing: true}, "resourceUnsubscribing", true},
		{"no unsubscribing marker when unset", contract.Request{}, "resourceUnsubscribing", nil},
		{"the app's delete marker", contract.Request{Operation: "unsubscribe", AppDeleting: true}, "appDeleting", true},
		{"no app's delete marker outside an app's delete", contract.Request{Operation: "unsubscribe"}, "appDeleting", nil},
		{"no resource type when unset", contract.Request{}, "resourceType", nil},
		{"no never-active key when it may have been active", contract.Request{}, "neverActive", nil},
		{"the settling marker, on the validate that settles a verb", contract.Request{Operation: "validate", Settling: true}, "settling", true},
		{"no settling key on a validate that settles no verb", contract.Request{Operation: "validate"}, "settling", nil},
		{"no recorded resources when unset", contract.Request{}, "recordedResources", nil},
		{"no requirement types when unset", contract.Request{}, "requirementTypes", nil},
		{"no resource key at all for a validate naming no resource", contract.Request{Operation: "validate"}, "resource", nil},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			got := marshalObject(t, tc.req)

			if tc.want == nil {
				for key := range got {
					if strings.HasPrefix(key, tc.key) {
						t.Errorf("json.Marshal(%+v) carries key %q, want no key starting %q", tc.req, key, tc.key)
					}
				}
				return
			}
			wantField(t, "encoded "+tc.key, got[tc.key], tc.want)
		})
	}
}

func dependsOnResource(t *testing.T, doc map[string]any, field string) {
	t.Helper()
	dependent := schemaAt(t, doc, doc, "dependentRequired")
	if needs, _ := dependent[field].([]any); !reflect.DeepEqual(needs, []any{"resource"}) {
		t.Errorf(`dependentRequired.%s = %v, want ["resource"]`, field, dependent[field])
	}
}

func objectOfStrings(t *testing.T, doc, node map[string]any, what string) {
	t.Helper()
	if !hasType(node, "object") {
		t.Fatalf("%s type = %v, want object", what, schemaTypes(node))
	}
	if values := schemaAt(t, doc, node, "additionalProperties"); !hasType(values, "string") {
		t.Errorf("%s values type = %v, want string", what, schemaTypes(values))
	}
}

func TestEnvelopeSchema_DeclaresEachOptionalRequestFieldByItsShape(t *testing.T) {
	doc := jsonObject(t, contract.EnvelopeSchema)
	cases := []struct {
		field string
		check func(t *testing.T, prop map[string]any)
	}{
		{"resource", func(t *testing.T, prop map[string]any) {
			if !hasType(prop, "string") {
				t.Errorf("properties.resource type = %v, want string", schemaTypes(prop))
			}
		}},
		{"resourceIdentity", func(t *testing.T, prop map[string]any) {
			objectOfStrings(t, doc, prop, "properties.resourceIdentity")
			dependsOnResource(t, doc, "resourceIdentity")
		}},
		{"resourceType", func(t *testing.T, prop map[string]any) {
			if !hasType(prop, "string") {
				t.Errorf("properties.resourceType = %v, want a string", prop)
			}
			dependsOnResource(t, doc, "resourceType")
		}},
		{"recordedResources", func(t *testing.T, prop map[string]any) {
			if !hasType(prop, "object") {
				t.Fatalf("properties.recordedResources = %v, want an object", prop)
			}
			entry := schemaAt(t, doc, prop, "additionalProperties", "properties")
			if ty, _ := entry["type"].(map[string]any); !hasType(ty, "string") {
				t.Errorf("a recorded resource's type = %v, want a string", entry["type"])
			}
			identity, _ := entry["identity"].(map[string]any)
			objectOfStrings(t, doc, identity, "a recorded resource's identity")
			state, _ := entry["state"].(map[string]any)
			if !hasType(state, "string") || !reflect.DeepEqual(state["enum"], []any{"absent"}) {
				t.Errorf("a recorded resource's state = %v, want a string whose one value is absent", entry["state"])
			}
		}},
		{"requirementTypes", func(t *testing.T, prop map[string]any) {
			objectOfStrings(t, doc, prop, "properties.requirementTypes")
		}},
		{"absences", func(t *testing.T, prop map[string]any) {
			schemaAt(t, doc, prop, "additionalProperties", "properties", "provesAbsence")
		}},
		{"settlesAt", func(t *testing.T, prop map[string]any) {
			if !hasType(prop, "string") || prop["format"] != "date-time" {
				t.Errorf("properties.settlesAt = %v, want a string of format date-time", prop)
			}
		}},
		{"neverActive", func(t *testing.T, prop map[string]any) {
			if !hasType(prop, "boolean") {
				t.Errorf("properties.neverActive type = %v, want boolean", schemaTypes(prop))
			}
		}},
		{"resourceUnsubscribing", func(t *testing.T, prop map[string]any) {
			if !hasType(prop, "boolean") {
				t.Errorf("properties.resourceUnsubscribing type = %v, want boolean", schemaTypes(prop))
			}
		}},
		{"appDeleting", func(t *testing.T, prop map[string]any) {
			if !hasType(prop, "boolean") {
				t.Errorf("properties.appDeleting type = %v, want boolean", schemaTypes(prop))
			}
		}},
		{"settling", func(t *testing.T, prop map[string]any) {
			if !hasType(prop, "boolean") {
				t.Errorf("properties.settling type = %v, want boolean", schemaTypes(prop))
			}
		}},
		{"ownSecrets", func(t *testing.T, prop map[string]any) {
			objectOfStrings(t, doc, prop, "properties.ownSecrets")
		}},
		{"leaseId", func(t *testing.T, prop map[string]any) {
			if !hasType(prop, "string") {
				t.Errorf("properties.leaseId type = %v, want string", schemaTypes(prop))
			}
		}},
		{"want", func(t *testing.T, prop map[string]any) {
			if !hasType(prop, "object") {
				t.Errorf("properties.want type = %v, want object", schemaTypes(prop))
			}
		}},
		{"retire", func(t *testing.T, prop map[string]any) {
			if items := schemaAt(t, doc, prop, "items"); !hasType(prop, "array") || !hasType(items, "string") {
				t.Errorf("properties.retire = %v, want an array of strings", prop)
			}
		}},
		{"runId", func(t *testing.T, prop map[string]any) {
			if !hasType(prop, "string") || prop["pattern"] != "^run_[0-9a-f]{12}$" {
				t.Errorf("properties.runId = %v, want a string of pattern ^run_[0-9a-f]{12}$", prop)
			}
		}},
		{"command", func(t *testing.T, prop map[string]any) {
			if !hasType(prop, "string") || prop["maxLength"] != float64(contract.MaxRunCommandLength) || prop["pattern"] != "^[ -~]+$" {
				t.Errorf("properties.command = %v, want a string of at most %d printable ASCII characters", prop, contract.MaxRunCommandLength)
			}
		}},
		{"process", func(t *testing.T, prop map[string]any) {
			if !hasType(prop, "string") {
				t.Errorf("properties.process type = %v, want string", schemaTypes(prop))
			}
		}},
		{"unit", func(t *testing.T, prop map[string]any) {
			if !hasType(prop, "string") {
				t.Errorf("properties.unit type = %v, want string", schemaTypes(prop))
			}
		}},
		{"replacing", func(t *testing.T, prop map[string]any) {
			if !hasType(prop, "boolean") {
				t.Errorf("properties.replacing type = %v, want boolean", schemaTypes(prop))
			}
		}},
		{"caller", func(t *testing.T, prop map[string]any) {
			if !hasType(prop, "object") {
				t.Fatalf("properties.caller type = %v, want object", schemaTypes(prop))
			}
			if sub := schemaAt(t, doc, prop, "properties", "subscriptionId"); !hasType(sub, "string") {
				t.Errorf("properties.caller.subscriptionId type = %v, want string", schemaTypes(sub))
			}
			placed := schemaAt(t, doc, prop, "properties", "placed")
			entry := schemaAt(t, doc, placed, "items", "properties")
			for _, field := range []string{"app", "blockId", "subscriptionId"} {
				if f := schemaAt(t, doc, entry, field); !hasType(f, "string") {
					t.Errorf("properties.caller.placed[].%s type = %v, want string", field, schemaTypes(f))
				}
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			prop := schemaAt(t, doc, doc, "properties", tc.field)

			if slices.Contains(sortedStrings(doc["required"]), tc.field) {
				t.Errorf("envelope.schema.json requires %q, but a request without it must stay valid", tc.field)
			}
			tc.check(t, prop)
		})
	}
}

func TestEnvelopeSchema_IsVersion1_18_0(t *testing.T) {
	doc := jsonObject(t, contract.EnvelopeSchema)

	if doc["version"] != "1.18.0" {
		t.Errorf("envelope.schema.json version = %v, want 1.18.0: it adds the optional field appDeleting, a minor change from 1.17.0, which added the optional field settling", doc["version"])
	}
}

func TestEnvelopeSchema_ListsTheRunOperation(t *testing.T) {
	doc := jsonObject(t, contract.EnvelopeSchema)
	operation, _ := doc["properties"].(map[string]any)["operation"].(map[string]any)

	if enum, _ := operation["enum"].([]any); !slices.Contains(enum, any("run")) {
		t.Errorf("envelope.schema.json operation enum = %v, want it to list \"run\"", operation["enum"])
	}
}

func TestEnvelopeSchema_ListsTheSnapshotAndRestoreOperations(t *testing.T) {
	doc := jsonObject(t, contract.EnvelopeSchema)
	operation, _ := doc["properties"].(map[string]any)["operation"].(map[string]any)
	enum, _ := operation["enum"].([]any)

	for _, op := range []string{"snapshot", "restore"} {
		if !slices.Contains(enum, any(op)) {
			t.Errorf("envelope.schema.json operation enum = %v, want it to list %q", operation["enum"], op)
		}
	}
}

func TestEnvelopeSchema_ListsTheLeaseOperation(t *testing.T) {
	doc := jsonObject(t, contract.EnvelopeSchema)
	operation, _ := doc["properties"].(map[string]any)["operation"].(map[string]any)

	if enum, _ := operation["enum"].([]any); !slices.Contains(enum, any("lease")) {
		t.Errorf("envelope.schema.json operation enum = %v, want it to list \"lease\"", operation["enum"])
	}
}

func TestEnvelopeSchema_ListsTheLogsOperation(t *testing.T) {
	doc := jsonObject(t, contract.EnvelopeSchema)
	operation, _ := doc["properties"].(map[string]any)["operation"].(map[string]any)

	if enum, _ := operation["enum"].([]any); !slices.Contains(enum, any("logs")) {
		t.Errorf("envelope.schema.json operation enum = %v, want it to list \"logs\"", operation["enum"])
	}
}

func TestDesired_AttestedAbsentCountsOnlyAWholeAttestationOfThatBindingsSubscription(t *testing.T) {
	whole := contract.Absence{Observer: "o", Resource: "r", ReadAbsentAt: time.Unix(1, 0), ProvesAbsence: true}
	for name, tc := range map[string]struct {
		sub string
		a   contract.Absence
	}{
		"a whole attestation":                                {"sub_host", whole},
		"a whole attestation of a host bound by bindingsAll": {"sub_all", whole},
		"another subscription":                               {"sub_other", whole},
		"no proof":                                           {"sub_host", contract.Absence{Observer: "o", Resource: "r", ReadAbsentAt: time.Unix(1, 0)}},
		"no observer":                                        {"sub_host", contract.Absence{Resource: "r", ReadAbsentAt: time.Unix(1, 0), ProvesAbsence: true}},
		"no resource":                                        {"sub_host", contract.Absence{Observer: "o", ReadAbsentAt: time.Unix(1, 0), ProvesAbsence: true}},
		"no time":                                            {"sub_host", contract.Absence{Observer: "o", Resource: "r", ProvesAbsence: true}},
	} {
		t.Run(name, func(t *testing.T) {
			req := contract.Request{Bindings: map[string]map[string]string{"host": {"subscriptionId": "sub_host"}}, BindingsAll: map[string][]map[string]string{"host": {{"subscriptionId": "sub_all"}}}, Absences: map[string]contract.Absence{"host": tc.a}}

			got, ok := req.Desired().AttestedAbsent(tc.sub)

			if want := tc.a == whole && tc.sub != "sub_other"; ok != want || (ok && got != whole) {
				t.Errorf("AttestedAbsent(%q) = %v, %v; want found %v", tc.sub, got, ok, want)
			}
		})
	}
}

func leaseBody(t *testing.T, mutate func(m map[string]any)) []byte {
	t.Helper()
	m := map[string]any{
		"operation": "lease", "subscriptionId": "sub_provider", "blockId": "hcloud-docker-host", "orgId": "org_acme",
		"nonce": "n0nce-lease-0001", "createdAt": "2026-07-15T21:15:06Z",
		"resource": "hetzner.backup-bucket", "leaseId": "lease_0001",
		"want":   map[string]any{"signals": "metrics,logs"},
		"retire": []string{"tok_old_1", "tok_old_2"},
		"caller": map[string]any{"subscriptionId": "sub_host", "placed": []map[string]any{
			{"app": "shop", "blockId": "docker-mariadb", "subscriptionId": "sub_db"},
			{"app": "shop", "blockId": "docker-app-slice", "subscriptionId": "sub_slice"},
		}},
	}
	if mutate != nil {
		mutate(m)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("json.Marshal(lease body): %v", err)
	}
	return raw
}

func TestDecodeRequest_ReadsALeaseNamingItsResourceItsCallerAndWhatItWants(t *testing.T) {
	raw := leaseBody(t, nil)

	req, err := contract.DecodeRequest(raw)

	if err != nil {
		t.Fatalf("DecodeRequest(lease): %v", err)
	}
	want := contract.LeaseRequest{
		ID: "lease_0001", Resource: "hetzner.backup-bucket", Want: json.RawMessage(`{"signals":"metrics,logs"}`), Retire: []string{"tok_old_1", "tok_old_2"},
		Caller: contract.LeaseCaller{SubscriptionID: "sub_host", Placed: []contract.Placed{
			{App: "shop", BlockID: "docker-mariadb", SubscriptionID: "sub_db"},
			{App: "shop", BlockID: "docker-app-slice", SubscriptionID: "sub_slice"},
		}},
	}
	if got := req.Lease(); !reflect.DeepEqual(got, want) {
		t.Errorf("DecodeRequest(lease).Lease() =\n%+v\nwant\n%+v", got, want)
	}
	if req.Operation != contract.OperationLease {
		t.Errorf("operation = %q, want %q", req.Operation, contract.OperationLease)
	}
}

func TestDecodeRequest_AcceptsALeaseThatOnlyRetiresCredentials(t *testing.T) {
	raw := leaseBody(t, func(m map[string]any) { delete(m, "leaseId"); delete(m, "want"); delete(m, "caller") })

	req, err := contract.DecodeRequest(raw)

	if err != nil {
		t.Fatalf("DecodeRequest(a lease with retire alone): %v", err)
	}
	if got := req.Lease(); got.ID != "" || !slices.Equal(got.Retire, []string{"tok_old_1", "tok_old_2"}) {
		t.Errorf("Lease() = %+v, want no id and the two credentials to retire", got)
	}
}

func TestDecodeRequest_RefusesALeaseMissingWhatItNeeds(t *testing.T) {
	cases := []struct {
		label  string
		mutate func(m map[string]any)
		wants  []string
	}{
		{"no resource", func(m map[string]any) { delete(m, "resource") }, []string{`"resource"`}},
		{"an empty resource", func(m map[string]any) { m["resource"] = "" }, []string{`"resource"`}},
		{"neither a lease id nor credentials to retire", func(m map[string]any) { delete(m, "leaseId"); delete(m, "retire") }, []string{`"leaseId"`, `"retire"`}},
		{"a credential to retire named by an empty handle", func(m map[string]any) { m["retire"] = []string{"tok_old_1", ""} }, []string{`"retire"`}},
		{"a lease id and no caller", func(m map[string]any) { delete(m, "caller") }, []string{`"caller.subscriptionId"`}},
		{"a caller naming no subscription", func(m map[string]any) { m["caller"] = map[string]any{"placed": []any{}} }, []string{`"caller.subscriptionId"`}},
		{"want that is not an object", func(m map[string]any) { m["want"] = "metrics" }, []string{`"want"`}},
		{"retire that is not a list", func(m map[string]any) { m["retire"] = "tok_old_1" }, []string{`"retire"`}},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			_, err := contract.DecodeRequest(leaseBody(t, tc.mutate))

			requireErrorNaming(t, "DecodeRequest(lease)", err, tc.wants...)
		})
	}
}

// runBody is the run golden with mutate applied, so each refusal starts from the shape the platform sends.
func runBody(t *testing.T, mutate func(m map[string]any)) []byte {
	t.Helper()
	m := jsonObject(t, kittest.Golden(t, "run"))
	if mutate != nil {
		mutate(m)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("json.Marshal(run body): %v", err)
	}
	return raw
}

func TestDecodeRequest_ReadsTheRunGoldenNamingItsIdItsCommandAndItsProcess(t *testing.T) {
	raw := runBody(t, nil)

	req, err := contract.DecodeRequest(raw)

	if err != nil {
		t.Fatalf("DecodeRequest(run): %v", err)
	}
	want := contract.RunRequest{ID: "run_4c1e9a7f2b3d", Command: "php bin/add-user.php alice@example.com", Process: "web"}
	if got := req.Run(); got != want {
		t.Errorf("DecodeRequest(run).Run() = %+v, want %+v", got, want)
	}
	if req.Operation != contract.OperationRun {
		t.Errorf("operation = %q, want %q", req.Operation, contract.OperationRun)
	}
}

func TestDecodeRequest_AcceptsARunNamingNoProcessAndACommandOfTheLongestLength(t *testing.T) {
	longest := strings.Repeat("a", contract.MaxRunCommandLength)
	raw := runBody(t, func(m map[string]any) { delete(m, "process"); m["command"] = longest })

	req, err := contract.DecodeRequest(raw)

	if err != nil {
		t.Fatalf("DecodeRequest(a run of %d characters naming no process): %v", contract.MaxRunCommandLength, err)
	}
	if got := req.Run(); got.Process != "" || got.Command != longest {
		t.Errorf("Run() = process %q and a command of %d characters, want no process and %d characters", got.Process, len(got.Command), contract.MaxRunCommandLength)
	}
}

func TestDecodeRequest_RefusesARunWhoseIdCommandOrProcessIsOutOfForm(t *testing.T) {
	const oneLine = "one line of at most 1000 printable ASCII characters"
	set := func(field string, value any) func(m map[string]any) {
		return func(m map[string]any) { m[field] = value }
	}
	cases := []struct {
		label  string
		mutate func(m map[string]any)
		wants  []string
	}{
		{"no run id", func(m map[string]any) { delete(m, "runId") }, []string{`"runId"`}},
		{"a run id of eleven hex characters", set("runId", "run_4c1e9a7f2b3"), []string{`"runId"`, "run_ and twelve"}},
		{"a run id of thirteen hex characters", set("runId", "run_4c1e9a7f2b3d0"), []string{`"runId"`, "run_ and twelve"}},
		{"a run id in capitals", set("runId", "run_4C1E9A7F2B3D"), []string{`"runId"`, "run_ and twelve"}},
		{"a run id of another prefix", set("runId", "job_4c1e9a7f2b3d"), []string{`"runId"`, "run_ and twelve"}},
		{"no command", func(m map[string]any) { delete(m, "command") }, []string{`"command"`}},
		{"an empty command", set("command", ""), []string{`"command"`}},
		{"a command of 1,001 characters", set("command", strings.Repeat("a", contract.MaxRunCommandLength+1)), []string{`"command"`, oneLine}},
		{"a command of a second line", set("command", "php bin/add-user.php alice@example.com\nrm -rf /"), []string{`"command"`, oneLine}},
		{"a command ending in a carriage return", set("command", "php bin/add-user.php alice@example.com\r"), []string{`"command"`, oneLine}},
		{"a command holding a tab", set("command", "php\tbin/add-user.php"), []string{`"command"`, oneLine}},
		{"a command holding a NUL", set("command", "php bin/add-user.php\x00"), []string{`"command"`, oneLine}},
		{"a command holding a letter outside ASCII", set("command", "php bin/add-user.php josé@example.com"), []string{`"command"`, oneLine}},
		{"a process with a space", set("process", "web app"), []string{`"process"`}},
		{"a process in capitals", set("process", "Web"), []string{`"process"`}},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			_, err := contract.DecodeRequest(runBody(t, tc.mutate))

			requireErrorNaming(t, "DecodeRequest(run)", err, tc.wants...)
		})
	}
}

func TestDecodeRequest_ReadsTheUnitAndTheReplacementMarkerOfASnapshotAndARestore(t *testing.T) {
	cases := []struct {
		golden       string
		wantHandover string
	}{
		{"snapshot", ""},
		{"restore", "hnd_0001"},
	}
	for _, tc := range cases {
		t.Run(tc.golden, func(t *testing.T) {
			req := decodeGolden(t, tc.golden)

			if req.Operation != tc.golden || req.Unit != "app-data" || !req.Replacing {
				t.Errorf("the %s golden decodes to operation %q, unit %q, replacing %v; want %q, \"app-data\", true", tc.golden, req.Operation, req.Unit, req.Replacing, tc.golden)
			}
			if got := req.PriorOutputs[contract.HandoverOutputKey("app-data")]; got != tc.wantHandover {
				t.Errorf("the %s golden carries the handover %q under priorOutputs[%q]; want %q", tc.golden, got, contract.HandoverOutputKey("app-data"), tc.wantHandover)
			}
			if d := req.Desired(); d.Replacing != req.Replacing {
				t.Errorf("Desired().Replacing = %v for a request sent during a replacement, want %v", d.Replacing, req.Replacing)
			}
		})
	}
}

func TestDecodeRequest_ARequestOutsideAReplacementCarriesNoMarker(t *testing.T) {
	req := decodeGolden(t, "upgrade")

	if req.Replacing || req.Desired().Replacing {
		t.Errorf("the upgrade golden sent outside a replacement decodes with replacing set: request %v, desired %v", req.Replacing, req.Desired().Replacing)
	}
}

func TestDecodeRequest_RefusesASnapshotOrARestoreNamingNoUnit(t *testing.T) {
	for _, operation := range []string{contract.OperationSnapshot, contract.OperationRestore} {
		for label, unit := range map[string]any{"no unit": nil, "an empty unit": ""} {
			t.Run(operation+" with "+label, func(t *testing.T) {
				raw := mutateGolden(t, operation, func(m map[string]any) {
					if unit == nil {
						delete(m, "unit")
						return
					}
					m["unit"] = unit
				})

				_, err := contract.DecodeRequest(raw)

				requireErrorNaming(t, "DecodeRequest("+operation+")", err, `"unit"`)
			})
		}
	}
}

func TestHandoverOutputKey_NamesTheUnitItHandsOver(t *testing.T) {
	for unit, want := range map[string]string{"app-data": "handover.app-data", "vault": "handover.vault"} {
		if got := contract.HandoverOutputKey(unit); got != want {
			t.Errorf("HandoverOutputKey(%q) = %q, want %q", unit, got, want)
		}
	}
}

func TestRequest_SecretValuesKeepsEachSecretApartByWhereTheRequestCarriesIt(t *testing.T) {
	req := contract.Request{
		Secrets:           map[string]map[string]string{"github": {"token": "ghs-token", "owner": "acme-co", "unset": ""}, "db": {"password": "db-password"}},
		SecretInputs:      map[string]string{"apiKey": "input-key"},
		AppSecrets:        map[string]string{"APP_KEY": "app-key", "APP_URL": ""},
		OwnSecrets:        map[string]string{"caKey": "own-ca-key"},
		EnteredCredential: map[string]string{"token": "entered-token"},
		OBO:               "obo-token",
		EventsCallback:    &contract.EventsCallback{URL: "https://events.example", Token: "events-token"},
	}

	fromRequest, fromDesired := req.SecretValues(), req.Desired().SecretValues()

	want := contract.SecretValues{
		Connections: []string{"acme-co", "db-password", "ghs-token"}, SecretInputs: []string{"input-key"}, AppSecrets: []string{"app-key"},
		OwnSecrets: []string{"own-ca-key"}, Credentials: []string{"entered-token", "events-token", "obo-token"},
	}
	if !reflect.DeepEqual(fromRequest, want) {
		t.Errorf("Request.SecretValues() = %+v\nwant %+v, each part sorted and no empty value", fromRequest, want)
	}
	if want.Credentials = nil; !reflect.DeepEqual(fromDesired, want) {
		t.Errorf("Desired.SecretValues() = %+v\nwant %+v, every part but the request's own credentials", fromDesired, want)
	}
	if all := fromRequest.All(); !slices.Equal(all, slices.Concat(want.Connections, want.SecretInputs, want.AppSecrets, want.OwnSecrets, []string{"entered-token", "events-token", "obo-token"})) {
		t.Errorf("All() = %q, want every part's values", all)
	}
}
