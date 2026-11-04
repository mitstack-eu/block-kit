package ghapi_test

// EnsureRepo keeps a ruleset on main that refuses every update, force-push and deletion but mitstack's GitHub App's.

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"reflect"
	"testing"

	"github.com/mitstack-eu/block-kit/capabilities/github/ghapi"
	"github.com/mitstack-eu/block-kit/contract"
)

const (
	appID       = "4175240"
	rulesets    = widget + "/rulesets"
	listed      = rulesets + "?includes_parents=false&per_page=100"
	ours        = rulesets + "/42"
	rulesetName = "mitstack: main takes pushes from mitstack's GitHub App alone"
)

// wantRuleset is the ruleset EnsureRepo sends, naming app as its one bypass actor.
func wantRuleset(app string) string {
	return `{"name":"` + rulesetName + `","target":"branch","enforcement":"active",` +
		`"conditions":{"ref_name":{"include":["~DEFAULT_BRANCH"],"exclude":[]}},` +
		`"rules":[{"type":"update","parameters":{"update_allows_fetch_and_merge":false}},{"type":"non_fast_forward"},{"type":"deletion"}],` +
		`"bypass_actors":[{"actor_id":` + app + `,"actor_type":"Integration","bypass_mode":"always"}]}`
}

// held is GitHub's answer for ruleset 42 holding wantRuleset, after edit changes it.
func held(edit func(r map[string]any)) reply {
	var r map[string]any
	_ = json.Unmarshal([]byte(wantRuleset(appID)), &r)
	r["id"], r["source_type"], r["source"], r["current_user_can_bypass"] = 42, "Repository", "acme/widget", "always"
	if edit != nil {
		edit(r)
	}
	raw, _ := json.Marshal(r)
	return reply{body: string(raw)}
}

var (
	listingOurs    = reply{body: `[{"id":7,"name":"release tags","source_type":"Repository"},{"id":42,"name":"` + rulesetName + `","source_type":"Repository"}]`}
	listingNothing = reply{body: `[]`}
)

func ensureWith(app string) func(c *ghapi.Client) (any, error) {
	return func(c *ghapi.Client) (any, error) { return c.EnsureRepo(context.Background(), "acme", "widget", app) }
}

// sent checks that the one request to key carried wantRuleset for app.
func sent(key, app string) func(t *testing.T, f *fakeGitHub) {
	return func(t *testing.T, f *fakeGitHub) {
		t.Helper()
		got := f.bodies(key)
		if want := jsonBody(t, wantRuleset(app)); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
			t.Errorf("%s bodies = %v\nwant one carrying %v", key, got, want)
		}
	}
}

func TestEnsureRepo_KeepsTheRulesetOnMain(t *testing.T) {
	existing := map[string][]reply{get(widget): {{}}}
	with := func(routes, extra map[string][]reply) map[string][]reply {
		out := maps.Clone(routes)
		maps.Copy(out, extra)
		return out
	}
	repaired := func(name string, edit func(r map[string]any)) apiCase {
		return apiCase{name: name, routes: with(existing, map[string][]reply{get(listed): {listingOurs}, get(ours): {held(edit)}, "PUT " + ours: {{}}}),
			call: ensureWith(appID), want: "acme/widget", requests: []string{get(widget), get(listed), get(ours), "PUT " + ours}, check: sent("PUT "+ours, appID)}
	}
	runAPICases(t, []apiCase{
		{name: "a new repository carries the ruleset with the App as its one bypass actor",
			routes: map[string][]reply{get(widget): {notFound}, post("/orgs/acme/repos"): {{http.StatusCreated, `{"full_name":"acme/widget"}`}},
				get(listed): {listingNothing}, post(rulesets): {{http.StatusCreated, `{"id":42}`}}},
			call: ensureWith(appID), want: "acme/widget", requests: []string{get(widget), post("/orgs/acme/repos"), get(listed), post(rulesets)},
			check: sent(post(rulesets), appID)},
		{name: "an existing repository holding only other rulesets gains it",
			routes: with(existing, map[string][]reply{get(listed): {{body: `[{"id":7,"name":"release tags","source_type":"Repository"}]`}}, post(rulesets): {{http.StatusCreated, `{"id":42}`}}}),
			call:   ensureWith(appID), want: "acme/widget", requests: []string{get(widget), get(listed), post(rulesets)}, check: sent(post(rulesets), appID)},
		repaired("a ruleset whose bypass list an owner widened is put back to the App alone", func(r map[string]any) {
			r["bypass_actors"] = append(r["bypass_actors"].([]any), map[string]any{"actor_id": 1, "actor_type": "OrganizationAdmin", "bypass_mode": "always"})
		}),
		repaired("a ruleset naming another App is put back to mitstack's", func(r map[string]any) {
			r["bypass_actors"] = []any{map[string]any{"actor_id": 29110, "actor_type": "Integration", "bypass_mode": "always"}}
		}),
		repaired("a ruleset letting the App bypass only through a pull request lets it push again", func(r map[string]any) {
			r["bypass_actors"] = []any{map[string]any{"actor_id": 4175240, "actor_type": "Integration", "bypass_mode": "pull_request"}}
		}),
		repaired("a ruleset missing the deletion rule is repaired", func(r map[string]any) { r["rules"] = r["rules"].([]any)[:2] }),
		repaired("a ruleset carrying a rule of its own is repaired", func(r map[string]any) {
			r["rules"] = append(r["rules"].([]any), map[string]any{"type": "required_signatures"})
		}),
		repaired("a ruleset whose update rule allows fetch and merge is repaired", func(r map[string]any) {
			r["rules"].([]any)[0] = map[string]any{"type": "update", "parameters": map[string]any{"update_allows_fetch_and_merge": true}}
		}),
		repaired("a ruleset an owner disabled is enforced again", func(r map[string]any) { r["enforcement"] = "disabled" }),
		repaired("a ruleset moved off the default branch is put back on it", func(r map[string]any) {
			r["conditions"] = map[string]any{"ref_name": map[string]any{"include": []any{"refs/heads/dev"}, "exclude": []any{}}}
		}),
		repaired("a ruleset excluding main is repaired", func(r map[string]any) {
			r["conditions"] = map[string]any{"ref_name": map[string]any{"include": []any{"~DEFAULT_BRANCH"}, "exclude": []any{"refs/heads/main"}}}
		}),
		{name: "a matching ruleset is left unchanged",
			routes: with(existing, map[string][]reply{get(listed): {listingOurs}, get(ours): {held(nil)}}),
			call:   ensureWith(appID), want: "acme/widget", requests: []string{get(widget), get(listed), get(ours)}},
		{name: "a matching ruleset whose answer lists its rules in another order and omits the update rule's parameters is left unchanged",
			routes: with(existing, map[string][]reply{get(listed): {listingOurs}, get(ours): {held(func(r map[string]any) {
				r["rules"] = []any{map[string]any{"type": "deletion"}, map[string]any{"type": "non_fast_forward"}, map[string]any{"type": "update"}}
			})}}),
			call: ensureWith(appID), want: "acme/widget", requests: []string{get(widget), get(listed), get(ours)}},
		{name: "no App id fails naming it before any request", call: ensureWith(""), want: "", err: ghapi.ErrNoAppID, errHas: "App id"},
		{name: "an App id that is no number fails naming it before any request", call: ensureWith("mitstack-platform"), want: "", err: errAny, errHas: `"mitstack-platform"`},
		{name: "a negative App id fails naming it before any request", call: ensureWith("-4175240"), want: "", err: errAny, errHas: `"-4175240"`},
		{name: "a ruleset create refused with a body echoing the token fails naming its route without carrying it",
			routes: with(existing, map[string][]reply{get(listed): {listingNothing}, post(rulesets): {{http.StatusForbidden, `{"message":"Resource not accessible by integration","auth":"{auth}"}`}}}),
			call:   ensureWith(appID), want: "", err: errAny, errHas: "POST /repos/acme/widget/rulesets returned 403", requests: []string{get(widget), get(listed), post(rulesets)}},
		{name: "a ruleset repair refused fails naming its route",
			routes: with(existing, map[string][]reply{get(listed): {listingOurs}, get(ours): {held(func(r map[string]any) { r["enforcement"] = "evaluate" })},
				"PUT " + ours: {{http.StatusUnprocessableEntity, `{"message":"Validation Failed"}`}}}),
			call: ensureWith(appID), want: "", err: errAny, errHas: "PUT /repos/acme/widget/rulesets/42 returned 422", requests: []string{get(widget), get(listed), get(ours), "PUT " + ours}},
		{name: "a rulesets listing GitHub refuses fails and writes nothing",
			routes: with(existing, map[string][]reply{get(listed): {{http.StatusForbidden, `{"message":"Must have admin rights to Repository."}`}}}),
			call:   ensureWith(appID), want: "", err: errAny, errHas: "returned 403", requests: []string{get(widget), get(listed)}},
		{name: "an oversized rulesets listing is refused rather than read cut off",
			routes: with(existing, map[string][]reply{get(listed): {{body: `[` + huge(`"id":42,"name":"`+rulesetName+`"`) + `]`}}}),
			call:   ensureWith(appID), want: "", err: errAny, errHas: "GET /repos/acme/widget/rulesets", requests: []string{get(widget), get(listed)}},
		{name: "a ruleset answer that does not decode fails and writes nothing",
			routes: with(existing, map[string][]reply{get(listed): {listingOurs}, get(ours): {{body: `{"rules":`}}}),
			call:   ensureWith(appID), want: "", err: errAny, errHas: "GET /repos/acme/widget/rulesets/42", requests: []string{get(widget), get(listed), get(ours)}},
	})
}

func TestEnsureRepo_NamesTheAppASubscribeRequestCarriesAsTheOneBypassActor(t *testing.T) {
	valid, err := contract.ValidGoldens()
	if err != nil {
		t.Fatalf("contract.ValidGoldens(): %v", err)
	}
	req, err := contract.DecodeRequest(valid["subscribe"])
	if err != nil {
		t.Fatalf("decoding the subscribe golden: %v", err)
	}
	carried := req.Secrets["github"]["appId"]
	if carried == "" {
		t.Fatalf("the subscribe golden's secrets.github carries no appId: %v", req.Secrets)
	}
	f := &fakeGitHub{routes: map[string][]reply{get(widget): {notFound}, post("/orgs/acme/repos"): {{http.StatusCreated, `{"full_name":"acme/widget"}`}},
		get(listed): {listingNothing}, post(rulesets): {{http.StatusCreated, `{"id":42}`}}}}

	_, err = serve(t, f).EnsureRepo(context.Background(), "acme", "widget", carried)

	if err != nil {
		t.Fatalf("EnsureRepo with the golden's App id: %v", err)
	}
	sent(post(rulesets), carried)(t, f)
}
