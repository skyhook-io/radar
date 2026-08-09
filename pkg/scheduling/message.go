package scheduling

import (
	"regexp"
	"strconv"
	"strings"
)

type ReasonClass string

const (
	InsufficientResource ReasonClass = "InsufficientResource"
	UntoleratedTaint     ReasonClass = "UntoleratedTaint"
	NodeAffinitySelector ReasonClass = "NodeAffinitySelector"
	PodAffinity          ReasonClass = "PodAffinity"
	PodAntiAffinity      ReasonClass = "PodAntiAffinity"
	TopologySpread       ReasonClass = "TopologySpread"
	VolumeNodeAffinity   ReasonClass = "VolumeNodeAffinity"
	VolumeBinding        ReasonClass = "VolumeBinding"
	VolumeCount          ReasonClass = "VolumeCount"
	NoPorts              ReasonClass = "NoPorts"
	NodeUnschedulable    ReasonClass = "NodeUnschedulable"
	Other                ReasonClass = "Other"
)

type Reason struct {
	Class      ReasonClass
	NodeCount  int
	Resource   string
	TaintKey   string
	TaintValue string
	Raw        string
}

var (
	nodesAvailablePattern = regexp.MustCompile(`(\d+)/(\d+)\s+nodes? are available`)
	leadingCountPattern   = regexp.MustCompile(`^\s*(\d+)\s+`)
	insufficientPattern   = regexp.MustCompile(`Insufficient\s+([A-Za-z0-9./_-]+)`)
	taintPattern          = regexp.MustCompile(`\{([^}]*)\}`)
)

func ParseMessage(message string) (totalNodes int, reasons []Reason) {
	message = strings.TrimSpace(message)
	if message == "" {
		return 0, nil
	}

	if before, _, ok := strings.Cut(message, ". preemption:"); ok {
		message = before
	} else if before, _, ok := strings.Cut(message, " preemption:"); ok {
		message = before
	}

	if match := nodesAvailablePattern.FindStringSubmatch(message); match != nil {
		totalNodes, _ = strconv.Atoi(match[2])
	}

	clauses := message
	if _, rest, ok := strings.Cut(message, ":"); ok {
		clauses = rest
	}
	clauses = strings.TrimRight(strings.TrimSpace(clauses), ".")
	if clauses == "" {
		return totalNodes, nil
	}

	for clause := range strings.SplitSeq(clauses, ", ") {
		clause = NormalizeClause(clause)
		if clause == "" {
			continue
		}
		if reason, ok := classifyClause(clause); ok {
			reasons = append(reasons, reason)
		}
	}
	return totalNodes, reasons
}

func NormalizeClause(clause string) string {
	return strings.TrimSpace(strings.TrimRight(strings.TrimSpace(clause), ",."))
}

func ParseTaint(clause string) (key, value string) {
	match := taintPattern.FindStringSubmatch(clause)
	if match == nil {
		return "", ""
	}
	inner := strings.TrimSpace(match[1])
	if inner == "" {
		return "", ""
	}
	if key, value, ok := strings.Cut(inner, ":"); ok {
		return strings.TrimSpace(key), strings.TrimSpace(value)
	}
	return inner, ""
}

func IsNodeLifecycleTaint(key string) bool {
	return strings.HasPrefix(key, "node.kubernetes.io/") ||
		strings.HasPrefix(key, "node-role.kubernetes.io/") ||
		strings.HasPrefix(key, "node.cloudprovider.kubernetes.io/")
}

func IsIgnoredClause(clause string) bool {
	return strings.Contains(strings.ToLower(NormalizeClause(clause)), "no new claims to deallocate")
}

func classifyClause(clause string) (Reason, bool) {
	clause = NormalizeClause(clause)
	if clause == "" {
		return Reason{}, false
	}
	reason := Reason{Raw: clause}
	if match := leadingCountPattern.FindStringSubmatch(clause); match != nil {
		reason.NodeCount, _ = strconv.Atoi(match[1])
	}

	lower := strings.ToLower(clause)
	switch {
	case strings.Contains(clause, "Insufficient"):
		reason.Class = InsufficientResource
		if match := insufficientPattern.FindStringSubmatch(clause); match != nil {
			reason.Resource = match[1]
		}
	case strings.Contains(lower, "too many pods"):
		reason.Class = InsufficientResource
		reason.Resource = "pods"
	case strings.Contains(lower, "untolerated taint"):
		reason.Class = UntoleratedTaint
		reason.TaintKey, reason.TaintValue = ParseTaint(clause)
		if IsNodeLifecycleTaint(reason.TaintKey) {
			reason.Class = NodeUnschedulable
		}
	case strings.Contains(lower, "volume node affinity"):
		reason.Class = VolumeNodeAffinity
	case strings.Contains(lower, "anti-affinity"):
		reason.Class = PodAntiAffinity
	case strings.Contains(lower, "node affinity") || strings.Contains(lower, "node selector"):
		reason.Class = NodeAffinitySelector
	case strings.Contains(lower, "pod affinity"):
		reason.Class = PodAffinity
	case strings.Contains(lower, "topology spread"):
		reason.Class = TopologySpread
	case strings.Contains(lower, "max volume count"):
		reason.Class = VolumeCount
	case strings.Contains(lower, "free ports"):
		reason.Class = NoPorts
	case strings.Contains(lower, "unbound") && strings.Contains(lower, "persistentvolumeclaim"),
		strings.Contains(lower, "persistent volumes to bind"):
		reason.Class = VolumeBinding
	case IsIgnoredClause(clause):
		return Reason{}, false
	case strings.Contains(lower, "unschedulable"), strings.Contains(lower, "were not ready"):
		reason.Class = NodeUnschedulable
	default:
		reason.Class = Other
	}
	return reason, true
}
