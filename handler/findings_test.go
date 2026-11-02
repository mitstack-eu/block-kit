package handler_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
)

func TestFindings_AValidateAnswersAPlanAndRefusesOnABlockerCarryingEveryFindingScrubbed(t *testing.T) {
	const token = "ghp_finding-leak-0c4e2b19"
	plan := contract.Finding{Kind: contract.FindingPlan, Text: "adds the build workflow"}
	blocker := contract.Finding{Kind: contract.FindingBlocker, Text: "the Dockerfile names no port " + token, Fix: "add an EXPOSE line"}
	scrubbed := contract.Finding{Kind: contract.FindingBlocker, Text: "the Dockerfile names no port [REDACTED]", Fix: "add an EXPOSE line"}
	cases := []struct {
		label    string
		findings []contract.Finding
		status   int
		class    string
		want     []contract.Finding
	}{
		{"a plan finding answers present", []contract.Finding{plan}, http.StatusOK, "", []contract.Finding{plan}},
		{"a blocker answers as a refusal naming its fix", []contract.Finding{plan, blocker}, http.StatusBadRequest, handler.ClassBlocker, []contract.Finding{plan, scrubbed}},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			obs := asObservers(blockObservers(standingCloud()))
			obs["a"] = fixed(contract.Observation{State: contract.Present, Findings: tc.findings})
			secrets := map[string]any{"secrets": map[string]any{"github": map[string]any{"token": token}}}

			status, resp := serveBlock(t, blockCall{r: &blockResource{}, obs: obs, op: "validate", fields: secrets})

			got := resp.Resources["a"].Observe
			if status != tc.status || handler.RefusedClass(resp.StatusDetail) != tc.class || got == nil || !reflect.DeepEqual(got.Findings, tc.want) {
				t.Fatalf("validate = %d %q, a observe %+v; want %d class %q carrying %+v", status, resp.StatusDetail, got, tc.status, tc.class, tc.want)
			}
			if tc.class != "" && (!strings.Contains(resp.StatusDetail, "add an EXPOSE line") || strings.Contains(resp.StatusDetail, token)) {
				t.Errorf("statusDetail %q, want the blocker's fix with the secret scrubbed", resp.StatusDetail)
			}
		})
	}
}

func TestFindings_TheReadmeShowsTheAnswerAValidateWithFindingsSends(t *testing.T) {
	manifest := v1betaManifest("app-build", []string{"{ name: ci-workflows, type: github.workflow-file }"})
	serve := func(findings []contract.Finding) []byte {
		absent := fixed(contract.Observation{State: contract.Absent, Findings: findings})
		x := newHarness(t, &blockResource{}, withManifest(manifest), func(c *handler.Config) {
			c.Observers = map[string]handler.Observer{"ci-workflows": absent}
		})
		_, raw := x.post(map[string]any{"operation": "validate"})
		return raw
	}

	if got, want := string(serve(nil)), `{"outputs":null,"absent":true,"resources":{"ci-workflows":{"observe":{"state":"absent","type":"github.workflow-file"}}}}`; got != want {
		t.Errorf("a validate with no findings answers %s, want byte for byte %s", got, want)
	}

	raw := serve([]contract.Finding{
		{Kind: contract.FindingPlan, Text: "adds .github/workflows/build.yml, which builds the image from the Dockerfile at the repository root"},
		{Kind: contract.FindingBlocker, Text: "the Dockerfile names no port, so the host cannot route requests to the app", Fix: "add an EXPOSE line naming the port the app listens on"},
	})
	var indented bytes.Buffer
	if err := json.Indent(&indented, raw, "", "  "); err != nil {
		t.Fatalf("json.Indent(%s): %v", raw, err)
	}
	want, err := os.ReadFile("../examples/validate-answer.json")
	if err != nil {
		t.Fatalf("reading the answer the README shows: %v", err)
	}
	if indented.String()+"\n" != string(want) {
		t.Errorf("the validate answers\n%s\nbut examples/validate-answer.json, which the README shows, holds\n%s", indented.String(), want)
	}
}
