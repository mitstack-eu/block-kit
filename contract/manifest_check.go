package contract

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
)

// CheckOptions relaxes CheckManifest, whose zero value is what the platform applies in production.
type CheckOptions struct {
	// AllowInsecureEndpoint accepts an http:// lifecycle endpoint, for a block served on a local network.
	AllowInsecureEndpoint bool
}

// LifecycleOperations are the verbs a manifest's lifecycle may declare.
var LifecycleOperations = []string{"subscribe", "unsubscribe", "upgrade", "validate", "rotate", OperationLogs, OperationVerifyConnection, OperationLease, OperationRun}

// ReleaseClasses are the kinds of change a release may declare.
var ReleaseClasses = []string{"breaking", "correctness-fix", "feature"}

// The lengths a block's name and description may run to, counted after trimming spaces.
const (
	nameMinLength        = 2
	nameMaxLength        = 40
	descriptionMinLength = 10
	descriptionMaxLength = 240
)

// semverPattern is strict SemVer 2.0.0, so it refuses a leading v and a fourth number.
var semverPattern = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)

// CheckManifest refuses what the platform refuses at apply short of the checks that need its database, judging each type by serves.
func CheckManifest(b *BlockManifest, serves func(typeID string) bool, opts CheckOptions) error {
	if err := checkMetadata(b.Metadata); err != nil {
		return err
	}
	if err := checkRelease(b.Spec); err != nil {
		return err
	}
	if err := checkLifecycle(b.Spec.Lifecycle, opts); err != nil {
		return err
	}
	if err := checkCapabilitySections(b); err != nil {
		return err
	}
	if b.Spec.Observability != nil {
		return fmt.Errorf("spec.observability: a %s manifest declares none, since each resource type declares the metric series of its resources", ManifestAPIVersion)
	}
	return checkTypes(b.Spec, serves, "the types the platform serves")
}

func checkMetadata(m ManifestMetadata) error {
	namespace, _, err := ParseBlockID(m.ID)
	if err != nil {
		return fmt.Errorf("metadata.id: %w", err)
	}
	if n := len(strings.TrimSpace(m.Name)); n < nameMinLength || n > nameMaxLength {
		return fmt.Errorf("metadata.name must be %d-%d characters", nameMinLength, nameMaxLength)
	}
	if !isSlug(m.Owner) {
		return errors.New("metadata.owner must be an org slug")
	}
	if namespace != "" && namespace != m.Owner {
		return fmt.Errorf("metadata.id %q sits under the namespace %q, but metadata.owner is %q; a private id must sit under its owner's namespace",
			m.ID, namespace, m.Owner)
	}
	return nil
}

func checkRelease(s ManifestSpec) error {
	if !semverPattern.MatchString(s.Version) {
		return fmt.Errorf("spec.version %q is not strict semver (MAJOR.MINOR.PATCH, e.g. 1.2.3)", s.Version)
	}
	if n := len(strings.TrimSpace(s.Description)); n < descriptionMinLength || n > descriptionMaxLength {
		return fmt.Errorf("spec.description must be %d-%d characters", descriptionMinLength, descriptionMaxLength)
	}
	if s.ReleaseClass != "" && !slices.Contains(ReleaseClasses, s.ReleaseClass) {
		return fmt.Errorf("spec.releaseClass %q is not one of %s", s.ReleaseClass, strings.Join(ReleaseClasses, ", "))
	}
	if s.ReleaseClass == "correctness-fix" && strings.TrimSpace(s.ReleaseNote) == "" {
		return errors.New("spec.releaseNote is required when spec.releaseClass is correctness-fix")
	}
	return nil
}

func checkLifecycle(l ManifestLifecycle, opts CheckOptions) error {
	switch {
	case strings.HasPrefix(l.Endpoint, "https://"):
	case opts.AllowInsecureEndpoint && strings.HasPrefix(l.Endpoint, "http://"):
	case opts.AllowInsecureEndpoint:
		return errors.New("spec.lifecycle.endpoint must be an http:// or https:// URL")
	default:
		return errors.New("spec.lifecycle.endpoint must be an https:// URL")
	}
	if len(l.Operations) == 0 {
		return errors.New("spec.lifecycle.operations must list at least one operation")
	}
	for i, op := range l.Operations {
		if !slices.Contains(LifecycleOperations, op) {
			return fmt.Errorf("spec.lifecycle.operations: %q is not one of %s", op, strings.Join(LifecycleOperations, ", "))
		}
		if slices.Contains(l.Operations[:i], op) {
			return fmt.Errorf("spec.lifecycle.operations: %q is listed twice", op)
		}
	}
	// Dispatch refuses an undeclared verb, so a block that can subscribe but not unsubscribe could never be torn down.
	if slices.Contains(l.Operations, "subscribe") && !slices.Contains(l.Operations, "unsubscribe") {
		return errors.New(`spec.lifecycle.operations: declaring "subscribe" requires declaring "unsubscribe", so that what the block provisions can be torn down`)
	}
	return nil
}

func checkTypes(spec ManifestSpec, held func(typeID string) bool, catalogue string) error {
	for i, q := range spec.Requires {
		if err := checkTypesHeld(fmt.Sprintf("spec.requires[%d]", i), q.Types, held, catalogue); err != nil {
			return err
		}
	}
	for i, r := range spec.Resources {
		if err := checkTypesHeld(fmt.Sprintf("spec.resources[%d]", i), r.Types, held, catalogue); err != nil {
			return err
		}
	}
	return nil
}

func checkTypesHeld(path string, types []string, held func(typeID string) bool, catalogue string) error {
	for _, id := range types {
		if !held(id) {
			return fmt.Errorf("%s: type %s is not in %s", path, id, catalogue)
		}
	}
	return nil
}

const manifestVerdictsDir = "manifest-verdicts"

//go:embed manifest-verdicts
var manifestVerdicts embed.FS

// ManifestVerdict is one row of the table the kit's and the platform's tests both run, with an empty Refusal for a manifest CheckManifest accepts.
type ManifestVerdict struct {
	Name     string
	Manifest []byte
	Refusal  string
}

// ManifestVerdicts reads the table's rows in the order verdicts.json lists them.
func ManifestVerdicts() ([]ManifestVerdict, error) {
	raw, err := manifestVerdicts.ReadFile(path.Join(manifestVerdictsDir, "verdicts.json"))
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Manifest string `json:"manifest"`
		Refusal  string `json:"refusal"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("contract: %s/verdicts.json: %w", manifestVerdictsDir, err)
	}
	verdicts := make([]ManifestVerdict, 0, len(rows))
	for _, row := range rows {
		manifest, err := manifestVerdicts.ReadFile(path.Join(manifestVerdictsDir, row.Manifest))
		if err != nil {
			return nil, err
		}
		verdicts = append(verdicts, ManifestVerdict{Name: row.Manifest, Manifest: manifest, Refusal: row.Refusal})
	}
	return verdicts, nil
}

// checkCapabilitySections keeps the four sections and verify-connection to a capability and holds its connection to its mechanism.
func checkCapabilitySections(b *BlockManifest) error {
	s := b.Spec
	verifies := slices.Contains(s.Lifecycle.Operations, OperationVerifyConnection)
	if b.Type != ManifestTypeCapability {
		for _, section := range []struct {
			name string
			held bool
		}{
			{"presentation", s.Presentation != nil}, {"connection", s.Connection != nil},
			{"kinds", len(s.Kinds) > 0}, {"reach", s.Reach != nil},
		} {
			if section.held {
				return fmt.Errorf("spec.%s: only a capability manifest declares it", section.name)
			}
		}
		if verifies {
			return fmt.Errorf("spec.lifecycle.operations: %q is declared by a capability manifest alone", OperationVerifyConnection)
		}
		return nil
	}
	if verifies && len(s.Lifecycle.Operations) > 1 {
		return fmt.Errorf("spec.lifecycle.operations: %q must be the only operation, since a connect-time check has no subscription", OperationVerifyConnection)
	}
	if verifies && (s.Connection == nil || s.Connection.Mechanism != MechanismStoredToken) {
		return fmt.Errorf("spec.connection: a manifest declaring %q needs a %s connection, since only a stored credential is checked", OperationVerifyConnection, MechanismStoredToken)
	}
	if s.Connection != nil {
		if err := checkConnection(*s.Connection); err != nil {
			return err
		}
	}
	for i, kind := range s.Kinds {
		if !isKebab(kind) {
			return fmt.Errorf("spec.kinds[%d]: %q is not a resource kind in lowercase kebab case", i, kind)
		}
		if slices.Contains(s.Kinds[:i], kind) {
			return fmt.Errorf("spec.kinds: %q is listed twice", kind)
		}
	}
	if s.Reach != nil && !isSlug(s.Reach.Capability) {
		return fmt.Errorf("spec.reach.capability: %q is not a capability slug", s.Reach.Capability)
	}
	return nil
}

func checkConnection(c ManifestConnection) error {
	if !slices.Contains(ConnectionMechanisms, c.Mechanism) {
		return fmt.Errorf("spec.connection.mechanism: %q is not one of %s", c.Mechanism, strings.Join(ConnectionMechanisms, ", "))
	}
	stored := c.Mechanism == MechanismStoredToken
	switch {
	case stored && len(c.TokenSchema) == 0:
		return fmt.Errorf("spec.connection.tokenSchema: a %s connection lists at least one field", MechanismStoredToken)
	case !stored && len(c.TokenSchema) > 0:
		return fmt.Errorf("spec.connection.tokenSchema: only a %s connection lists token fields", MechanismStoredToken)
	case c.Audience != "" && c.Mechanism != MechanismOIDC:
		return fmt.Errorf("spec.connection.audience: only an %s connection sets it", MechanismOIDC)
	case (c.InstallHint != "" || c.HeldOnlyByGrant) && c.Mechanism != MechanismOAuthApp:
		return fmt.Errorf("spec.connection: installHint and heldOnlyByGrant belong to an %s connection", MechanismOAuthApp)
	}
	for i, f := range c.TokenSchema {
		for _, earlier := range c.TokenSchema[:i] {
			if earlier.Name == f.Name {
				return fmt.Errorf("spec.connection.tokenSchema[%d]: the field %q is listed twice", i, f.Name)
			}
		}
	}
	return nil
}
