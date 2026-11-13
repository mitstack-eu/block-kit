package dockerhost_test

// A per-app block reads its size inputs, judges its fit and words its refusal as the console's host-room panel does.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
	"github.com/mitstack-eu/block-kit/helpers/dockerhost"
	"github.com/mitstack-eu/block-kit/observe"
)

const (
	mib = int64(1) << 20
	gib = int64(1) << 30
)

func TestParseCPU_ReadsVCPUsToThreeDecimalsOrMillicores(t *testing.T) {
	for in, want := range map[string]int64{"0.5": 500, "2": 2000, "1.255": 1255, "500m": 500, "0.001": 1} {
		if got, err := dockerhost.ParseCPU(in); err != nil || got != want {
			t.Errorf("ParseCPU(%q) = %d, %v; want %d, nil", in, got, err, want)
		}
	}
}

func TestParseCPU_RefusesAnAmountItCannotReadOrOfNothing(t *testing.T) {
	for _, in := range []string{"", "0", "0m", "-1", "1.2345", "half", "1e3", "2147483648m", " 1"} {
		if got, err := dockerhost.ParseCPU(in); err == nil {
			t.Errorf("ParseCPU(%q) = %d, nil; want an error", in, got)
		}
	}
}

func TestParseSize_ReadsWholeMiOrGi(t *testing.T) {
	for in, want := range map[string]int64{"512Mi": 512 * mib, "1Gi": gib, "10Gi": 10 * gib, "1Mi": mib} {
		if got, err := dockerhost.ParseSize(in); err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d, nil", in, got, err, want)
		}
	}
}

func TestParseSize_RefusesAnAmountItCannotReadOrOfNothing(t *testing.T) {
	for _, in := range []string{"", "0Mi", "512", "512M", "1GB", "1.5Gi", "-1Gi", "2147483648Mi"} {
		if got, err := dockerhost.ParseSize(in); err == nil {
			t.Errorf("ParseSize(%q) = %d, nil; want an error", in, got)
		}
	}
}

func TestFree_IsWhatTheHostCanGiveLessWhatTheOthersHold(t *testing.T) {
	allocatable := dockerhost.Allocatable{CPUMillis: 3500, MemoryBytes: 7 * gib, DiskBytes: 140 * gib}
	others := dockerhost.Reservation{CPUMillis: 1000, MemoryBytes: 2 * gib, DiskBytes: 10 * gib}

	got := dockerhost.Free(allocatable, others)

	want := dockerhost.Reservation{CPUMillis: 2500, MemoryBytes: 5 * gib, DiskBytes: 130 * gib}
	if got != want {
		t.Errorf("Free(%+v, %+v) = %+v; want %+v", allocatable, others, got, want)
	}
}

func TestFits_HoldsEachAmountWithinWhatIsFreeAndNoDiskAlwaysFits(t *testing.T) {
	free := dockerhost.Reservation{CPUMillis: 500, MemoryBytes: gib, DiskBytes: -gib}
	for _, tc := range []struct {
		name string
		want dockerhost.Reservation
		fits bool
	}{
		{"exactly what is free, and no disk on a host whose disk others overfill", dockerhost.Reservation{CPUMillis: 500, MemoryBytes: gib}, true},
		{"one millicore too many", dockerhost.Reservation{CPUMillis: 501, MemoryBytes: gib}, false},
		{"one byte of memory too many", dockerhost.Reservation{CPUMillis: 500, MemoryBytes: gib + 1}, false},
		{"any disk on a host with none free", dockerhost.Reservation{CPUMillis: 1, MemoryBytes: 1, DiskBytes: 1}, false},
	} {
		if got := tc.want.Fits(free); got != tc.fits {
			t.Errorf("%s: %+v.Fits(%+v) = %v; want %v", tc.name, tc.want, free, got, tc.fits)
		}
	}
}

func TestDescribe_WritesTheAmountsAsTheHostRoomPanelDoesWithDiskOnlyWhenThereIsSome(t *testing.T) {
	const cpx32Allocatable = int64(7048208384)
	for _, tc := range []struct {
		r    dockerhost.Reservation
		want string
	}{
		{dockerhost.Reservation{CPUMillis: 3500, MemoryBytes: 512 * mib}, "3.5 vCPU and 512 MiB"},
		{dockerhost.Reservation{CPUMillis: 1255, MemoryBytes: cpx32Allocatable}, "1.25 vCPU and 6.5 GiB"},
		{dockerhost.Reservation{CPUMillis: -500, MemoryBytes: gib - 1}, "0 vCPU and 1023 MiB"},
		{dockerhost.Reservation{CPUMillis: 250, MemoryBytes: 256 * mib, DiskBytes: 10 * gib}, "0.25 vCPU, 256 MiB and 10 GiB of disk"},
		{dockerhost.Reservation{CPUMillis: 500, MemoryBytes: gib, DiskBytes: -gib}, "0.5 vCPU, 1 GiB and 0 MiB of disk"},
	} {
		if got := dockerhost.Describe(tc.r); got != tc.want {
			t.Errorf("Describe(%+v) = %q; want %q", tc.r, got, tc.want)
		}
	}
}

// waitsForSignal reports whether err is a wait that asks for the subscribe to be sent again on the host's next report.
func waitsForSignal(err error) bool {
	var w *handler.Waiting
	return errors.As(err, &w) && w.Redrive
}

// reportOf reads the host's newest report from reports as a block does, with d naming what the platform attests.
func reportOf(d contract.Desired, reports ...observe.NodeReport) func(*testing.T) (dockerhost.Report, bool) {
	return func(t *testing.T) (dockerhost.Report, bool) {
		t.Helper()
		report, found, err := dockerhost.NewestReport(context.Background(), &hostReports{reports: reports}, d, hostSub)
		if err != nil {
			t.Fatalf("NewestReport = %v; want a report or none", err)
		}
		return report, found
	}
}

const wholeCapacity = `{"cpuMillis":3500,"memoryBytes":7516192768,"diskBytes":75161927680}`

func giving(allocatable string) func(*testing.T) (dockerhost.Report, bool) {
	return reportOf(contract.Desired{}, received(39, `{"host":{"capacity":{"allocatable":`+allocatable+`}}}`))
}

func TestCapacity_GivesTheCapacityTheReportStatesWhole(t *testing.T) {
	report, found := giving(wholeCapacity)(t)

	got, err := dockerhost.Capacity(report, found, "shop")

	if want := (dockerhost.Allocatable{CPUMillis: 3500, MemoryBytes: 7 * gib, DiskBytes: 70 * gib}); err != nil || got != want {
		t.Errorf("Capacity(%s) = %+v, %v; want %+v", wholeCapacity, got, err, want)
	}
}

func TestCapacity_WaitsNamingTheMeasureTheReportsCapacityLacks(t *testing.T) {
	const withoutMemory = `{"cpuMillis":4000,"diskBytes":75161927680}`
	report, found := giving(withoutMemory)(t)

	got, err := dockerhost.Capacity(report, found, "shop")

	if want := "host.capacity.allocatable lacks memoryBytes"; !waitsForSignal(err) || !strings.Contains(err.Error(), want) {
		t.Errorf("Capacity(%s) = %+v, %v; want a wait for the next report naming %q", withoutMemory, got, err, want)
	}
}

func TestCheckRoom_WaitsForTheHostsCapacityReportRefusesAnAbsentOrFullHostAndPassesWhatFits(t *testing.T) {
	reporting := giving(wholeCapacity)
	others := dockerhost.Reservation{CPUMillis: 3000, MemoryBytes: 6 * gib}
	small := dockerhost.Ask{Part: "shop", Kind: "app", Size: dockerhost.Reservation{CPUMillis: 500, MemoryBytes: 512 * mib}}
	large := dockerhost.Ask{Part: "shop", Kind: "app", Size: dockerhost.Reservation{CPUMillis: 1000, MemoryBytes: 512 * mib}}
	withDisk := dockerhost.Ask{Part: "shop", Kind: "app", Size: dockerhost.Reservation{CPUMillis: 500, MemoryBytes: 512 * mib, DiskBytes: gib}}
	for _, tc := range []struct {
		name   string
		report func(*testing.T) (dockerhost.Report, bool)
		ask    dockerhost.Ask
		others dockerhost.Reservation
		wait   bool
		class  string
		says   []string
	}{
		{name: "a host that has sent no report waits for its next report, naming the capacity report", report: reportOf(contract.Desired{}), ask: small, others: others, wait: true,
			says: []string{"no node report giving its allocatable capacity", "shop waits for that capacity report"}},
		{name: "a report with no allocatable capacity waits the same", report: reportOf(contract.Desired{}, received(39, `{"host":{"capacity":{}}}`)), ask: small, others: others, wait: true,
			says: []string{"no node report giving its allocatable capacity", "shop waits for that capacity report"}},
		{name: "a report stating why it gives no capacity waits, naming the host's own error",
			report: reportOf(contract.Desired{}, received(39, `{"host":{"capacity":{"error":"nproc printed nothing"}}}`)), ask: small, others: others, wait: true,
			says: []string{"nproc printed nothing", "shop waits for a report that holds it"}},
		{name: "a capacity without its memory waits naming memoryBytes, and is never refused as a full host", report: giving(`{"cpuMillis":4000,"diskBytes":75161927680}`), ask: small, others: others, wait: true,
			says: []string{"host.capacity.allocatable lacks memoryBytes", "shop waits for a report that holds it"}},
		{name: "a capacity whose cpuMillis is text waits naming cpuMillis", report: giving(`{"cpuMillis":"[redacted]6","memoryBytes":7516192768,"diskBytes":75161927680}`), ask: small, others: others, wait: true,
			says: []string{"host.capacity.allocatable.cpuMillis holds a JSON string", "shop waits for a report that holds it"}},
		{name: "a host the platform attests absent is refused host-absent", report: reportOf(attestedAbsent(hostSub)), ask: small, others: others, class: "host-absent",
			says: []string{"hetzner.docker-host", "hetzner.server absent", "shop cannot be placed on it"}},
		{name: "an app too large is refused host-full, naming the free and asked amounts", report: reporting, ask: large, others: others, class: "host-full",
			says: []string{"the host has 0.5 vCPU and 1 GiB free", "shop asks for 1 vCPU and 512 MiB", "give the app less"}},
		{name: "an app asking for disk on a host its others overfill is refused naming no disk", report: reporting, ask: withDisk,
			others: dockerhost.Reservation{CPUMillis: 1000, MemoryBytes: gib, DiskBytes: 80 * gib}, class: "host-full",
			says: []string{"the host has 2.5 vCPU and 6 GiB and no disk free"}},
		{name: "an app that fits passes", report: reporting, ask: small, others: others},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report, found := tc.report(t)

			err := dockerhost.CheckRoom(report, found, tc.ask, tc.others)

			if waitsForSignal(err) != tc.wait || refused(err) != tc.class || (err == nil) != (!tc.wait && tc.class == "") {
				t.Fatalf("CheckRoom(%s) = %v; want a wait for the next report %v, refusal class %q", tc.ask.Part, err, tc.wait, tc.class)
			}
			for _, want := range tc.says {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("CheckRoom(%s) = %q; want it to name %q", tc.ask.Part, err, want)
				}
			}
		})
	}
}
