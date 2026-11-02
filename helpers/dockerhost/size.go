package dockerhost

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
)

const (
	millisPerCPU = 1000
	bytesPerMiB  = int64(1) << 20
	mibPerGiB    = 1024
	bytesPerGiB  = bytesPerMiB * mibPerGiB
)

var (
	cpuRe   = regexp.MustCompile(`^(\d{1,6})(?:\.(\d{1,3}))?$`)
	milliRe = regexp.MustCompile(`^(\d{1,9})m$`)
	sizeRe  = regexp.MustCompile(`^(\d{1,9})(Mi|Gi)$`)
)

// ParseCPU reads vCPUs to three decimals, such as 0.5, or millicores, such as 500m, into millicores above zero.
func ParseCPU(v string) (int64, error) {
	var millis int64
	if m := milliRe.FindStringSubmatch(v); m != nil {
		millis, _ = strconv.ParseInt(m[1], 10, 64)
	} else if m := cpuRe.FindStringSubmatch(v); m != nil {
		whole, _ := strconv.ParseInt(m[1], 10, 64)
		fraction, _ := strconv.ParseInt((m[2] + "000")[:3], 10, 64)
		millis = whole*millisPerCPU + fraction
	}
	if millis <= 0 || millis > math.MaxInt32 {
		return 0, errors.New("give vCPUs such as 0.5 or 2, or millicores such as 500m, above zero")
	}
	return millis, nil
}

// ParseSize reads a whole number of Mi or Gi, such as 512Mi or 1Gi, into bytes above zero.
func ParseSize(v string) (int64, error) {
	var mib int64
	if m := sizeRe.FindStringSubmatch(v); m != nil {
		mib, _ = strconv.ParseInt(m[1], 10, 64)
		if m[2] == "Gi" {
			mib *= mibPerGiB
		}
	}
	if mib <= 0 || mib > math.MaxInt32 {
		return 0, errors.New("give Mi or Gi, such as 512Mi or 1Gi, above zero")
	}
	return mib * bytesPerMiB, nil
}

// Free is what the host can give its apps less what the other reservations hold, an amount below zero when they overfill it.
func Free(allocatable, others Reservation) Reservation {
	return Reservation{
		CPUMillis:   allocatable.CPUMillis - others.CPUMillis,
		MemoryBytes: allocatable.MemoryBytes - others.MemoryBytes,
		DiskBytes:   allocatable.DiskBytes - others.DiskBytes,
	}
}

// Fits holds each amount within free, and a reservation of no disk fits whatever disk is free.
func (r Reservation) Fits(free Reservation) bool {
	return r.CPUMillis <= free.CPUMillis && r.MemoryBytes <= free.MemoryBytes && (r.DiskBytes == 0 || r.DiskBytes <= free.DiskBytes)
}

// Describe writes the amounts as the console's host-room panel does, rounded down so a refusal never names room the host lacks.
func Describe(r Reservation) string {
	if r.DiskBytes == 0 {
		return cpuText(r.CPUMillis) + " and " + bytesText(r.MemoryBytes)
	}
	return fmt.Sprintf("%s, %s and %s of disk", cpuText(r.CPUMillis), bytesText(r.MemoryBytes), bytesText(r.DiskBytes))
}

func cpuText(millis int64) string {
	hundredths := max(millis, 0) / (millisPerCPU / 100)
	return strconv.FormatFloat(float64(hundredths)/100, 'f', -1, 64) + " vCPU"
}

func bytesText(bytes int64) string {
	b := max(bytes, 0)
	if b < bytesPerGiB {
		return fmt.Sprintf("%d MiB", b/bytesPerMiB)
	}
	tenths := b/bytesPerGiB*10 + b%bytesPerGiB*10/bytesPerGiB
	return strconv.FormatFloat(float64(tenths)/10, 'f', -1, 64) + " GiB"
}
