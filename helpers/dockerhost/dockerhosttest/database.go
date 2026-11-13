package dockerhosttest

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/helpers/dockerhost"
	"github.com/mitstack-eu/block-kit/observe"
	"github.com/mitstack-eu/block-kit/verify/kittest"
)

// Owner, Repo and Sub name the host a Database is served from, and Token is the GitHub connection's token.
const (
	Owner = "acme"
	Repo  = Owner + "/docker-host-sub-host0123456789"
	Sub   = "sub_host0123456789"
	Token = "ghs_github-token-sentinel-9b2c"
)

const (
	hostType   = "hetzner.docker-host"
	bucketType = "hetzner.backup-bucket"
	readme     = "README.md"
)

// Database is a dockerhost.Database served from a fake host, the harness a database block's tests share.
type Database struct {
	*Host
	DB dockerhost.Database
}

// NewDatabase serves db from a fresh host at a fixed clock, and fails t on a request to the host carrying another token than Token.
func NewDatabase(t testing.TB, db dockerhost.Database) *Database {
	t.Helper()
	host := New(t, Repo, Sub, db.Block, db.Engine.Service)
	t.Cleanup(func() {
		for _, r := range host.Requests() {
			if r.Authorization != "Bearer "+Token {
				t.Errorf("%s %s carried Authorization %q; want the GitHub connection's token", r.Method, r.Path, r.Authorization)
			}
		}
	})
	db.Reports, db.GitHub, db.Now = host, host.GitHub, func() time.Time { return time.Unix(1793620800, 0) }
	return &Database{Host: host, DB: db}
}

// Requirements are the host and bucket a Database is bound to, by requirement name and type, as kittest.Block takes them.
func Requirements() map[string]map[string]map[string]string {
	return map[string]map[string]map[string]string{"host": {hostType: hostBinding()}, "bucket": {bucketType: bucketBinding()}}
}

func hostBinding() map[string]string {
	return map[string]string{"clusterRepoUrl": "https://github.com/" + Repo, "subscriptionId": Sub, "publicIp": "203.0.113.10"}
}

func bucketBinding() map[string]string {
	return map[string]string{"bucket": "docker-host-sub-host0123456789-backups", "bucketLocation": "hel1"}
}

// Desired is app's subscription to the database, with params beside its appName.
func (d *Database) Desired(app string, params map[string]string) contract.Desired {
	p := map[string]string{"appName": app}
	maps.Copy(p, params)
	return contract.Desired{
		Identity:         contract.Identity{SubscriptionID: "sub_db_" + app, BlockID: d.DB.Block, OrgID: "org_0123456789ab", AppID: "app_" + app},
		Params:           p,
		Secrets:          map[string]map[string]string{"github": {"token": Token, "owner": Owner}},
		Bindings:         map[string]map[string]string{"host": hostBinding(), "bucket": bucketBinding()},
		RequirementTypes: map[string]string{"host": hostType, "bucket": bucketType},
	}
}

func (d *Database) Land(t testing.TB, desired contract.Desired) {
	t.Helper()
	if _, err := d.DB.EnsureResources(context.Background(), desired); err != nil {
		t.Fatalf("EnsureResources(%s) = %v, want the database landed", desired.Params["appName"], err)
	}
}

func (d *Database) Compose(t testing.TB, app string) dockerhost.Compose {
	t.Helper()
	var c dockerhost.Compose
	if err := json.Unmarshal([]byte(d.Main()[d.composePath(app)]), &c); err != nil {
		t.Fatalf("%s's compose file does not parse: %v", app, err)
	}
	return c
}

func (d *Database) composePath(app string) string { return app + "/" + d.DB.Block + ".compose.yaml" }

func (d *Database) Observe(t testing.TB, desired contract.Desired) contract.Observation {
	t.Helper()
	obs, err := d.DB.ObserveResource(context.Background(), contract.ResourceRef{Name: dockerhost.DatabaseResource, Desired: desired})
	if err != nil {
		t.Fatalf("ObserveResource(%s) = %v", desired.Params["appName"], err)
	}
	return obs
}

// BackupLine is what key holds in app's backup file, which the host reads one key=value line at a time.
func (d *Database) BackupLine(t testing.TB, app, key string) string {
	t.Helper()
	for _, line := range strings.Split(d.Main()[app+"/"+d.DB.Block+".backup"], "\n") {
		if value, ok := strings.CutPrefix(line, key+"="); ok {
			return value
		}
	}
	t.Fatalf("%s's backup file names no %s", app, key)
	return ""
}

// ListTagged lists each app whose compose file carries subID's label, as kittest's tagging guard reads a provider.
func (d *Database) ListTagged(_ context.Context, subID string) ([]observe.Tagged, error) {
	var tagged []observe.Tagged
	for path, content := range d.Main() {
		app, ok := strings.CutSuffix(path, "/"+d.DB.Block+".compose.yaml")
		var c dockerhost.Compose
		if ok && json.Unmarshal([]byte(content), &c) == nil && c.Services[d.Service].Labels["mitstack.dev/subscription"] == subID {
			tagged = append(tagged, observe.Tagged{Type: d.DB.Type, ID: app})
		}
	}
	return tagged, nil
}

// Lose removes every file of r's app from the host, as a person deleting it by hand would.
func (d *Database) Lose(_ context.Context, r contract.CreatedResource) error {
	gone := map[string]string{}
	for path := range d.Under(r.Identity["app"]) {
		gone[path] = ""
	}
	d.Set(gone)
	return nil
}

// Breaking makes each write to the host a side effect faults may break, for kittest.AssertTeardownAfterBrokenSetup.
func (d *Database) Breaking(faults *kittest.Faults) kittest.FaultProvider {
	d.BeforeWrite(func(method, path string) error { return faults.SideEffect(method + " " + path) })
	return d
}

// Process serves the database as a newly started operator, which keeps nothing of its own between calls.
func (d *Database) Process() (handler.Resource, map[string]handler.Observer) {
	return d.DB, d.DB.Observers()
}

// Holds lists every file on the host but the README it starts with, which only the database's block writes.
func (d *Database) Holds(string) []string {
	var held []string
	for path := range d.Main() {
		if path != readme {
			held = append(held, path)
		}
	}
	slices.Sort(held)
	return held
}
