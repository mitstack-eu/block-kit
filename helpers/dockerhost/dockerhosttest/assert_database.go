package dockerhosttest

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/helpers/dockerhost"
)

// AssertDatabase runs, each as a subtest named for what it proves, the scenarios every database block's Database holds on its host.
func AssertDatabase(t *testing.T, db dockerhost.Database) {
	t.Helper()
	for _, s := range []struct {
		name  string
		check func(*testing.T, *Database)
	}{
		{"lands each app on its own network with no published port and its volume at its disk", landsEachApp},
		{"keeps the password on the host and writes no gives file", keepsThePasswordOnTheHost},
		{"outputs the connection and the password's secret name and no password", outputsTheConnection},
		{"refuses a reservation past the host's free CPU, memory or disk and writes nothing", refusesWhatDoesNotFit},
		{"keeps the volume, its size and its mount through an upgrade", keepsTheVolumeThroughAnUpgrade},
		{"removes nothing until the final backup reads uploaded", waitsForTheFinalBackup},
	} {
		t.Run(s.name, func(t *testing.T) { s.check(t, NewDatabase(t, db)) })
	}
}

func landsEachApp(t *testing.T, d *Database) {
	d.Land(t, d.Desired("shop", map[string]string{"dbDisk": "2Gi"}))
	d.Land(t, d.Desired("blog", nil))

	e, volume := d.DB.Engine, d.Service+"-data"
	for app, disk := range map[string]string{"shop": "2GiB", "blog": "1GiB"} {
		c := d.Compose(t, app)
		s, ok := c.Services[e.Service]
		if !ok || len(c.Services) != 1 {
			t.Fatalf("%s's compose file holds %v; want %s alone", app, slices.Collect(maps.Keys(c.Services)), e.Service)
		}
		if len(s.Ports) != 0 || c.Network("default") != "app-"+app || !slices.Equal(s.Networks, []string{"default"}) {
			t.Errorf("%s's database publishes %v on %v named %q; want no port, on the app's own network app-%s alone", app, s.Ports, s.Networks, c.Network("default"), app)
		}
		if got := c.Volumes[volume].Labels["eu.mitstack.size"]; got != disk || !slices.Equal(s.Volumes, []string{volume + ":" + e.DataDir}) {
			t.Errorf("%s's data volume is %v sized %q; want %s at %s sized %s", app, s.Volumes, got, volume, e.DataDir, disk)
		}
		if s.Image != e.Image || s.ContainerName != app+"-"+e.Service || s.Labels["mitstack.dev/subscription"] != "sub_db_"+app {
			t.Errorf("%s's database runs %q as %q labelled %v; want %s as %s-%s labelled with its subscription", app, s.Image, s.ContainerName, s.Labels, e.Image, app, e.Service)
		}
	}
	var held dockerhost.Reservation
	if err := json.Unmarshal([]byte(d.Main()["shop/reservations/"+d.DB.Block+".json"]), &held); err != nil || held.DiskBytes != 2<<30 {
		t.Errorf("shop's reservation reads %+v, %v; want its 2 GiB of disk counted against the host", held, err)
	}
}

func keepsThePasswordOnTheHost(t *testing.T, d *Database) {
	d.Land(t, d.Desired("shop", nil))

	b := "shop/" + d.DB.Block
	got := slices.Sorted(maps.Keys(d.Under("shop")))
	want := []string{b + ".backup", b + ".compose.yaml", b + ".env.names", "shop/reservations/" + d.DB.Block + ".json"}
	if !slices.Equal(got, want) {
		t.Errorf("shop holds %v; want %v, and no gives file", got, want)
	}
	if got, want := d.Main()[b+".env.names"], dockerhost.PasswordVariable+"=generated:"+d.DB.Block+":password\n"; got != want {
		t.Errorf("the database's env.names is %q; want %q alone, the password the host generates and keeps", got, want)
	}
	e := d.DB.Engine
	for key, want := range map[string]string{"service": e.Service, "every": "24h", "command": e.Dump, "restore": e.Restore} {
		if got := d.BackupLine(t, "shop", key); got != want {
			t.Errorf("the backup file's %s is %q; want %q", key, got, want)
		}
	}
}

func outputsTheConnection(t *testing.T, d *Database) {
	d.Land(t, d.Desired("shop", nil))

	obs := d.Observe(t, d.Desired("shop", nil))

	want := map[string]string{"host": "shop-" + d.Service, "port": d.DB.Engine.Port, "dbName": dockerhost.DatabaseName,
		"username": dockerhost.DatabaseUser, "passwordSecret": "generated:" + d.DB.Block + ":password"}
	if obs.State != contract.Present || !maps.Equal(obs.Outputs, want) {
		t.Errorf("ObserveResource = %s with outputs %v; want present with %v", obs.State, obs.Outputs, want)
	}
}

func refusesWhatDoesNotFit(t *testing.T, d *Database) {
	d.Set(map[string]string{"blog/reservations/docker-app-slice.json": `{"cpuMillis":500,"memoryBytes":536870912}`})

	for _, params := range []map[string]string{{"dbCpu": "4"}, {"dbMemory": "8Gi"}, {"dbDisk": "71Gi"}} {
		_, err := d.DB.EnsureResources(context.Background(), d.Desired("shop", params))

		var refused *handler.Refused
		if !errors.As(err, &refused) || refused.Class != "host-full" || !strings.Contains(err.Error(), "the host has 3 vCPU, 6.5 GiB and 70 GiB of disk free") {
			t.Errorf("EnsureResources asking %v = %v; want a host-full refusal naming what is free", params, err)
		}
		if got := d.Under("shop"); len(got) != 0 {
			t.Errorf("a subscribe asking %v and refused wrote %v", params, slices.Collect(maps.Keys(got)))
		}
	}
}

func keepsTheVolumeThroughAnUpgrade(t *testing.T, d *Database) {
	d.Land(t, d.Desired("shop", map[string]string{"dbDisk": "2Gi"}))
	before := d.Compose(t, "shop")

	d.Land(t, d.Desired("shop", map[string]string{"dbDisk": "2Gi", "dbMemory": "1Gi"}))

	after, s := d.Compose(t, "shop"), d.Service
	if after.Services[s].MemLimit == before.Services[s].MemLimit {
		t.Fatalf("the upgrade left the memory limit at %d; want a changed compose file", after.Services[s].MemLimit)
	}
	if !maps.EqualFunc(after.Volumes, before.Volumes, func(a, b dockerhost.ComposeVolume) bool { return maps.Equal(a.Labels, b.Labels) }) ||
		!slices.Equal(after.Services[s].Volumes, before.Services[s].Volumes) {
		t.Errorf("the upgrade moved the volume from %v mounted %v to %v mounted %v; want it kept as it was",
			before.Volumes, before.Services[s].Volumes, after.Volumes, after.Services[s].Volumes)
	}
}

func waitsForTheFinalBackup(t *testing.T, d *Database) {
	ctx := context.Background()
	d.Land(t, d.Desired("shop", nil))
	d.Volumes["shop"], d.BackupResult = true, ""
	waiting := func(err error) bool { var w *handler.Waiting; return errors.As(err, &w) }
	landed := len(d.Under("shop"))

	_, asked := d.DB.Delete(ctx, d.Desired("shop", nil))
	_, pending := d.DB.Delete(ctx, d.Desired("shop", nil))
	if !waiting(asked) || !waiting(pending) || len(d.Under("shop")) != landed+1 {
		t.Fatalf("Delete = %v, then %v, leaving %d files; want both waiting with the database and its backup request kept", asked, pending, len(d.Under("shop")))
	}

	d.BackupResult = "failed: the store answered 403"
	if _, err := d.DB.Delete(ctx, d.Desired("shop", nil)); err == nil || waiting(err) || len(d.Under("shop")) != landed+1 {
		t.Errorf("Delete after a failed final backup = %v leaving %d files; want a failure naming it that removed nothing", err, len(d.Under("shop")))
	}

	d.BackupResult = Uploaded
	if _, err := d.DB.Delete(ctx, d.Desired("shop", nil)); err != nil || len(d.Under("shop")) != 0 {
		t.Errorf("Delete once the final backup reads uploaded = %v leaving %v; want every file removed", err, d.Under("shop"))
	}
}
