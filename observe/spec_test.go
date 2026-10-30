package observe_test

// The kit observer answers a catalogue type's observe spec as ADR 0108's agreement table and ADR 0109's usable reads set, from the type, the recorded identity and the subscription's id alone.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/observe"
)

const sub = "sub_abc"

func snapshotType(raw string) contract.SnapshotType {
	var t contract.SnapshotType
	if err := json.Unmarshal([]byte(raw), &t); err != nil {
		panic(err)
	}
	return t
}

const s3BucketJSON = `{"id": "aws.s3-bucket", "observeSpec": {"find": {"tag": "mitstack:subscription", "identity": "bucket"},
	"probe": {"reads": ["tags", "existence"], "maxReads": 4}, "usable": ["existence"], "fullRead": {"cloudControl": "AWS::S3::Bucket"}}}`

const ecrRepositoryJSON = `{"id": "aws.ecr-repository", "outputs": [{"name": "registryHost", "required": true}],
	"observeSpec": {"find": {"tag": "mitstack:subscription", "identity": "repositoryName"}, "probe": {"reads": ["existence", "tags"], "maxReads": 2},
	"usable": ["existence"], "fullRead": {"cloudControl": "AWS::ECR::Repository"}, "outputs": [{"name": "registryHost", "property": "RepositoryUri", "part": "host"}]}}`

const ec2InstanceJSON = `{"id": "aws.ec2-instance", "outputs": [{"name": "hostId", "required": true}],
	"observeSpec": {"find": {"tag": "mitstack:subscription", "identity": "instanceId"}, "probe": {"reads": ["existence", "tags"], "maxReads": 3},
	"usable": ["ssmOnline"], "fullRead": {"cloudControl": "AWS::EC2::Instance"}, "outputs": [{"name": "hostId", "property": "InstanceId"}]}}`

var s3Bucket, ecrRepository, ec2Instance = snapshotType(s3BucketJSON), snapshotType(ecrRepositoryJSON), snapshotType(ec2InstanceJSON)

var hetznerServer = snapshotType(`{"id": "hetzner.server", "observeSpec": {"find": {"tag": "mitstack.dev/subscription", "identity": "hostId"},
	"probe": {"reads": ["existence", "tags"], "maxReads": 1}, "usable": ["existence"], "fullRead": {"collection": "/servers/{id}"}}}`)

type fakeAWS struct {
	buckets map[string]map[string]string
	online  map[string]bool
	ssmErr  error
	calls   []string
}

func (f *fakeAWS) Exists(_ context.Context, _ contract.Desired, typeName, id string) (bool, error) {
	f.calls = append(f.calls, "Exists "+typeName+" "+id)
	_, ok := f.buckets[id]
	return ok, nil
}

func (f *fakeAWS) Tags(_ context.Context, _ contract.Desired, typeName, id string) (map[string]string, bool, error) {
	f.calls = append(f.calls, "Tags "+typeName+" "+id)
	tags, ok := f.buckets[id]
	return tags, ok, nil
}

func (f *fakeAWS) GetResource(_ context.Context, _ contract.Desired, typeName, id string) ([]byte, bool, error) {
	f.calls = append(f.calls, "GetResource "+typeName+" "+id)
	tags, ok := f.buckets[id]
	if !ok {
		return nil, false, nil
	}
	type pair struct{ Key, Value string }
	doc := struct {
		BucketName, InstanceId string
		RepositoryUri          string
		Tags                   []pair
	}{BucketName: id, InstanceId: id, RepositoryUri: "123456789012.dkr.ecr.eu-central-1.amazonaws.com/" + id}
	for k, v := range tags {
		doc.Tags = append(doc.Tags, pair{k, v})
	}
	raw, err := json.Marshal(doc)
	return raw, true, err
}

func (f *fakeAWS) ResourceARNs(_ context.Context, _ contract.Desired, key, value string) ([]string, error) {
	f.calls = append(f.calls, "ResourceARNs "+key+"="+value)
	var arns []string
	for name, tags := range f.buckets {
		if tags[key] == value {
			arns = append(arns, "arn:aws:s3:::"+name)
		}
	}
	return arns, nil
}

func (f *fakeAWS) SSMOnline(_ context.Context, _ contract.Desired, id string) (bool, error) {
	f.calls = append(f.calls, "SSMOnline "+id)
	return f.online[id], f.ssmErr
}

func (f *fakeAWS) onlyRead(t *testing.T) {
	t.Helper()
	for _, c := range f.calls {
		method, _, _ := strings.Cut(c, " ")
		if !slices.Contains([]string{"Exists", "Tags", "GetResource", "ResourceARNs", "SSMOnline"}, method) {
			t.Errorf("the observer made %q, but it may only read", c)
		}
	}
}

func bucketRef(identity map[string]string, recorded map[string]contract.RecordedResource) contract.ResourceRef {
	return contract.ResourceRef{Name: "data", Type: "aws.s3-bucket", Identity: identity, Desired: contract.Desired{
		Identity:          contract.Identity{SubscriptionID: sub},
		RecordedResources: recorded,
	}}
}

func TestKitObserver_AnswersEachRowOfTheAgreementTable(t *testing.T) {
	recordedData := map[string]contract.RecordedResource{"data": {Type: "aws.s3-bucket", Identity: map[string]string{"bucket": "acme-data"}}}
	cases := []struct {
		name                string
		buckets             map[string]map[string]string
		identity            map[string]string
		full, unsubscribing bool
		state               contract.State
		detail              []string
		calls               int
	}{
		{name: "the recorded identity exists and carries the subscription's tag: present, its usable existence read costing no call",
			buckets: map[string]map[string]string{"acme-data": {observe.AWSSubscriptionTag: sub}}, identity: map[string]string{"bucket": "acme-data"},
			state: contract.Present, calls: 2},
		{name: "the recorded identity exists and lacks the tag: unhealthy, naming the tag",
			buckets: map[string]map[string]string{"acme-data": {"team": "data"}}, identity: map[string]string{"bucket": "acme-data"},
			state: contract.Unknown, detail: []string{"unhealthy", observe.AWSSubscriptionTag, "never deleted"}, calls: 2},
		{name: "the recorded identity exists and carries another subscription's tag: unhealthy, naming it",
			buckets: map[string]map[string]string{"acme-data": {observe.AWSSubscriptionTag: "sub_other"}}, identity: map[string]string{"bucket": "acme-data"},
			state: contract.Unknown, detail: []string{"unhealthy", observe.AWSSubscriptionTag + "=sub_other", "never deleted"}, calls: 2},
		{name: "the recorded identity does not exist: absent",
			buckets: map[string]map[string]string{}, identity: map[string]string{"bucket": "acme-data"},
			state: contract.Absent, calls: 2},
		{name: "a tagged decoy no recorded identity names stops the absent answer, reported by name and never deleted",
			buckets: map[string]map[string]string{"decoy": {observe.AWSSubscriptionTag: sub}}, identity: map[string]string{"bucket": "acme-data"},
			state: contract.Unknown, detail: []string{"unrecorded", "arn:aws:s3:::decoy", "never deleted"}, calls: 2},
		{name: "a full read reports a tagged decoy by name beside a present resource",
			buckets:  map[string]map[string]string{"acme-data": {observe.AWSSubscriptionTag: sub}, "decoy": {observe.AWSSubscriptionTag: sub}},
			identity: map[string]string{"bucket": "acme-data"}, full: true,
			state: contract.Present, detail: []string{"unrecorded", "arn:aws:s3:::decoy"}, calls: 2},
		{name: "the block recorded no identity: unknown, with no read",
			buckets: map[string]map[string]string{"acme-data": {observe.AWSSubscriptionTag: sub}}, identity: nil,
			state: contract.Unknown, detail: []string{"recorded no bucket"}, calls: 0},
		{name: "an unsubscribe of a resource the block recorded no identity for, with nothing tagged: absent", unsubscribing: true, buckets: map[string]map[string]string{}, state: contract.Absent, calls: 1},
		{name: "an unsubscribe of a resource the block recorded no identity for, with a tagged decoy: unknown, naming it", unsubscribing: true, buckets: map[string]map[string]string{"decoy": {observe.AWSSubscriptionTag: sub}}, state: contract.Unknown, detail: []string{"recorded no bucket", "unrecorded", "arn:aws:s3:::decoy", "never deleted"}, calls: 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			api := &fakeAWS{buckets: c.buckets}
			k := observe.KitObserver{AWS: api}
			ref := bucketRef(c.identity, recordedData)
			ref.Unsubscribing = c.unsubscribing

			var obs contract.Observation
			var err error
			if c.full {
				obs, _, err = k.FullRead(context.Background(), s3Bucket, ref)
			} else {
				obs, err = k.Probe(context.Background(), s3Bucket, ref)
			}

			if err != nil {
				t.Fatalf("observe: %v", err)
			}
			if obs.State != c.state {
				t.Errorf("state = %s, want %s; detail %q", obs.State, c.state, obs.StatusDetail)
			}
			for _, want := range c.detail {
				if !strings.Contains(obs.StatusDetail, want) {
					t.Errorf("detail = %q, want it to name %q", obs.StatusDetail, want)
				}
			}
			if len(api.calls) != c.calls {
				t.Errorf("calls = %q, want %d", api.calls, c.calls)
			}
			api.onlyRead(t)
		})
	}
}

// A Go block, an OpenTofu module and the AWS CLI each leave different tags and different block facts beside the same two the observer reads.
func TestKitObserver_GivesTheSameAnswerWhoeverMadeTheBucket(t *testing.T) {
	makers := []struct {
		name    string
		tags    map[string]string
		desired contract.Desired
	}{
		{"a full-Go block", map[string]string{observe.AWSSubscriptionTag: sub},
			contract.Desired{Identity: contract.Identity{SubscriptionID: sub, BlockID: "go-s3-bucket"}}},
		{"an OpenTofu module", map[string]string{observe.AWSSubscriptionTag: sub, "AppId": "app_1", "mitstack:org": "org_1", "mitstack:block": "opentofu-s3-bucket"},
			contract.Desired{Identity: contract.Identity{SubscriptionID: sub, BlockID: "opentofu-s3-bucket"}, Params: map[string]string{"module": "s3"}}},
		{"the AWS CLI", map[string]string{observe.AWSSubscriptionTag: sub, "made-by": "aws s3api create-bucket"},
			contract.Desired{Identity: contract.Identity{SubscriptionID: sub}}},
	}
	var answers []contract.Observation
	for _, m := range makers {
		k := observe.KitObserver{AWS: &fakeAWS{buckets: map[string]map[string]string{"acme-data": m.tags}}}
		ref := contract.ResourceRef{Name: "data", Type: "aws.s3-bucket", Identity: map[string]string{"bucket": "acme-data"}, Desired: m.desired}
		probe, err := k.Probe(context.Background(), s3Bucket, ref)
		if err != nil {
			t.Fatalf("%s: probe: %v", m.name, err)
		}
		full, _, err := k.FullRead(context.Background(), s3Bucket, ref)
		if err != nil {
			t.Fatalf("%s: full read: %v", m.name, err)
		}
		answers = append(answers, probe, full)
	}
	for i, a := range answers {
		if !reflect.DeepEqual(a, answers[0]) || a.State != contract.Present {
			t.Errorf("answer %d (%s) = %+v, want present and equal to %+v", i, makers[i/2].name, a, answers[0])
		}
	}
}

func TestKitObserver_FullReadHandsBackTheCloudControlDocument(t *testing.T) {
	api := &fakeAWS{buckets: map[string]map[string]string{"acme-data": {observe.AWSSubscriptionTag: sub}}}

	obs, read, err := observe.KitObserver{AWS: api}.FullRead(context.Background(), s3Bucket, bucketRef(map[string]string{"bucket": "acme-data"}, nil))

	if err != nil || obs.State != contract.Present || read == nil {
		t.Fatalf("full read = %+v, %+v, %v; want present with its read", obs, read, err)
	}
	if read.Identifier != "acme-data" || read.Tags[observe.AWSSubscriptionTag] != sub || !strings.Contains(string(read.Properties), `"BucketName":"acme-data"`) {
		t.Errorf("read = %+v, want acme-data's tags and its Cloud Control document", read)
	}
	if want := []string{"GetResource AWS::S3::Bucket acme-data", "ResourceARNs " + observe.AWSSubscriptionTag + "=" + sub}; !slices.Equal(api.calls, want) {
		t.Errorf("calls = %q, want %q", api.calls, want)
	}
}

func TestKitObserver_ReportsAnECRRepositorysRegistryHostOnAProbeWithinItsMaxReadsAndOnAFullRead(t *testing.T) {
	api := &fakeAWS{buckets: map[string]map[string]string{"app": {observe.AWSSubscriptionTag: sub}}}
	ref := contract.ResourceRef{Name: "images", Type: ecrRepository.ID, Identity: map[string]string{"repositoryName": "app"}, Desired: contract.Desired{Identity: contract.Identity{SubscriptionID: sub}}}
	want := map[string]string{"registryHost": "123456789012.dkr.ecr.eu-central-1.amazonaws.com"}

	probe, err := observe.KitObserver{AWS: api}.Probe(context.Background(), ecrRepository, ref)
	if err != nil || probe.State != contract.Present || !reflect.DeepEqual(probe.Outputs, want) {
		t.Errorf("probe = %+v, %v; want present with %v", probe, err, want)
	}
	if reads := []string{"GetResource AWS::ECR::Repository app", "Tags AWS::ECR::Repository app"}; !slices.Equal(api.calls, reads) {
		t.Errorf("probe calls = %q, want %q, within maxReads %d", api.calls, reads, ecrRepository.ObserveSpec.Probe.MaxReads)
	}
	full, _, err := observe.KitObserver{AWS: api}.FullRead(context.Background(), ecrRepository, ref)
	if err != nil || full.State != contract.Present || !reflect.DeepEqual(full.Outputs, want) {
		t.Errorf("full read = %+v, %v; want present with %v", full, err, want)
	}
}

type fakeHetzner struct {
	servers map[string]map[string]string
	calls   []string
}

func (f *fakeHetzner) Get(_ context.Context, _ contract.Desired, path string) (observe.HetznerObjectRead, bool, error) {
	f.calls = append(f.calls, "GET "+path)
	labels, ok := f.servers[strings.TrimPrefix(path, "/servers/")]
	return observe.HetznerObjectRead{Labels: labels, Body: []byte(`{"server":{}}`)}, ok, nil
}

func (f *fakeHetzner) ListByLabel(_ context.Context, _ contract.Desired, selector string) ([]observe.HetznerObject, error) {
	f.calls = append(f.calls, "LIST "+selector)
	var out []observe.HetznerObject
	for id, labels := range f.servers {
		if fmt.Sprintf("%s=%s", observe.HetznerSubscriptionLabel, labels[observe.HetznerSubscriptionLabel]) == selector {
			out = append(out, observe.HetznerObject{Kind: "server", ID: id})
		}
	}
	return out, nil
}

func TestKitObserver_ReadsAHetznerCollectionPathInOneRequest(t *testing.T) {
	api := &fakeHetzner{servers: map[string]map[string]string{"42": {observe.HetznerSubscriptionLabel: sub}, "43": {observe.HetznerSubscriptionLabel: sub}}}
	ref := contract.ResourceRef{Name: "host", Type: "hetzner.server", Identity: map[string]string{"hostId": "42"}, Desired: contract.Desired{Identity: contract.Identity{SubscriptionID: sub}}}

	probe, err := observe.KitObserver{Hetzner: api}.Probe(context.Background(), hetznerServer, ref)
	if err != nil || probe.State != contract.Present || !slices.Equal(api.calls, []string{"GET /servers/42"}) {
		t.Fatalf("probe = %+v, %v, calls %q; want present in one GET /servers/42", probe, err, api.calls)
	}
	full, _, err := observe.KitObserver{Hetzner: api}.FullRead(context.Background(), hetznerServer, ref)
	if err != nil || full.State != contract.Present || !strings.Contains(full.StatusDetail, "server 43") {
		t.Errorf("full read = %+v, %v; want present, reporting server 43 unrecorded", full, err)
	}
}

func TestKitObserver_RefusesASpecItCannotServe(t *testing.T) {
	tooMany := snapshotType(strings.Replace(s3BucketJSON, `"maxReads": 4`, `"maxReads": 1`, 1))
	outputsUnread := snapshotType(strings.Replace(ecrRepositoryJSON, `"reads": ["existence", "tags"], "maxReads": 2`, `"reads": ["tags"], "maxReads": 1`, 1))
	cases := []struct {
		name string
		k    observe.KitObserver
		t    contract.SnapshotType
		want string
	}{
		{"a probe planning more reads than maxReads", observe.KitObserver{AWS: &fakeAWS{}}, tooMany, "above its maxReads of 1"},
		{"a probe with no existence read, which reads the outputs", observe.KitObserver{AWS: &fakeAWS{}}, outputsUnread, "makes no existence read"},
		{"a spec that finds its resources and names no usable read", observe.KitObserver{AWS: &fakeAWS{}},
			snapshotType(strings.Replace(s3BucketJSON, `"usable": ["existence"], `, "", 1)), "names no usable read"},
		{"a usable read the kit cannot make", observe.KitObserver{AWS: &fakeAWS{}}, snapshotType(strings.Replace(s3BucketJSON, `["existence"], "fullRead"`, `["dnsResolves"], "fullRead"`, 1)), `"dnsResolves"`},
		{"a usable read that takes the probe above maxReads", observe.KitObserver{AWS: &fakeAWS{}}, snapshotType(strings.Replace(ec2InstanceJSON, `"maxReads": 3`, `"maxReads": 2`, 1)), "above its maxReads of 2"},
		{"ssmOnline with an AWS API that makes no SSMOnline read", observe.KitObserver{AWS: struct{ observe.AWSSpecAPI }{&fakeAWS{}}}, ec2Instance, "SSMOnline"},
		{"an AWS type with no AWS API", observe.KitObserver{}, s3Bucket, "no AWS API"},
		{"a Hetzner type with no Hetzner API", observe.KitObserver{}, hetznerServer, "no Hetzner API"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.k.Serves(c.t)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("Serves = %v, want an error naming %q", err, c.want)
			}
		})
	}
}

func TestKitObserver_AnswersPresentOnlyOnceEveryUsableReadPasses(t *testing.T) {
	cases := []struct {
		name    string
		owner   string
		online  bool
		ssmErr  error
		state   contract.State
		detail  string
		outputs map[string]string
	}{
		{"SSM lists the instance online: present with its outputs", sub, true, nil, contract.Present, "", map[string]string{"hostId": "i-1"}},
		{"SSM does not list it online: unknown, naming ssmOnline", sub, false, nil, contract.Unknown, "ssmOnline", nil},
		{"the SSM read fails: unknown, naming ssmOnline", sub, true, errors.New("ThrottlingException"), contract.Unknown, "ssmOnline", nil},
		{"another subscription's tag: unhealthy, with no SSM read", "sub_other", true, nil, contract.Unknown, "unhealthy", nil},
	}
	ref := contract.ResourceRef{Name: "host", Type: ec2Instance.ID, Identity: map[string]string{"instanceId": "i-1"}, Desired: contract.Desired{Identity: contract.Identity{SubscriptionID: sub}}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			api := &fakeAWS{buckets: map[string]map[string]string{"i-1": {observe.AWSSubscriptionTag: c.owner}}, online: map[string]bool{"i-1": c.online}, ssmErr: c.ssmErr}
			k := observe.KitObserver{AWS: api}

			probe, probeErr := k.Probe(context.Background(), ec2Instance, ref)
			probeCalls := len(api.calls)
			full, _, fullErr := k.FullRead(context.Background(), ec2Instance, ref)

			ssmRead := slices.ContainsFunc(api.calls, func(call string) bool { return strings.HasPrefix(call, "SSMOnline") })
			if maxReads := ec2Instance.ObserveSpec.Probe.MaxReads; probeCalls > maxReads || (c.owner != sub && ssmRead) {
				t.Errorf("calls = %q, want a probe within maxReads %d and no SSMOnline call before the tag agrees", api.calls, maxReads)
			}
			for _, obs := range []contract.Observation{probe, full} {
				if probeErr != nil || fullErr != nil || obs.State != c.state || !reflect.DeepEqual(obs.Outputs, c.outputs) || !strings.Contains(obs.StatusDetail, c.detail) {
					t.Errorf("observation = %+v, errors %v and %v; want %s naming %q with outputs %v", obs, probeErr, fullErr, c.state, c.detail, c.outputs)
				}
			}
		})
	}
}
