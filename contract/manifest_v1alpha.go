package contract

import (
	"fmt"
	"reflect"
	"strings"
)

// v1alphaDecoder reads a ManifestAPIVersionV1Alpha manifest by the yaml
// tags of its types, which name the keys the platform's decoder reads, and
// reads each scalar as that decoder does.
var v1alphaDecoder = manifestDecoder{version: ManifestAPIVersionV1Alpha, tag: "yaml", coerce: true}

// The top-level type key is the `type: block` spelling of kind: Block.
const (
	v1alphaKindBlock = "Block"
	v1alphaTypeBlock = "block"
)

// V1AlphaManifest is a manifest under ManifestAPIVersionV1Alpha, read
// whole: Kind, Metadata and Spec hold every field the platform's decoder
// reads, and ID, Version, ReleaseClass, ReleaseNote, Outputs and Requires
// repeat the fields blockverify's manifest suite checks.
type V1AlphaManifest struct {
	// ID is metadata.id, untrimmed.
	ID string
	// Version, ReleaseClass and ReleaseNote are spec.version,
	// spec.releaseClass and spec.releaseNote, trimmed.
	Version      string
	ReleaseClass string
	ReleaseNote  string
	// Outputs is spec.outputs, each name trimmed.
	Outputs []string
	// Requires is spec.requires, each capability and read name trimmed.
	Requires []V1AlphaRequirement

	// Kind is "Block" when the manifest writes kind: Block or type: block.
	Kind     string
	Metadata V1AlphaMetadata
	Spec     V1AlphaSpec
}

// V1AlphaMetadata names a block and its owner.
type V1AlphaMetadata struct {
	ID    string `yaml:"id,omitempty"`
	Name  string `yaml:"name,omitempty"`
	Owner string `yaml:"owner,omitempty"`
}

// V1AlphaSpec is what a v1alpha block declares.
type V1AlphaSpec struct {
	Layer       string            `yaml:"layer,omitempty"`
	Version     string            `yaml:"version,omitempty"`
	Description string            `yaml:"description,omitempty"`
	Optional    bool              `yaml:"optional,omitempty"`
	Tags        []string          `yaml:"tags,omitempty"`
	Resources   []V1AlphaResource `yaml:"resources,omitempty"`
	_           []string          `yaml:"dependsOn,omitempty"`
	Provides    []string          `yaml:"provides,omitempty"`
	// ProvidedContracts types entries of Provides.
	ProvidedContracts []V1AlphaProvidedContract    `yaml:"providedContracts,omitempty"`
	Requires          []V1AlphaRequirement         `yaml:"requires,omitempty"`
	UpgradeStrategy   string                       `yaml:"upgradeStrategy,omitempty"`
	ReleaseClass      string                       `yaml:"releaseClass,omitempty"`
	ReleaseNote       string                       `yaml:"releaseNote,omitempty"`
	Lifecycle         *V1AlphaLifecycle            `yaml:"lifecycle,omitempty"`
	Params            []V1AlphaParam               `yaml:"params,omitempty"`
	Outputs           []string                     `yaml:"outputs,omitempty"`
	Observability     *V1AlphaObservability        `yaml:"observability,omitempty"`
	Docs              *V1AlphaDocs                 `yaml:"docs,omitempty"`
	Troubleshooting   []V1AlphaTroubleshootingRule `yaml:"troubleshooting,omitempty"`
	// ReceivesAppSecrets declares that the block is handed the consumer
	// app's own secrets.
	ReceivesAppSecrets bool `yaml:"receivesAppSecrets,omitempty"`
	// Continuity declares the durable state the block keeps.
	Continuity []V1AlphaContinuityUnit `yaml:"continuity,omitempty"`
	// Naming is the kit's own spec.naming, which holds AppNameNamingField
	// and nothing else.
	Naming *V1AlphaNaming `yaml:"naming,omitempty"`
}

// V1AlphaResource is one named bundle of outputs a block provisions.
type V1AlphaResource struct {
	Name        string   `yaml:"name,omitempty"`
	Description string   `yaml:"description,omitempty"`
	Produces    []string `yaml:"produces,omitempty"`
}

// V1AlphaRequirement is one item of a v1alpha spec.requires. Kind is also
// read under the key format.
type V1AlphaRequirement struct {
	Capability string   `yaml:"capability,omitempty"`
	Reads      []string `yaml:"reads,omitempty"`
	// Extension is the retired fan-in entry, read so it can be refused by
	// name.
	Extension          string                    `yaml:"extension,omitempty"`
	Hint               string                    `yaml:"hint,omitempty"`
	Kind               string                    `yaml:"kind,omitempty" fold:"format"`
	Schema             string                    `yaml:"schema,omitempty"`
	RequiresOrgAccount bool                      `yaml:"requiresOrgAccount,omitempty"`
	Optional           bool                      `yaml:"optional,omitempty"`
	TokenSchema        []V1AlphaTokenSchemaField `yaml:"tokenSchema,omitempty"`
	Verify             *V1AlphaVerifyProbe       `yaml:"verify,omitempty"`
	Scope              string                    `yaml:"scope,omitempty"`
}

// V1AlphaTokenSchemaField is one credential field a stored-token
// capability needs. Kind is also read under the key format.
type V1AlphaTokenSchemaField struct {
	Name        string `yaml:"name,omitempty"`
	Kind        string `yaml:"kind,omitempty" fold:"format"`
	Secret      bool   `yaml:"secret,omitempty"`
	Placeholder string `yaml:"placeholder,omitempty"`
}

// V1AlphaVerifyProbe is the HTTP probe a requirement declares to check its
// stored credential.
type V1AlphaVerifyProbe struct {
	Method        string            `yaml:"method,omitempty"`
	URLTemplate   string            `yaml:"urlTemplate,omitempty"`
	Headers       map[string]string `yaml:"headers,omitempty"`
	BasicAuthUser string            `yaml:"basicAuthUser,omitempty"`
	BasicAuthPass string            `yaml:"basicAuthPass,omitempty"`
	ExpectStatus  []int             `yaml:"expectStatus,omitempty"`
}

// V1AlphaProvidedContract types a capability the block provides. Kind is
// also read under the key format.
type V1AlphaProvidedContract struct {
	Capability string `yaml:"capability,omitempty"`
	Kind       string `yaml:"kind,omitempty" fold:"format"`
	Schema     string `yaml:"schema,omitempty"`
}

// V1AlphaLifecycle is the endpoint a block serves and how the platform
// drives it.
type V1AlphaLifecycle struct {
	Endpoint               string            `yaml:"endpoint,omitempty"`
	Operations             []string          `yaml:"operations,omitempty"`
	NeedsCredentialRefresh bool              `yaml:"needsCredentialRefresh,omitempty"`
	AbsenceProbe           bool              `yaml:"absenceProbe,omitempty"`
	ExclusiveNames         bool              `yaml:"exclusiveNames,omitempty"`
	VerbBudgets            map[string]string `yaml:"verbBudgets,omitempty"`
	// Contract names the operator-kit lifecycle contract the endpoint
	// speaks.
	Contract string `yaml:"contract,omitempty"`
	// RetainsOnUnsubscribe declares that the block keeps its resource on
	// unsubscribe by design.
	RetainsOnUnsubscribe bool `yaml:"retainsOnUnsubscribe,omitempty"`
	// Hooks is LifecycleHooksField: the hook points the block's kit handler
	// registers.
	Hooks []string `yaml:"hooks,omitempty"`
}

// V1AlphaParam is one value a subscriber or the platform supplies.
type V1AlphaParam struct {
	Name        string `yaml:"name,omitempty"`
	Default     string `yaml:"default,omitempty"`
	Label       string `yaml:"label,omitempty"`
	Description string `yaml:"description,omitempty"`
	Required    bool   `yaml:"required,omitempty"`
	Secret      bool   `yaml:"secret,omitempty"`
}

// V1AlphaObservability is what a block provisions and where its telemetry
// and cost live.
type V1AlphaObservability struct {
	Resources []V1AlphaObservabilityResource `yaml:"resources,omitempty"`
	Metrics   []V1AlphaMetricGroup           `yaml:"metrics,omitempty"`
	Cost      *V1AlphaCost                   `yaml:"cost,omitempty"`
}

// V1AlphaObservabilityResource is one provisioned resource, identified by an
// output the block publishes.
type V1AlphaObservabilityResource struct {
	ID     string         `yaml:"id,omitempty"`
	Type   string         `yaml:"type,omitempty"`
	From   string         `yaml:"from,omitempty"`
	Priced *V1AlphaPriced `yaml:"priced,omitempty"`
}

// V1AlphaPriced maps a resource onto a provider's price list.
type V1AlphaPriced struct {
	Service    string            `yaml:"service,omitempty"`
	Attributes map[string]string `yaml:"attributes,omitempty"`
	UsageBased bool              `yaml:"usageBased,omitempty"`
}

// V1AlphaMetricGroup is where one resource's metrics live.
type V1AlphaMetricGroup struct {
	Resource   string                `yaml:"resource,omitempty"`
	Namespace  string                `yaml:"namespace,omitempty"`
	Region     string                `yaml:"region,omitempty"`
	Dimensions map[string]string     `yaml:"dimensions,omitempty"`
	Series     []V1AlphaMetricSeries `yaml:"series,omitempty"`
}

// V1AlphaMetricSeries is one named metric of a group.
type V1AlphaMetricSeries struct {
	Name   string `yaml:"name,omitempty"`
	Metric string `yaml:"metric,omitempty"`
	Stat   string `yaml:"stat,omitempty"`
	Unit   string `yaml:"unit,omitempty"`
}

// V1AlphaCost names the keys a block's cost is grouped by.
type V1AlphaCost struct {
	GroupBy []string `yaml:"groupBy,omitempty"`
}

// V1AlphaDocs is where a block's documentation lives.
type V1AlphaDocs struct {
	Source *V1AlphaDocsSource `yaml:"source,omitempty"`
	Index  string             `yaml:"index,omitempty"`
	Pages  []V1AlphaDocsPage  `yaml:"pages,omitempty"`
	Links  []V1AlphaDocsLink  `yaml:"links,omitempty"`
}

// V1AlphaDocsSource is the git source the documentation is read from.
type V1AlphaDocsSource struct {
	Git  string `yaml:"git,omitempty"`
	Ref  string `yaml:"ref,omitempty"`
	Path string `yaml:"path,omitempty"`
}

// V1AlphaDocsPage is one documentation page, with the anchor each param it
// explains is found at.
type V1AlphaDocsPage struct {
	Title   string            `yaml:"title,omitempty"`
	Path    string            `yaml:"path,omitempty"`
	Anchors map[string]string `yaml:"anchors,omitempty"`
}

// V1AlphaDocsLink is one typed external link.
type V1AlphaDocsLink struct {
	Rel   string `yaml:"rel,omitempty"`
	Title string `yaml:"title,omitempty"`
	URL   string `yaml:"url,omitempty"`
}

// V1AlphaTroubleshootingRule is one failure a block knows how to explain:
// the text that matches it and the fixes for it.
type V1AlphaTroubleshootingRule struct {
	Signature string   `yaml:"signature,omitempty"`
	Title     string   `yaml:"title,omitempty"`
	Matches   []string `yaml:"matches,omitempty"`
	Fixes     []string `yaml:"fixes,omitempty"`
	Doc       string   `yaml:"doc,omitempty"`
}

// V1AlphaContinuityUnit is one durable thing a block keeps, and what
// happens to it when the block is replaced. Kind is also read under the
// key store.
type V1AlphaContinuityUnit struct {
	Name           string `yaml:"name,omitempty"`
	Kind           string `yaml:"kind,omitempty" fold:"store"`
	Stateful       bool   `yaml:"stateful,omitempty"`
	Handover       string `yaml:"handover,omitempty"`
	HandoverOutput string `yaml:"handoverOutput,omitempty"`
}

// V1AlphaNaming is spec.naming, whose one field is AppNameNamingField.
type V1AlphaNaming struct {
	AppName *AppNameNaming `yaml:"appName,omitempty"`
}

// v1alphaDocument is a v1alpha manifest's top level as the decoder reads
// it. Type is the type: block spelling of Kind, which readV1Alpha folds
// into Kind.
type v1alphaDocument struct {
	APIVersion string          `yaml:"apiVersion,omitempty"`
	Type       string          `yaml:"type,omitempty"`
	Kind       string          `yaml:"kind,omitempty"`
	Metadata   V1AlphaMetadata `yaml:"metadata,omitempty"`
	Spec       V1AlphaSpec     `yaml:"spec,omitempty"`
}

// v1alphaRetiredKeys maps the path of each key a v1alpha manifest may carry
// and the decoder drops to the message of the warning it gives.
var v1alphaRetiredKeys = map[string]string{
	"spec.dependsOn": "spec.dependsOn is retired and was ignored; " +
		"declare the dependency with spec.requires as a capability the target provides",
}

// readV1Alpha reads root strictly into a V1AlphaManifest and folds a
// top-level type: block into Kind. A type other than block, or a kind beside
// it other than Block, is refused. It also returns the manifest's warnings,
// as v1alphaWarnings gives them.
func readV1Alpha(root *yamlNode) (*V1AlphaManifest, []ManifestWarning, error) {
	var doc v1alphaDocument
	var dropped []string
	d := v1alphaDecoder
	d.dropped = &dropped
	if err := d.decode(root, reflect.ValueOf(&doc).Elem(), ""); err != nil {
		return nil, nil, err
	}
	if doc.Type != "" {
		if doc.Type != v1alphaTypeBlock {
			return nil, nil, fmt.Errorf("type is %q, but the type of a %s manifest must be %q",
				doc.Type, ManifestAPIVersionV1Alpha, v1alphaTypeBlock)
		}
		if doc.Kind != "" && doc.Kind != v1alphaKindBlock {
			return nil, nil, fmt.Errorf("type %q and kind %q disagree: kind must be %q beside type, or be left out",
				doc.Type, doc.Kind, v1alphaKindBlock)
		}
		doc.Kind = v1alphaKindBlock
	}
	a := &V1AlphaManifest{
		ID:           doc.Metadata.ID,
		Version:      strings.TrimSpace(doc.Spec.Version),
		ReleaseClass: strings.TrimSpace(doc.Spec.ReleaseClass),
		ReleaseNote:  strings.TrimSpace(doc.Spec.ReleaseNote),
		Outputs:      trimmedNames(doc.Spec.Outputs),
		Requires:     trimmedRequires(doc.Spec.Requires),
		Kind:         doc.Kind,
		Metadata:     doc.Metadata,
		Spec:         doc.Spec,
	}
	return a, v1alphaWarnings(dropped, a.Requires), nil
}

// v1alphaWarnings returns one warning per retired key among the dropped
// paths, then one per requirement whose optional is true, in requirement
// order and keyed by its capability.
func v1alphaWarnings(dropped []string, requires []V1AlphaRequirement) []ManifestWarning {
	var warnings []ManifestWarning
	for _, path := range dropped {
		if message, ok := v1alphaRetiredKeys[path]; ok {
			warnings = append(warnings, ManifestWarning{Path: path, Message: message})
		}
	}
	for _, r := range requires {
		if !r.Optional {
			continue
		}
		entry := "spec.requires[" + r.Capability + "]"
		warnings = append(warnings, ManifestWarning{
			Path: entry + ".optional",
			Message: "the optional flag on " + entry + " is retired and its value is still honoured; " +
				"a requires entry either names a capability the block needs or leaves it out, " +
				"so drop the entry rather than the flag when the block runs without a provider",
		})
	}
	return warnings
}

// trimmedNames returns a copy of names with each name trimmed.
func trimmedNames(names []string) []string {
	if names == nil {
		return nil
	}
	out := make([]string, len(names))
	for i, name := range names {
		out[i] = strings.TrimSpace(name)
	}
	return out
}

// trimmedRequires returns a copy of requires with each capability and read
// name trimmed.
func trimmedRequires(requires []V1AlphaRequirement) []V1AlphaRequirement {
	if requires == nil {
		return nil
	}
	out := make([]V1AlphaRequirement, len(requires))
	for i, r := range requires {
		r.Capability = strings.TrimSpace(r.Capability)
		r.Reads = trimmedNames(r.Reads)
		out[i] = r
	}
	return out
}
