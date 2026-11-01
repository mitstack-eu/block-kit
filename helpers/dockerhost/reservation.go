package dockerhost

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
)

// ReservationDir holds, inside an app's directory of the host repository, each per-app block's <block>.json record.
const ReservationDir = "reservations"

// Reservation is the CPU, memory and disk one per-app block holds back on a Docker host for its app.
type Reservation struct {
	CPUMillis   int64 `json:"cpuMillis"`
	MemoryBytes int64 `json:"memoryBytes"`
	// DiskBytes is zero for a block whose app keeps no data of its own, so its record may leave it out.
	DiskBytes int64 `json:"diskBytes,omitempty"`
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
		if sum.CPUMillis > math.MaxInt64-r.CPUMillis || sum.MemoryBytes > math.MaxInt64-r.MemoryBytes || sum.DiskBytes > math.MaxInt64-r.DiskBytes {
			return Reservation{}, fmt.Errorf("dockerhost: the reservation %s takes the host's sum past what an int64 holds", path)
		}
		sum.CPUMillis += r.CPUMillis
		sum.MemoryBytes += r.MemoryBytes
		sum.DiskBytes += r.DiskBytes
	}
	return sum, nil
}

func isReservationPath(path string) bool {
	parts := strings.Split(path, "/")
	return len(parts) == 3 && parts[0] != "" && !strings.HasPrefix(parts[0], ".") &&
		parts[1] == ReservationDir && strings.HasSuffix(parts[2], ".json")
}

// recordedReservation holds pointers, so a record missing CPU or memory is refused rather than counted as zero.
type recordedReservation struct {
	CPUMillis   *int64 `json:"cpuMillis"`
	MemoryBytes *int64 `json:"memoryBytes"`
	DiskBytes   int64  `json:"diskBytes"`
}

func parseReservation(content []byte) (Reservation, error) {
	var rec recordedReservation
	dec := json.NewDecoder(bytes.NewReader(content))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rec); err != nil {
		return Reservation{}, fmt.Errorf("does not parse as a reservation: %w", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Reservation{}, errors.New("holds more than one JSON value")
	}
	if rec.CPUMillis == nil || rec.MemoryBytes == nil {
		return Reservation{}, errors.New("lacks cpuMillis or memoryBytes")
	}
	r := Reservation{CPUMillis: *rec.CPUMillis, MemoryBytes: *rec.MemoryBytes, DiskBytes: rec.DiskBytes}
	if r.CPUMillis < 0 || r.MemoryBytes < 0 || r.DiskBytes < 0 {
		return Reservation{}, fmt.Errorf("reserves a negative amount, %d CPU millis, %d memory bytes and %d disk bytes", r.CPUMillis, r.MemoryBytes, r.DiskBytes)
	}
	return r, nil
}
