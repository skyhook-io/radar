package server

// ReadSource is the outcome of one read a response depends on. Grant names
// what a denied read needs; Reason explains any other outcome. The State
// vocabulary belongs to the caller: sources classify the same failure
// differently on purpose (a timed-out HA read is unavailable, a timed-out
// recovery read is an error).
type ReadSource struct {
	State  string `json:"state"`
	Grant  *Grant `json:"grant,omitempty"`
	Reason string `json:"reason,omitempty"`
}
