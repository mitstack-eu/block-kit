package contract

import (
	"fmt"
	"strings"
)

// ManifestAPIVersionV1Alpha is the apiVersion of today's block manifest.
const ManifestAPIVersionV1Alpha = "mitstack/v1alpha"

// ManifestAPIVersion is the apiVersion of the block manifest ADR 0083 sets:
// a block's params, requirements by kind, resources in creation order, the
// resources it provides, its lifecycle and its observability.
const ManifestAPIVersion = "mitstack/v1beta"

// ParseManifest is the kit's one block manifest parser. It reads raw as
// YAML and switches on its apiVersion. Under ManifestAPIVersion it reads the
// whole manifest strictly into a BlockManifest: a key the types do not name,
// a missing required field, a many that is not true or false, a resource
// name used twice and a provides entry naming no resource are each refused,
// naming the path. Under ManifestAPIVersionV1Alpha it reads what
// blockverify's manifest suite checks into a V1AlphaManifest and refuses no
// field it does not know. Any other apiVersion, or none, is refused naming
// both known versions.
//
// The YAML it reads is a subset: block and flow mappings and sequences,
// comments, quoted scalars and block scalars. An alias or a merge key brings
// in text from elsewhere in the document, so where the parser reads one it
// refuses the manifest rather than guessing.
func ParseManifest(raw []byte) (Manifest, error) {
	root := parseYAML(string(raw))
	switch root.kind {
	case yamlNull:
		return Manifest{}, apiVersionError("")
	case yamlMap:
	default:
		return Manifest{}, unfollowable("the document")
	}
	apiVersion, err := rawScalarField(root, "apiVersion", "apiVersion")
	if err != nil {
		return Manifest{}, err
	}
	switch apiVersion {
	case ManifestAPIVersion:
		b, err := decodeBlockManifest(root)
		if err != nil {
			return Manifest{}, err
		}
		return Manifest{APIVersion: apiVersion, Block: b}, nil
	case ManifestAPIVersionV1Alpha:
		a, err := readV1Alpha(root)
		if err != nil {
			return Manifest{}, err
		}
		return Manifest{APIVersion: apiVersion, V1Alpha: a}, nil
	}
	return Manifest{}, apiVersionError(apiVersion)
}

func apiVersionError(got string) error {
	if got == "" {
		return fmt.Errorf("apiVersion is missing, so the manifest's shape is unknown; it must be %s or %s",
			ManifestAPIVersion, ManifestAPIVersionV1Alpha)
	}
	return fmt.Errorf("apiVersion %q is not a manifest version the kit knows; it must be %s or %s",
		got, ManifestAPIVersion, ManifestAPIVersionV1Alpha)
}

// Manifest is a parsed block manifest. Exactly one of Block and V1Alpha is
// non-nil, chosen by APIVersion.
type Manifest struct {
	APIVersion string
	// Block is the manifest under ManifestAPIVersion.
	Block *BlockManifest
	// V1Alpha is what the kit reads of a manifest under
	// ManifestAPIVersionV1Alpha.
	V1Alpha *V1AlphaManifest
}

// ID returns metadata.id under either version, untrimmed, since every
// character of an id is judged.
func (m Manifest) ID() string {
	switch {
	case m.Block != nil:
		return m.Block.Metadata.ID
	case m.V1Alpha != nil:
		return m.V1Alpha.ID
	}
	return ""
}

// Version returns spec.version, trimmed.
func (m Manifest) Version() string {
	switch {
	case m.Block != nil:
		return strings.TrimSpace(m.Block.Spec.Version)
	case m.V1Alpha != nil:
		return strings.TrimSpace(m.V1Alpha.Version)
	}
	return ""
}

// ReleaseClass returns spec.releaseClass, trimmed.
func (m Manifest) ReleaseClass() string {
	switch {
	case m.Block != nil:
		return strings.TrimSpace(m.Block.Spec.ReleaseClass)
	case m.V1Alpha != nil:
		return strings.TrimSpace(m.V1Alpha.ReleaseClass)
	}
	return ""
}

// ReleaseNote returns spec.releaseNote, trimmed, so a blank note reads as
// none.
func (m Manifest) ReleaseNote() string {
	switch {
	case m.Block != nil:
		return strings.TrimSpace(m.Block.Spec.ReleaseNote)
	case m.V1Alpha != nil:
		return strings.TrimSpace(m.V1Alpha.ReleaseNote)
	}
	return ""
}

// V1AlphaManifest is what the kit reads of a manifest under
// ManifestAPIVersionV1Alpha: the fields blockverify's manifest suite checks.
type V1AlphaManifest struct {
	// ID is metadata.id, untrimmed.
	ID string
	// Version, ReleaseClass and ReleaseNote are spec.version,
	// spec.releaseClass and spec.releaseNote, trimmed.
	Version      string
	ReleaseClass string
	ReleaseNote  string
	// Outputs is spec.outputs.
	Outputs []string
	// Requires is spec.requires.
	Requires []V1AlphaRequirement
}

// V1AlphaRequirement is one item of a v1alpha spec.requires.
type V1AlphaRequirement struct {
	Capability string
	Reads      []string
}

// BlockManifest is a block manifest under ManifestAPIVersion. The JSON
// Schema in ManifestSchema is generated from these types and their json
// tags, and ParseManifest reads a manifest by the same tags: a field
// without omitempty is required.
type BlockManifest struct {
	APIVersion string `json:"apiVersion"`
	// Type must be "block".
	Type     string           `json:"type"`
	Metadata ManifestMetadata `json:"metadata"`
	Spec     ManifestSpec     `json:"spec"`
}

// ManifestMetadata names a block and its owner.
type ManifestMetadata struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Owner string `json:"owner,omitempty"`
}

// ManifestSpec is what a block declares.
type ManifestSpec struct {
	Version      string `json:"version"`
	ReleaseClass string `json:"releaseClass,omitempty"`
	ReleaseNote  string `json:"releaseNote,omitempty"`
	Description  string `json:"description,omitempty"`
	// Params are the values a subscriber or the platform supplies.
	Params []ManifestParam `json:"params,omitempty"`
	// Requires is what the block needs, by kind.
	Requires []ManifestRequirement `json:"requires,omitempty"`
	// Resources is what the block creates, in creation order.
	Resources []ManifestResource `json:"resources"`
	// Provides names the resources the block offers to the rest of the
	// app. Each names an entry of Resources.
	Provides      []string               `json:"provides,omitempty"`
	Lifecycle     ManifestLifecycle      `json:"lifecycle"`
	Observability *ManifestObservability `json:"observability,omitempty"`
}

// ManifestParam is one value a subscriber or the platform supplies.
type ManifestParam struct {
	Name        string `json:"name"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
	Default     string `json:"default,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Secret      bool   `json:"secret,omitempty"`
}

// ManifestRequirement is one thing a block needs, by kind.
type ManifestRequirement struct {
	Kind string `json:"kind"`
	// In lists the capabilities the resource must live in. The manifest
	// takes one capability or a list; one gives a one-item slice.
	In []string `json:"in,omitempty"`
	// Name names the requirement, set only when the block needs two
	// resources of one kind.
	Name string `json:"name,omitempty"`
	// Many collects every offer of the kind and accepts none.
	Many bool `json:"many,omitempty"`
}

// ManifestResource is one resource a block creates.
type ManifestResource struct {
	// Name is unique in the block and stable across versions.
	Name   string                `json:"name"`
	Type   string                `json:"type"`
	Inputs map[string]InputValue `json:"inputs,omitempty"`
	// On names the resource or requirement the type lives on.
	On string `json:"on,omitempty"`
}

// InputValue is one input: a scalar (a binding such as params.image, or a
// literal such as 8080, kept as its YAML text) or a mapping of named inputs
// (as env is). Exactly one is set.
type InputValue struct {
	Scalar string
	Map    map[string]InputValue
}

// ManifestLifecycle is the endpoint a block serves, the operations it
// answers and the kit contract version it speaks.
type ManifestLifecycle struct {
	Endpoint   string   `json:"endpoint"`
	Operations []string `json:"operations"`
	Contract   string   `json:"contract"`
}

// ManifestObservability is what only the block's operator knows to watch.
type ManifestObservability struct {
	Metrics []ManifestMetricGroup `json:"metrics,omitempty"`
	Cost    *ManifestCost         `json:"cost,omitempty"`
}

// ManifestMetricGroup is the metrics of one resource, in one namespace.
type ManifestMetricGroup struct {
	Resource   string                 `json:"resource"`
	Namespace  string                 `json:"namespace"`
	Region     string                 `json:"region,omitempty"`
	Dimensions map[string]string      `json:"dimensions,omitempty"`
	Series     []ManifestMetricSeries `json:"series"`
}

// ManifestMetricSeries is one metric series of a group.
type ManifestMetricSeries struct {
	Name   string `json:"name"`
	Metric string `json:"metric"`
	Stat   string `json:"stat"`
	Unit   string `json:"unit,omitempty"`
}

// ManifestCost says how the block's cost is grouped.
type ManifestCost struct {
	GroupBy []string `json:"groupBy,omitempty"`
}
