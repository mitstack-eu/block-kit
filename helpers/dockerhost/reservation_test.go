package dockerhost_test

import (
	"strings"
	"testing"

	"github.com/mitstack-eu/block-kit/helpers/dockerhost"
)

func TestSumReservations_AddsEveryAppsRecordsWhicheverBlockWroteThem(t *testing.T) {
	files := map[string][]byte{
		"shop/reservations/docker-app-slice.json": []byte(`{"cpuMillis":500,"memoryBytes":536870912}`),
		"shop/reservations/mariadb.json":          []byte(`{"cpuMillis":250,"memoryBytes":268435456}`),
		"blog/reservations/docker-app-slice.json": []byte(`{"cpuMillis":1000,"memoryBytes":1073741824}`),
		"blog/compose.yaml":                       []byte("services: {}\n"),
		"blog/reservations/notes.txt":             []byte("not a record"),
		"reservations/stray.json":                 []byte(`{"cpuMillis":9000}`),
		".github/reservations/ci.json":            []byte(`{"cpuMillis":9000}`),
	}

	got, err := dockerhost.SumReservations(files, "blog/reservations/docker-app-slice.json")

	want := dockerhost.Reservation{CPUMillis: 750, MemoryBytes: 805306368}
	if err != nil || got != want {
		t.Errorf("SumReservations(files, except blog's slice) = %+v, %v; want %+v, nil", got, err, want)
	}
}

func TestSumReservations_RefusesARecordItCannotTrustNamingItsPath(t *testing.T) {
	for _, tc := range []struct {
		name   string
		record string
	}{
		{"a record that is not JSON", `cpuMillis: 500`},
		{"a record with a field no reservation holds", `{"cpuMillis":500,"memoryBytes":1,"disk":1}`},
		{"a negative CPU reservation", `{"cpuMillis":-500,"memoryBytes":1}`},
		{"a negative memory reservation", `{"cpuMillis":500,"memoryBytes":-1}`},
		{"a record followed by more text", `{"cpuMillis":500,"memoryBytes":1} {"cpuMillis":1}`},
		{"a record missing its memory", `{"cpuMillis":500}`},
		{"an empty record", `{}`},
		{"a null record", `null`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string][]byte{"shop/reservations/docker-app-slice.json": []byte(tc.record)}

			got, err := dockerhost.SumReservations(files, "")

			if err == nil || !strings.Contains(err.Error(), "shop/reservations/docker-app-slice.json") {
				t.Errorf("SumReservations(%s) = %+v, %v; want an error naming the record's path", tc.record, got, err)
			}
		})
	}
}

func TestSumReservations_RefusesASumPastWhatAnInt64Holds(t *testing.T) {
	files := map[string][]byte{
		"shop/reservations/docker-app-slice.json": []byte(`{"cpuMillis":9223372036854775807,"memoryBytes":1}`),
		"blog/reservations/docker-app-slice.json": []byte(`{"cpuMillis":1,"memoryBytes":1}`),
	}

	got, err := dockerhost.SumReservations(files, "")

	if err == nil {
		t.Errorf("SumReservations(two records summing past the int64 maximum) = %+v, nil; want an error", got)
	}
}

func TestSumReservations_AddsTheDiskOfEveryRecordThatCarriesOneAndNoneForARecordWithout(t *testing.T) {
	files := map[string][]byte{
		"shop/reservations/docker-app-slice.json": []byte(`{"cpuMillis":500,"memoryBytes":536870912}`),
		"shop/reservations/docker-mariadb.json":   []byte(`{"cpuMillis":250,"memoryBytes":268435456,"diskBytes":10737418240}`),
		"blog/reservations/docker-mariadb.json":   []byte(`{"cpuMillis":250,"memoryBytes":268435456,"diskBytes":5368709120}`),
	}

	got, err := dockerhost.SumReservations(files, "")

	want := dockerhost.Reservation{CPUMillis: 1000, MemoryBytes: 1073741824, DiskBytes: 16106127360}
	if err != nil || got != want {
		t.Errorf("SumReservations(two records with disk and one without) = %+v, %v; want %+v, nil", got, err, want)
	}
}

func TestSumReservations_RefusesADiskAmountItCannotCountNamingTheRecordsPath(t *testing.T) {
	const shop, blog = "shop/reservations/docker-mariadb.json", "blog/reservations/docker-mariadb.json"
	for _, tc := range []struct {
		name   string
		files  map[string]string
		reason string
	}{
		{"a negative disk reservation", map[string]string{shop: `{"cpuMillis":250,"memoryBytes":1,"diskBytes":-1}`}, "negative"},
		{"a disk amount past what an int64 holds", map[string]string{shop: `{"cpuMillis":250,"memoryBytes":1,"diskBytes":9223372036854775808}`}, "int64"},
		{"disk amounts summing past what an int64 holds", map[string]string{
			shop: `{"cpuMillis":250,"memoryBytes":1,"diskBytes":9223372036854775807}`,
			blog: `{"cpuMillis":250,"memoryBytes":1,"diskBytes":1}`,
		}, "int64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string][]byte{}
			for p, record := range tc.files {
				files[p] = []byte(record)
			}

			got, err := dockerhost.SumReservations(files, "")

			if err == nil || !strings.Contains(err.Error(), "reservations/docker-mariadb.json") || !strings.Contains(err.Error(), tc.reason) {
				t.Errorf("SumReservations(%v) = %+v, %v; want an error naming the record's path and %q", tc.files, got, err, tc.reason)
			}
		})
	}
}
