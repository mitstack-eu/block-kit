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
