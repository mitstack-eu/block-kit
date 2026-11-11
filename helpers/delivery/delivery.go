// Package delivery mirrors the record the platform's CI dispatcher serves, keeping Verdict raw and the goldens' field order so a round trip is byte for byte.
package delivery

import "encoding/json"

// ConclusionMitstackFailure is a build stopped at one of mitstack's own calls, so its commit is not to blame.
const (
	ConclusionSuccess         = "success"
	ConclusionFailure         = "failure"
	ConclusionMitstackFailure = "mitstack-failure"
)

const (
	PhaseBuilding    = "building"
	PhaseBuildFailed = "build-failed"
	PhaseGatesFailed = "gates-failed"
	PhaseDeploying   = "deploying"
	PhaseHealthy     = "healthy"
	PhaseRolledBack  = "rolled-back"
)

type BuildResult struct {
	Conclusion string `json:"conclusion"`
	RunURL     string `json:"runUrl"`
	Cause      string `json:"cause,omitempty"`
}

type Record struct {
	AppID       string          `json:"appId"`
	SHA         string          `json:"sha"`
	Repo        string          `json:"repo"`
	Verdict     json.RawMessage `json:"verdict"`
	DeployState *string         `json:"deployState"`
	BuildResult *BuildResult    `json:"buildResult"`
	Phase       string          `json:"phase"`
}
