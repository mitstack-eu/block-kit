package dockerhost

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// ReservationDir is the directory of an app's directory in the host repository where each per-app block records its
// reservation, as <block>.json.
const ReservationDir = "reservations"

// Reservation is the CPU and memory one per-app block holds back on a Docker host for its app.
type Reservation struct {
	CPUMillis   int64 `json:"cpuMillis"`
	MemoryBytes int64 `json:"memoryBytes"`
}

// SumReservations adds every reservation the host repository's files record at <app>/reservations/<block>.json,
// leaving out the path except, so a block sizing its own app counts only what the others hold.
func SumReservations(files map[string][]byte, except string) (Reservation, error) {
	var sum Reservation
	for path, content := range files {
		if path == except || !isReservationPath(path) {
			continue
		}
		r, err := parseReservation(content)
		if err != nil {
			return Reservation{}, fmt.Errorf("dockerhost: the reservation %s: %w", path, err)
		}
		sum.CPUMillis += r.CPUMillis
		sum.MemoryBytes += r.MemoryBytes
	}
	return sum, nil
}

// isReservationPath leaves out a top-level directory whose name starts with a dot, since the host runs no app from it.
func isReservationPath(path string) bool {
	parts := strings.Split(path, "/")
	return len(parts) == 3 && parts[0] != "" && !strings.HasPrefix(parts[0], ".") &&
		parts[1] == ReservationDir && strings.HasSuffix(parts[2], ".json")
}

func parseReservation(content []byte) (Reservation, error) {
	var r Reservation
	dec := json.NewDecoder(bytes.NewReader(content))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return Reservation{}, fmt.Errorf("does not parse as a reservation: %w", err)
	}
	if r.CPUMillis < 0 || r.MemoryBytes < 0 {
		return Reservation{}, fmt.Errorf("reserves a negative amount, %d CPU millis and %d memory bytes", r.CPUMillis, r.MemoryBytes)
	}
	return r, nil
}
