package igdebug

import (
	"encoding/json"
	"time"
)

type Aspect string

const (
	AspectProcesses   Aspect = "processes"
	AspectConnections Aspect = "connections"
	AspectDNS         Aspect = "dns"
)

func (a Aspect) Valid() bool {
	return a == AspectProcesses || a == AspectConnections || a == AspectDNS
}

type State string

const (
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateStopping State = "stopping"
	StateComplete State = "complete"
	StateStopped  State = "stopped"
	StateFailed   State = "failed"
	StatePartial  State = "partial"
)

func (s State) Terminal() bool {
	return s == StateComplete || s == StateStopped || s == StateFailed || s == StatePartial
}

type Owner struct {
	User    string `json:"-"`
	Context string `json:"-"`
}

type Target struct {
	Namespace   string `json:"namespace"`
	Pod         string `json:"pod"`
	PodUID      string `json:"podUid,omitempty"`
	Node        string `json:"node,omitempty"`
	Container   string `json:"container"`
	ContainerID string `json:"containerId,omitempty"`
}

type Request struct {
	Owner    Owner         `json:"-"`
	Target   Target        `json:"target"`
	Aspect   Aspect        `json:"aspect"`
	Duration time.Duration `json:"-"`
}

type Process struct {
	Command       string `json:"command"`
	PID           int64  `json:"pid"`
	ParentPID     int64  `json:"parentPid,omitempty"`
	ParentCommand string `json:"parentCommand,omitempty"`
	User          string `json:"user,omitempty"`
	UID           int64  `json:"uid,omitempty"`
}

type EndpointRef struct {
	Kind      string `json:"kind,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
}

type Endpoint struct {
	Address  string       `json:"address"`
	Port     uint16       `json:"port,omitempty"`
	Protocol string       `json:"protocol,omitempty"`
	Resource *EndpointRef `json:"resource,omitempty"`
}

type Connection struct {
	Protocol string   `json:"protocol"`
	State    string   `json:"state"`
	Local    Endpoint `json:"local"`
	Remote   Endpoint `json:"remote"`
}

type DNSGroup struct {
	Name             string   `json:"name"`
	Type             string   `json:"type"`
	ResponseCode     string   `json:"responseCode"`
	Count            int      `json:"count"`
	Addresses        []string `json:"addresses,omitempty"`
	AverageLatencyMs float64  `json:"averageLatencyMs,omitempty"`
	MaxLatencyMs     float64  `json:"maxLatencyMs,omitempty"`
	Service          string   `json:"service,omitempty"`
}

type Result struct {
	Aspect            Aspect        `json:"aspect"`
	Target            Target        `json:"target"`
	CapturedAt        time.Time     `json:"capturedAt"`
	CaptureDuration   time.Duration `json:"-"`
	CaptureDurationMS int64         `json:"captureDurationMs"`
	EventCount        int           `json:"eventCount"`
	Truncated         bool          `json:"truncated"`
	EventLoss         string        `json:"eventLoss"`
	UnmatchedQueries  int           `json:"unmatchedQueries,omitempty"`
	Processes         []Process     `json:"processes,omitempty"`
	Connections       []Connection  `json:"connections,omitempty"`
	DNS               []DNSGroup    `json:"dns,omitempty"`
}

type Run struct {
	ID        string            `json:"id"`
	Aspect    Aspect            `json:"aspect"`
	Target    Target            `json:"target"`
	State     State             `json:"state"`
	StartedAt time.Time         `json:"startedAt"`
	UpdatedAt time.Time         `json:"updatedAt"`
	Result    *Result           `json:"result,omitempty"`
	Error     string            `json:"error,omitempty"`
	Reason    string            `json:"reason,omitempty"`
	Events    []json.RawMessage `json:"-"`
}

type StreamEvent struct {
	Type string          `json:"type"`
	Run  *Run            `json:"run,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}
