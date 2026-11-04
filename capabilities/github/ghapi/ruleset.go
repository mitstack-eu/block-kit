package ghapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"

	"github.com/mitstack-eu/block-kit/helpers/safe"
)

// ErrNoAppID is returned by EnsureRepo given no GitHub App id, before it sends any request.
var ErrNoAppID = errors.New("ghapi: no GitHub App id was given, so the ruleset on main cannot name mitstack's GitHub App as its one bypass actor; pass the request's secrets.github.appId")

// rulesetName is how EnsureRepo finds its ruleset, so a renamed one is left behind beside a new one.
const rulesetName = "mitstack: main takes pushes from mitstack's GitHub App alone"

const (
	maxRulesetBody  = 256 << 10
	maxRulesetsBody = 1 << 20
)

type ruleset struct {
	ID           int64         `json:"id,omitempty"`
	Name         string        `json:"name"`
	Target       string        `json:"target"`
	Enforcement  string        `json:"enforcement"`
	Conditions   conditions    `json:"conditions"`
	Rules        []rule        `json:"rules"`
	BypassActors []bypassActor `json:"bypass_actors"`
}

type conditions struct {
	RefName struct {
		Include []string `json:"include"`
		Exclude []string `json:"exclude"`
	} `json:"ref_name"`
}

type rule struct {
	Type       string          `json:"type"`
	Parameters *ruleParameters `json:"parameters,omitempty"`
}

type ruleParameters struct {
	UpdateAllowsFetchAndMerge bool `json:"update_allows_fetch_and_merge"`
}

type bypassActor struct {
	ActorID    int64  `json:"actor_id"`
	ActorType  string `json:"actor_type"`
	BypassMode string `json:"bypass_mode"`
}

// parseAppID refuses anything but a positive whole number, since GitHub names an App bypass actor by its numeric id.
func parseAppID(appID string) (int64, error) {
	if appID == "" {
		return 0, ErrNoAppID
	}
	id, err := strconv.ParseInt(appID, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("ghapi: GitHub App id %q is not a positive whole number, so the ruleset on main cannot name the App", appID)
	}
	return id, nil
}

// mainRuleset refuses updates, force-pushes and deletion of the default branch to all but the App, which bypasses it on every push.
func mainRuleset(appID int64) ruleset {
	r := ruleset{
		Name:        rulesetName,
		Target:      "branch",
		Enforcement: "active",
		Rules: []rule{
			{Type: "update", Parameters: &ruleParameters{}},
			{Type: "non_fast_forward"},
			{Type: "deletion"},
		},
		BypassActors: []bypassActor{{ActorID: appID, ActorType: "Integration", BypassMode: "always"}},
	}
	r.Conditions.RefName.Include = []string{"~DEFAULT_BRANCH"}
	r.Conditions.RefName.Exclude = []string{}
	return r
}

// matches ignores the order GitHub lists rules in, and reads an update rule with no parameters as refusing fetch and merge.
func (r ruleset) matches(want ruleset) bool {
	return r.Target == want.Target && r.Enforcement == want.Enforcement &&
		slices.Equal(r.Conditions.RefName.Include, want.Conditions.RefName.Include) &&
		len(r.Conditions.RefName.Exclude) == 0 &&
		slices.Equal(r.BypassActors, want.BypassActors) &&
		slices.Equal(ruleTypes(r.Rules), ruleTypes(want.Rules))
}

// ruleTypes marks an update rule allowing fetch and merge apart, so it never equals the plain update rule.
func ruleTypes(rules []rule) []string {
	types := make([]string, 0, len(rules))
	for _, r := range rules {
		t := r.Type
		if r.Parameters != nil && r.Parameters.UpdateAllowsFetchAndMerge {
			t += "+fetch-and-merge"
		}
		types = append(types, t)
	}
	slices.Sort(types)
	return types
}

// ensureRuleset creates the main ruleset when repo has none of its name, replaces one that differs, and writes nothing when it matches.
func (c *Client) ensureRuleset(ctx context.Context, repo string, appID int64) error {
	want := mainRuleset(appID)
	path := "/repos/" + repo + "/rulesets"
	var listed []ruleset
	if err := c.getJSON(ctx, path+"?includes_parents=false&per_page=100", maxRulesetsBody, &listed); err != nil {
		return err
	}
	i := slices.IndexFunc(listed, func(r ruleset) bool { return r.Name == want.Name })
	if i < 0 {
		return c.sendRuleset(ctx, http.MethodPost, path, want, http.StatusCreated)
	}
	path += "/" + strconv.FormatInt(listed[i].ID, 10)
	var held ruleset
	if err := c.getJSON(ctx, path, maxRulesetBody, &held); err != nil {
		return err
	}
	if held.matches(want) {
		return nil
	}
	return c.sendRuleset(ctx, http.MethodPut, path, want, http.StatusOK)
}

func (c *Client) sendRuleset(ctx context.Context, method, path string, r ruleset, want int) error {
	resp, err := c.doJSON(ctx, method, path, r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != want {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("ghapi: github %s %s returned %d: %s", method, path, resp.StatusCode, safe.LogSafe(string(raw), c.Token))
	}
	return nil
}

// getJSON decodes a 200 answer into v, refusing any other status, and get refuses one longer than limit.
func (c *Client) getJSON(ctx context.Context, path string, limit int64, v any) error {
	route := "GET " + path
	status, raw, _, err := c.get(ctx, path, limit)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("ghapi: github %s returned %d: %s", route, status, safe.LogSafe(string(raw[:min(len(raw), 4<<10)]), c.Token))
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("ghapi: decode the answer to %s: %w", route, err)
	}
	return nil
}
