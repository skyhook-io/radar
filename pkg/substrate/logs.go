package substrate

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Log sources. containers and lifecycle are kubectl ate logs actor's
// --source: container output carries the container's name in its labels;
// the lifecycle events around a start, checkpoint, restore or termination
// carry none. worker is Radar's addition: the worker process's own records
// about the actor ("Killing a sandbox over its memory limit", "Sandbox
// died"), which name the actor with flat keys rather than labels, so
// kubectl ate does not show them. all is every one of them.
const (
	LogSourceAll        = "all"
	LogSourceContainers = "containers"
	LogSourceLifecycle  = "lifecycle"
	LogSourceWorker     = "worker"
)

// Line kinds, which source a selected line belongs to.
const (
	LogKindContainer = "container"
	LogKindLifecycle = "lifecycle"
	LogKindWorker    = "worker"
)

// Substrate's log attribute keys (its internal/ateattr).
const (
	logAtespaceKey      = "ate.atespace"
	logActorNameKey     = "ate.actor.name"
	logContainerNameKey = "ate.actor.container.name"
	logActorUIDKey      = "ate.actor.uid"
	// logPlainUIDKey is how gVisor's ateworker names the actor in "Sandbox
	// died" and its teardown records.
	logPlainUIDKey    = "actorUID"
	logReservedPrefix = "ate."
	// memoryKillMessage is ateworker's record of a memory-limit kill, with
	// the limit and the usage that crossed it.
	memoryKillMessage = "Killing a sandbox over its memory limit"
	logMemoryLimitKey = "memoryLimitBytes"
	logMemoryUsageKey = "ate.stats.memory.usage"
	// sandboxContainer is runsc's ID for the gVisor sandbox itself, whose
	// output is not the actor's own unless asked for by name.
	sandboxContainer = "_sandbox"
)

var containerKeyQuoted = `"` + logContainerNameKey + `"`

var logLabelKeys = []string{"logging.googleapis.com/labels", "labels"}

// workerIdentityKeys are the flat keys a worker record names its actor by
// (ateattr.ActorLogAttrs, and ateworker's actorUID), removed from its text as
// the ate.* labels are from a container line's.
var workerIdentityKeys = []string{logAtespaceKey, logActorNameKey, logActorUIDKey, "ate.template.atespace", "ate.template.name", logPlainUIDKey}

// LogFilter selects one actor's lines from its worker Pod's log, exactly as
// kubectl ate logs actor does.
type LogFilter struct {
	Target    Ref
	Source    string
	Container string
	// UID is the actor's UID, which worker records name it by. Without it,
	// only worker records naming the actor by atespace and name are kept.
	UID string
}

// NewLogFilter validates source and container; lifecycle events come from no
// container, so the two cannot be combined.
func NewLogFilter(target Ref, source, container string) (LogFilter, error) {
	if source == "" {
		source = LogSourceAll
	}
	switch source {
	case LogSourceAll, LogSourceContainers:
	case LogSourceLifecycle, LogSourceWorker:
		if container != "" {
			return LogFilter{}, errors.New("container cannot be combined with source=" + source + ": those lines are not emitted by a container")
		}
	default:
		return LogFilter{}, errors.New("source must be all, containers, lifecycle or worker")
	}
	return LogFilter{Target: target, Source: source, Container: container}, nil
}

func (f LogFilter) matches(emitter Ref, container string) bool {
	// Actor names are unique only within an atespace, and a worker Pod hosts
	// actors of several atespaces over time, so both must match.
	if emitter != f.Target || f.Target.Atespace == "" || f.Target.Name == "" {
		return false
	}
	switch f.Source {
	case LogSourceLifecycle:
		return container == ""
	case LogSourceContainers:
		if container == "" {
			return false
		}
	}
	if f.Container == "" {
		return container != sandboxContainer
	}
	return container == f.Container
}

// workerLine reports whether a record with no label envelope is the worker's
// own record about the target: it names the target's UID, or, with no UID
// at all, the target's atespace and name. A record naming another UID is
// another actor's, even under the same name (a recreated actor).
func (f LogFilter) workerLine(m map[string]any) bool {
	for _, key := range []string{logActorUIDKey, logPlainUIDKey} {
		if uid, ok := m[key].(string); ok && uid != "" {
			return f.UID != "" && uid == f.UID
		}
	}
	atespace, _ := m[logAtespaceKey].(string)
	name, _ := m[logActorNameKey].(string)
	return atespace != "" && name != "" && atespace == f.Target.Atespace && name == f.Target.Name
}

// LogLine is one selected line.
type LogLine struct {
	// Kind is container, lifecycle or worker.
	Kind string `json:"kind"`
	// Time is the record's own time field when it has one, else the
	// kubelet's timestamp; absent when neither parses.
	Time time.Time `json:"time,omitzero"`
	// Container is the emitting container; absent for a lifecycle event.
	Container string `json:"container,omitempty"`
	Severity  string `json:"severity,omitempty"`
	// Message is the record's message or msg field, when it is a string.
	Message string `json:"message,omitempty"`
	// Text is the record as kubectl ate prints it: Substrate's ate.* labels
	// (a worker record's flat identity keys) removed and time first.
	Text string `json:"text"`
	// MemoryLimitBytes and MemoryUsageBytes are set on a worker's
	// memory-limit kill: the limit and the usage that crossed it.
	MemoryLimitBytes *int64 `json:"memoryLimitBytes,omitempty"`
	MemoryUsageBytes *int64 `json:"memoryUsageBytes,omitempty"`
}

// SplitKubeletTimestamp splits the RFC 3339 timestamp the kubelet prefixes
// when timestamps are requested; ok is false for a line without one.
func SplitKubeletTimestamp(line string) (time.Time, string, bool) {
	stampText, rest, found := strings.Cut(line, " ")
	if !found {
		return time.Time{}, line, false
	}
	stamp, err := time.Parse(time.RFC3339Nano, stampText)
	if err != nil {
		return time.Time{}, line, false
	}
	return stamp, rest, true
}

// FilterLine returns the line if the filter selects it. kubeletTime is the
// kubelet's stamp for the line, zero when there was none.
func FilterLine(line string, f LogFilter, kubeletTime time.Time) (LogLine, bool) {
	// Under lifecycle nearly every line is a container's; skip them before
	// decoding. An actor cannot forge the key: Substrate strips reserved keys
	// from an actor's own labels.
	if f.Source == LogSourceLifecycle && strings.Contains(line, containerKeyQuoted) {
		return LogLine{}, false
	}
	var m map[string]any
	dec := json.NewDecoder(strings.NewReader(line))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return LogLine{}, false
	}

	var emitter Ref
	var container string
	for _, key := range logLabelKeys {
		labels, ok := m[key].(map[string]any)
		if !ok {
			continue
		}
		if name, ok := labels[logActorNameKey].(string); ok && name != "" {
			emitter.Name = name
			emitter.Atespace, _ = labels[logAtespaceKey].(string)
			container, _ = labels[logContainerNameKey].(string)
			break
		}
	}
	kind := LogKindContainer
	switch {
	case emitter.Name != "":
		if f.Source == LogSourceWorker || !f.matches(emitter, container) {
			return LogLine{}, false
		}
		if container == "" {
			kind = LogKindLifecycle
		}
	case (f.Source == LogSourceAll || f.Source == LogSourceWorker) && f.Container == "" && f.workerLine(m):
		kind = LogKindWorker
		for _, key := range workerIdentityKeys {
			delete(m, key)
		}
	default:
		return LogLine{}, false
	}

	// Strip the whole reserved prefix, not the known keys, so an actor cannot
	// have an ate.* label of its own shown as platform attribution.
	for _, key := range logLabelKeys {
		labels, ok := m[key].(map[string]any)
		if !ok {
			continue
		}
		for k := range labels {
			if strings.HasPrefix(k, logReservedPrefix) {
				delete(labels, k)
			}
		}
		if len(labels) == 0 {
			delete(m, key)
		}
	}

	out := LogLine{Kind: kind, Container: container, Time: kubeletTime}
	if kind == LogKindWorker {
		if msg, _ := m["msg"].(string); msg == memoryKillMessage {
			out.MemoryLimitBytes, out.MemoryUsageBytes = jsonInt(m[logMemoryLimitKey]), jsonInt(m[logMemoryUsageKey])
		}
	}
	timeVal, hasTime := m["time"]
	if hasTime {
		delete(m, "time")
		if s, ok := timeVal.(string); ok {
			if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
				out.Time = t
			}
		}
	}
	for _, key := range []string{"message", "msg"} {
		if s, ok := m[key].(string); ok {
			out.Message = s
			break
		}
	}
	for _, key := range []string{"severity", "level"} {
		if s, ok := m[key].(string); ok {
			out.Severity = s
			break
		}
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return LogLine{}, false
	}
	encoded := strings.TrimSpace(buf.String())
	if hasTime {
		timeJSON, _ := json.Marshal(timeVal)
		switch {
		case encoded == "{}":
			encoded = `{"time":` + string(timeJSON) + `}`
		case strings.HasPrefix(encoded, "{"):
			encoded = `{"time":` + string(timeJSON) + "," + encoded[1:]
		}
	}
	out.Text = encoded
	return out, true
}

func jsonInt(v any) *int64 {
	n, ok := v.(json.Number)
	if !ok {
		return nil
	}
	i, err := n.Int64()
	if err != nil {
		return nil
	}
	return &i
}
