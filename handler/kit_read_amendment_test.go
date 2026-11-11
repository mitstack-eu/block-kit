package handler_test

// A KitReadAmender adds the outputs a kit-observed type declares to a present answer, or holds any answer unknown, and the handler refuses every other amendment.

import (
	"cmp"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
)

// linkedSnapshot's type declares a secret and a plain output its spec cannot read, as a type whose resource has a linked object does, and one plain output the spec reads.
const linkedSnapshot = `{"version": 1, "types": [{"id": "aws.s3-bucket", "capability": "aws", "kind": "object-storage", "scope": "per-app",
	"stateful": true, "pricing": {"priced": true}, "category": "storage", "version": 1,
	"outputs": [{"name": "linkArn", "secret": true, "required": false}, {"name": "zone", "secret": false, "required": false},
		{"name": "region", "secret": false, "required": false}],
	"observeSpec": {"find": {"tag": "mitstack:subscription", "identity": "bucket"}, "probe": {"reads": ["tags", "existence"], "maxReads": 4},
	"usable": ["existence"], "fullRead": {"cloudControl": "AWS::S3::Bucket"}, "outputs": [{"name": "region", "property": "Region"}]}}]}`

const linkARN = "arn:aws:iam::123456789012:role/acme-data-link"

// linkedManifest provides its resource, since a validate answers the outputs of a provided resource alone.
var linkedManifest = v1betaManifest("linked-block", []string{"{ name: data, type: aws.s3-bucket }"}, "data")

func bucket(t *testing.T, subscription string) *recordingAWS {
	return &recordingAWS{t: t, tags: map[string]string{"mitstack:subscription": subscription}, props: map[string]any{"Region": "eu-central-1"}}
}

func amending(a handler.KitReadAmendment) *checkingBlock {
	return &checkingBlock{blockResource: &blockResource{cloud: newFakeCloud()}, amendment: a}
}

func amended(t *testing.T, operation string, aws *recordingAWS, block *checkingBlock, extra ...map[string]any) (int, contract.Response) {
	t.Helper()
	x, clock := kitObservedBy(t, linkedSnapshot, linkedManifest, aws, block)
	return validateAt(t, x, clock, append([]map[string]any{{"operation": operation}}, extra...)...)
}

func TestKitReadAmender_AddsDeclaredOutputsToAPresentAnswerOrHoldsAnyAnswerAndRefusesTheRest(t *testing.T) {
	const token, hold = "ghp_kit-read-hold-leak-3e9a", "its link role does not stand"
	link, zone := map[string]string{"linkArn": linkARN}, map[string]string{"zone": "eu-central-1a"}
	for _, tc := range []struct {
		name, operation, subscription, problem string
		missing, ends                          bool
		readErr, amendErr                      error
		scribble                               map[string]string
		amendment                              handler.KitReadAmendment
		status                                 int
		state                                  contract.State
		detail                                 []string
		secret                                 bool
	}{
		{name: "declared outputs join a present answer", amendment: handler.KitReadAmendment{Outputs: zone, SecretOutputs: link}, status: 200, state: contract.Present, secret: true},
		{name: "a hold turns a present answer unknown", amendment: handler.KitReadAmendment{Hold: hold}, status: 200, state: contract.Unknown, detail: []string{`resource "data" reads present, but ` + hold}},
		{name: "a hold turns an absent answer unknown, scrubbed of the request's secrets", missing: true, amendment: handler.KitReadAmendment{Hold: "its link role " + token + " still stands"}, status: 200, state: contract.Unknown, detail: []string{"reads absent, but its link role"}},
		{name: "an empty amendment leaves an absent answer", missing: true, status: 200, state: contract.Absent},
		{name: "a hold joins an unknown answer's detail", subscription: "sub-2", amendment: handler.KitReadAmendment{Hold: hold}, status: 200, state: contract.Unknown, detail: []string{"mitstack:subscription", "is held: " + hold}},
		{name: "a hold joins a full read checker's problem", problem: "versioning off", amendment: handler.KitReadAmendment{Hold: hold}, status: 200, state: contract.Unknown, detail: []string{"unhealthy: the full read found versioning off", "is held: " + hold}},
		{name: "a subscribe waits on a held present answer", operation: "subscribe", amendment: handler.KitReadAmendment{Hold: hold}, status: 200, state: contract.Unknown, detail: []string{hold}},
		{name: "a subscribe carries the outputs added to a present answer", operation: "subscribe", amendment: handler.KitReadAmendment{SecretOutputs: link}, status: 200, state: contract.Present, secret: true},
		{name: "an unsubscribe waits on a held absent answer", operation: "unsubscribe", missing: true, amendment: handler.KitReadAmendment{Hold: "its link role still stands"}, status: 200, state: contract.Unknown},
		{name: "an unsubscribe waits on an amender error", operation: "unsubscribe", missing: true, amendErr: errors.New("reading the link role failed"), status: 500},
		{name: "an unsubscribe ends on an absent answer left as it is", operation: "unsubscribe", missing: true, status: 200, state: contract.Absent, ends: true},
		{name: "writing into the answer handed over changes nothing", scribble: map[string]string{"rack": "r1"}, status: 200, state: contract.Present},
		{name: "a plain output the type does not declare", amendment: handler.KitReadAmendment{Outputs: map[string]string{"rack": "r1"}}, status: 500, detail: []string{`output "rack"`, "does not declare"}},
		{name: "a secret output the type does not declare", amendment: handler.KitReadAmendment{SecretOutputs: map[string]string{"token": "t"}}, status: 500, detail: []string{`output "token"`, "does not declare"}},
		{name: "a secret output added as plain", amendment: handler.KitReadAmendment{Outputs: link}, status: 500, detail: []string{`output "linkArn"`, "declares it secret"}},
		{name: "a plain output added as secret", amendment: handler.KitReadAmendment{SecretOutputs: zone}, status: 500, detail: []string{`output "zone"`, "declares it not secret"}},
		{name: "an output the spec already reads", amendment: handler.KitReadAmendment{Outputs: map[string]string{"region": "us-east-1"}}, status: 500, detail: []string{`output "region"`, "already reads"}},
		{name: "outputs beside an absent answer", missing: true, amendment: handler.KitReadAmendment{SecretOutputs: link}, status: 500, detail: []string{"present answer alone"}},
		{name: "outputs beside an unknown answer", subscription: "sub-2", amendment: handler.KitReadAmendment{Outputs: zone}, status: 500, detail: []string{"present answer alone"}},
		{name: "outputs beside a checker's problem", problem: "versioning off", amendment: handler.KitReadAmendment{Outputs: zone}, status: 500, detail: []string{"present answer alone"}},
		{name: "outputs beside a hold", amendment: handler.KitReadAmendment{Hold: hold, SecretOutputs: link}, status: 500, detail: []string{"present answer alone"}},
		{name: "an amender error", amendErr: errors.New("reading the link role failed"), status: 500, detail: []string{"reading the link role failed"}},
		{name: "a read error reaches no amender", readErr: errors.New("GetResource throttled"), amendment: handler.KitReadAmendment{Hold: hold}, status: 500, detail: []string{"GetResource throttled"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			aws := bucket(t, cmp.Or(tc.subscription, "sub-1"))
			aws.missing, aws.err = tc.missing, tc.readErr
			block := amending(tc.amendment)
			block.problem, block.amendErr, block.scribble = tc.problem, tc.amendErr, tc.scribble
			operation := cmp.Or(tc.operation, "validate")

			status, resp := amended(t, operation, aws, block, githubToken(token))

			o := resp.Resources["data"].Observe
			state, detail := contract.State(""), resp.Error+resp.StatusDetail
			if o != nil {
				state, detail = o.State, detail+o.StatusDetail
			}
			if status != tc.status || (tc.state != "" && state != tc.state) {
				t.Fatalf("answered %d, %s %q; want %d, %s", status, state, detail, tc.status, tc.state)
			}
			for _, want := range tc.detail {
				if !strings.Contains(detail, want) {
					t.Errorf("the answer reads %q, want it naming %q", detail, want)
				}
			}
			if ends := resp.Absent != nil && *resp.Absent; operation == "unsubscribe" && ends != tc.ends {
				t.Errorf("unsubscribe answered absent %v; want %t", resp.Absent, tc.ends)
			}
			if tc.state == contract.Unknown && operation != "unsubscribe" && !resp.Pending {
				t.Errorf("an unknown answer is not pending: %+v", resp)
			}
			if got := resp.ResourceSecretOutputs["data"]["linkArn"]; (got == linkARN) != tc.secret {
				t.Errorf("resourceSecretOutputs[data][linkArn] = %q; want it only when the block added it to a present answer", got)
			}
			if o != nil && (o.Outputs["linkArn"] != "" || o.Outputs["rack"] != "" || tc.status == 200 && o.Outputs["zone"] != tc.amendment.Outputs["zone"]) {
				t.Errorf("observe outputs = %v; want the zone the block added, if any, never the secret linkArn, and nothing written into the answer handed over", o.Outputs)
			}
			if strings.Contains(detail, linkARN) || strings.Contains(detail, token) {
				t.Errorf("the answer %q carries a secret output's value or the request's token", detail)
			}
			if !tc.missing && state == contract.Absent {
				t.Errorf("observe = %+v; the kit observer read the resource standing, so no amendment may answer it absent", o)
			}
			if handed := len(block.handed); (tc.readErr == nil) != (handed == 1) || handed > 1 {
				t.Errorf("the amender was handed %v; want one answer, or none after a read error", block.handed)
			}
		})
	}
}

func TestKitReadAmender_AmendsAFullReadAndAProbeAlike(t *testing.T) {
	logs := captureLog(t)
	block := amending(handler.KitReadAmendment{SecretOutputs: map[string]string{"linkArn": linkARN}})
	x, clock := kitObservedBy(t, linkedSnapshot, linkedManifest, bucket(t, "sub-1"), block)

	for _, read := range []string{"full read", "probe"} {
		status, resp := validateAt(t, x, clock)

		if status != http.StatusOK || resp.ResourceSecretOutputs["data"]["linkArn"] != linkARN || !strings.Contains(logs.String(), "aws.s3-bucket "+read+": present") {
			t.Errorf("the %s answered %d with secret outputs %v, logging %q; want the linkArn the block added", read, status, resp.ResourceSecretOutputs["data"], logs.String())
		}
		clock.set(clock.Now().Add(time.Minute))
	}
	if len(block.handed) != 2 {
		t.Errorf("the amender was handed %v, want one answer from the full read and one from the probe", block.handed)
	}
}
