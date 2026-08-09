// Package autoscalerstatus parses the cluster-autoscaler status ConfigMap
// (kube-system/cluster-autoscaler-status) published by the Kubernetes Cluster
// Autoscaler — including the GKE- and AKS-managed autoscalers, which publish
// the same ConfigMap. It is the only cloud-API-free, in-cluster source of
// node-group bounds (min/max/target), autoscaler health, and backoff state.
//
// Two formats exist: structured YAML (cluster-autoscaler >= 1.30) and the
// legacy human-readable text emitted by older releases. Parsing is
// best-effort by design — a field a payload does not carry stays nil, and a
// published-but-null timestamp (live GKE payloads publish explicit
// `lastProbeTime: null` for inactive groups) also stays nil. Nil is "not
// published", never zero.
package autoscalerstatus

import "time"

type Format string

const (
	FormatStructured Format = "structured"
	FormatLegacyText Format = "legacy_text"
)

type Status struct {
	Format Format
	// AutoscalerState is the top-level autoscalerStatus field ("Running",
	// "Initializing"); structured format only.
	AutoscalerState string
	// Time is the payload's own timestamp — freshness renders as "as of T",
	// never as broken: healthy quiet clusters publish hours-old payloads.
	Time        *time.Time
	ClusterWide ClusterWide
	NodeGroups  []NodeGroup
}

type ClusterWide struct {
	Health    Health
	ScaleUp   Condition
	ScaleDown Condition
}

type NodeGroup struct {
	// Name as published — on GKE a full MIG URL, elsewhere a bare group name.
	Name string
	// Basename is the last path segment of Name: the stable child identity.
	// It is never parsed for a logical pool name — provider name truncation
	// makes that unreliable; logical identity comes from node labels.
	Basename  string
	MinSize   *int
	MaxSize   *int
	Target    *int
	Health    Health
	ScaleUp   Condition
	ScaleDown Condition
}

type Health struct {
	// Status as published: "Healthy", "Unhealthy", or "" when not stated.
	Status           string
	Ready            *int
	Unready          *int
	NotStarted       *int
	Registered       *int
	LongUnregistered *int
	Unregistered     *int
	LastProbeTime    *time.Time
	LastTransition   *time.Time
}

type Condition struct {
	// Status as published: "NoActivity", "InProgress", "NoCandidates",
	// "CandidatesPresent", "Backoff", or "" when not stated.
	Status         string
	Backoff        *Backoff
	LastProbeTime  *time.Time
	LastTransition *time.Time
}

type Backoff struct {
	ErrorClass   string
	ErrorCode    string
	ErrorMessage string
}
