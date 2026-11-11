package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"
)

type State string

// The three states an Observation can carry; Unknown means the question was not settled this call, for example an async delete still converging.
const (
	Present State = "present"
	Absent  State = "absent"
	Unknown State = "unknown"
)

type InstanceConfig struct {
	Name     string            `json:"name"`
	Endpoint string            `json:"endpoint"`
	Config   map[string]string `json:"config"`
}

// CloudIdentity is the federated cloud-account identity, nil when the org has no connected cloud_connection.
type CloudIdentity struct {
	Provider         string `json:"provider"`
	RoleARN          string `json:"roleArn"`
	WebIdentityToken string `json:"webIdentityToken"`
	AccountRef       string `json:"accountRef,omitempty"`
}

type EventsCallback struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

// Request's JSON tags mirror the engine's request struct field for field, and a request naming no resource leaves Resource, ResourceIdentity and ResourceType empty and runs the verb on every declared resource.
type Request struct {
	Operation        string                         `json:"operation"`
	SubscriptionID   string                         `json:"subscriptionId"`
	IdempotencyKey   string                         `json:"idempotencyKey"`
	BlockID          string                         `json:"blockId"`
	BlockVersion     string                         `json:"blockVersion"`
	OrgID            string                         `json:"orgId"`
	AppID            string                         `json:"appId"`
	Params           map[string]string              `json:"params"`
	Instances        map[string]InstanceConfig      `json:"instances,omitempty"`
	Secrets          map[string]map[string]string   `json:"secrets,omitempty"`
	OBO              string                         `json:"obo,omitempty"`
	EventsCallback   *EventsCallback                `json:"eventsCallback,omitempty"`
	SecretInputs     map[string]string              `json:"secretInputs,omitempty"`
	AppSecrets       map[string]string              `json:"appSecrets,omitempty"`
	Bindings         map[string]map[string]string   `json:"bindings,omitempty"`
	BindingsAll      map[string][]map[string]string `json:"bindingsAll,omitempty"`
	Absences         map[string]Absence             `json:"absences,omitempty"`
	CloudIdentity    *CloudIdentity                 `json:"cloudIdentity,omitempty"`
	CreatedAt        time.Time                      `json:"createdAt"`
	Nonce            string                         `json:"nonce"`
	PriorOutputs     map[string]string              `json:"priorOutputs,omitempty"`
	Resource         string                         `json:"resource,omitempty"`
	ResourceIdentity map[string]string              `json:"resourceIdentity,omitempty"`
	// ResourceUnsubscribing marks a request naming one resource as a teardown's probe, so the resource's observer answers as it does during an unsubscribe.
	ResourceUnsubscribing bool `json:"resourceUnsubscribing,omitempty"`

	ResourceType      string                      `json:"resourceType,omitempty"`
	RecordedResources map[string]RecordedResource `json:"recordedResources,omitempty"`
	RequirementTypes  map[string]string           `json:"requirementTypes,omitempty"`
	SettlesAt         *time.Time                  `json:"settlesAt,omitempty"`
	NeverActive       bool                        `json:"neverActive,omitempty"`

	IssueAppID string `json:"issueAppId,omitempty"`

	Unit      string `json:"unit,omitempty"`
	Replacing bool   `json:"replacing,omitempty"`

	// LeaseID, Want, Retire and Caller are read on a lease alone, and Resource then names the resource the credential is leased from.
	LeaseID string          `json:"leaseId,omitempty"`
	Want    json.RawMessage `json:"want,omitempty"`
	Retire  []string        `json:"retire,omitempty"`
	Caller  *LeaseCaller    `json:"caller,omitempty"`

	// RunID, Command and Process are read on a run alone.
	RunID   string `json:"runId,omitempty"`
	Command string `json:"command,omitempty"`
	Process string `json:"process,omitempty"`

	OwnSecrets map[string]string `json:"ownSecrets,omitempty"`

	CapabilitySlug    string            `json:"capabilitySlug,omitempty"`
	EnteredCredential map[string]string `json:"enteredCredential,omitempty"`
}

// OperationSnapshot asks a block to snapshot one declared state, check the snapshot by a scratch restore and publish its handover only when the check passes, and OperationRestore asks it to put that state back from the handover.
const (
	OperationSnapshot = "snapshot"
	OperationRestore  = "restore"
)

// HandoverOutputKey is the output a snapshot publishes the handover of unit under, and the key of the prior output a restore reads it from.
func HandoverOutputKey(unit string) string { return "handover." + unit }

// OperationIssue asks a capability for one app's credential, OperationLogs asks a block for one subscription's recent lines on /logs, never on /hook, OperationVerifyConnection asks a capability's operator whether the credential an org entered works, before any subscription exists, OperationLease asks a block for a short-lived credential of a resource it provides, or to retire the ones it minted, and OperationRun asks a block to run a builder's one-off command in a fresh container of the app.
const (
	OperationIssue            = "issue"
	OperationLogs             = "logs"
	OperationVerifyConnection = "verify-connection"
	OperationLease            = "lease"
	OperationRun              = "run"
)

// MaxRunCommandLength is the longest command a run carries, in printable ASCII characters.
const MaxRunCommandLength = 1000

// RunRequest is what a run asks of a block: run Command once for the run ID in Process, the block's entry when empty.
type RunRequest struct {
	ID      string
	Command string
	Process string
}

// RunResult is a run that ended; a command that exited non-zero ended too.
type RunResult struct {
	ExitCode  int       `json:"exitCode"`
	Output    string    `json:"output"`
	Digest    string    `json:"digest,omitempty"`
	StartedAt time.Time `json:"startedAt,omitzero"`
	EndedAt   time.Time `json:"endedAt,omitzero"`
}

// MaxLeaseLife is the longest a leased credential may live, since a credential that lives longer is no lease.
const MaxLeaseLife = 24 * time.Hour

// LeaseCaller is the subscription that asked for a lease and the blocks the platform records as placed on it, which bound what a provider may mint for it.
type LeaseCaller struct {
	SubscriptionID string   `json:"subscriptionId"`
	Placed         []Placed `json:"placed,omitempty"`
}

type Placed struct {
	App            string `json:"app"`
	BlockID        string `json:"blockId"`
	SubscriptionID string `json:"subscriptionId"`
}

// LeaseRequest is what a lease asks of a provider: with ID set, a credential of Resource shaped by Want for Caller, and Retire the credential ids to delete; with ID empty, only to delete Retire.
type LeaseRequest struct {
	ID       string
	Resource string
	Want     json.RawMessage
	Retire   []string
	Caller   LeaseCaller
}

// Lease is the credential a provider minted, which the platform passes on unread and never stores. ID is the provider's handle for deleting it and is left out for one that expires on its own.
type Lease struct {
	Credential json.RawMessage `json:"credential"`
	ExpiresAt  time.Time       `json:"expiresAt"`
	ID         string          `json:"id,omitempty"`
}

// The three answers to a verify-connection: the vendor accepted the credential, refused it, or said nothing about it.
const (
	VerdictVerified    = "verified"
	VerdictRejected    = "rejected"
	VerdictUnreachable = "unreachable"
)

// Credential is what a capability issues one app: values that are secret, and when they stop working.
type Credential struct {
	Values    map[string]string `json:"values"`
	ExpiresAt time.Time         `json:"expiresAt"`
}

// Absence is the platform's attestation, keyed by requirement reference like Bindings, that its own observer read the bound provider's resource absent, whose ProvesAbsence takes the meaning of reportSignature.provesAbsence.
type Absence struct {
	Observer      string    `json:"observer"`
	Resource      string    `json:"resource"`
	ReadAbsentAt  time.Time `json:"readAbsentAt"`
	ProvesAbsence bool      `json:"provesAbsence"`
}

// AttestedAbsent is the attestation that the resource bound with subscriptionID, through Bindings or BindingsAll, is absent, false for any other binding and for an attestation that proves nothing.
func (d Desired) AttestedAbsent(subscriptionID string) (Absence, bool) {
	if subscriptionID == "" {
		return Absence{}, false
	}
	for ref, a := range d.Absences {
		bound := slices.ContainsFunc(append([]map[string]string{d.Bindings[ref]}, d.BindingsAll[ref]...), func(b map[string]string) bool { return b["subscriptionId"] == subscriptionID })
		if bound && a.ProvesAbsence && a.Observer != "" && a.Resource != "" && !a.ReadAbsentAt.IsZero() {
			return a, true
		}
	}
	return Absence{}, false
}

type RecordedResource struct {
	Type     string            `json:"type"`
	Identity map[string]string `json:"identity,omitempty"`
	State    State             `json:"state,omitempty"`
}

type ResourceRef struct {
	Name          string
	Type          string
	Identity      map[string]string
	Desired       Desired
	Unsubscribing bool
}

type CreatedResource struct {
	Name     string
	Type     string
	Identity map[string]string
}

// Response's Outputs has no omitempty because a nil map must still encode as JSON null, the shape the engine tolerates from a verb with nothing to return.
type Response struct {
	Outputs       map[string]string `json:"outputs"`
	SecretOutputs map[string]string `json:"secretOutputs,omitempty"`
	Error         string            `json:"error,omitempty"`
	Pending       bool              `json:"pending,omitempty"`
	StatusDetail  string            `json:"statusDetail,omitempty"`
	LastError     string            `json:"lastError,omitempty"`
	Absent        *bool             `json:"absent,omitempty"`
	Redrive       bool              `json:"redrive,omitempty"`
	// Resources has no entry for a resource the verb never reached.
	Resources map[string]ResourceOutcome `json:"resources,omitempty"`
	// Replaces names, on a validate, the resources an upgrade would replace rather than change in place.
	Replaces []string `json:"replaces,omitempty"`
	// ResourceSecretOutputs keeps each provided resource's secret outputs apart, since the merged SecretOutputs keeps one value of a key two resources share.
	ResourceSecretOutputs map[string]map[string]string `json:"resourceSecretOutputs,omitempty"`
	// Credential answers an issue alone.
	Credential *Credential       `json:"credential,omitempty"`
	OwnSecrets map[string]string `json:"ownSecrets,omitempty"`
	Lease      *Lease            `json:"lease,omitempty"`
	Run        *RunResult        `json:"run,omitempty"`
	Verdict    string            `json:"verdict,omitempty"`
}

// ResourceOutcome is what a verb's calls did to one declared resource; a nil call did not run.
type ResourceOutcome struct {
	Ensure  *CallOutcome `json:"ensure,omitempty"`
	Observe *CallOutcome `json:"observe,omitempty"`
	Delete  *CallOutcome `json:"delete,omitempty"`
}

// CallOutcome is the result of one ensure, observe or delete call; the zero value is a call that ran and succeeded with nothing to report.
type CallOutcome struct {
	Error           string            `json:"error,omitempty"`
	State           State             `json:"state,omitempty"`
	Outputs         map[string]string `json:"outputs,omitempty"`
	StatusDetail    string            `json:"statusDetail,omitempty"`
	Identity        map[string]string `json:"identity,omitempty"`
	Type            string            `json:"type,omitempty"`
	Kept            []Kept            `json:"kept,omitempty"`
	Unhealthy       *Unhealthy        `json:"unhealthy,omitempty"`
	Removing        *Removing         `json:"removing,omitempty"`
	WaitingOnPerson *WaitingOnPerson  `json:"waitingOnPerson,omitempty"`
	Findings        []Finding         `json:"findings,omitempty"`
}

type FindingKind string

const (
	FindingPlan    FindingKind = "plan"
	FindingBlocker FindingKind = "blocker"
)

// Finding is one thing a validate alone answers before the block writes anything, a plan it will carry out or a blocker that refuses the validate, with Text in plain words and, for a blocker, the Fix to make.
type Finding struct {
	Kind FindingKind `json:"kind"`
	Text string      `json:"text"`
	Fix  string      `json:"fix,omitempty"`
}

// Kept is a file an unsubscribe left in an app's repository, naming no content, and Reason is "changed" when the app changed it after the landing.
type Kept struct {
	Repository string `json:"repository"`
	Path       string `json:"path"`
	Reason     string `json:"reason"`
}

// Identity names which subscription an operation targets and where its resource lives, and carries Instances because a region-scoped block reads its region here.
type Identity struct {
	SubscriptionID string
	BlockID        string
	BlockVersion   string
	OrgID          string
	AppID          string
	Instances      map[string]InstanceConfig
	PriorOutputs   map[string]string
	CloudIdentity  *CloudIdentity
}

// Desired's Instances reach a block through the embedded Identity, so d.Instances in Ensure is the same map Delete and each observer's ResourceRef are handed. VerbAt is the request's settlesAt when set, else its createdAt.
type Desired struct {
	Identity

	Params       map[string]string
	SecretInputs map[string]string
	AppSecrets   map[string]string
	Secrets      map[string]map[string]string
	Bindings     map[string]map[string]string
	BindingsAll  map[string][]map[string]string
	Absences     map[string]Absence

	Resource              string
	ResourceIdentity      map[string]string
	ResourceType          string
	ResourceUnsubscribing bool

	RecordedResources map[string]RecordedResource
	RequirementTypes  map[string]string
	VerbAt            time.Time
	NeverActive       bool

	Replacing bool

	OwnSecrets map[string]string
}

type Observation struct {
	State         State
	Outputs       map[string]string
	SecretOutputs map[string]string
	StatusDetail  string
	Identity      map[string]string
	// Redrive, read beside State Unknown alone, reports from what the provider holds, never a clock, that the work the last Ensure left in flight has finished, so a validate asks the engine to re-drive subscribe.
	Redrive         bool
	Unhealthy       *Unhealthy
	Removing        *Removing
	WaitingOnPerson *WaitingOnPerson
	Findings        []Finding
	// OwnSecrets are the block's own secrets, which the handler returns beside the answer's outputs whatever State reads.
	OwnSecrets map[string]string
}

// Unhealthy, read beside State Present alone, names one failure a resource meets while the engine still reads it present: Cause is a cause its catalogue type lists under failures, word for word, and Detail says what was seen.
type Unhealthy struct {
	Cause  string `json:"cause"`
	Detail string `json:"detail"`
}

// Removing, read beside State Present alone, says the resource's removal is under way and moving: WaitsOn says in plain words what it waits on, and is never blank.
type Removing struct {
	WaitsOn string `json:"waitsOn"`
}

// WaitingOnPerson, read beside State Unknown alone, says only a person can move the resource on, so the engine runs no settle budget: Action says in plain words what they do and where, and is never blank.
type WaitingOnPerson struct {
	Action string `json:"action"`
}

type requiredField struct {
	name    string
	present bool
}

// requiredRequestFields reports a missing property by the JSON name a schema validator would use.
func requiredRequestFields(r Request) []requiredField {
	fields := []requiredField{
		{"operation", r.Operation != ""},
		{"nonce", r.Nonce != ""},
		{"createdAt", !r.CreatedAt.IsZero()},
	}
	if r.Operation == OperationVerifyConnection {
		fields = append(fields,
			requiredField{"orgId", r.OrgID != ""},
			requiredField{"capabilitySlug", r.CapabilitySlug != ""},
			requiredField{"enteredCredential", len(r.EnteredCredential) > 0},
		)
	} else {
		fields = append(fields,
			requiredField{"subscriptionId", r.SubscriptionID != ""},
			requiredField{"blockId", r.BlockID != ""},
			requiredField{"orgId", r.OrgID != ""},
		)
	}

	if ci := r.CloudIdentity; ci != nil {
		fields = append(fields,
			requiredField{"cloudIdentity.provider", ci.Provider != ""},
			requiredField{"cloudIdentity.roleArn", ci.RoleARN != ""},
			requiredField{"cloudIdentity.webIdentityToken", ci.WebIdentityToken != ""},
		)
	}

	if r.Operation == OperationIssue {
		fields = append(fields, requiredField{"issueAppId", r.IssueAppID != ""})
	}

	return fields
}

// envelopeTimes lets a value that is not an RFC 3339 string be reported by field name, which time.Time.UnmarshalJSON's error lacks.
type envelopeTimes struct {
	CreatedAt json.RawMessage `json:"createdAt"`
	SettlesAt json.RawMessage `json:"settlesAt"`
}

var validOperations = map[string]bool{
	"subscribe":    true,
	"unsubscribe":  true,
	"upgrade":      true,
	"validate":     true,
	OperationIssue: true,
	OperationLogs:  true,

	OperationVerifyConnection: true,
	OperationLease:            true,
	OperationRun:              true,
	OperationSnapshot:         true,
	OperationRestore:          true,
}

// DecodeRequest decodes raw into a Request validated against envelope.schema.json, naming the JSON field in every error.
func DecodeRequest(raw []byte) (Request, error) {
	var probe envelopeTimes
	if err := json.Unmarshal(raw, &probe); err != nil {
		return Request{}, wrapDecodeError("envelope", err)
	}
	if len(probe.CreatedAt) > 0 && !bytes.Equal(probe.CreatedAt, []byte("null")) && probe.CreatedAt[0] != '"' {
		return Request{}, fmt.Errorf("contract: envelope field %q must be an RFC 3339 string", "createdAt")
	}
	if len(probe.SettlesAt) > 0 && !bytes.Equal(probe.SettlesAt, []byte("null")) {
		var t time.Time
		if err := json.Unmarshal(probe.SettlesAt, &t); err != nil {
			return Request{}, fmt.Errorf("contract: envelope field %q must be an RFC 3339 string: %w", "settlesAt", err)
		}
	}

	var req Request
	if err := json.Unmarshal(raw, &req); err != nil {
		return Request{}, wrapDecodeError("envelope", err)
	}

	for _, f := range requiredRequestFields(req) {
		if !f.present {
			return Request{}, fmt.Errorf("contract: envelope field %q is required", f.name)
		}
	}

	if !validOperations[req.Operation] {
		return Request{}, fmt.Errorf(
			"contract: envelope field %q must be one of subscribe, unsubscribe, upgrade, validate, issue, logs, verify-connection, lease, run, snapshot, restore, got %q",
			"operation", req.Operation)
	}

	if req.Operation == OperationLease {
		if err := checkLease(req); err != nil {
			return Request{}, err
		}
	}
	if req.Operation == OperationRun {
		if err := checkRun(req); err != nil {
			return Request{}, err
		}
	}
	if (req.Operation == OperationSnapshot || req.Operation == OperationRestore) && req.Unit == "" {
		return Request{}, fmt.Errorf("contract: envelope field %q is required on a %s", "unit", req.Operation)
	}

	if len(req.ResourceIdentity) > 0 && req.Resource == "" {
		return Request{}, fmt.Errorf("contract: envelope field %q needs a non-empty %q naming the resource the identity is for",
			"resourceIdentity", "resource")
	}
	if req.ResourceType != "" && req.Resource == "" {
		return Request{}, fmt.Errorf("contract: envelope field %q needs a non-empty %q naming the resource the type is for",
			"resourceType", "resource")
	}

	return req, nil
}

// checkLease enforces the rules for a lease that envelope.schema.json documents, since DecodeRequest never validates a request against the schema, so a change to either needs the other.
func checkLease(req Request) error {
	if slices.Contains(req.Retire, "") {
		return fmt.Errorf("contract: envelope field %q must not hold an empty handle", "retire")
	}
	if req.Resource == "" {
		return fmt.Errorf("contract: envelope field %q is required on a lease", "resource")
	}
	if req.LeaseID == "" && len(req.Retire) == 0 {
		return fmt.Errorf("contract: a lease needs envelope field %q to mint a credential or %q to retire some", "leaseId", "retire")
	}
	if req.LeaseID != "" && (req.Caller == nil || req.Caller.SubscriptionID == "") {
		return fmt.Errorf("contract: envelope field %q is required on a lease that mints", "caller.subscriptionId")
	}
	if want := bytes.TrimSpace(req.Want); len(want) > 0 && !bytes.Equal(want, []byte("null")) && want[0] != '{' {
		return fmt.Errorf("contract: envelope field %q must be a JSON object", "want")
	}
	return nil
}

func (r Request) Lease() LeaseRequest {
	l := LeaseRequest{ID: r.LeaseID, Resource: r.Resource, Retire: r.Retire}
	if want := bytes.TrimSpace(r.Want); len(want) > 0 && !bytes.Equal(want, []byte("null")) {
		l.Want = r.Want
	}
	if r.Caller != nil {
		l.Caller = *r.Caller
	}
	return l
}

var (
	runIDRe   = regexp.MustCompile(`^run_[0-9a-f]{12}$`)
	commandRe = regexp.MustCompile(`^[ -~]+$`)
	processRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
)

// checkRun enforces the rules for a run that envelope.schema.json documents, since DecodeRequest never validates a request against the schema, so a change to either needs the other.
func checkRun(req Request) error {
	if !runIDRe.MatchString(req.RunID) {
		return fmt.Errorf("contract: envelope field %q must be run_ and twelve lowercase hex characters, got %q", "runId", req.RunID)
	}
	if len(req.Command) > MaxRunCommandLength || !commandRe.MatchString(req.Command) {
		return fmt.Errorf("contract: envelope field %q must be one line of at most %d printable ASCII characters", "command", MaxRunCommandLength)
	}
	if req.Process != "" && !processRe.MatchString(req.Process) {
		return fmt.Errorf("contract: envelope field %q must be lowercase letters, digits, hyphens and underscores, got %q", "process", req.Process)
	}
	return nil
}

func (r Request) Run() RunRequest {
	return RunRequest{ID: r.RunID, Command: r.Command, Process: r.Process}
}

// DecodeResponse decodes raw into a Response, naming the JSON field of any type mismatch.
func DecodeResponse(raw []byte) (Response, error) {
	var resp Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		return Response{}, wrapDecodeError("response", err)
	}
	return resp, nil
}

// wrapDecodeError names createdAt for a *time.ParseError because DecodeRequest checks settlesAt before the full decode.
func wrapDecodeError(kind string, err error) error {
	var ute *json.UnmarshalTypeError
	if errors.As(err, &ute) {
		return fmt.Errorf("contract: %s field %q has the wrong type: %w", kind, ute.Field, err)
	}

	var perr *time.ParseError
	if kind == "envelope" && errors.As(err, &perr) {
		return fmt.Errorf("contract: %s field %q is not RFC 3339: %w", kind, "createdAt", err)
	}

	return fmt.Errorf("contract: invalid %s JSON: %w", kind, err)
}

func (r Request) Identity() Identity {
	return Identity{
		SubscriptionID: r.SubscriptionID,
		BlockID:        r.BlockID,
		BlockVersion:   r.BlockVersion,
		OrgID:          r.OrgID,
		AppID:          r.AppID,
		Instances:      r.Instances,
		PriorOutputs:   r.PriorOutputs,
		CloudIdentity:  r.CloudIdentity,
	}
}

func (r Request) Desired() Desired {
	return Desired{
		Identity:              r.Identity(),
		Params:                r.Params,
		SecretInputs:          r.SecretInputs,
		AppSecrets:            r.AppSecrets,
		Secrets:               r.Secrets,
		Bindings:              r.Bindings,
		BindingsAll:           r.BindingsAll,
		Absences:              r.Absences,
		Resource:              r.Resource,
		ResourceIdentity:      r.ResourceIdentity,
		ResourceType:          r.ResourceType,
		ResourceUnsubscribing: r.ResourceUnsubscribing,
		RecordedResources:     r.RecordedResources,
		RequirementTypes:      r.RequirementTypes,
		VerbAt:                r.verbAt(),
		NeverActive:           r.NeverActive,
		Replacing:             r.Replacing,
		OwnSecrets:            r.OwnSecrets,
	}
}

func (r Request) verbAt() time.Time {
	if r.SettlesAt != nil {
		return *r.SettlesAt
	}
	return r.CreatedAt
}

// Response turns an Observation into a Response whose Absent stays nil for Unknown, so the engine fails open as for an operator without the marker.
func (o Observation) Response() Response {
	resp := Response{
		Outputs:       o.Outputs,
		SecretOutputs: o.SecretOutputs,
		StatusDetail:  o.StatusDetail,
		OwnSecrets:    o.OwnSecrets,
	}

	switch o.State {
	case Present:
		f := false
		resp.Absent = &f
	case Absent:
		t := true
		resp.Absent = &t
	case Unknown:
	}

	return resp
}
