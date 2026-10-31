package dockerhost

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// ReservationDir holds, inside an app's directory of the host repository, each per-app block's <block>.json record.
const ReservationDir = "reservations"

// Reservation is the CPU and memory one per-app block holds back on a Docker host for its app.
type Reservation struct {
	CPUMillis   int64 `json:"cpuMillis"`
	MemoryBytes int64 `json:"memoryBytes"`
}

// SumReservations adds every <app>/reservations/<block>.json record but except, so a block counts what the others hold.
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
