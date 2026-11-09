package dockerhost_test

// A Database lands each app's database in the app's own directory on the app's own network with no published port,
// refuses a host without room or a variable the app holds with nothing written, reads the database through the host's
// node report, and removes it only once its final backup is uploaded.

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mitstack-eu/block-kit/capabilities/github/ghapi"
	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/helpers/dockerhost"
	"github.com/mitstack-eu/block-kit/helpers/dockerhost/dockerhosttest"
)

const (
	dbBlock    = "docker-store"
	dbOwner    = "acme"
	dbRepo     = dbOwner + "/docker-host-sub-host0123456789"
	dbHostSub  = "sub_host0123456789"
	dbToken    = "ghs_github-token-sentinel-9b2c"
	storeImage = "example.org/store:2.1@sha256:0f9e"
)

type dbEnv struct {
	host *dockerhosttest.Host
	db   dockerhost.Database
}

func newDBEnv(t *testing.T) *dbEnv {
	t.Helper()
	host := dockerhosttest.New(t, dbRepo, dbHostSub, dbBlock, "store")
	t.Cleanup(func() {
		for _, r := range host.Requests() {
			if r.Authorization != "Bearer "+dbToken {
				t.Errorf("%s %s carried Authorization %q; want the GitHub connection's token", r.Method, r.Path, r.Authorization)
			}
		}
	})
	return &dbEnv{host: host, db: dockerhost.Database{
		Block: dbBlock, Type: "example.store-database", Engine: storeEngine(),
		Reports: host, GitHub: host.GitHub, Now: func() time.Time { return time.Unix(1793620800, 0) },
	}}
}

func storeEngine() dockerhost.Engine {
	return dockerhost.Engine{
		Service: "store", Image: storeImage, Port: "7000", DataDir: "/data",
		Entrypoint: func(n dockerhost.Names) []string {
			return []string{"sh", "-c", `exec store --user="$` + n["username"] + `" --password="$` + n["password"] + `"`}
		},
		Command:     []string{"serve"},
		Healthcheck: []string{"CMD", "store", "ping"},
		Dump:        func(n dockerhost.Names) string { return "store dump --db=$" + n["dbName"] },
		Restore:     func(n dockerhost.Names) string { return "store load --db=$" + n["dbName"] },
		Trouble:     regexp.MustCompile(`FATAL`),
	}
}

func dbDesired(app string, params map[string]string) contract.Desired {
	p := map[string]string{"appName": app}
	maps.Copy(p, params)
	return contract.Desired{
		Identity: contract.Identity{SubscriptionID: "sub_db_" + app, BlockID: dbBlock, OrgID: "org_0123456789ab", AppID: "app_" + app},
		Params:   p,
		Secrets:  map[string]map[string]string{"github": {"token": dbToken, "owner": dbOwner}},
		Bindings: map[string]map[string]string{
			"host":   {"clusterRepoUrl": "https://github.com/" + dbRepo, "subscriptionId": dbHostSub},
			"bucket": {"bucket": "docker-host-sub-host0123456789-backups"},
		},
	}
}

func (e *dbEnv) land(t *testing.T, d contract.Desired) {
	t.Helper()
	if _, err := e.db.EnsureResources(context.Background(), d); err != nil {
		t.Fatalf("EnsureResources(%s) = %v, want the database landed", d.Params["appName"], err)
	}
}

func (e *dbEnv) compose(t *testing.T, app string) dockerhost.Compose {
	t.Helper()
	var c dockerhost.Compose
	if err := json.Unmarshal([]byte(e.host.Main()[app+"/"+dbBlock+".compose.yaml"]), &c); err != nil {
		t.Fatalf("%s's compose file does not parse: %v", app, err)
	}
	return c
}

func (e *dbEnv) observe(t *testing.T, d contract.Desired) contract.Observation {
	t.Helper()
	obs, err := e.db.Observers()[dockerhost.DatabaseResource].ObserveResource(context.Background(), contract.ResourceRef{Name: dockerhost.DatabaseResource, Desired: d})
	if err != nil {
		t.Fatalf("ObserveResource = %v", err)
	}
	return obs
}

func refused(err error) string {
	var r *handler.Refused
	if errors.As(err, &r) {
		return r.Class
	}
	return ""
}

func waits(err error) bool {
	var w *handler.Waiting
	return errors.As(err, &w)
}

func keyLines(content string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(content), "\n") {
		if name, value, ok := strings.Cut(line, "="); ok {
			out[name] = value
		}
	}
	return out
}

func TestDatabase_LandsTwoAppsEachOnItsOwnNetworkWithNoPublishedPortAndItsVolumeAtItsDisk(t *testing.T) {
	e := newDBEnv(t)

	e.land(t, dbDesired("shop", map[string]string{"dbCpu": "250m", "dbMemory": "1Gi", "dbDisk": "2Gi"}))
	e.land(t, dbDesired("blog", nil))

	for app, disk := range map[string]string{"shop": "2GiB", "blog": "1GiB"} {
		c := e.compose(t, app)
		s, ok := c.Services["store"]
		if !ok || len(c.Services) != 1 {
			t.Fatalf("%s's compose file holds %v; want the store service alone", app, slices.Collect(maps.Keys(c.Services)))
		}
		if len(s.Ports) != 0 || c.Network("default") != "app-"+app || !slices.Equal(s.Networks, []string{"default"}) {
			t.Errorf("%s's database publishes %v on %v named %q; want no port on the app's own network app-%s", app, s.Ports, s.Networks, c.Network("default"), app)
		}
		if got := c.Volumes["store-data"].Labels["eu.mitstack.size"]; got != disk || !slices.Equal(s.Volumes, []string{"store-data:/data"}) {
			t.Errorf("%s's volume is %v sized %q; want store-data at /data sized %s", app, s.Volumes, got, disk)
		}
		if s.Image != storeImage || s.ContainerName != app+"-store" || s.Labels["mitstack.dev/subscription"] != "sub_db_"+app {
			t.Errorf("%s's database runs %q as %q labelled %v; want the engine's image as %s-store labelled with its subscription", app, s.Image, s.ContainerName, s.Labels, app)
		}
	}
	var got dockerhost.Reservation
	if err := json.Unmarshal([]byte(e.host.Main()["shop/reservations/"+dbBlock+".json"]), &got); err != nil || got != (dockerhost.Reservation{CPUMillis: 250, MemoryBytes: gib, DiskBytes: 2 * gib}) {
		t.Errorf("shop's reservation reads %+v, %v; want 250 millicores, 1 GiB and 2 GiB of disk", got, err)
	}
	if s := e.compose(t, "shop").Services["store"]; s.CPUs != "0.25" || s.MemLimit != gib || s.MemswapLimit != gib {
		t.Errorf("shop's limits are %s CPUs and %d bytes, swap %d; want 0.25 and 1 GiB with no swap beyond", s.CPUs, s.MemLimit, s.MemswapLimit)
	}
}

func TestDatabase_GivesTheValuesUnderTheNamesTheEnvInputSetsToTheAppTheEntrypointAndTheBackup(t *testing.T) {
	e := newDBEnv(t)

	e.land(t, dbDesired("shop", map[string]string{"env": "STORE_USER=username, STORE_DB=dbName"}))

	gives := keyLines(e.host.Main()["shop/"+dbBlock+".gives"])
	want := map[string]string{"DB_HOST": "fixed:shop-store", "DB_PORT": "fixed:7000", "STORE_DB": "fixed:app", "STORE_USER": "fixed:app", "DB_PASSWORD": "generated"}
	if !maps.Equal(gives, want) {
		t.Errorf("the gives file lists %v; want %v", gives, want)
	}
	if got := e.compose(t, "shop").Services["store"].Entrypoint; !slices.Equal(got, []string{"sh", "-c", `exec store --user="$$STORE_USER" --password="$$DB_PASSWORD"`}) {
		t.Errorf("the entrypoint is %q; want the engine's, its variables renamed and each $ escaped for Compose", got)
	}
	backup := keyLines(e.host.Main()["shop/"+dbBlock+".backup"])
	if want := map[string]string{"service": "store", "command": "store dump --db=$STORE_DB", "restore": "store load --db=$STORE_DB", "every": "24h"}; !maps.Equal(backup, want) {
		t.Errorf("the backup file lists %v; want %v", backup, want)
	}
}

func TestDatabase_RefusesWhatItCannotLandAndWritesNothing(t *testing.T) {
	for _, tc := range []struct {
		name, class, says string
		params            map[string]string
		before            func(*testing.T, *dbEnv)
		edit              func(*contract.Desired)
	}{
		{name: "a variable the app's env.names holds", class: "env-mapping", says: "DB_HOST, which the app's env.names already names",
			before: func(_ *testing.T, e *dbEnv) { e.host.Set(map[string]string{"shop/env.names": "DB_HOST=secret:host\n"}) }},
		{name: "a variable another block gives", class: "env-mapping", says: "DB_PASSWORD, which docker-cache's gives file already names",
			before: func(_ *testing.T, e *dbEnv) {
				e.host.Set(map[string]string{"shop/docker-cache.gives": "DB_PASSWORD=generated\n"})
			}},
		{name: "an env input naming no value", class: "invalid-param", says: `"X=nothing" names "nothing"`, params: map[string]string{"env": "X=nothing"}},
		{name: "an env input giving two values one name", class: "invalid-param", says: "host and port would both be DB_PORT", params: map[string]string{"env": "DB_PORT=host"}},
		{name: "memory past what the host has free", class: "host-full", says: "the host has 2.5 vCPU, 512 MiB and 69 GiB of disk free",
			params: map[string]string{"dbMemory": "1Gi"},
			before: func(_ *testing.T, e *dbEnv) {
				e.host.Set(map[string]string{"blog/reservations/docker-app-slice.json": `{"cpuMillis":1000,"memoryBytes":6979321856,"diskBytes":1073741824}`})
			}},
		{name: "CPU past what the host has free", class: "host-full", says: "asks for 4 vCPU", params: map[string]string{"dbCpu": "4"}},
		{name: "disk past what the host has free", class: "host-full", says: "asks for 0.5 vCPU, 512 MiB and 71 GiB of disk", params: map[string]string{"dbDisk": "71Gi"}},
		{name: "a host that takes no backups", class: "host-takes-no-backups", says: "the host holds no backup public key",
			before: func(_ *testing.T, e *dbEnv) { e.host.BackupError = "the host holds no backup public key" }},
		{name: "a host that has sent no report", says: "no node report giving its allocatable capacity",
			before: func(_ *testing.T, e *dbEnv) { e.host.Silent = true }},
		{name: "an app whose database another subscription holds", class: "app-taken", says: "belongs to the subscription sub_db_other",
			before: func(t *testing.T, e *dbEnv) {
				other := dbDesired("shop", nil)
				other.SubscriptionID = "sub_db_other"
				e.land(t, other)
			}},
		{name: "a request without the GitHub connection", class: "missing-github-access", says: "the GitHub connection is missing",
			edit: func(d *contract.Desired) { d.Secrets = nil }},
		{name: "a host repository outside the connection's account", class: "host-binding", says: "is not a repository of the GitHub connection's account acme",
			edit: func(d *contract.Desired) {
				d.Bindings["host"]["clusterRepoUrl"] = "https://github.com/other/docker-host"
			}},
		{name: "a GitHub API base off the allowlist", class: "untrusted-api-base", says: "github api base rejected",
			edit: func(d *contract.Desired) {
				d.Instances = map[string]contract.InstanceConfig{"github": {Endpoint: "https://api.github.example.org"}}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newDBEnv(t)
			if tc.before != nil {
				tc.before(t, e)
			}
			held := e.host.Under("shop")
			d := dbDesired("shop", tc.params)
			if tc.edit != nil {
				tc.edit(&d)
			}

			_, err := e.db.EnsureResources(context.Background(), d)

			if err == nil || refused(err) != tc.class || !strings.Contains(err.Error(), tc.says) {
				t.Errorf("EnsureResources = %v; want a %q refusal naming %q", err, tc.class, tc.says)
			}
			if got := e.host.Under("shop"); !maps.Equal(got, held) {
				t.Errorf("a refused subscribe left shop holding %v; want it as it was, %v", slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(held)))
			}
		})
	}
}

func TestDatabase_UpgradeKeepsTheVolumesDiskAndAnUnchangedOneWritesNothing(t *testing.T) {
	e := newDBEnv(t)
	e.land(t, dbDesired("shop", map[string]string{"dbDisk": "2Gi"}))
	writes := len(e.host.Writes())

	e.land(t, dbDesired("shop", map[string]string{"dbDisk": "2Gi"}))
	_, err := e.db.EnsureResources(context.Background(), dbDesired("shop", map[string]string{"dbDisk": "3Gi"}))

	if got := len(e.host.Writes()); got != writes {
		t.Errorf("an unchanged upgrade made %d writes; want none", got-writes)
	}
	if refused(err) != "disk-fixed" || !strings.Contains(err.Error(), `keep params["dbDisk"] at 2Gi`) {
		t.Errorf("an upgrade to 3Gi = %v; want a disk-fixed refusal keeping 2Gi", err)
	}
}

func TestDatabase_GivesUpWhenMainMovesUnderEachOfThreeAttempts(t *testing.T) {
	e := newDBEnv(t)
	moves := 0
	e.host.BeforeWrite(func(method, _ string) error {
		if method == "PATCH" {
			moves++
			e.host.Set(map[string]string{"other/compose.yaml": strings.Repeat("x", moves)})
		}
		return nil
	})

	_, err := e.db.EnsureResources(context.Background(), dbDesired("shop", nil))

	if !errors.Is(err, ghapi.ErrNotFastForward) || moves != 3 {
		t.Errorf("EnsureResources under a main moving at each commit = %v after %d attempts; want it to give up after 3", err, moves)
	}
}

func TestDatabase_UnsubscribeRemovesTheFilesOnlyOnceTheFinalBackupIsUploaded(t *testing.T) {
	ctx := context.Background()
	e := newDBEnv(t)
	e.land(t, dbDesired("shop", nil))
	e.host.Set(map[string]string{"shop/compose.yaml": "services: {}\n"})
	e.host.Volumes["shop"] = true
	e.host.BackupResult = ""

	_, asked := e.db.Delete(ctx, dbDesired("shop", nil))
	_, pending := e.db.Delete(ctx, dbDesired("shop", nil))

	if !waits(asked) || !waits(pending) || strings.TrimSpace(e.host.Main()["shop/"+dbBlock+".backup-request"]) != "final-1793620800" {
		t.Fatalf("Delete = %v, then %v, holding the request %q; want both waiting on the final backup final-1793620800",
			asked, pending, e.host.Main()["shop/"+dbBlock+".backup-request"])
	}
	e.host.BackupResult = "failed: the store answered 403"
	if _, err := e.db.Delete(ctx, dbDesired("shop", nil)); err == nil || waits(err) || !strings.Contains(err.Error(), "the store answered 403") {
		t.Errorf("Delete after a failed final backup = %v; want a failure naming the host's reason", err)
	}
	if got, want := slices.Sorted(maps.Keys(e.host.Under("shop"))), append(landedFiles("shop"), "shop/compose.yaml", "shop/"+dbBlock+".backup-request"); !slices.Equal(got, slices.Sorted(slices.Values(want))) {
		t.Errorf("a failed final backup left shop holding %v; want everything kept, %v", got, want)
	}
	e.host.BackupResult = dockerhosttest.Uploaded
	if _, err := e.db.Delete(ctx, dbDesired("shop", nil)); err != nil {
		t.Fatalf("Delete once the backup is uploaded = %v", err)
	}
	if got := slices.Collect(maps.Keys(e.host.Under("shop"))); !slices.Equal(got, []string{"shop/compose.yaml"}) {
		t.Errorf("shop holds %v after the unsubscribe; want only the app's own compose file", got)
	}
}

func TestDatabase_UnsubscribeRemovesADatabaseTheHostNeverMadeAVolumeForAndLeavesAnotherSubscriptions(t *testing.T) {
	ctx := context.Background()
	e := newDBEnv(t)
	e.land(t, dbDesired("shop", nil))
	other := dbDesired("shop", nil)
	other.SubscriptionID = "sub_db_other"

	if _, err := e.db.Delete(ctx, other); err != nil || !slices.Equal(slices.Sorted(maps.Keys(e.host.Under("shop"))), landedFiles("shop")) {
		t.Errorf("another subscription's Delete = %v leaving %v; want shop's database left whole", err, slices.Sorted(maps.Keys(e.host.Under("shop"))))
	}
	if _, err := e.db.Delete(ctx, dbDesired("shop", nil)); err != nil || len(e.host.Under("shop")) != 0 {
		t.Errorf("Delete of a database with no volume = %v leaving %v; want it removed with no backup", err, e.host.Under("shop"))
	}
}

func landedFiles(app string) []string {
	return slices.Sorted(slices.Values([]string{app + "/" + dbBlock + ".backup", app + "/" + dbBlock + ".compose.yaml", app + "/" + dbBlock + ".gives", app + "/reservations/" + dbBlock + ".json"}))
}

func TestDatabase_UnsubscribeKeepsTheDatabaseWhileTheHostCannotShowAFinalBackup(t *testing.T) {
	for name, host := range map[string]func(*dockerhosttest.Host){
		"a host that has sent no report": func(h *dockerhosttest.Host) { h.Silent = true },
		"a host that takes no backups":   func(h *dockerhosttest.Host) { h.BackupError = "the host holds no backup public key" },
	} {
		t.Run(name, func(t *testing.T) {
			e := newDBEnv(t)
			e.land(t, dbDesired("shop", nil))
			e.host.Volumes["shop"] = true
			host(e.host)

			_, err := e.db.Delete(context.Background(), dbDesired("shop", nil))

			if err == nil || !slices.Equal(slices.Sorted(maps.Keys(e.host.Under("shop"))), landedFiles("shop")) {
				t.Errorf("Delete = %v leaving %v; want an error or a wait with shop's database kept whole", err, slices.Sorted(maps.Keys(e.host.Under("shop"))))
			}
		})
	}
}

func TestDatabase_SubscriptionLogsReadsTheDatabaseContainersOwnLines(t *testing.T) {
	e := newDBEnv(t)
	e.land(t, dbDesired("shop", nil))
	e.host.Containers["shop"] = map[string]any{"subscription": "sub_db_shop", "logs": "LOG: ready\n"}

	lines, err := e.db.SubscriptionLogs(context.Background(), dbDesired("shop", nil))

	if err != nil || !slices.Equal(lines, []string{"shop-store | LOG: ready"}) {
		t.Errorf("SubscriptionLogs = %q, %v; want the container's line led by its name", lines, err)
	}
}

func TestDatabase_ObserveReadsTheDatabasePresentOnlyOnceTheHostRunsItsFilesHealthy(t *testing.T) {
	logs := "LOG: ready\nFATAL: password authentication failed for user \"app\"\n"
	for _, tc := range []struct {
		name                          string
		land                          bool
		host                          func(*dockerhosttest.Host)
		state                         contract.State
		says                          string
		unwell, neverActive, recorded bool
	}{
		{name: "nothing landed", state: contract.Absent},
		{name: "landed and running", land: true, state: contract.Present, says: "shop-store runs"},
		{name: "a silent host", land: true, host: func(h *dockerhosttest.Host) { h.Silent = true }, state: contract.Unknown, says: "no node report"},
		{name: "an agent behind on the database's files", land: true, host: func(h *dockerhosttest.Host) { h.Lagging = "0000000" }, state: contract.Unknown, says: "last synced"},
		{name: "a container still starting", land: true, host: func(h *dockerhosttest.Host) { h.Containers["shop"] = map[string]any{"health": "starting"} },
			state: contract.Unknown, says: "still starting"},
		{name: "a database missing its gives file", land: true, host: func(h *dockerhosttest.Host) { h.Set(map[string]string{"shop/" + dbBlock + ".gives": ""}) },
			state: contract.Unknown, says: "without " + dbBlock + ".gives"},
		{name: "a report naming no container of the database", land: true, host: func(h *dockerhosttest.Host) { h.Containers["shop"] = map[string]any{"container": "shop-web"} },
			state: contract.Unknown, says: "shows no shop-store"},
		{name: "a container that exited", land: true, host: func(h *dockerhosttest.Host) { h.Containers["shop"] = map[string]any{"state": "exited", "logs": logs} },
			state: contract.Unknown, says: "reads exited: FATAL: password authentication failed"},
		{name: "an unhealthy container that never read active", land: true, neverActive: true, state: contract.Unknown, says: "before it ever read active",
			host: func(h *dockerhosttest.Host) {
				h.Containers["shop"] = map[string]any{"health": "unhealthy", "logs": logs}
			}},
		{name: "a request naming its repository only in the recorded identity", land: true, recorded: true, state: contract.Present, says: "shop-store runs"},
		{name: "an unhealthy container", land: true, state: contract.Present, unwell: true, says: "FATAL: password authentication failed",
			host: func(h *dockerhosttest.Host) {
				h.Containers["shop"] = map[string]any{"health": "unhealthy", "logs": logs}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newDBEnv(t)
			if tc.land {
				e.land(t, dbDesired("shop", nil))
			}
			if tc.host != nil {
				tc.host(e.host)
			}

			d := dbDesired("shop", nil)
			d.NeverActive = tc.neverActive
			if tc.recorded {
				d.Bindings["host"]["clusterRepoUrl"] = ""
				d.RecordedResources = map[string]contract.RecordedResource{dockerhost.DatabaseResource: {Identity: map[string]string{"repo": dbRepo}}}
			}

			obs := e.observe(t, d)

			detail := obs.StatusDetail
			if obs.Unhealthy != nil {
				detail = obs.Unhealthy.Detail
			}
			if obs.State != tc.state || (obs.Unhealthy != nil) != tc.unwell || !strings.Contains(detail, tc.says) {
				t.Errorf("ObserveResource = %+v; want %s, unhealthy %v, naming %q", obs, tc.state, tc.unwell, tc.says)
			}
			if obs.State == contract.Present && (obs.Outputs["host"] != "shop-store" || obs.Outputs["port"] != "7000") {
				t.Errorf("the outputs are %v; want host shop-store on port 7000", obs.Outputs)
			}
		})
	}
}
