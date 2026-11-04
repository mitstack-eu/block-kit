package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/observe"
	"github.com/mitstack-eu/block-kit/webhook"
)

func TestDeclared_AValidateAnswersTheIdentityItsObserverReadAndItsUnhealthyCauseScrubbed(t *testing.T) {
	const token, cause = "ghp_unhealthy-leak-5d1e88a0", "the connected AWS role lacks permission for the call"
	read := map[string]string{"roleName": "a-role-read"}
	obs := asObservers(blockObservers(standingCloud()))
	obs["a"] = fixed(contract.Observation{State: contract.Present, Identity: read, Unhealthy: &contract.Unhealthy{Cause: cause + token, Detail: "denied for " + token}})

	_, resp := serveBlock(t, blockCall{r: &blockResource{}, obs: obs, op: "validate", fields: githubToken(token)})

	want := contract.Unhealthy{Cause: cause + "[REDACTED]", Detail: "denied for [REDACTED]"}
	if got := resp.Resources["a"].Observe; got == nil || !maps.Equal(got.Identity, read) || got.Unhealthy == nil || *got.Unhealthy != want || resp.Pending {
		t.Errorf("a observe = %+v, pending %t; want the identity its observer read, %v, and settled present with unhealthy %+v", got, resp.Pending, read, want)
	}
}

func githubToken(token string) map[string]any {
	return map[string]any{"secrets": map[string]any{"github": map[string]any{"token": token}}}
}

func TestDeclared_AValidateAnswersAPlanAndRefusesOnABlockerCarryingEveryFindingScrubbed(t *testing.T) {
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
		{"a finding of neither kind answers an error naming its kind", []contract.Finding{{Kind: "Blocker", Text: "no port"}}, http.StatusInternalServerError, "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			obs := asObservers(blockObservers(standingCloud()))
			obs["a"] = fixed(contract.Observation{State: contract.Present, Findings: tc.findings})

			status, resp := serveBlock(t, blockCall{r: &blockResource{}, obs: obs, op: "validate", fields: githubToken(token)})

			got := resp.Resources["a"].Observe
			if status != tc.status || handler.RefusedClass(resp.StatusDetail) != tc.class || got == nil || !reflect.DeepEqual(got.Findings, tc.want) {
				t.Fatalf("validate = %d %q %q, a observe %+v; want %d class %q carrying %+v", status, resp.StatusDetail, resp.Error, got, tc.status, tc.class, tc.want)
			}
			if tc.status == http.StatusInternalServerError && !strings.Contains(resp.Error, `"Blocker"`) {
				t.Errorf("error %q, want it to name the kind %q", resp.Error, "Blocker")
			}
			if tc.class != "" && (!strings.Contains(resp.StatusDetail, "add an EXPOSE line") || strings.Contains(resp.StatusDetail, token)) {
				t.Errorf("statusDetail %q, want the blocker's fix with the secret scrubbed", resp.StatusDetail)
			}
		})
	}
}

func TestDeclared_AValidateWhoseCheckNeedsACreatedAppAnswersTheNeedsAppRefusalNamingWhy(t *testing.T) {
	const why = "the scaffold commit's build is read by the app's id, and the request names no app"
	obs := asObservers(blockObservers(standingCloud()))
	obs["a"] = &observer{err: handler.NeedsApp("%s", why)}

	status, resp := serveBlock(t, blockCall{r: &blockResource{}, obs: obs, op: "validate"})

	if want := "refused:needs-app: " + why; status != http.StatusBadRequest || resp.StatusDetail != want || handler.RefusedClass(resp.StatusDetail) != handler.ClassNeedsApp {
		t.Fatalf("validate = %d %q; want 400 %q, the class the platform reads as checked at create", status, resp.StatusDetail, want)
	}
}

func TestDeclared_AValidateWithFindingsAnswersTheBytesTheReadmeShows(t *testing.T) {
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

const s3Snapshot = `{"version": 1, "types": [{"id": "aws.s3-bucket", "capability": "aws", "kind": "object-storage", "scope": "per-app",
	"stateful": true, "pricing": {"priced": true}, "category": "storage", "outputs": [], "version": 1,
	"observeSpec": {"find": {"tag": "mitstack:subscription", "identity": "bucket"}, "probe": {"reads": ["tags", "existence"], "maxReads": 4},
	"usable": ["existence"], "fullRead": {"cloudControl": "AWS::S3::Bucket"}}}]}`

var s3Manifest = v1betaManifest("s3-block", []string{"{ name: data, type: aws.s3-bucket }"})

type recordingAWS struct {
	t     *testing.T
	tags  map[string]string
	reads []string
	err   error
}

func (a *recordingAWS) Exists(context.Context, contract.Desired, string, string) (bool, error) {
	a.reads = append(a.reads, "Exists")
	return true, nil
}

func (a *recordingAWS) Tags(context.Context, contract.Desired, string, string) (map[string]string, bool, error) {
	a.reads = append(a.reads, "Tags")
	return a.tags, true, nil
}

func (a *recordingAWS) GetResource(_ context.Context, _ contract.Desired, _, id string) ([]byte, bool, error) {
	a.reads = append(a.reads, "GetResource")
	if a.err != nil {
		return nil, false, a.err
	}
	raw, err := json.Marshal(map[string]any{"BucketName": id, "Tags": []map[string]string{{"Key": "mitstack:subscription", "Value": a.tags["mitstack:subscription"]}}})
	return raw, true, err
}

func (a *recordingAWS) ResourceARNs(context.Context, contract.Desired, string, string) ([]string, error) {
	a.reads = append(a.reads, "ResourceARNs")
	return []string{"arn:aws:s3:::acme-data"}, nil
}

func (a *recordingAWS) probed(maxReads int) []string {
	a.t.Helper()
	reads := a.reads
	a.reads = nil
	if len(reads) > maxReads {
		a.t.Errorf("a probe made %d reads %q, above the type's maxReads of %d", len(reads), reads, maxReads)
	}
	return reads
}

type checkingBlock struct {
	*blockResource
	problem string
	checked []observe.FullRead
}

func (b *checkingBlock) CheckFullRead(_ context.Context, r contract.ResourceRef, read observe.FullRead) (string, error) {
	b.checked = append(b.checked, read)
	return b.problem, nil
}

func kitObserved(t *testing.T, problem string) (*harness, *recordingAWS, *checkingBlock, *tickingClock) {
	t.Helper()
	snap, err := contract.ParseTypeSnapshot([]byte(s3Snapshot))
	if err != nil {
		t.Fatalf("ParseTypeSnapshot: %v", err)
	}
	aws := &recordingAWS{t: t, tags: map[string]string{"mitstack:subscription": "sub-1"}}
	block := &checkingBlock{blockResource: &blockResource{}, problem: problem}
	clock := &tickingClock{now: time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)}
	x := newHarness(t, block, withManifest(s3Manifest), func(c *handler.Config) {
		c.Observers = nil
		c.Types = snap
		c.KitObserver = observe.KitObserver{AWS: aws}
		c.Now = clock.Now
	})
	return x, aws, block, clock
}

func (c *tickingClock) set(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = at
}

// healthCheck signs the validate at the clock's time, since the handler judges a request's age and its OBO token by that clock.
func healthCheck(t *testing.T, x *harness, clock *tickingClock) contract.Response {
	t.Helper()
	status, resp := validateAt(t, x, clock)
	if status != 200 {
		t.Fatalf("validate answered %d: %+v", status, resp)
	}
	return resp
}

func validateAt(t *testing.T, x *harness, clock *tickingClock, extra ...map[string]any) (int, contract.Response) {
	t.Helper()
	at := clock.Now()
	obo, err := webhook.MintOBO(x.s.Private, x.s.Kid, webhook.OBOClaims{Audience: x.audience, OrgID: "org-1", SubscriptionID: "sub-1"}, time.Hour, at)
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]any{"operation": "validate", "createdAt": at.Format(time.RFC3339Nano), "obo": obo, "recordedResources": map[string]any{
		"data": map[string]any{"type": "aws.s3-bucket", "identity": map[string]string{"bucket": "acme-data"}}}}
	for _, e := range extra {
		maps.Copy(fields, e)
	}
	return x.answer(fields)
}

func TestKitObserver_AHealthCheckIsAProbeWithinSixHoursOfAFullRead(t *testing.T) {
	x, aws, block, clock := kitObserved(t, "")
	logs := captureLog(t)
	start := clock.Now()
	cases := []struct {
		name   string
		at     time.Time
		reads  []string
		probe  bool
		hooked int
	}{
		{"a check with no recorded time is a full read that calls the hook", start, []string{"GetResource", "ResourceARNs"}, false, 1},
		{"a check within six hours of it is a probe", start.Add(6*time.Hour - time.Minute), []string{"Tags", "Exists"}, true, 1},
		{"a check six hours after the full read is a full read that calls the hook again", start.Add(6 * time.Hour), []string{"GetResource", "ResourceARNs"}, false, 2},
		{"a check just after that is a probe again", start.Add(6*time.Hour + time.Second), []string{"Tags", "Exists"}, true, 2},
	}
	for _, c := range cases {
		clock.set(c.at)

		resp := healthCheck(t, x, clock)

		var reads []string
		kind := "full read"
		if c.probe {
			reads, kind = aws.probed(4), "probe"
		} else {
			reads, aws.reads = aws.reads, nil
		}
		if !slices.Equal(reads, c.reads) {
			t.Errorf("%s: reads = %q, want %q", c.name, reads, c.reads)
		}
		if o := resp.Resources["data"].Observe; o == nil || o.State != contract.Present {
			t.Errorf("%s: observe = %+v, want present", c.name, o)
		}
		if len(block.checked) != c.hooked {
			t.Errorf("%s: the full-read hook ran %d times, want %d", c.name, len(block.checked), c.hooked)
		}
		if want := `observe: subscription sub-1 resource "data" aws.s3-bucket ` + kind + ": present\n"; strings.Count(logs.String(), "observe: ") != 1 || !strings.HasSuffix(logs.String(), want) {
			t.Errorf("%s: log = %q, want the one line %q", c.name, logs, want)
		}
		logs.Reset()
	}
	if len(block.checked) > 0 && (block.checked[0].Identifier != "acme-data" || !strings.Contains(string(block.checked[0].Properties), "acme-data")) {
		t.Errorf("the hook was handed %+v, want acme-data's Cloud Control document", block.checked[0])
	}
}

func TestKitObserver_AProblemTheFullReadHookFindsAnswersUnhealthy(t *testing.T) {
	x, _, _, clock := kitObserved(t, "versioning is suspended")
	logs := captureLog(t)

	resp := healthCheck(t, x, clock)

	if want := `aws.s3-bucket full read: unknown`; !strings.Contains(logs.String(), want) {
		t.Errorf("log = %q, want a line ending %q", logs, want)
	}

	o := resp.Resources["data"].Observe
	if o == nil || o.State != contract.Unknown || !strings.Contains(o.StatusDetail, "versioning is suspended") || !resp.Pending {
		t.Errorf("observe = %+v, pending %v; want unknown naming the hook's problem, and the check pending", o, resp.Pending)
	}
}

func TestKitObserver_ADeclaredResourceOfASpeccedTypeNeedsNoObserverButAnAPI(t *testing.T) {
	snap, err := contract.ParseTypeSnapshot([]byte(s3Snapshot))
	if err != nil {
		t.Fatal(err)
	}
	cfg := handler.Config{Resource: &blockResource{}, Manifest: []byte(s3Manifest), JWKSURL: "https://jwks.example/jwks.json", Types: snap}

	_, err = handler.New(cfg)

	if err == nil || !strings.Contains(err.Error(), "no AWS API") {
		t.Errorf("handler.New with no KitObserver.AWS = %v, want a refusal naming the missing AWS API", err)
	}
	cfg.KitObserver = observe.KitObserver{AWS: &recordingAWS{t: t}}
	if _, err := handler.New(cfg); err != nil {
		t.Errorf("handler.New with KitObserver.AWS and no Observers = %v, want it served", err)
	}
	cfg.Types, _ = contract.ParseTypeSnapshot([]byte(strings.Replace(s3Snapshot, `"usable": ["existence"], `, "", 1)))
	if _, err := handler.New(cfg); err == nil || !strings.Contains(err.Error(), "no usable read") {
		t.Errorf("handler.New over a spec naming no usable read = %v, want a refusal naming it", err)
	}
}

func TestKitObserver_AReadLogsFailedWhenItErrorsAndNoRequestSecret(t *testing.T) {
	x, aws, _, clock := kitObserved(t, "")
	logs := captureLog(t)
	aws.err = errors.New("cloud control is down")
	validateAt(t, x, clock)
	aws.err = nil

	validateAt(t, x, clock, map[string]any{"secretInputs": map[string]string{"token": "sub-1"}})

	for _, want := range []string{`observe: subscription sub-1 resource "data" aws.s3-bucket full read: failed`, `observe: subscription [REDACTED] resource "data" aws.s3-bucket full read: present`} {
		if !strings.Contains(logs.String(), want+"\n") {
			t.Errorf("log = %q, want the line %q", logs, want)
		}
	}
}

type convergingBlock struct {
	reportingBlock
	detail   string
	finished bool
	err      error
}

func (c convergingBlock) Converging(context.Context, contract.Desired) (string, bool, error) {
	return c.detail, c.finished, c.err
}

func TestKitObserver_AValidateReadsNothingWhileTheBlocksWorkRunsAndAsksForTheSubscribeOnceItFinishes(t *testing.T) {
	snap, err := contract.ParseTypeSnapshot([]byte(s3Snapshot))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		block   convergingBlock
		status  int
		state   contract.State
		redrive bool
		reads   int
		detail  string
	}{
		{"while the work runs", convergingBlock{detail: "converging: the apply runs"}, 200, contract.Unknown, false, 0, "converging: the apply runs"},
		{"once the work finished unreported", convergingBlock{finished: true}, 200, contract.Unknown, true, 0, "has finished"},
		{"once the work failed", convergingBlock{err: errors.New("the apply failed")}, 500, "", false, 0, "the apply failed"},
		{"with no work in flight", convergingBlock{}, 200, contract.Present, false, 2, ""},
	} {
		aws := &recordingAWS{t: t, tags: map[string]string{"mitstack:subscription": "sub-1"}}
		clock := &tickingClock{now: time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)}
		tc.block.reportingBlock = reportingBlock{blockResource: &blockResource{}}
		x := newHarness(t, tc.block, withManifest(s3Manifest), func(c *handler.Config) {
			c.Observers, c.Types, c.KitObserver, c.Now = nil, snap, observe.KitObserver{AWS: aws}, clock.Now
		})

		status, resp := validateAt(t, x, clock)

		state, detail := contract.State(""), resp.Error
		if o := resp.Resources["data"].Observe; o != nil {
			state, detail = o.State, o.StatusDetail
			if o.Type != "aws.s3-bucket" || o.Identity["bucket"] != "acme-data" {
				t.Errorf("%s: observe %+v dropped the type or identity the request recorded", tc.name, o)
			}
		}
		if status != tc.status || state != tc.state || resp.Pending != (state == contract.Unknown) || resp.Redrive != tc.redrive || len(aws.reads) != tc.reads || !strings.Contains(detail, tc.detail) {
			t.Errorf("%s: %d, %s %q, pending %t, redrive %t after reads %q; want %d, %s naming %q, redrive %t after %d reads", tc.name, status, state, detail, resp.Pending, resp.Redrive, aws.reads, tc.status, tc.state, tc.detail, tc.redrive, tc.reads)
		}
	}
}
