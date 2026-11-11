package dockerhost

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"

	"github.com/mitstack-eu/block-kit/handler"
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

// Ask is the room one Part, such as shop's database, asks of a Docker host, and Kind, such as database, is what a
// refusal tells the developer to give less.
type Ask struct {
	Part string
	Kind string
	Size Reservation
}

// Capacity is the allocatable capacity the host's report gives, whatever error it also carries, a wait for the host's next report while it gives none, or a refusal of a host the platform attests absent.
func Capacity(report Report, found bool, part string) (Reservation, error) {
	switch {
	case report.absence != nil:
		return Reservation{}, handler.Refuse("host-absent", "the platform's %s read the host's %s absent, so %s cannot be placed on it; give the app a running host",
			report.absence.Observer, report.absence.Resource, part)
	case found && report.Allocatable == nil && report.capacityError != "":
		return Reservation{}, handler.WaitForSignal("the host's newest node report gives no allocatable capacity but the error %q, so %s waits for a report that gives it before it is sized",
			report.capacityError, part)
	case !found || report.Allocatable == nil:
		return Reservation{}, handler.WaitForSignal("the host has sent no node report giving its allocatable capacity yet, so %s waits for that capacity report before it is sized", part)
	}
	return *report.Allocatable, nil
}

// CheckRoom waits or refuses as Capacity does, and refuses host-full, naming the free and asked amounts, when ask does not fit in what the host can give its apps less what others hold.
func CheckRoom(report Report, found bool, ask Ask, others Reservation) error {
	allocatable, err := Capacity(report, found, ask.Part)
	if err != nil {
		return err
	}
	return fitRoom(allocatable, ask, others)
}

func fitRoom(allocatable Reservation, ask Ask, others Reservation) error {
	free := Free(allocatable, others)
	if ask.Size.Fits(free) {
		return nil
	}
	return handler.Refuse("host-full",
		"the host has %s free beyond what its other apps reserve, and %s asks for %s; give the %s less, or remove an app from the host first",
		roomText(free, ask.Size), ask.Part, Describe(ask.Size), ask.Kind)
}

func roomText(free, asked Reservation) string {
	if asked.DiskBytes == 0 {
		free.DiskBytes = 0
		return Describe(free)
	}
	if free.DiskBytes <= 0 {
		return Describe(Reservation{CPUMillis: free.CPUMillis, MemoryBytes: free.MemoryBytes}) + " and no disk"
	}
	return Describe(free)
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
