package velero

import (
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// validationOverdueAfter is how many validation intervals may pass without a
// validation before a location reads as overdue. It is Radar's threshold, not
// Velero's: the controller validates on its interval and records the attempt,
// so three missed intervals mean it is not validating this location.
const (
	validationOverdueIntervals = 3
	validationOverdueGrace     = 5 * time.Minute
	// neverValidatedAfter: Velero validates a new location as soon as its
	// controller sees it, even with recurring validation disabled.
	neverValidatedAfter = 10 * time.Minute
)

// DefaultSource says how Radar decided which location is the default.
type DefaultSource string

const (
	// DefaultFromSpec: a location in the namespace sets spec.default.
	DefaultFromSpec DefaultSource = "spec"
	// DefaultFromServer: none does, and the server's
	// --default-backup-storage-location names one.
	DefaultFromServer DefaultSource = "server"
	// DefaultUnknown: none does and the server's flags could not be read.
	DefaultUnknown DefaultSource = "unknown"
	// DefaultNone: no location sets spec.default and the server's flag is
	// explicitly empty, so Velero has no default.
	DefaultNone DefaultSource = "none"
)

// DefaultLocation resolves an installation's default storage location: the
// one with spec.default, else the server flag's name.
func DefaultLocation(locations []*unstructured.Unstructured, server ServerConfig) (string, DefaultSource) {
	for _, l := range locations {
		if l == nil {
			continue
		}
		if d, _, _ := unstructured.NestedBool(l.Object, "spec", "default"); d {
			return l.GetName(), DefaultFromSpec
		}
	}
	if server.FlagsKnown() {
		if server.DefaultLocation == "" {
			return "", DefaultNone
		}
		return server.DefaultLocation, DefaultFromServer
	}
	return "", DefaultUnknown
}

// LocationOf is the storage location a Backup targets. Velero writes the
// resolved location into spec.storageLocation when it first processes the
// backup, so an unset field means it has not been processed yet: resolving it
// against today's default could attribute it to a location it never used.
func LocationOf(b *unstructured.Unstructured) (string, bool) {
	name, _, _ := unstructured.NestedString(b.Object, "spec", "storageLocation")
	return name, name != ""
}

// LocationBackups counts the backups targeting one location. None of these
// says the data is in the bucket or that a restore would succeed.
type LocationBackups struct {
	Targeting int `json:"targeting"`
	// CompletedNotExpired: Velero reported the backup Completed and its
	// expiration has not passed.
	CompletedNotExpired int `json:"completedNotExpired"`
	// Expired: Completed backups whose expiration has passed; Velero's garbage
	// collector will delete them.
	Expired int `json:"expired"`
}

// ValidationFrequency is the interval a location is validated at, and where it
// was read.
type ValidationFrequency struct {
	Seconds  int64  `json:"seconds"`
	Source   string `json:"source"` // "spec" or "server"
	Disabled bool   `json:"disabled,omitempty"`
}

// ScheduleRef is a schedule writing to a location.
type ScheduleRef struct {
	Name       string `json:"name"`
	ViaDefault bool   `json:"viaDefault,omitempty"`
}

// LocationReading is one BackupStorageLocation and what depends on it.
type LocationReading struct {
	Namespace      string     `json:"namespace"`
	Name           string     `json:"name"`
	Phase          string     `json:"phase,omitempty"`
	Message        string     `json:"message,omitempty"`
	Provider       string     `json:"provider,omitempty"`
	Bucket         string     `json:"bucket,omitempty"`
	AccessMode     string     `json:"accessMode,omitempty"`
	LastValidation *time.Time `json:"lastValidation,omitempty"`
	// Frequency is nil when neither spec.validationFrequency nor the server's
	// flag could be read; no overdue verdict is given then.
	Frequency         *ValidationFrequency `json:"frequency,omitempty"`
	ValidationOverdue bool                 `json:"validationOverdue,omitempty"`
	// NeverValidated: no validation was recorded although the location is
	// older than neverValidatedAfter.
	NeverValidated bool          `json:"neverValidated,omitempty"`
	Default        bool          `json:"default"`
	DefaultSource  DefaultSource `json:"defaultSource"`
	// Backups is nil when the namespace's Backups were not read.
	Backups *LocationBackups `json:"backups,omitempty"`
	// Schedules is nil when the namespace's Schedules were not read.
	Schedules []ScheduleRef `json:"schedules"`
	Attention bool          `json:"attention"`
	// Reason is ReasonServerDown when an overdue validation is explained by
	// the Velero server having no available pod.
	Reason string `json:"reason,omitempty"`
}

// LocationInput is what ReadLocation needs; the slices are the namespace's.
type LocationInput struct {
	Location        *unstructured.Unstructured
	Backups         []*unstructured.Unstructured
	BackupsRead     bool
	Schedules       []*unstructured.Unstructured
	SchedulesRead   bool
	Server          ServerConfig
	DefaultLocation string
	DefaultSource   DefaultSource
	Now             time.Time
}

// ReadLocation reads one storage location.
func ReadLocation(in LocationInput) LocationReading {
	l := in.Location
	r := LocationReading{Namespace: l.GetNamespace(), Name: l.GetName(), DefaultSource: in.DefaultSource}
	r.Phase, _, _ = unstructured.NestedString(l.Object, "status", "phase")
	r.Message, _, _ = unstructured.NestedString(l.Object, "status", "message")
	r.Provider, _, _ = unstructured.NestedString(l.Object, "spec", "provider")
	r.Bucket, _, _ = unstructured.NestedString(l.Object, "spec", "objectStorage", "bucket")
	r.AccessMode, _, _ = unstructured.NestedString(l.Object, "spec", "accessMode")
	r.LastValidation = timeField(l, "status", "lastValidationTime")
	r.Default = in.DefaultLocation != "" && in.DefaultLocation == l.GetName()
	r.Frequency = validationFrequency(l, in.Server)
	if f := r.Frequency; f != nil && !f.Disabled && f.Seconds > 0 && r.LastValidation != nil {
		limit := time.Duration(f.Seconds)*time.Second*validationOverdueIntervals + validationOverdueGrace
		r.ValidationOverdue = in.Now.Sub(*r.LastValidation) > limit
	}
	// An Unavailable location is already an Issue (BackupStorageLocationUnavailable).
	if r.LastValidation == nil && in.Now.Sub(l.GetCreationTimestamp().Time) > neverValidatedAfter {
		r.NeverValidated = true
	}
	r.Attention = r.ValidationOverdue || r.NeverValidated
	if r.Attention && in.Server.Down() {
		r.Reason = ReasonServerDown
	}

	if in.BackupsRead {
		counts := LocationBackups{}
		for _, b := range in.Backups {
			if b == nil {
				continue
			}
			if name, ok := LocationOf(b); !ok || name != l.GetName() {
				continue
			}
			counts.Targeting++
			if phase, _, _ := unstructured.NestedString(b.Object, "status", "phase"); phase == "Completed" {
				if exp := timeField(b, "status", "expiration"); exp != nil && exp.Before(in.Now) {
					counts.Expired++
				} else {
					counts.CompletedNotExpired++
				}
			}
		}
		r.Backups = &counts
	}
	if in.SchedulesRead {
		r.Schedules = []ScheduleRef{}
		for _, s := range in.Schedules {
			if s == nil {
				continue
			}
			loc := scheduleLocation(s, in.DefaultLocation)
			if loc.Name == l.GetName() {
				r.Schedules = append(r.Schedules, ScheduleRef{Name: s.GetName(), ViaDefault: loc.ViaDefault})
			}
		}
		sort.Slice(r.Schedules, func(i, j int) bool { return r.Schedules[i].Name < r.Schedules[j].Name })
	}
	return r
}

// validationFrequency follows the controller (internal/storage/storagelocation.go):
// a positive spec.validationFrequency wins, zero disables recurring validation,
// and an absent or negative value falls back to the server's frequency.
func validationFrequency(l *unstructured.Unstructured, server ServerConfig) *ValidationFrequency {
	if v, ok, _ := unstructured.NestedString(l.Object, "spec", "validationFrequency"); ok && v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			return &ValidationFrequency{Seconds: int64(d / time.Second), Source: "spec", Disabled: d == 0}
		}
	}
	if server.ValidationFrequencySeconds != nil {
		secs := *server.ValidationFrequencySeconds
		return &ValidationFrequency{Seconds: secs, Source: "server", Disabled: secs == 0}
	}
	return nil
}

// UnassignedBackups counts a namespace's backups Velero has not yet assigned
// a storage location to.
func UnassignedBackups(backups []*unstructured.Unstructured) int {
	n := 0
	for _, b := range backups {
		if b == nil {
			continue
		}
		if _, ok := LocationOf(b); !ok {
			n++
		}
	}
	return n
}
