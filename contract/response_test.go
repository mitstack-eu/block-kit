package contract_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/verify/kittest"
)

func validResponseGoldens(t *testing.T) map[string][]byte {
	t.Helper()
	valid, err := contract.ValidResponseGoldens()
	if err != nil {
		t.Fatalf("contract.ValidResponseGoldens(): %v", err)
	}
	return valid
}

func decodeResponse(t *testing.T, raw []byte) contract.Response {
	t.Helper()
	resp, err := contract.DecodeResponse(raw)
	if err != nil {
		t.Fatalf("DecodeResponse(%s): %v", raw, err)
	}
	return resp
}

func TestDecodeResponse_CarriesEveryGoldenFieldOntoTheResponse(t *testing.T) {
	for name, raw := range validResponseGoldens(t) {
		t.Run(name, func(t *testing.T) {
			m := jsonObject(t, raw)
			want := contract.Response{Outputs: strMap(m["outputs"]), SecretOutputs: strMap(m["secretOutputs"]), Error: strOf(m, "error"),
				StatusDetail: strOf(m, "statusDetail"), LastError: strOf(m, "lastError"), OwnSecrets: strMap(m["ownSecrets"])}
			want.Pending, _ = m["pending"].(bool)
			want.Redrive, _ = m["redrive"].(bool)
			if absent, ok := m["absent"].(bool); ok {
				want.Absent = &absent
			}

			got := decodeResponse(t, raw)

			wantField(t, name+" decoded", got, want)
		})
	}
}

func TestDecodeResponse_EveryResponseGoldenDecodesAsBefore(t *testing.T) {
	f, tr := false, true
	want := map[string]contract.Response{
		"absent":          {Absent: &tr},
		"error":           {Error: "subscription quota exceeded for this org"},
		"last-error":      {LastError: "timed out waiting for the load balancer to become healthy"},
		"null-outputs":    {},
		"outputs":         {Outputs: map[string]string{"instanceId": "i-0123456789abcdef0", "endpoint": "https://example.apps.mitstack.dev"}},
		"pending":         {Pending: true, StatusDetail: "waiting for the VPC to finish provisioning"},
		"pending-redrive": {Pending: true, Redrive: true, StatusDetail: "the builder powered off; the snapshot comes next"},
		"own-secrets":     {OwnSecrets: map[string]string{"gatewayCaKey": "own-secret-not-a-real-key"}},
		"secret-outputs":  {SecretOutputs: map[string]string{"apiKey": "sk-example-not-a-real-secret"}},
		"still-present":   {Absent: &f},
	}
	valid := validResponseGoldens(t)
	for name := range valid {
		if _, ok := want[name]; !ok {
			t.Errorf("response golden %q has no pinned decoded value", name)
		}
	}
	for _, name := range sortedKeys(want) {
		t.Run(name, func(t *testing.T) {
			raw, ok := valid[name]
			if !ok {
				t.Fatalf("response golden %q is gone, so its decoded value can no longer be compared", name)
			}

			got := decodeResponse(t, raw)

			wantField(t, name+" decoded", got, want[name])
		})
	}
}

func TestDecodeResponse_ReencodedResponsesConformViaKittest(t *testing.T) {
	bodies := map[string][]byte{}
	for name, raw := range validResponseGoldens(t) {
		reencoded, err := json.Marshal(decodeResponse(t, raw))
		if err != nil {
			t.Fatalf("json.Marshal(DecodeResponse(%s)): %v", name, err)
		}
		bodies[name] = reencoded
	}

	kittest.AssertEmitsConformingResponses(t, bodies)
}

func TestDecodeResponse_RejectsInvalidGoldensNamingField(t *testing.T) {
	wantNamed := map[string]string{
		"invalid-absent-not-bool":                          "absent",
		"invalid-outputs-not-object":                       "outputs",
		"invalid-outputs-not-strings":                      "outputs",
		"invalid-pending-not-bool":                         "pending",
		"invalid-pending-redrive-not-bool":                 "redrive",
		"invalid-secret-outputs-not-strings":               "secretOutputs",
		"invalid-own-secrets-not-strings":                  "ownSecrets",
		"invalid-resource-secret-outputs-not-object":       "resourceSecretOutputs",
		"invalid-resource-secret-outputs-entry-not-object": "resourceSecretOutputs",
		"invalid-resource-secret-outputs-value-not-string": "resourceSecretOutputs",
	}
	invalid, err := contract.InvalidResponseGoldens()
	if err != nil {
		t.Fatalf("contract.InvalidResponseGoldens(): %v", err)
	}
	for name, raw := range invalid {
		t.Run(name, func(t *testing.T) {
			field, ok := wantNamed[name]
			if !ok {
				t.Fatalf("invalid response golden %q has no named field, so it ships unchecked", name)
			}

			_, err := contract.DecodeResponse(raw)

			requireErrorNaming(t, "DecodeResponse("+name+")", err, field)
		})
	}
}

func TestDecodeResponse_RefusesAFieldOfTheWrongTypeNamingIt(t *testing.T) {
	cases := []struct{ label, raw, field string }{
		{"resources not an object", `{"resources": ["network"]}`, "resources"},
		{"outcome not an object", `{"resources": {"network": "ensured"}}`, "resources"},
		{"call outcome not an object", `{"resources": {"network": {"ensure": true}}}`, "resources"},
		{"observe outputs not strings", `{"resources": {"network": {"observe": {"outputs": {"port": 8080}}}}}`, "resources"},
		{"call outcome error not string", `{"resources": {"network": {"delete": {"error": 7}}}}`, "resources"},
		{"statusDetail not a string", `{"statusDetail": 5}`, "statusDetail"},
		{"own secrets not strings", `{"ownSecrets": {"gatewayCaKey": 7}}`, "ownSecrets"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			_, err := contract.DecodeResponse([]byte(tc.raw))

			requireErrorNaming(t, "DecodeResponse("+tc.raw+")", err, tc.field)
		})
	}
}

const resourcesResponse = `{
  "outputs": {"vpcId": "vpc-0abc", "publicIp": "203.0.113.7"},
  "secretOutputs": {"rolePassword": "not-a-real-secret"},
  "resources": {
    "network": {"ensure": {}, "observe": {"state": "present", "outputs": {"vpcId": "vpc-0abc"}, "statusDetail": "available"}},
    "instance": {"ensure": {"error": "refused:quota: no capacity in eu-west-1", "statusDetail": "refused:quota: no capacity in eu-west-1"}},
    "old-volume": {"delete": {}, "observe": {"state": "absent"}}
  }
}`

func TestDecodeResponse_ReadsEachResourceOutcomeByName(t *testing.T) {
	refused := "refused:quota: no capacity in eu-west-1"
	cases := []struct {
		label, raw string
		want       contract.Response
	}{
		{"an ensured, a failed and a deleted resource", resourcesResponse, contract.Response{
			Outputs:       map[string]string{"vpcId": "vpc-0abc", "publicIp": "203.0.113.7"},
			SecretOutputs: map[string]string{"rolePassword": "not-a-real-secret"},
			Resources: map[string]contract.ResourceOutcome{
				"network": {Ensure: &contract.CallOutcome{},
					Observe: &contract.CallOutcome{State: contract.Present, Outputs: map[string]string{"vpcId": "vpc-0abc"}, StatusDetail: "available"}},
				"instance":   {Ensure: &contract.CallOutcome{Error: refused, StatusDetail: refused}},
				"old-volume": {Delete: &contract.CallOutcome{}, Observe: &contract.CallOutcome{State: contract.Absent}},
			},
		}},
		{"the replacements a validate names and the type an observe reports",
			`{"outputs": null, "replaces": ["aws.ebs-volume", "deploy-role"], "resources": {` +
				`"aws.ebs-volume": {"observe": {"state": "present", "type": "aws.ebs-volume", "identity": {"volumeId": "vol-0abc"}}}}}`,
			contract.Response{Replaces: []string{"aws.ebs-volume", "deploy-role"}, Resources: map[string]contract.ResourceOutcome{
				"aws.ebs-volume": {Observe: &contract.CallOutcome{State: contract.Present, Type: "aws.ebs-volume", Identity: map[string]string{"volumeId": "vol-0abc"}}},
			}}},
		{"the identity an ensure reported", `{"resources":{"firewall":{"ensure":{"identity":{"groupId":"sg-0abc"},"outputs":{"groupArn":"arn:sg"}}}}}`,
			contract.Response{Resources: map[string]contract.ResourceOutcome{
				"firewall": {Ensure: &contract.CallOutcome{Identity: map[string]string{"groupId": "sg-0abc"}, Outputs: map[string]string{"groupArn": "arn:sg"}}},
			}}},
		{"the failure an observe found a present resource meets", `{"resources":{"data":{"observe":{"state":"present","unhealthy":{"cause":"the volume is still creating, attaching or detaching","detail":"attaching for two hours"}}}}}`,
			contract.Response{Resources: map[string]contract.ResourceOutcome{
				"data": {Observe: &contract.CallOutcome{State: contract.Present, Unhealthy: &contract.Unhealthy{
					Cause: "the volume is still creating, attaching or detaching", Detail: "attaching for two hours"}}},
			}}},
		{"the wait of a removal an observe found under way", `{"resources":{"app":{"observe":{"state":"present","removing":{"waitsOn":"pull request 4 to pass its managed-ci check"}}}}}`,
			contract.Response{Resources: map[string]contract.ResourceOutcome{
				"app": {Observe: &contract.CallOutcome{State: contract.Present, Removing: &contract.Removing{WaitsOn: "pull request 4 to pass its managed-ci check"}}},
			}}},
		{"the action of a person an observe found the resource waiting on", `{"resources":{"app":{"observe":{"state":"unknown","waitingOnPerson":{"action":"approve pull request 4 at https://github.com/acme/shop/pull/4"}}}}}`,
			contract.Response{Resources: map[string]contract.ResourceOutcome{
				"app": {Observe: &contract.CallOutcome{State: contract.Unknown, WaitingOnPerson: &contract.WaitingOnPerson{Action: "approve pull request 4 at https://github.com/acme/shop/pull/4"}}},
			}}},
		{"the files a delete kept", `{"resources":{"starter":{"delete":{"kept":[{"repository":"acme/weather","path":"CLAUDE.md","reason":"changed"}]}}}}`,
			contract.Response{Resources: map[string]contract.ResourceOutcome{
				"starter": {Delete: &contract.CallOutcome{Kept: []contract.Kept{{Repository: "acme/weather", Path: "CLAUDE.md", Reason: "changed"}}}},
			}}},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			got := decodeResponse(t, []byte(tc.raw))

			wantField(t, "DecodeResponse", got, tc.want)
		})
	}
}

func TestObservationResponse_MarksAbsenceByState(t *testing.T) {
	f, tr := false, true
	outputs, secrets := map[string]string{"instanceId": "i-0123456789abcdef0"}, map[string]string{"apiKey": "sk-example-not-a-real-secret"}
	own := map[string]string{"gatewayCaKey": "own-secret-not-a-real-key"}
	cases := []struct {
		obs  contract.Observation
		want contract.Response
	}{
		{contract.Observation{State: contract.Present, Outputs: outputs, SecretOutputs: secrets, OwnSecrets: own, StatusDetail: "converged"},
			contract.Response{Outputs: outputs, SecretOutputs: secrets, OwnSecrets: own, StatusDetail: "converged", Absent: &f}},
		{contract.Observation{State: contract.Absent, StatusDetail: "resource is gone"},
			contract.Response{StatusDetail: "resource is gone", Absent: &tr}},
		{contract.Observation{State: contract.Unknown, StatusDetail: "checking"},
			contract.Response{StatusDetail: "checking"}},
	}
	for _, tc := range cases {
		t.Run(string(tc.obs.State), func(t *testing.T) {
			got := tc.obs.Response()

			wantField(t, "Observation{State: "+string(tc.obs.State)+"}.Response()", got, tc.want)
		})
	}
}

func TestWire_EncodesEachValueExactly(t *testing.T) {
	cases := []struct {
		label    string
		value    any
		want     string
		conforms bool
	}{
		{"a response with nil outputs encodes JSON null, which the engine tolerates", contract.Response{}, `{"outputs":null}`, true},
		{"a response reporting no resources encodes as before", contract.Response{Pending: true}, `{"outputs":null,"pending":true}`, false},
		{"a response returning own secrets encodes them under ownSecrets", contract.Response{OwnSecrets: map[string]string{"gatewayCaKey": "own-secret-not-a-real-key"}},
			`{"outputs":null,"ownSecrets":{"gatewayCaKey":"own-secret-not-a-real-key"}}`, true},
		{"a lease answer encodes the credential as the operator wrote it, its expiry and the handle to delete it by", contract.Response{Lease: &contract.Lease{
			Credential: json.RawMessage(`{"uploads":{"shop":{"url":"https://store.example/shop/a.age?sig=1","object":"shop/a.age"}}}`),
			ExpiresAt:  time.Date(2026, 7, 15, 23, 15, 6, 0, time.UTC), ID: "tok_1"}},
			`{"outputs":null,"lease":{"credential":{"uploads":{"shop":{"url":"https://store.example/shop/a.age?sig=1","object":"shop/a.age"}}},"expiresAt":"2026-07-15T23:15:06Z","id":"tok_1"}}`, true},
		{"a lease answer for a credential that expires on its own leaves the handle out", contract.Response{Lease: &contract.Lease{
			Credential: json.RawMessage(`{"links":{}}`), ExpiresAt: time.Date(2026, 7, 15, 23, 15, 6, 0, time.UTC)}},
			`{"outputs":null,"lease":{"credential":{"links":{}},"expiresAt":"2026-07-15T23:15:06Z"}}`, true},
		{"a response that leases nothing encodes as before", contract.Response{Lease: nil}, `{"outputs":null}`, true},
		{"a finished run encodes its exit code, output, digest, start and end", contract.Response{Run: &contract.RunResult{
			ExitCode: 0, Output: "added alice@example.com\n", Digest: "sha256:5f1c",
			StartedAt: time.Date(2026, 11, 11, 14, 0, 9, 0, time.UTC), EndedAt: time.Date(2026, 11, 11, 14, 0, 10, 0, time.UTC)}},
			`{"outputs":null,"run":{"exitCode":0,"output":"added alice@example.com\n","digest":"sha256:5f1c","startedAt":"2026-11-11T14:00:09Z","endedAt":"2026-11-11T14:00:10Z"}}`, true},
		{"a run that failed with no output keeps its exit code and an empty output", contract.Response{Run: &contract.RunResult{ExitCode: 3}},
			`{"outputs":null,"run":{"exitCode":3,"output":""}}`, true},
		{"an ensure reporting an identity", contract.CallOutcome{Identity: map[string]string{"groupId": "sg-0abc"}}, `{"identity":{"groupId":"sg-0abc"}}`, false},
		{"an ensure reporting no identity encodes as before", contract.CallOutcome{}, `{}`, false},
		{"an observe naming the failure its type lists encodes it beside present", contract.Response{Resources: map[string]contract.ResourceOutcome{
			"data": {Observe: &contract.CallOutcome{State: contract.Present, Unhealthy: &contract.Unhealthy{
				Cause: "the volume is still creating, attaching or detaching", Detail: "the volume has been attaching for two hours"}}}}},
			`{"outputs":null,"resources":{"data":{"observe":{"state":"present","unhealthy":{"cause":"the volume is still creating, attaching or detaching","detail":"the volume has been attaching for two hours"}}}}}`, true},
		{"an observe naming no failure encodes byte for byte as the release before", contract.Response{Resources: map[string]contract.ResourceOutcome{
			"data": {Observe: &contract.CallOutcome{State: contract.Present, StatusDetail: "in-use", Type: "aws.ebs-volume"}}}},
			`{"outputs":null,"resources":{"data":{"observe":{"state":"present","statusDetail":"in-use","type":"aws.ebs-volume"}}}}`, true},
		{"an observe of a removal under way encodes what it waits on beside present", contract.Response{Resources: map[string]contract.ResourceOutcome{
			"app": {Observe: &contract.CallOutcome{State: contract.Present, Removing: &contract.Removing{WaitsOn: "pull request 4 to pass its managed-ci check"}}}}},
			`{"outputs":null,"resources":{"app":{"observe":{"state":"present","removing":{"waitsOn":"pull request 4 to pass its managed-ci check"}}}}}`, true},
		{"an observe waiting on a person encodes the action beside unknown", contract.Response{Resources: map[string]contract.ResourceOutcome{
			"app": {Observe: &contract.CallOutcome{State: contract.Unknown, WaitingOnPerson: &contract.WaitingOnPerson{Action: "approve pull request 4"}}}}},
			`{"outputs":null,"resources":{"app":{"observe":{"state":"unknown","waitingOnPerson":{"action":"approve pull request 4"}}}}}`, true},
		{"an observe carrying a plan and a blocker encodes each with its kind, text and fix", contract.CallOutcome{State: contract.Absent, Findings: []contract.Finding{
			{Kind: contract.FindingPlan, Text: "adds the build workflow"}, {Kind: contract.FindingBlocker, Text: "the Dockerfile names no port", Fix: "add an EXPOSE line"}}},
			`{"state":"absent","findings":[{"kind":"plan","text":"adds the build workflow"},{"kind":"blocker","text":"the Dockerfile names no port","fix":"add an EXPOSE line"}]}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			raw, err := json.Marshal(tc.value)

			if err != nil {
				t.Fatalf("json.Marshal(%+v): %v", tc.value, err)
			}
			if string(raw) != tc.want {
				t.Errorf("json.Marshal(%+v) = %s, want %s", tc.value, raw, tc.want)
			}
			if problems := kittest.CheckResponse(raw); tc.conforms && len(problems) != 0 {
				t.Errorf("kittest.CheckResponse(%s) = %v, want no problems", raw, problems)
			}
		})
	}
}

func TestResponse_RoundTripsThroughJSONUnderItsWireNames(t *testing.T) {
	cases := []struct {
		label   string
		in      contract.Response
		encoded func(t *testing.T, m map[string]any)
	}{
		{"resource secret outputs", contract.Response{
			ResourceSecretOutputs: map[string]map[string]string{"deploy-role": {"roleArn": "not-a-real-secret"}},
		}, func(t *testing.T, m map[string]any) {
			if _, ok := m["resourceSecretOutputs"]; !ok {
				t.Errorf("encoded response %v has no resourceSecretOutputs key", m)
			}
		}},
		{"own secrets", contract.Response{
			OwnSecrets: map[string]string{"gatewayCaKey": "own-secret-not-a-real-key", "joinToken": "own-secret-not-a-real-token"},
		}, func(t *testing.T, m map[string]any) {
			if _, ok := m["ownSecrets"]; !ok {
				t.Errorf("encoded response %v has no ownSecrets key", m)
			}
		}},
		{"a lease", contract.Response{Lease: &contract.Lease{
			Credential: json.RawMessage(`{"token":"lease-not-a-real-token"}`), ExpiresAt: time.Date(2026, 7, 15, 23, 15, 6, 0, time.UTC), ID: "tok_1"},
		}, func(t *testing.T, m map[string]any) {
			lease, _ := m["lease"].(map[string]any)
			for _, key := range []string{"credential", "expiresAt", "id"} {
				if _, ok := lease[key]; !ok {
					t.Errorf("encoded response %v has no lease.%s key", m, key)
				}
			}
		}},
		{"resources by name", contract.Response{
			Outputs: map[string]string{"vpcId": "vpc-0abc"},
			Resources: map[string]contract.ResourceOutcome{
				"network": {Ensure: &contract.CallOutcome{},
					Observe: &contract.CallOutcome{State: contract.Present, Outputs: map[string]string{"vpcId": "vpc-0abc"}}},
				"instance": {Delete: &contract.CallOutcome{Error: "instance still terminating"},
					Observe: &contract.CallOutcome{State: contract.Unknown, StatusDetail: "shutting-down"}},
			},
		}, func(t *testing.T, m map[string]any) {
			resources, _ := m["resources"].(map[string]any)
			network, _ := resources["network"].(map[string]any)
			if _, ok := network["ensure"]; !ok {
				t.Errorf(`network has no "ensure" key in %v: an ensure that ran is reported even when it carries nothing`, m)
			}
			if _, ok := network["delete"]; ok {
				t.Errorf(`network carries a "delete" key in %v, but no delete ran`, m)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			raw, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatal(err)
			}

			got := decodeResponse(t, raw)

			tc.encoded(t, jsonObject(t, raw))
			wantField(t, "round trip", got, tc.in)
		})
	}
}

func jsonNames(typ reflect.Type) []string {
	var out []string
	for i := 0; i < typ.NumField(); i++ {
		if name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ","); name != "" && name != "-" {
			out = append(out, name)
		}
	}
	return out
}

func TestResponseSchema_DeclaresEachFieldTheResponseTypesEncode(t *testing.T) {
	doc := jsonObject(t, contract.ResponseSchema)
	props := schemaAt(t, doc, doc, "properties")
	calls := schemaAt(t, doc, props, "resources", "additionalProperties", "properties")
	cases := []struct {
		label string
		check func(t *testing.T)
	}{
		{"replaces is a list of resource names", func(t *testing.T) {
			replaces := schemaAt(t, doc, props, "replaces")
			if !hasType(replaces, "array") || !hasType(schemaAt(t, doc, replaces, "items"), "string") {
				t.Errorf("properties.replaces = %v, want an array of strings", replaces)
			}
		}},
		{"ownSecrets is an optional object of strings", func(t *testing.T) {
			if slices.Contains(sortedStrings(doc["required"]), "ownSecrets") {
				t.Error(`response.schema.json requires "ownSecrets", but a response that returns none must stay valid`)
			}
			node := schemaAt(t, doc, props, "ownSecrets")
			objectOfStrings(t, doc, node, "properties.ownSecrets")
			if !hasType(node, "null") {
				t.Errorf("properties.ownSecrets type = %v, want null allowed, as the engine and kittest read null as none", schemaTypes(node))
			}
		}},
		{"lease is an optional object requiring a credential object and an expiry", func(t *testing.T) {
			if slices.Contains(sortedStrings(doc["required"]), "lease") {
				t.Error(`response.schema.json requires "lease", but a response that leases nothing must stay valid`)
			}
			lease := schemaAt(t, doc, props, "lease")
			if !hasType(lease, "object") || !slices.Equal(sortedStrings(lease["required"]), []string{"credential", "expiresAt"}) {
				t.Errorf("properties.lease = %v, want an object requiring credential and expiresAt", lease)
			}
			leaseProps := schemaAt(t, doc, lease, "properties")
			if f := schemaAt(t, doc, leaseProps, "credential"); !hasType(f, "object") {
				t.Errorf("properties.lease.credential type = %v, want object", schemaTypes(f))
			}
			if f := schemaAt(t, doc, leaseProps, "expiresAt"); !hasType(f, "string") || f["format"] != "date-time" {
				t.Errorf("properties.lease.expiresAt = %v, want a string of format date-time", f)
			}
			if f := schemaAt(t, doc, leaseProps, "id"); !hasType(f, "string") {
				t.Errorf("properties.lease.id type = %v, want string", schemaTypes(f))
			}
		}},
		{"run is an optional object requiring an exit code and the output", func(t *testing.T) {
			if slices.Contains(sortedStrings(doc["required"]), "run") {
				t.Error(`response.schema.json requires "run", but a response that ran nothing must stay valid`)
			}
			run := schemaAt(t, doc, props, "run")
			if !hasType(run, "object") || !slices.Equal(sortedStrings(run["required"]), []string{"exitCode", "output"}) {
				t.Errorf("properties.run = %v, want an object requiring exitCode and output", run)
			}
			runProps := schemaAt(t, doc, run, "properties")
			for field, ty := range map[string]string{"exitCode": "integer", "output": "string", "digest": "string"} {
				if f := schemaAt(t, doc, runProps, field); !hasType(f, ty) {
					t.Errorf("properties.run.%s type = %v, want %s", field, schemaTypes(f), ty)
				}
			}
			for _, field := range []string{"startedAt", "endedAt"} {
				if f := schemaAt(t, doc, runProps, field); !hasType(f, "string") || f["format"] != "date-time" {
					t.Errorf("properties.run.%s = %v, want a string of format date-time", field, f)
				}
			}
		}},
		{"redrive is a boolean", func(t *testing.T) {
			if f := schemaAt(t, doc, props, "redrive"); !hasType(f, "boolean") {
				t.Errorf("properties.redrive type = %v, want boolean", schemaTypes(f))
			}
		}},
		{"resources is an optional object of outcomes by name", func(t *testing.T) {
			if slices.Contains(sortedStrings(doc["required"]), "resources") {
				t.Error(`response.schema.json requires "resources", but a response that reports none must stay valid`)
			}
			resources := schemaAt(t, doc, props, "resources")
			if !hasType(resources, "object") || !hasType(schemaAt(t, doc, resources, "additionalProperties"), "object") {
				t.Errorf("properties.resources = %v, want an object of objects", resources)
			}
		}},
		{"each call outcome types its fields", func(t *testing.T) {
			wantTypes := map[string]string{"error": "string", "state": "string", "outputs": "object", "statusDetail": "string"}
			for _, verb := range []string{"ensure", "observe", "delete"} {
				call := schemaAt(t, doc, calls, verb)
				if !hasType(call, "object") {
					t.Errorf("resource outcome %q type = %v, want object", verb, schemaTypes(call))
				}
				for field, ty := range wantTypes {
					if f := schemaAt(t, doc, call, "properties", field); !hasType(f, ty) {
						t.Errorf("resource outcome %q field %q type = %v, want %s", verb, field, schemaTypes(f), ty)
					}
				}
			}
		}},
		{"an ensure's identity is an object of strings", func(t *testing.T) {
			objectOfStrings(t, doc, schemaAt(t, doc, calls, "ensure", "properties", "identity"), "ensure identity")
		}},
		{"removing is an object requiring waitsOn under each call", func(t *testing.T) {
			for _, verb := range []string{"ensure", "observe", "delete"} {
				removing := schemaAt(t, doc, calls, verb, "properties", "removing")
				if !hasType(removing, "object") || !slices.Contains(sortedStrings(removing["required"]), "waitsOn") || !hasType(schemaAt(t, doc, removing, "properties", "waitsOn"), "string") {
					t.Errorf("resource outcome %q removing = %v, want an object requiring the string waitsOn", verb, removing)
				}
			}
		}},
		{"every field the types encode is declared", func(t *testing.T) {
			for _, name := range jsonNames(reflect.TypeOf(contract.Response{})) {
				if _, ok := props[name]; !ok {
					t.Errorf("contract.Response encodes %q, which response.schema.json does not declare", name)
				}
			}
			for _, name := range jsonNames(reflect.TypeOf(contract.ResourceOutcome{})) {
				if _, ok := calls[name]; !ok {
					t.Errorf("contract.ResourceOutcome encodes %q, which response.schema.json does not declare", name)
				}
			}
			for verb, raw := range calls {
				call, _ := raw.(map[string]any)
				fields, _ := call["properties"].(map[string]any)
				for _, name := range jsonNames(reflect.TypeOf(contract.CallOutcome{})) {
					if _, ok := fields[name]; !ok {
						t.Errorf("contract.CallOutcome encodes %q, which response.schema.json does not declare under %q", name, verb)
					}
				}
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.label, tc.check)
	}
}
