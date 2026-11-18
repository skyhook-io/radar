package substrate

import (
	"maps"
	"time"

	aicontext "github.com/skyhook-io/radar/pkg/ai/context"

	"github.com/skyhook-io/radar/internal/substrate/ateapi"
)

// TemplateSpec is a template's definition as the template page shows it,
// read only. Secret-shaped values are redacted as Radar's AI context redacts
// them: an env value under a sensitive name entirely, any other value,
// command or argument by its secret patterns.
type TemplateSpec struct {
	Containers []ContainerSpec `json:"containers"`
	Volumes    []VolumeSpec    `json:"volumes"`
	// WorkerSelector is absent when every pool is eligible.
	WorkerSelector map[string]string `json:"workerSelector,omitempty"`
	CPUMillicores  int64             `json:"cpuMillicores,omitempty"`
	MemoryBytes    int64             `json:"memoryBytes,omitempty"`
	Snapshot       SnapshotSpec      `json:"snapshot"`
	// GoldenEgressRules counts the rules of the policy the golden actor runs
	// under during warm-up; absent when the template sets none.
	GoldenEgressRules int       `json:"goldenEgressRules,omitempty"`
	CreateTime        time.Time `json:"createTime,omitzero"`
}

// ContainerSpec is one container of a template.
type ContainerSpec struct {
	Name         string          `json:"name"`
	Image        string          `json:"image"`
	Command      []string        `json:"command,omitempty"`
	Args         []string        `json:"args,omitempty"`
	Env          []EnvSpec       `json:"env,omitempty"`
	VolumeMounts []VolumeMountTo `json:"volumeMounts,omitempty"`
	// WakeupProbe is the HTTP path Substrate probes after a resume; absent
	// when the container has no probe.
	WakeupProbe string `json:"wakeupProbe,omitempty"`
}

// EnvSpec is one environment variable; Redacted says its value was hidden.
type EnvSpec struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Redacted bool   `json:"redacted,omitempty"`
}

type VolumeMountTo struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"`
}

// VolumeSpec is one template volume and its kind: durableDir, external,
// systemInfo or image.
type VolumeSpec struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Image is the OCI image an image volume mounts.
	Image string `json:"image,omitempty"`
}

// SnapshotSpec is the template's snapshot configuration.
type SnapshotSpec struct {
	PreferredFidelity string `json:"preferredFidelity"`
	// StorageLocation is absent when the API server's default applies.
	StorageLocation string `json:"storageLocation,omitempty"`
	// Golden is enabled or disabled.
	Golden string `json:"golden"`
}

const redacted = "[REDACTED]"

// TemplateSpecOf reads a template's spec for display.
func TemplateSpecOf(t *ateapi.ActorTemplate) TemplateSpec {
	m := TemplateOf(t)
	spec := TemplateSpec{
		Containers:     []ContainerSpec{},
		Volumes:        []VolumeSpec{},
		WorkerSelector: maps.Clone(m.WorkerSelector),
		CPUMillicores:  m.CPUMillicores,
		MemoryBytes:    m.MemoryBytes,
		Snapshot:       SnapshotSpec{PreferredFidelity: m.PreferredFidelity, StorageLocation: m.StorageLocation, Golden: "enabled"},
		CreateTime:     m.CreateTime,
	}
	if m.GoldenDisabled {
		spec.Snapshot.Golden = "disabled"
	}
	spec.GoldenEgressRules = len(t.GetGoldenEgressPolicy().GetRules())
	for _, c := range t.GetContainers() {
		cs := ContainerSpec{Name: c.GetName(), Image: c.GetImage(), Command: redactAll(c.GetCommand()), Args: redactAll(c.GetArgs()),
			WakeupProbe: c.GetWakeupProbe().GetHttpGet().GetPath()}
		for _, e := range c.GetEnv() {
			env := EnvSpec{Name: e.GetName()}
			switch {
			case aicontext.IsSensitiveEnvName(e.GetName()):
				env.Value, env.Redacted = redacted, e.GetValue() != ""
			default:
				env.Value = aicontext.RedactSecrets(e.GetValue())
				env.Redacted = env.Value != e.GetValue()
			}
			cs.Env = append(cs.Env, env)
		}
		for _, vm := range c.GetVolumeMounts() {
			cs.VolumeMounts = append(cs.VolumeMounts, VolumeMountTo{Name: vm.GetName(), MountPath: vm.GetMountPath()})
		}
		spec.Containers = append(spec.Containers, cs)
	}
	for _, v := range t.GetVolumes() {
		vs := VolumeSpec{Name: v.GetName()}
		switch {
		case v.GetDurableDir() != nil:
			vs.Kind = "durableDir"
		case v.GetExternal() != nil:
			vs.Kind = "external"
		case v.GetSystemInfo() != nil:
			vs.Kind = "systemInfo"
		case v.GetImage() != nil:
			vs.Kind, vs.Image = "image", v.GetImage().GetReference()
		}
		spec.Volumes = append(spec.Volumes, vs)
	}
	return spec
}

func redactAll(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = aicontext.RedactSecrets(s)
	}
	return out
}
