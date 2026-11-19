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

	nrcron "github.com/netresearch/go-cron"
	"github.com/robfig/cron/v3"
)

// DayMatch is how Velero matches a cron that restricts both day of month and
// day of week (neither is * or ?). Velero 1.16–1.18.2 parse schedules with
// robfig/cron v3, which runs on a day matching either field; 1.18.3 and later
// with netresearch/go-cron, which by default needs both. A cron leaving either
// field unrestricted runs the same on every version.
type DayMatch string

const (
	// DayMatchEither: Velero before 1.18.3; "0 0 1-7 * MON" is days 1–7 and
	// every Monday.
	DayMatchEither DayMatch = "either"
	// DayMatchBoth: Velero 1.18.3 and later; "0 0 1-7 * MON" is the first
	// Monday of the month.
	DayMatchBoth DayMatch = "both"
	// DayMatchUnknown: the server's Velero version is unknown, so times are
	// estimated with the earlier rule, the earliest either version runs.
	DayMatchUnknown DayMatch = "unknown"
)

// starBit is the bit both cron libraries set on a field written as * or ?
// (a stepped */n clears it). robfig/cron matches days with OR only when
// neither day field carries it.
const starBit = 1 << 63

type cronSchedule interface{ Next(time.Time) time.Time }

// parsedCron is a schedule expression parsed as the Velero server parses it:
// robfig/cron v3's standard parser before 1.18.3, netresearch/go-cron's from
// 1.18.3, robfig/cron's when the version is unknown.
type parsedCron struct {
	sched cronSchedule
	// zoned is set when the expression names its own zone (CRON_TZ= or TZ=);
	// otherwise it runs in the Velero server's local zone.
	zoned bool
	// fixedDelay: an @every expression adds a duration to its anchor, so its
	// times do not depend on any zone.
	fixedDelay bool
	// dayMatch is set when the expression restricts both day fields, the one
	// case where Velero versions disagree.
	dayMatch DayMatch
	// both is the expression as Velero 1.18.3 and later evaluate it, kept
	// when dayMatch is unknown; nil when that parser rejects it.
	both cronSchedule
}

// parseCron parses expr as a Velero server applying rule parses it; rule is
// DayMatchEither, DayMatchBoth or DayMatchUnknown. It recovers from a parser
// panic the way Velero's controller does (schedule_controller.go
// parseCronSchedule): a malformed zone prefix panics inside the parser rather
// than returning an error.
func parseCron(expr string, rule DayMatch) (p parsedCron, err error) {
	if strings.TrimSpace(expr) == "" {
		return p, fmt.Errorf("empty schedule")
	}
	var s cronSchedule
	if rule == DayMatchBoth {
		s, err = parseWith(expr, parseNetresearch)
	} else {
		s, err = parseWith(expr, parseRobfig)
	}
	if err != nil {
		return parsedCron{}, err
	}
	trimmed := strings.TrimSpace(expr)
	p = parsedCron{sched: s, zoned: strings.HasPrefix(trimmed, "CRON_TZ=") || strings.HasPrefix(trimmed, "TZ=")}
	switch spec := s.(type) {
	case cron.ConstantDelaySchedule, nrcron.ConstantDelaySchedule:
		p.fixedDelay = true
	case *cron.SpecSchedule:
		if spec.Dom&starBit == 0 && spec.Dow&starBit == 0 {
			p.dayMatch = rule
		}
	case *nrcron.SpecSchedule:
		if spec.Dom&starBit == 0 && spec.Dow&starBit == 0 {
			p.dayMatch = rule
		}
	}
	if p.dayMatch == DayMatchUnknown {
		p.both, _ = parseWith(expr, parseNetresearch)
	}
	return p, nil
}

func parseRobfig(expr string) (cronSchedule, error)      { return cron.ParseStandard(expr) }
func parseNetresearch(expr string) (cronSchedule, error) { return nrcron.ParseStandard(expr) }

func parseWith(expr string, parse func(string) (cronSchedule, error)) (s cronSchedule, err error) {
	defer func() {
		if r := recover(); r != nil {
			s, err = nil, fmt.Errorf("invalid schedule: %v", r)
		}
	}()
	return parse(expr)
}

// next is the first run strictly after t. Without its own zone the expression
// is evaluated in loc, as the controller evaluates it in its process zone.
func (p parsedCron) next(t time.Time, loc *time.Location) time.Time {
	return p.eval(p.sched, t, loc)
}

// nextBoth is next as Velero 1.18.3 and later evaluate an expression whose
// dayMatch is unknown; zero when that parser rejects it or it has no time.
func (p parsedCron) nextBoth(t time.Time, loc *time.Location) time.Time {
	if p.both == nil {
		return time.Time{}
	}
	return p.eval(p.both, t, loc)
}

func (p parsedCron) eval(s cronSchedule, t time.Time, loc *time.Location) time.Time {
	if !p.zoned && loc != nil {
		t = t.In(loc)
	}
	return s.Next(t)
}

// cronGap is the time between s's first two runs after t, a cadence estimate
// used only for the uncertain-zone margin. A cadence that cannot be read falls
// back to a day.
func (p parsedCron) cronGap(s cronSchedule, t time.Time, loc *time.Location) time.Duration {
	first := p.eval(s, t, loc)
	if first.IsZero() {
		return 24 * time.Hour
	}
	second := p.eval(s, first, loc)
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
	// ZoneFromServer: the Velero server Deployment sets TZ to a literal zone
	// other than UTC.
	ZoneFromServer ZoneSource = "server"
	// ZoneServerDefault: the server runs in UTC, because the Deployment sets
	// no TZ (Velero's image carries no local zone) or sets TZ=UTC.
	ZoneServerDefault ZoneSource = "serverDefault"
	// ZoneInferred: an offset read from the name of the controller's
	// submission matching status.lastBackup, which Velero formats in its local
	// time. Manual and CLI backups (named in UTC) and backups recreated by
	// sync are not used. An offset carries no daylight-saving rules.
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
