package substrate

import (
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/substrate/ateapi"
)

func TestTemplateSpecRedactsSecrets(t *testing.T) {
	spec := TemplateSpecOf(&ateapi.ActorTemplate{
		Metadata: &ateapi.ResourceMetadata{Atespace: "a", Name: "t"},
		Containers: []*ateapi.Container{{
			Name: "agent", Image: "ghcr.io/example/agent:1.2",
			Args: []string{"--model", "large", "--key=Bearer abcdefghijklmnopqrstuvwxyz0123456789"},
			Env: []*ateapi.EnvVar{
				{Name: "OPENAI_API_KEY", Value: "sk-live-0123456789abcdef"},
				{Name: "LOG_LEVEL", Value: "debug"},
				{Name: "UPSTREAM", Value: "https://user:hunter2@example.com/path"},
			},
			VolumeMounts: []*ateapi.VolumeMount{{Name: "work", MountPath: "/work"}},
		}},
		Volumes: []*ateapi.Volume{{Name: "work", DurableDir: &ateapi.DurableDirVolumeSource{}}},
		SnapshotConfig: &ateapi.SnapshotConfig{
			PreferredFidelity:    ateapi.SnapshotFidelity_SNAPSHOT_FIDELITY_ROOTFS,
			GoldenSnapshotConfig: &ateapi.GoldenSnapshotConfig{Mode: ateapi.GoldenSnapshotMode_GOLDEN_SNAPSHOT_MODE_DISABLED},
		},
	})
	c := spec.Containers[0]
	if c.Image != "ghcr.io/example/agent:1.2" || c.VolumeMounts[0].MountPath != "/work" || spec.Volumes[0].Kind != "durableDir" {
		t.Errorf("spec = %+v", spec)
	}
	env := map[string]EnvSpec{}
	for _, e := range c.Env {
		env[e.Name] = e
	}
	if e := env["OPENAI_API_KEY"]; e.Value != redacted || !e.Redacted {
		t.Errorf("a sensitive name hides its value: %+v", e)
	}
	if e := env["LOG_LEVEL"]; e.Value != "debug" || e.Redacted {
		t.Errorf("an ordinary value is shown: %+v", e)
	}
	if e := env["UPSTREAM"]; strings.Contains(e.Value, "hunter2") || !e.Redacted {
		t.Errorf("a credential inside a value is redacted: %+v", e)
	}
	if strings.Contains(strings.Join(c.Args, " "), "abcdefghijklmnopqrstuvwxyz0123456789") {
		t.Errorf("a bearer token in args is redacted: %v", c.Args)
	}
	if spec.Snapshot.Golden != "disabled" || spec.Snapshot.PreferredFidelity != "rootfs" {
		t.Errorf("snapshot = %+v", spec.Snapshot)
	}
}
