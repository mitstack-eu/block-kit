package dockerhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/observe"
)

// SyncedCommitSince is the first hcloud-docker-host release whose node report names the commit its agent last synced.
const SyncedCommitSince = "0.8.0"

// Report is a Docker host's newest node report, read the same way by every block whose app runs on the host.
type Report struct {
	// Allocatable is what the host can give its apps, nil when the report gives none.
	Allocatable *Reservation
	body        json.RawMessage
	agent       map[string]json.RawMessage
}

type reportParts struct {
	Host struct {
		Capacity struct {
			Allocatable *Reservation `json:"allocatable"`
		} `json:"capacity"`
		Agent map[string]json.RawMessage `json:"agent"`
	} `json:"host"`
}

// NewestReport reads the newest of the host subscription's node reports, found false while the host has sent none.
func NewestReport(ctx context.Context, api observe.NodeReportsAPI, d contract.Desired, hostSubscription string) (Report, bool, error) {
	if hostSubscription == "" {
		return Report{}, false, handler.Refuse("host-binding", "the host binding carries no subscriptionId, so the host's node reports cannot be read")
	}
	reports, err := api.NodeReports(ctx, d, hostSubscription)
	if err != nil {
		return Report{}, false, fmt.Errorf("reading the host's node reports: %w", err)
	}
	var newest *observe.NodeReport
	for i, r := range reports {
		if newest == nil || r.ReceivedAt.After(newest.ReceivedAt) {
			newest = &reports[i]
		}
	}
	if newest == nil {
		return Report{}, false, nil
	}
	var parts reportParts
	if err := json.Unmarshal(newest.Body, &parts); err != nil {
		return Report{}, false, fmt.Errorf("decoding the host's newest node report: %w", err)
	}
	return Report{Allocatable: parts.Host.Capacity.Allocatable, body: newest.Body, agent: parts.Host.Agent}, true, nil
}

// Decode reads the report's body into v, for the fields only one block reads.
func (r Report) Decode(v any) error {
	return json.Unmarshal(r.body, v)
}

// SyncedCommit is the commit the host's agent last synced, from host.agent.syncedCommit and never from the agent's
// log. Its error says why the report names none, for the block to read the resource unknown with.
func (r Report) SyncedCommit() (string, error) {
	raw, ok := r.agent["syncedCommit"]
	if !ok {
		return "", fmt.Errorf("the host's newest node report carries no host.agent.syncedCommit, so the host runs a hcloud-docker-host release before %s; upgrade the host to %s or later, whose report names the commit its agent last synced",
			SyncedCommitSince, SyncedCommitSince)
	}
	var commit *string
	if err := json.Unmarshal(raw, &commit); err != nil {
		return "", fmt.Errorf("the host's newest node report gives host.agent.syncedCommit as %s, which is not a commit: %w", raw, err)
	}
	if commit == nil || *commit == "" {
		return "", errors.New("the host's agent has not synced since it took its current release, so the commit it runs cannot be read until its next sync")
	}
	return *commit, nil
}
