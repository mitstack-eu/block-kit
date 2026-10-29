package handler_test

// A health check of a resource whose type carries an observe spec is the kit observer's probe, or its full read when the last is six hours old or unrecorded, which the block's full-read hook is handed.

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/observe"
	"github.com/mitstack-eu/block-kit/webhook"
)

const s3Snapshot = `{"version": 1, "types": [{"id": "aws.s3-bucket", "capability": "aws", "kind": "object-storage", "scope": "per-app",
	"stateful": true, "pricing": {"priced": true}, "category": "storage", "outputs": [], "version": 1,
	"observeSpec": {"find": {"tag": "mitstack:subscription", "identity": "bucket"}, "probe": {"reads": ["tags", "existence"], "maxReads": 4},
	"fullRead": {"cloudControl": "AWS::S3::Bucket"}}}]}`

var s3Manifest = v1betaManifest("s3-block", []string{"{ name: data, type: aws.s3-bucket }"})

// recordingAWS stands in for AWS, holding one bucket and recording every read; a probe check fails the test when it reads more than the type's maxReads.
type recordingAWS struct {
	t     *testing.T
	tags  map[string]string
	reads []string
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

// checkingBlock is a block-level Resource whose full-read hook records each read and answers problem.
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
	at := clock.Now()
	obo, err := webhook.MintOBO(x.s.Private, x.s.Kid, webhook.OBOClaims{Audience: x.audience, OrgID: "org-1", SubscriptionID: "sub-1"}, time.Hour, at)
	if err != nil {
		t.Fatal(err)
	}
	status, resp := x.answer(map[string]any{"operation": "validate", "createdAt": at.Format(time.RFC3339Nano), "obo": obo, "recordedResources": map[string]any{
		"data": map[string]any{"type": "aws.s3-bucket", "identity": map[string]string{"bucket": "acme-data"}}}})
	if status != 200 {
		t.Fatalf("validate answered %d: %+v", status, resp)
	}
	return resp
}

func TestKitObserver_AHealthCheckIsAProbeWithinSixHoursOfAFullRead(t *testing.T) {
	x, aws, block, clock := kitObserved(t, "")
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
		if c.probe {
			reads = aws.probed(4)
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
	}
	if len(block.checked) > 0 && (block.checked[0].Identifier != "acme-data" || !strings.Contains(string(block.checked[0].Properties), "acme-data")) {
		t.Errorf("the hook was handed %+v, want acme-data's Cloud Control document", block.checked[0])
	}
}

func TestKitObserver_AProblemTheFullReadHookFindsAnswersUnhealthy(t *testing.T) {
	x, _, _, clock := kitObserved(t, "versioning is suspended")

	resp := healthCheck(t, x, clock)

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
}
