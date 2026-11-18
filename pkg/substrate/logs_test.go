package substrate

import (
	"strings"
	"testing"
	"time"
)

const (
	containerLine = `{"time":"2026-11-17T15:48:03Z","severity":"INFO","message":"embedding batch 7/9","labels":{"ate.atespace":"research","ate.actor.name":"lit-0193","ate.actor.container.name":"agent","ate.actor.uid":"u1","team":"ml"}}`
	lifecycleLine = `{"time":"2026-11-17T15:48:12Z","severity":"WARNING","message":"container agent exited: OOMKilled","labels":{"ate.atespace":"research","ate.actor.name":"lit-0193"}}`
	otherActor    = `{"message":"not mine","labels":{"ate.atespace":"research","ate.actor.name":"lit-0194","ate.actor.container.name":"agent"}}`
	otherAtespace = `{"message":"same name, other atespace","labels":{"ate.atespace":"code","ate.actor.name":"lit-0193","ate.actor.container.name":"agent"}}`
	sandboxLine   = `{"message":"runsc","labels":{"ate.atespace":"research","ate.actor.name":"lit-0193","ate.actor.container.name":"_sandbox"}}`
	sidecarLine   = `{"message":"sidecar up","logging.googleapis.com/labels":{"ate.atespace":"research","ate.actor.name":"lit-0193","ate.actor.container.name":"proxy"}}`
	workerOwnLine = `{"message":"worker heartbeat"}`
	notJSON       = `plain text from the worker`
)

var target = Ref{Atespace: "research", Name: "lit-0193"}

func selected(t *testing.T, source, container string) []LogLine {
	t.Helper()
	f, err := NewLogFilter(target, source, container)
	if err != nil {
		t.Fatal(err)
	}
	var out []LogLine
	for _, l := range []string{containerLine, lifecycleLine, otherActor, otherAtespace, sandboxLine, sidecarLine, workerOwnLine, notJSON} {
		if got, ok := FilterLine(l, f, time.Time{}); ok {
			out = append(out, got)
		}
	}
	return out
}

func messages(lines []LogLine) []string {
	out := []string{}
	for _, l := range lines {
		out = append(out, l.Message)
	}
	return out
}

func TestLogFilterSources(t *testing.T) {
	tests := []struct {
		source, container string
		want              []string
	}{
		{"", "", []string{"embedding batch 7/9", "container agent exited: OOMKilled", "sidecar up"}},
		{LogSourceContainers, "", []string{"embedding batch 7/9", "sidecar up"}},
		{LogSourceLifecycle, "", []string{"container agent exited: OOMKilled"}},
		{LogSourceAll, "proxy", []string{"sidecar up"}},
		{LogSourceContainers, "_sandbox", []string{"runsc"}},
	}
	for _, tc := range tests {
		got := messages(selected(t, tc.source, tc.container))
		if len(got) != len(tc.want) {
			t.Errorf("source=%q container=%q: %v, want %v", tc.source, tc.container, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("source=%q container=%q: %v, want %v", tc.source, tc.container, got, tc.want)
			}
		}
	}
}

func TestLogFilterRejectsImpossibleCombinations(t *testing.T) {
	if _, err := NewLogFilter(target, LogSourceLifecycle, "agent"); err == nil {
		t.Error("lifecycle with a container can never match")
	}
	if _, err := NewLogFilter(target, "stderr", ""); err == nil {
		t.Error("unknown source accepted")
	}
}

func TestLogLineOutputMatchesKubectlAte(t *testing.T) {
	f, _ := NewLogFilter(target, "", "")
	got, ok := FilterLine(containerLine, f, time.Time{})
	if !ok {
		t.Fatal("container line not selected")
	}
	want := `{"time":"2026-11-17T15:48:03Z","labels":{"team":"ml"},"message":"embedding batch 7/9","severity":"INFO"}`
	if got.Text != want {
		t.Errorf("text =\n%s\nwant\n%s", got.Text, want)
	}
	if got.Container != "agent" || got.Severity != "INFO" || !got.Time.Equal(time.Date(2026, 11, 17, 15, 48, 3, 0, time.UTC)) {
		t.Errorf("line = %+v", got)
	}
	life, _ := FilterLine(lifecycleLine, f, time.Time{})
	if life.Container != "" || life.Text != `{"time":"2026-11-17T15:48:12Z","message":"container agent exited: OOMKilled","severity":"WARNING"}` {
		t.Errorf("lifecycle line = %+v; its label map empties and is dropped", life)
	}
}

func TestKubeletTimestampFallback(t *testing.T) {
	stamp, rest, ok := SplitKubeletTimestamp("2026-11-17T15:49:00.123456789Z " + `{"message":"x","labels":{"ate.atespace":"research","ate.actor.name":"lit-0193"}}`)
	if !ok {
		t.Fatal("stamp not split")
	}
	f, _ := NewLogFilter(target, "", "")
	got, ok := FilterLine(rest, f, stamp)
	if !ok || !got.Time.Equal(stamp) {
		t.Errorf("a record without time takes the kubelet's: %+v", got)
	}
	if _, rest, ok := SplitKubeletTimestamp("no stamp here"); ok || rest != "no stamp here" {
		t.Error("a line without a stamp is returned whole")
	}
}

// Upstream gVisor ateworker records, as its slog JSON handler writes them:
// flat identity keys (ateattr.ActorLogAttrs) or a plain actorUID.
const (
	memoryKill    = `{"time":"2026-11-17T15:48:11Z","level":"WARN","msg":"Killing a sandbox over its memory limit","ate.atespace":"research","ate.actor.name":"lit-0193","ate.actor.uid":"uid-0193","ate.template.atespace":"research","ate.template.name":"lang","memoryLimitBytes":2147483648,"ate.stats.memory.usage":2150000000}`
	sandboxDied   = `{"time":"2026-11-17T15:48:12Z","level":"WARN","msg":"Sandbox died","actorUID":"uid-0193","pid":4100}`
	otherUIDDied  = `{"time":"2026-11-17T15:48:12Z","level":"WARN","msg":"Sandbox died","actorUID":"uid-recreated","pid":4101}`
	beforeResolve = `{"time":"2026-11-17T15:48:01Z","level":"INFO","msg":"Resolving actor","ate.atespace":"research","ate.actor.name":"lit-0193"}`
)

func TestWorkerLinesByActorUID(t *testing.T) {
	read := func(source, container string) []LogLine {
		f, err := NewLogFilter(target, source, container)
		if err != nil {
			t.Fatal(err)
		}
		f.UID = "uid-0193"
		var out []LogLine
		for _, l := range []string{containerLine, lifecycleLine, memoryKill, sandboxDied, otherUIDDied, beforeResolve, workerOwnLine} {
			if got, ok := FilterLine(l, f, time.Time{}); ok {
				out = append(out, got)
			}
		}
		return out
	}
	kinds := func(lines []LogLine) []string {
		out := []string{}
		for _, l := range lines {
			out = append(out, l.Kind+":"+l.Message)
		}
		return out
	}
	worker := kinds(read(LogSourceWorker, ""))
	want := []string{"worker:Killing a sandbox over its memory limit", "worker:Sandbox died", "worker:Resolving actor"}
	if strings.Join(worker, "|") != strings.Join(want, "|") {
		t.Errorf("worker = %v, want %v (another UID's records are another actor's, even under the same name)", worker, want)
	}
	all := kinds(read(LogSourceAll, ""))
	if len(all) != 5 || all[0] != "container:embedding batch 7/9" || all[1] != "lifecycle:container agent exited: OOMKilled" {
		t.Errorf("all = %v", all)
	}
	for _, source := range []string{LogSourceContainers, LogSourceLifecycle} {
		for _, l := range read(source, "") {
			if l.Kind == LogKindWorker {
				t.Errorf("source %s must not include worker records: %+v", source, l)
			}
		}
	}
	if _, err := NewLogFilter(target, LogSourceWorker, "agent"); err == nil {
		t.Error("worker records come from no container")
	}

	kill := read(LogSourceWorker, "")[0]
	if kill.MemoryLimitBytes == nil || *kill.MemoryLimitBytes != 2147483648 || kill.MemoryUsageBytes == nil || *kill.MemoryUsageBytes != 2150000000 {
		t.Errorf("memory kill = %+v", kill)
	}
	if strings.Contains(kill.Text, "uid-0193") || strings.Contains(kill.Text, "ate.atespace") || !strings.Contains(kill.Text, "memoryLimitBytes") {
		t.Errorf("text drops the identity keys and keeps the rest: %s", kill.Text)
	}

	f, _ := NewLogFilter(target, LogSourceWorker, "")
	if _, ok := FilterLine(sandboxDied, f, time.Time{}); ok {
		t.Error("without the actor's UID a record that names only a UID cannot be attributed")
	}
}
