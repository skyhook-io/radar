package velero

import (
	"encoding/json"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

// ServerSource says how much of a Velero installation's server configuration
// Radar established.
type ServerSource string

const (
	// ServerFromDeployment: the server Deployment was read and its rollout is
	// complete, so its template is what the running controller uses.
	ServerFromDeployment ServerSource = "deployment"
	// ServerRolling: a rollout is in progress; the running controller may still
	// use the previous template, so nothing is taken from it.
	ServerRolling ServerSource = "rolloutInProgress"
	// ServerNotFound: Deployments in the namespace were read and none runs
	// `velero server`.
	ServerNotFound ServerSource = "notFound"
	// ServerUnread: the caller may not read Deployments there, or Radar holds
	// none for it.
	ServerUnread ServerSource = "unread"
)

const (
	defaultLocationName        = "default"
	defaultValidationFrequency = time.Minute
)

// ServerConfig is what a Velero server's flags and environment say, as far as
// Radar could establish it. A field is set only when its effective value is
// known: a TZ supplied through envFrom or valueFrom, or a flag built from
// $(VAR) expansion, leaves it unset rather than guessed.
type ServerConfig struct {
	Source     ServerSource `json:"source"`
	Deployment string       `json:"deployment,omitempty"`
	// TimeZone is the zone the controller runs in ("UTC" when none is set).
	TimeZone string `json:"timeZone,omitempty"`
	// DefaultLocation is --default-backup-storage-location, used when no
	// location sets spec.default.
	DefaultLocation string `json:"defaultLocation,omitempty"`
	// ValidationFrequencySeconds is --store-validation-frequency, the interval
	// a location without spec.validationFrequency is validated at; 0 disables
	// recurring validation.
	ValidationFrequencySeconds *int64 `json:"validationFrequencySeconds,omitempty"`
	// SchedulingDisabled: --restore-only or --disable-controllers removes the
	// schedule controller, so no Schedule runs.
	SchedulingDisabled bool `json:"schedulingDisabled,omitempty"`
	// BackupsDisabled is independent of scheduling: manual Backups need the backup controller too.
	BackupsDisabled bool `json:"backupsDisabled,omitempty"`
	// Replicas counts non-terminating pods, including Pending and CrashLoop.
	// Readiness is separate: a pod can fail its readiness probe while still
	// processing schedules. Terminating pods stop their controllers on SIGTERM.
	Replicas      *int32 `json:"replicas,omitempty"`
	ReadyReplicas *int32 `json:"readyReplicas,omitempty"`

	zone     *time.Location
	flagsSet bool
}

// Down reports that the server Deployment was read and has no running replica
// (scaled to zero, or its pods cannot be created), so nothing new submits
// scheduled backups or validates locations.
func (c ServerConfig) Down() bool { return c.Replicas != nil && *c.Replicas == 0 }

// FlagsKnown reports whether the server's flags were read in full.
func (c ServerConfig) FlagsKnown() bool { return c.flagsSet }

// ServerConfigFromDeployments reads the Velero server among a namespace's
// Deployments. read=false means the Deployments could not be read.
func ServerConfigFromDeployments(deployments []*appsv1.Deployment, read bool) ServerConfig {
	if !read {
		return ServerConfig{Source: ServerUnread}
	}
	// Several Deployments can run `velero server` during a migration, one
	// scaled to zero beside its running replacement. The running one is the
	// server; list order must not decide that Velero is stopped.
	var server *appsv1.Deployment
	var container corev1.Container
	for _, d := range deployments {
		if d == nil {
			continue
		}
		c, ok := serverContainer(d)
		if !ok {
			continue
		}
		if server == nil || (server.Status.Replicas == 0 && d.Status.Replicas > 0) {
			server, container = d, c
		}
	}
	if server != nil {
		d, c := server, container
		replicas, ready := d.Status.Replicas, d.Status.ReadyReplicas
		cfg := ServerConfig{Source: ServerFromDeployment, Deployment: d.Name, Replicas: &replicas, ReadyReplicas: &ready}
		if !rolloutComplete(d) {
			cfg.Source = ServerRolling
			return cfg
		}
		cfg.readZone(c)
		cfg.readFlags(c)
		return cfg
	}
	return ServerConfig{Source: ServerNotFound}
}

// serverContainer finds the container running `velero server`.
func serverContainer(d *appsv1.Deployment) (corev1.Container, bool) {
	for _, c := range d.Spec.Template.Spec.Containers {
		tokens := append(append([]string{}, c.Command...), c.Args...)
		velero := false
		for i, t := range tokens {
			if t == "/velero" || strings.HasSuffix(t, "/velero") || (i == 0 && t == "velero") {
				velero = true
			}
			if t == "server" && (velero || imageIsVelero(c.Image)) {
				return c, true
			}
		}
	}
	return corev1.Container{}, false
}

func imageIsVelero(image string) bool {
	repo := image
	if at := strings.Index(repo, "@"); at >= 0 {
		repo = repo[:at]
	}
	if colon := strings.LastIndex(repo, ":"); colon > strings.LastIndex(repo, "/") {
		repo = repo[:colon]
	}
	return repo == "velero/velero" || strings.HasSuffix(repo, "/velero/velero")
}

func rolloutComplete(d *appsv1.Deployment) bool {
	want := int32(1)
	if d.Spec.Replicas != nil {
		want = *d.Spec.Replicas
	}
	return d.Status.ObservedGeneration >= d.Generation &&
		d.Status.UpdatedReplicas == want &&
		d.Status.Replicas == d.Status.UpdatedReplicas
}

func (cfg *ServerConfig) readZone(c corev1.Container) {
	for _, e := range c.Env {
		if e.Name != "TZ" {
			continue
		}
		if e.ValueFrom != nil {
			return
		}
		name := e.Value
		if name == "" {
			name = "UTC"
		}
		loc, err := time.LoadLocation(name)
		if err != nil {
			return
		}
		cfg.TimeZone, cfg.zone = name, loc
		return
	}
	if len(c.EnvFrom) > 0 {
		// TZ may come from a ConfigMap or Secret Radar does not resolve.
		return
	}
	cfg.TimeZone, cfg.zone = "UTC", time.UTC
}

func (cfg *ServerConfig) readFlags(c corev1.Container) {
	tokens := append(append([]string{}, c.Command...), c.Args...)
	for _, t := range tokens {
		if strings.Contains(t, "$(") {
			return
		}
	}
	freq := defaultValidationFrequency
	cfg.DefaultLocation = defaultLocationName
	for i := 0; i < len(tokens); i++ {
		name, value, hasValue := strings.Cut(strings.TrimLeft(tokens[i], "-"), "=")
		if !strings.HasPrefix(tokens[i], "--") {
			continue
		}
		takeValue := func() (string, bool) {
			if hasValue {
				return value, true
			}
			if i+1 < len(tokens) && !strings.HasPrefix(tokens[i+1], "-") {
				i++
				return tokens[i], true
			}
			return "", false
		}
		switch name {
		case "default-backup-storage-location":
			// An explicitly empty value is kept: Velero then has no default,
			// and a backup that names no location fails to resolve one.
			if v, ok := takeValue(); ok {
				cfg.DefaultLocation = v
			}
		case "store-validation-frequency":
			v, ok := takeValue()
			if !ok {
				return
			}
			d, err := time.ParseDuration(v)
			if err != nil {
				return
			}
			freq = d
		case "restore-only":
			if !hasValue || value == "true" {
				cfg.SchedulingDisabled = true
				cfg.BackupsDisabled = true
			}
		case "disable-controllers":
			if v, ok := takeValue(); ok {
				for ctl := range strings.SplitSeq(v, ",") {
					if strings.TrimSpace(ctl) == "backup" {
						cfg.BackupsDisabled = true
					}
					if strings.TrimSpace(ctl) == "schedule" {
						cfg.SchedulingDisabled = true
					}
				}
			}
		}
	}
	secs := int64(freq / time.Second)
	cfg.ValidationFrequencySeconds = &secs
	cfg.flagsSet = true
}

// BackupsDisabled is absent when the effective flags could not be established.
func (c ServerConfig) MarshalJSON() ([]byte, error) {
	type config ServerConfig
	var disabled *bool
	if c.FlagsKnown() {
		disabled = &c.BackupsDisabled
	}
	return json.Marshal(struct {
		config
		BackupsDisabled *bool `json:"backupsDisabled,omitempty"`
	}{config: config(c), BackupsDisabled: disabled})
}
