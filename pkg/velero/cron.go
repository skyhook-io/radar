// Package velero interprets Velero objects the way Velero's own controllers
// do: when a Schedule is next due, what holds it, which backups it produced,
// and what a storage location holds. It reads objects and returns readings; it
// does no Kubernetes calls and knows nothing of the caller's permissions.
package velero

import (
	"fmt"
	"strings"
	"time"
	// Schedules and the Velero server name IANA zones; without zoneinfo on the
	// host every one of them would read as unknown.
	_ "time/tzdata"

	"github.com/robfig/cron/v3"
)

// parsedCron is a schedule expression parsed as Velero 1.16–1.18.2 parse it,
// with robfig/cron v3's standard parser (Velero 1.18.3 moved to a fork that
// keeps standard expressions compatible).
type parsedCron struct {
	sched cron.Schedule
	// zoned is set when the expression names its own zone (CRON_TZ= or TZ=);
	// otherwise it runs in the Velero server's local zone.
	zoned bool
	// fixedDelay: an @every expression adds a duration to its anchor, so its
	// times do not depend on any zone.
	fixedDelay bool
}

// parseCron recovers from a parser panic the way Velero's controller does
// (schedule_controller.go parseCronSchedule): a malformed zone prefix panics
// inside the parser rather than returning an error.
func parseCron(expr string) (p parsedCron, err error) {
	if strings.TrimSpace(expr) == "" {
		return p, fmt.Errorf("empty schedule")
	}
	defer func() {
		if r := recover(); r != nil {
			p, err = parsedCron{}, fmt.Errorf("invalid schedule: %v", r)
		}
	}()
	s, err := cron.ParseStandard(expr)
	if err != nil {
		return parsedCron{}, err
	}
	trimmed := strings.TrimSpace(expr)
	_, fixedDelay := s.(cron.ConstantDelaySchedule)
	return parsedCron{sched: s, zoned: strings.HasPrefix(trimmed, "CRON_TZ=") || strings.HasPrefix(trimmed, "TZ="), fixedDelay: fixedDelay}, nil
}

// next is the first run strictly after t. Without its own zone the expression
// is evaluated in loc, as the controller evaluates it in its process zone.
func (p parsedCron) next(t time.Time, loc *time.Location) time.Time {
	if !p.zoned && loc != nil {
		t = t.In(loc)
	}
	return p.sched.Next(t)
}

// cronGap is the time between the first two runs after t, a cadence estimate
// used only for the uncertain-zone margin. A cadence that cannot be read falls
// back to a day.
func (p parsedCron) cronGap(t time.Time, loc *time.Location) time.Duration {
	first := p.next(t, loc)
	if first.IsZero() {
		return 24 * time.Hour
	}
	second := p.next(first, loc)
	if second.IsZero() || !second.After(first) {
		return 24 * time.Hour
	}
	return second.Sub(first)
}

// ZoneSource says where the zone a schedule runs in comes from.
type ZoneSource string

const (
	// ZoneFromCron: the expression names its zone (CRON_TZ= or TZ=).
	ZoneFromCron ZoneSource = "cron"
	// ZoneFromServer: the Velero server Deployment sets TZ to a literal value.
	ZoneFromServer ZoneSource = "server"
	// ZoneServerDefault: the Deployment sets no TZ, so the server runs in UTC
	// (Velero's image carries no local zone).
	ZoneServerDefault ZoneSource = "serverDefault"
	// ZoneInferred: an offset read from the name of the schedule's newest
	// backup, which Velero formats in its local time. An offset carries no
	// daylight-saving rules.
	ZoneInferred ZoneSource = "inferred"
	// ZoneUnknown: none of the above could be read; times are estimated in UTC.
	ZoneUnknown ZoneSource = "unknown"
)

// Zone is the zone a schedule's times are evaluated in, and how Radar knows.
type Zone struct {
	// Name is an IANA zone ("Europe/Berlin", "UTC") or a fixed offset
	// ("UTC+02:00") for an inferred zone.
	Name   string     `json:"name"`
	Source ZoneSource `json:"source"`
}

// Certain reports whether the zone's full rules (including daylight saving)
// are known, which is what a definite "late" needs.
func (z Zone) Certain() bool {
	return z.Source == ZoneFromCron || z.Source == ZoneFromServer || z.Source == ZoneServerDefault
}

func fixedZoneName(offset time.Duration) string {
	sign := "+"
	if offset < 0 {
		sign, offset = "-", -offset
	}
	h := int(offset / time.Hour)
	m := int((offset % time.Hour) / time.Minute)
	return fmt.Sprintf("UTC%s%02d:%02d", sign, h, m)
}
