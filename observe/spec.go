package observe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/mitstack-eu/block-kit/contract"
)

const (
	readExistence   = "existence"
	readTags        = "tags"
	usableSSMOnline = "ssmOnline"
)

// AWSSpecAPI answers the kit observer's reads of AWS, and each call is one AWS API request.
type AWSSpecAPI interface {
	// Exists answers the existence read of the resource the Cloud Control type typeName names by identifier.
	Exists(ctx context.Context, d contract.Desired, typeName, identifier string) (bool, error)
	// Tags answers the tags read, with found false when the resource does not exist.
	Tags(ctx context.Context, d contract.Desired, typeName, identifier string) (tags map[string]string, found bool, err error)
	// GetResource is Cloud Control's GetResource, answering the resource's properties document.
	GetResource(ctx context.Context, d contract.Desired, typeName, identifier string) (properties []byte, found bool, err error)
	AWSTaggingAPI
}

// SSMOnlineAPI is the AWS API a type naming the usable read ssmOnline needs, and each call is one AWS API request.
type SSMOnlineAPI interface {
	// SSMOnline answers whether SSM's DescribeInstanceInformation lists the instance with PingStatus Online.
	SSMOnline(ctx context.Context, d contract.Desired, instanceID string) (bool, error)
}

// HetznerObjectRead is one object the Hetzner Cloud API answered, its labels apart from the whole body.
type HetznerObjectRead struct {
	Labels map[string]string
	Body   []byte
}

// HetznerSpecAPI answers the kit observer's reads of the Hetzner Cloud API, and each call is one request.
type HetznerSpecAPI interface {
	// Get requests path, such as /servers/42, with found false on a 404.
	Get(ctx context.Context, d contract.Desired, path string) (object HetznerObjectRead, found bool, err error)
	HetznerLabelAPI
}

// FullRead is what one full read found of a resource: its tags and the provider's whole document of it.
type FullRead struct {
	Type       string
	Identifier string
	Tags       map[string]string
	Properties []byte
}

// KitObserver runs a catalogue type's observe spec from the type, the identity the block recorded and the subscription's id, and nothing else of the block; AWS serves a spec whose fullRead names cloudControl, Hetzner one naming collection.
type KitObserver struct {
	AWS     AWSSpecAPI
	Hetzner HetznerSpecAPI
}

// Serves refuses a type whose spec names no usable read, names a read the kit cannot make, plans more probe and usable reads than its maxReads, or needs a provider API left unset.
func (k KitObserver) Serves(t contract.SnapshotType) error {
	if !t.KitObserved() {
		return fmt.Errorf("observe: type %s carries no observe spec that finds its resources", t.ID)
	}
	spec := t.ObserveSpec
	for _, read := range spec.Probe.Reads {
		if read != readExistence && read != readTags {
			return fmt.Errorf("observe: type %s's probe names the read %q, and the kit observer makes only %s and %s", t.ID, read, readExistence, readTags)
		}
	}
	if !slices.Contains(spec.Probe.Reads, readTags) {
		return fmt.Errorf("observe: type %s's probe reads no tags, so it cannot tell the subscription's resource from another", t.ID)
	}
	planned := len(spec.Probe.Reads)
	if spec.FullRead.Collection != "" {
		planned = 1
	}
	usableReads, err := k.usableReads(t)
	if err != nil {
		return err
	}
	planned += usableReads
	if len(spec.Outputs) > 0 && spec.FullRead.CloudControl != "" && !slices.Contains(spec.Probe.Reads, readExistence) {
		return fmt.Errorf("observe: type %s's spec reads outputs, which an AWS probe reads with its existence read, but the probe makes no existence read", t.ID)
	}
	if planned > spec.Probe.MaxReads {
		return fmt.Errorf("observe: type %s's probe makes %d reads, above its maxReads of %d", t.ID, planned, spec.Probe.MaxReads)
	}
	switch {
	case spec.FullRead.CloudControl != "" && k.AWS == nil:
		return fmt.Errorf("observe: type %s is read through AWS Cloud Control, but the KitObserver has no AWS API", t.ID)
	case spec.FullRead.Collection != "" && k.Hetzner == nil:
		return fmt.Errorf("observe: type %s is read through the Hetzner API, but the KitObserver has no Hetzner API", t.ID)
	}
	return nil
}

// usableReads refuses a spec naming no usable read or one the kit cannot make, and counts the provider reads its usable reads add to a probe.
func (k KitObserver) usableReads(t contract.SnapshotType) (int, error) {
	spec := t.ObserveSpec
	if len(spec.Usable) == 0 {
		return 0, fmt.Errorf("observe: type %s's spec finds its resources but names no usable read, so the kit observer cannot tell when a consumer can use one", t.ID)
	}
	_, makesSSM := k.AWS.(SSMOnlineAPI)
	reads := 0
	for _, read := range spec.Usable {
		switch {
		case read == readExistence:
		case read == usableSSMOnline && spec.FullRead.CloudControl != "" && makesSSM:
			reads++
		case read == usableSSMOnline:
			return 0, fmt.Errorf("observe: type %s names the usable read %s, which the kit observer makes only for an AWS type whose AWS API has an SSMOnline method", t.ID, read)
		default:
			return 0, fmt.Errorf("observe: type %s's usable part names the read %q, and the kit observer makes only %s and %s", t.ID, read, readExistence, usableSSMOnline)
		}
	}
	return reads, nil
}

// Probe reads the resource the recorded identity names and the ownership tag on it, within the spec's maxReads.
func (k KitObserver) Probe(ctx context.Context, t contract.SnapshotType, r contract.ResourceRef) (contract.Observation, error) {
	obs, _, err := k.observe(ctx, t, r, false)
	return obs, err
}

// FullRead reads every attribute of the resource and lists what carries the subscription's tag, and its FullRead is nil when the resource was not found.
func (k KitObserver) FullRead(ctx context.Context, t contract.SnapshotType, r contract.ResourceRef) (contract.Observation, *FullRead, error) {
	return k.observe(ctx, t, r, true)
}

func (k KitObserver) observe(ctx context.Context, t contract.SnapshotType, r contract.ResourceRef, full bool) (contract.Observation, *FullRead, error) {
	if err := k.Serves(t); err != nil {
		return contract.Observation{}, nil, err
	}
	find := t.ObserveSpec.Find
	id := r.Identity[find.Identity]
	if id == "" && r.Unsubscribing {
		return k.absentUnlessUnrecorded(ctx, t, r, fmt.Sprintf("the block recorded no %s for it", find.Identity))
	}
	if id == "" {
		return unknown("resource %q: the block recorded no %s for it, and a tag alone proves nothing", r.Name, find.Identity), nil, nil
	}
	read, found, err := k.read(ctx, t, r.Desired, id, full)
	if err != nil {
		return contract.Observation{}, nil, err
	}
	if !found {
		return k.absentUnlessUnrecorded(ctx, t, r, fmt.Sprintf("%s %s does not exist", t.ID, id))
	}
	if obs, ok := disagreement(r, t, id, read.Tags); !ok {
		return obs, nil, nil
	}
	if obs, ok := k.usable(ctx, t, r, id); !ok {
		return obs, nil, nil
	}
	outputs, err := specOutputs(t, read.Properties)
	if err != nil {
		return contract.Observation{}, nil, err
	}
	obs := present(r, outputs)
	if !full {
		return obs, nil, nil
	}
	unrecorded, err := k.unrecorded(ctx, t, r)
	if err != nil {
		return contract.Observation{}, nil, err
	}
	if len(unrecorded) > 0 {
		obs.StatusDetail = unrecordedDetail(t, unrecorded)
	}
	return obs, read, nil
}

// disagreement answers the resource unhealthy when its ownership tag is missing or names another subscription, which teardown never deletes.
func disagreement(r contract.ResourceRef, t contract.SnapshotType, id string, tags map[string]string) (contract.Observation, bool) {
	tag := t.ObserveSpec.Find.Tag
	value, ok := tags[tag]
	switch {
	case !ok:
		return unknown("resource %q: unhealthy: %s %s stands but lacks the tag %s, so it is not proven the subscription's and is never deleted by teardown", r.Name, t.ID, id, tag), false
	case value != r.Desired.SubscriptionID:
		return unknown("resource %q: unhealthy: %s %s carries the tag %s=%s, another subscription's, so it is never deleted by teardown", r.Name, t.ID, id, tag, value), false
	}
	return contract.Observation{}, true
}

// absentUnlessUnrecorded keeps a tagged object no recorded identity names from ending the subscription, reporting it by name after why and deleting nothing.
func (k KitObserver) absentUnlessUnrecorded(ctx context.Context, t contract.SnapshotType, r contract.ResourceRef, why string) (contract.Observation, *FullRead, error) {
	unrecorded, err := k.unrecorded(ctx, t, r)
	if err != nil {
		return contract.Observation{}, nil, err
	}
	if len(unrecorded) > 0 {
		return unknown("resource %q: %s, but %s", r.Name, why, unrecordedDetail(t, unrecorded)), nil, nil
	}
	return absent(), nil, nil
}

func unrecordedDetail(t contract.SnapshotType, names []string) string {
	return fmt.Sprintf("unrecorded: %s carries the tag %s and no recorded identity names it; it is reported for a person to remove and never deleted",
		strings.Join(names, ", "), t.ObserveSpec.Find.Tag)
}

func (k KitObserver) read(ctx context.Context, t contract.SnapshotType, d contract.Desired, id string, full bool) (*FullRead, bool, error) {
	spec := t.ObserveSpec
	if spec.FullRead.Collection != "" {
		path := strings.ReplaceAll(spec.FullRead.Collection, "{id}", id)
		obj, found, err := k.Hetzner.Get(ctx, d, path)
		if err != nil || !found {
			return nil, found, err
		}
		return &FullRead{Type: t.ID, Identifier: id, Tags: obj.Labels, Properties: obj.Body}, true, nil
	}
	typeName := spec.FullRead.CloudControl
	if full {
		props, found, err := k.AWS.GetResource(ctx, d, typeName, id)
		if err != nil || !found {
			return nil, found, err
		}
		tags, err := cloudControlTags(props)
		if err != nil {
			return nil, false, fmt.Errorf("observe: the Cloud Control properties of %s %s: %w", typeName, id, err)
		}
		return &FullRead{Type: t.ID, Identifier: id, Tags: tags, Properties: props}, true, nil
	}
	read := &FullRead{Type: t.ID, Identifier: id}
	for _, name := range spec.Probe.Reads {
		var found bool
		var err error
		switch {
		case name == readTags:
			read.Tags, found, err = k.AWS.Tags(ctx, d, typeName, id)
		case len(spec.Outputs) > 0:
			read.Properties, found, err = k.AWS.GetResource(ctx, d, typeName, id)
		default:
			found, err = k.AWS.Exists(ctx, d, typeName, id)
		}
		if err != nil || !found {
			return nil, found, err
		}
	}
	return read, true, nil
}

// usable answers unknown, naming the first usable read that fails or errors, and the existence read passed when the observer found the resource.
func (k KitObserver) usable(ctx context.Context, t contract.SnapshotType, r contract.ResourceRef, id string) (contract.Observation, bool) {
	for _, read := range t.ObserveSpec.Usable {
		if read != usableSSMOnline {
			continue
		}
		switch online, err := k.AWS.(SSMOnlineAPI).SSMOnline(ctx, r.Desired, id); {
		case err != nil:
			return unknown("resource %q: %s %s carries the subscription's tag, but its usable read %s failed: %v", r.Name, t.ID, id, read, err), false
		case !online:
			return unknown("resource %q: %s %s carries the subscription's tag, but its usable read %s has not passed, so no consumer can use it yet", r.Name, t.ID, id, read), false
		}
	}
	return contract.Observation{}, true
}

// specOutputs reads each output the type's spec names from the provider's document of the resource, leaving out one the document does not hold as text.
func specOutputs(t contract.SnapshotType, doc []byte) (map[string]string, error) {
	if len(t.ObserveSpec.Outputs) == 0 {
		return nil, nil
	}
	var props map[string]any
	if err := json.Unmarshal(doc, &props); err != nil {
		return nil, fmt.Errorf("observe: the provider's document of %s: %w", t.ID, err)
	}
	outputs := map[string]string{}
	for _, o := range t.ObserveSpec.Outputs {
		value, _ := props[o.Property].(string)
		if o.Part == contract.ObservePartHost {
			value, _, _ = strings.Cut(value, "/")
		}
		if value != "" {
			outputs[o.Name] = value
		}
	}
	return outputs, nil
}

// cloudControlTags reads the Tags property, which Cloud Control serves as a list of Key and Value pairs on most types and as a map on some.
func cloudControlTags(props []byte) (map[string]string, error) {
	var doc struct {
		Tags json.RawMessage `json:"Tags"`
	}
	if err := json.Unmarshal(props, &doc); err != nil {
		return nil, err
	}
	if len(doc.Tags) == 0 {
		return map[string]string{}, nil
	}
	var pairs []struct{ Key, Value string }
	if err := json.Unmarshal(doc.Tags, &pairs); err == nil {
		tags := make(map[string]string, len(pairs))
		for _, p := range pairs {
			tags[p.Key] = p.Value
		}
		return tags, nil
	}
	var tags map[string]string
	if err := json.Unmarshal(doc.Tags, &tags); err != nil {
		return nil, errors.New("its Tags property is neither a list of Key and Value pairs nor a map")
	}
	return tags, nil
}

// unrecorded lists the objects of t carrying the subscription's tag that no identity recorded for a resource of t names.
func (k KitObserver) unrecorded(ctx context.Context, t contract.SnapshotType, r contract.ResourceRef) ([]string, error) {
	find, fullRead := t.ObserveSpec.Find, t.ObserveSpec.FullRead
	recorded := map[string]bool{r.Identity[find.Identity]: true}
	for _, rec := range r.Desired.RecordedResources {
		if rec.Type == t.ID && rec.Identity[find.Identity] != "" {
			recorded[rec.Identity[find.Identity]] = true
		}
	}
	var out []string
	if fullRead.Collection != "" {
		objects, err := k.Hetzner.ListByLabel(ctx, r.Desired, find.Tag+"="+r.Desired.SubscriptionID)
		if err != nil {
			return nil, err
		}
		for _, o := range objects {
			if hetznerKindIn(o.Kind, fullRead.Collection) && !recorded[o.ID] {
				out = append(out, o.Kind+" "+o.ID)
			}
		}
	} else {
		arns, err := k.AWS.ResourceARNs(ctx, r.Desired, find.Tag, r.Desired.SubscriptionID)
		if err != nil {
			return nil, err
		}
		for _, arn := range arns {
			if id, ok := arnOfType(arn, fullRead.CloudControl); ok && !recorded[id] {
				out = append(out, arn)
			}
		}
	}
	slices.Sort(out)
	return out, nil
}

// hetznerKindIn matches an object's kind, such as server or primary_ip, to the first segment of a collection path, such as /servers/{id}.
func hetznerKindIn(kind, collection string) bool {
	first, _, _ := strings.Cut(strings.TrimPrefix(collection, "/"), "/")
	return first == kind || first == kind+"s"
}

// arnOfType answers the last segment of an ARN whose service and resource kind match a Cloud Control type such as AWS::EC2::SecurityGroup, and an ARN with no resource kind, as an S3 bucket's, matches on its service alone.
func arnOfType(arn, typeName string) (string, bool) {
	parts := strings.SplitN(arn, ":", 6)
	names := strings.Split(typeName, "::")
	if len(parts) != 6 || parts[0] != "arn" || len(names) != 3 || parts[2] != strings.ToLower(names[1]) {
		return "", false
	}
	resource := parts[5]
	i := strings.IndexAny(resource, "/:")
	if i < 0 {
		return resource, true
	}
	if strings.ReplaceAll(resource[:i], "-", "") != strings.ToLower(names[2]) {
		return "", false
	}
	segments := strings.FieldsFunc(resource[i+1:], func(r rune) bool { return r == '/' || r == ':' })
	if len(segments) == 0 {
		return "", false
	}
	return segments[len(segments)-1], true
}
