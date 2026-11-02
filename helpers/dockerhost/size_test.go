package dockerhost_test

// A per-app block reads its size inputs, judges its fit and words its refusal as the console's host-room panel does.

import (
	"testing"

	"github.com/mitstack-eu/block-kit/helpers/dockerhost"
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
	allocatable := dockerhost.Reservation{CPUMillis: 3500, MemoryBytes: 7 * gib, DiskBytes: 140 * gib}
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
