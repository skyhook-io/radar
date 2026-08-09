package autoscalerstatus

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

// ErrUnrecognizedFormat is returned when a payload is neither the structured
// YAML status (>= 1.30) nor the legacy human-readable text.
var ErrUnrecognizedFormat = errors.New("unrecognized cluster-autoscaler status format")

// Parse decodes the cluster-autoscaler status ConfigMap's data.status value.
// It auto-detects the structured YAML format (>= 1.30) and the legacy text
// format (pre-1.30). Parsing is best-effort: any field a payload does not
// carry stays nil.
func Parse(raw string) (Status, error) {
	if strings.TrimSpace(raw) == "" {
		return Status{}, ErrUnrecognizedFormat
	}

	var probe map[string]any
	if err := yaml.Unmarshal([]byte(raw), &probe); err == nil && probe != nil {
		if _, ok := probe["autoscalerStatus"]; ok {
			return parseStructured(raw)
		}
		if _, ok := probe["nodeGroups"]; ok {
			return parseStructured(raw)
		}
	}

	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "Cluster-autoscaler status") {
		return parseLegacyText(raw), nil
	}
	// Before the legacy autoscaler has its first health reading it writes the
	// bare word into data.status; treating that as unrecognized would report a
	// parse error for a perfectly healthy starting controller.
	if trimmed == legacyInitializing {
		return Status{Format: FormatLegacyText, AutoscalerState: legacyInitializing}, nil
	}

	return Status{}, ErrUnrecognizedFormat
}

const legacyInitializing = "Initializing"

// --- structured YAML (cluster-autoscaler >= 1.30) ---

// The struct tags are JSON: sigs.k8s.io/yaml routes YAML through JSON before
// unmarshaling.
type rawStatus struct {
	AutoscalerStatus string         `json:"autoscalerStatus"`
	Time             *string        `json:"time"`
	ClusterWide      rawClusterWide `json:"clusterWide"`
	NodeGroups       []rawNodeGroup `json:"nodeGroups"`
}

type rawClusterWide struct {
	Health    rawHealth    `json:"health"`
	ScaleUp   rawCondition `json:"scaleUp"`
	ScaleDown rawCondition `json:"scaleDown"`
}

type rawNodeGroup struct {
	Name      string       `json:"name"`
	Health    rawHealth    `json:"health"`
	ScaleUp   rawCondition `json:"scaleUp"`
	ScaleDown rawCondition `json:"scaleDown"`
}

type rawHealth struct {
	Status              string        `json:"status"`
	MinSize             *int          `json:"minSize"`
	MaxSize             *int          `json:"maxSize"`
	CloudProviderTarget *int          `json:"cloudProviderTarget"`
	NodeCounts          rawNodeCounts `json:"nodeCounts"`
	LastProbeTime       *string       `json:"lastProbeTime"`
	LastTransitionTime  *string       `json:"lastTransitionTime"`
}

type rawNodeCounts struct {
	LongUnregistered *int          `json:"longUnregistered"`
	Unregistered     *int          `json:"unregistered"`
	Registered       rawRegistered `json:"registered"`
}

type rawRegistered struct {
	NotStarted *int       `json:"notStarted"`
	Ready      *int       `json:"ready"`
	Total      *int       `json:"total"`
	Unready    rawUnready `json:"unready"`
}

type rawUnready struct {
	Total *int `json:"total"`
}

type rawCondition struct {
	Status             string         `json:"status"`
	BackoffInfo        map[string]any `json:"backoffInfo"`
	LastProbeTime      *string        `json:"lastProbeTime"`
	LastTransitionTime *string        `json:"lastTransitionTime"`
}

func parseStructured(raw string) (Status, error) {
	var rs rawStatus
	if err := yaml.Unmarshal([]byte(raw), &rs); err != nil {
		return Status{}, err
	}

	st := Status{
		Format:          FormatStructured,
		AutoscalerState: rs.AutoscalerStatus,
		Time:            parseTimePtr(rs.Time),
		ClusterWide: ClusterWide{
			Health:    mapHealth(rs.ClusterWide.Health),
			ScaleUp:   mapCondition(rs.ClusterWide.ScaleUp),
			ScaleDown: mapCondition(rs.ClusterWide.ScaleDown),
		},
	}
	for _, rng := range rs.NodeGroups {
		st.NodeGroups = append(st.NodeGroups, NodeGroup{
			Name:      rng.Name,
			Basename:  basename(rng.Name),
			MinSize:   rng.Health.MinSize,
			MaxSize:   rng.Health.MaxSize,
			Target:    rng.Health.CloudProviderTarget,
			Health:    mapHealth(rng.Health),
			ScaleUp:   mapCondition(rng.ScaleUp),
			ScaleDown: mapCondition(rng.ScaleDown),
		})
	}
	return st, nil
}

func mapHealth(rh rawHealth) Health {
	reg := rh.NodeCounts.Registered
	return Health{
		Status:           rh.Status,
		Ready:            reg.Ready,
		Unready:          reg.Unready.Total,
		NotStarted:       reg.NotStarted,
		Registered:       reg.Total,
		LongUnregistered: rh.NodeCounts.LongUnregistered,
		Unregistered:     rh.NodeCounts.Unregistered,
		LastProbeTime:    parseTimePtr(rh.LastProbeTime),
		LastTransition:   parseTimePtr(rh.LastTransitionTime),
	}
}

func mapCondition(rc rawCondition) Condition {
	return Condition{
		Status:         rc.Status,
		Backoff:        backoffFromMap(rc.BackoffInfo),
		LastProbeTime:  parseTimePtr(rc.LastProbeTime),
		LastTransition: parseTimePtr(rc.LastTransitionTime),
	}
}

// backoffFromMap keeps the honesty contract's empty-vs-populated distinction:
// an empty backoffInfo map ({}) means "no backoff" and stays nil; only a
// populated map yields a Backoff.
func backoffFromMap(m map[string]any) *Backoff {
	if len(m) == 0 {
		return nil
	}
	b := &Backoff{}
	if v, ok := m["errorClass"].(string); ok {
		b.ErrorClass = v
	}
	if v, ok := m["errorCode"].(string); ok {
		b.ErrorCode = v
	}
	if v, ok := m["errorMessage"].(string); ok {
		b.ErrorMessage = v
	}
	return b
}

// --- legacy human-readable text (cluster-autoscaler pre-1.30) ---

var legacyKVRe = regexp.MustCompile(`(\w+)=(\d+)`)

type legacyTarget int

const (
	tgtNone legacyTarget = iota
	tgtHealth
	tgtScaleUp
	tgtScaleDown
)

func parseLegacyText(raw string) Status {
	st := Status{Format: FormatLegacyText}
	lines := strings.Split(raw, "\n")

	for _, line := range lines {
		if t := parseLegacyHeaderTime(line); t != nil {
			st.Time = t
			break
		}
	}

	// Node groups are collected as pointers so LastProbeTime/LastTransitionTime
	// lines (which arrive after the condition line they belong to) can be
	// attached without a slice-reallocation invalidating the reference.
	var groups []*NodeGroup
	var cur *NodeGroup // nil while inside the cluster-wide section
	var target legacyTarget

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "Cluster-wide:", trimmed == "NodeGroups:":
			cur = nil
			target = tgtNone
		case strings.HasPrefix(trimmed, "Name:"):
			name := strings.TrimSpace(strings.TrimPrefix(trimmed, "Name:"))
			ng := &NodeGroup{Name: name, Basename: basename(name)}
			groups = append(groups, ng)
			cur = ng
			target = tgtNone
		case strings.HasPrefix(trimmed, "Health:"):
			target = tgtHealth
			rest := strings.TrimPrefix(trimmed, "Health:")
			h := legacyHealth(rest)
			if cur == nil {
				st.ClusterWide.Health = h
			} else {
				cur.Health = h
				kv := parseLegacyKV(rest)
				cur.MinSize = ptrFromMap(kv, "minSize")
				cur.MaxSize = ptrFromMap(kv, "maxSize")
				cur.Target = ptrFromMap(kv, "cloudProviderTarget")
			}
		case strings.HasPrefix(trimmed, "ScaleUp:"):
			target = tgtScaleUp
			c := Condition{Status: conditionStatus(strings.TrimPrefix(trimmed, "ScaleUp:"))}
			if cur == nil {
				st.ClusterWide.ScaleUp = c
			} else {
				cur.ScaleUp = c
			}
		case strings.HasPrefix(trimmed, "ScaleDown:"):
			target = tgtScaleDown
			c := Condition{Status: conditionStatus(strings.TrimPrefix(trimmed, "ScaleDown:"))}
			if cur == nil {
				st.ClusterWide.ScaleDown = c
			} else {
				cur.ScaleDown = c
			}
		case strings.HasPrefix(trimmed, "LastProbeTime:"):
			t := parseFlexibleTime(strings.TrimPrefix(trimmed, "LastProbeTime:"))
			assignLegacyTime(&st, cur, target, t, true)
		case strings.HasPrefix(trimmed, "LastTransitionTime:"):
			t := parseFlexibleTime(strings.TrimPrefix(trimmed, "LastTransitionTime:"))
			assignLegacyTime(&st, cur, target, t, false)
		}
	}

	for _, ng := range groups {
		st.NodeGroups = append(st.NodeGroups, *ng)
	}
	// The legacy format has no autoscalerStatus field, but a payload carrying
	// health sections is exactly what the controller writes once it is running.
	// Leaving the state blank would make every legacy cluster's manager rollup
	// "unknown" — the rollup reads AutoscalerState to reach "healthy".
	if st.ClusterWide.Health.Status != "" || len(st.NodeGroups) > 0 {
		st.AutoscalerState = "Running"
	}
	return st
}

func legacyHealth(rest string) Health {
	kv := parseLegacyKV(rest)
	return Health{
		Status:           conditionStatus(rest),
		Ready:            ptrFromMap(kv, "ready"),
		Unready:          ptrFromMap(kv, "unready"),
		NotStarted:       ptrFromMap(kv, "notStarted"),
		Registered:       ptrFromMap(kv, "registered"),
		LongUnregistered: ptrFromMap(kv, "longUnregistered"),
	}
}

func assignLegacyTime(st *Status, cur *NodeGroup, target legacyTarget, t *time.Time, isProbe bool) {
	if t == nil {
		return
	}
	var h *Health
	var c *Condition
	if cur == nil {
		switch target {
		case tgtHealth:
			h = &st.ClusterWide.Health
		case tgtScaleUp:
			c = &st.ClusterWide.ScaleUp
		case tgtScaleDown:
			c = &st.ClusterWide.ScaleDown
		}
	} else {
		switch target {
		case tgtHealth:
			h = &cur.Health
		case tgtScaleUp:
			c = &cur.ScaleUp
		case tgtScaleDown:
			c = &cur.ScaleDown
		}
	}
	switch {
	case h != nil && isProbe:
		h.LastProbeTime = t
	case h != nil:
		h.LastTransition = t
	case c != nil && isProbe:
		c.LastProbeTime = t
	case c != nil:
		c.LastTransition = t
	}
}

func parseLegacyHeaderTime(line string) *time.Time {
	line = strings.TrimSpace(line)
	const prefix = "Cluster-autoscaler status at "
	if !strings.HasPrefix(line, prefix) {
		return nil
	}
	ts := strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(line, prefix)), ":")
	return parseFlexibleTime(ts)
}

// conditionStatus returns the published status token (the first word) from a
// condition line body, dropping any trailing "(...)" message.
func conditionStatus(rest string) string {
	rest = strings.TrimSpace(rest)
	if i := strings.IndexAny(rest, " ("); i >= 0 {
		return strings.TrimSpace(rest[:i])
	}
	return rest
}

func parseLegacyKV(line string) map[string]int {
	out := map[string]int{}
	for _, m := range legacyKVRe.FindAllStringSubmatch(line, -1) {
		if n, err := strconv.Atoi(m[2]); err == nil {
			out[m[1]] = n
		}
	}
	return out
}

func ptrFromMap(m map[string]int, key string) *int {
	if v, ok := m[key]; ok {
		n := v
		return &n
	}
	return nil
}

// --- shared helpers ---

// basename is the last "/"-segment of a node-group name. It is deliberately
// never parsed into a logical pool name: provider names truncate.
func basename(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}

var timeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	// time.Time.String() output — how metav1.Time renders under %v, and the
	// top-level `time` field's own format.
	"2006-01-02 15:04:05.999999999 -0700 MST",
	"2006-01-02 15:04:05 -0700 MST",
}

func parseTimePtr(s *string) *time.Time {
	if s == nil {
		return nil
	}
	return parseFlexibleTime(*s)
}

// parseFlexibleTime returns nil for anything it cannot parse into a real
// timestamp — including the empty string, an explicit "null", and the zero
// time — so a missing or unset value never fabricates a moment.
func parseFlexibleTime(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" || s == "null" || s == "<nil>" {
		return nil
	}
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			if t.IsZero() {
				return nil
			}
			return &t
		}
	}
	return nil
}
