package prometheus

import (
	"context"
	"errors"
	"strings"
)

// AvailabilityStatus is the coarse answer to "can Radar reach Prometheus
// right now", for callers that decide whether to run metrics work at all
// (the investigation runner's prompt, the diagnose tool's vitals) rather
// than callers that need a client.
type AvailabilityStatus string

const (
	// AvailabilityConnected means a probe just succeeded against Address.
	AvailabilityConnected AvailabilityStatus = "connected"
	// AvailabilityConfiguredFailed means an endpoint is known (a manual URL,
	// or an address discovery previously connected to, in Address) or
	// discovery enumerated a candidate, but it could not be reached; Err says
	// why, including a caller deadline that cut the probe short.
	AvailabilityConfiguredFailed AvailabilityStatus = "configured_failed"
	// AvailabilityAbsent means there is nothing to reach: no client, no
	// Kubernetes client to discover with, or discovery found no Prometheus.
	// It is also the answer when the caller's context ended before discovery
	// produced an endpoint and none is known, with Err carrying the context
	// error so a timeout can be told from a definite "none".
	AvailabilityAbsent AvailabilityStatus = "absent"
)

// AvailabilityState is the result of Availability.
type AvailabilityState struct {
	State   AvailabilityStatus
	Address string
	Err     error
}

// Availability probes the process-wide Prometheus client under ctx, which
// bounds how long the call may block. It starts no work beyond what
// EnsureConnected already does: a discovery that outlives ctx keeps running
// detached under its own timeout so the next caller benefits from it.
func Availability(ctx context.Context) AvailabilityState {
	c := GetClient()
	if c == nil {
		return AvailabilityState{State: AvailabilityAbsent}
	}
	return c.Availability(ctx)
}

// Availability is the per-client form of the package-level Availability.
func (c *Client) Availability(ctx context.Context) AvailabilityState {
	c.mu.RLock()
	knownURL := c.baseURL
	c.mu.RUnlock()

	addr, _, err := c.EnsureConnected(ctx)
	if err == nil {
		return AvailabilityState{State: AvailabilityConnected, Address: addr}
	}

	c.mu.RLock()
	manualURL := c.manualURL
	baseURL := c.baseURL
	hasK8s := c.k8sClient != nil
	c.mu.RUnlock()
	if baseURL == "" && knownURL != "" && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		// The endpoint this call started with failed its probe and the caller
		// ran out of time while rediscovery was still in flight: the endpoint
		// was known, and only this call could not confirm it.
		baseURL = knownURL
	}

	switch {
	case manualURL != "":
		return AvailabilityState{
			State:   AvailabilityConfiguredFailed,
			Address: strings.TrimRight(manualURL, "/"),
			Err:     err,
		}
	case baseURL != "":
		// EnsureConnected keeps a previously discovered endpoint when the
		// caller's context ends mid-probe: the endpoint is known and only
		// this probe failed.
		return AvailabilityState{State: AvailabilityConfiguredFailed, Address: baseURL, Err: err}
	case errors.Is(err, errPrometheusUnreachable):
		return AvailabilityState{State: AvailabilityConfiguredFailed, Err: err}
	case !hasK8s,
		errors.Is(err, ErrPrometheusNotFound),
		errors.Is(err, context.Canceled),
		errors.Is(err, context.DeadlineExceeded):
		return AvailabilityState{State: AvailabilityAbsent, Err: err}
	default:
		// Discovery enumerated a candidate and could not reach it (port-forward
		// or probe failure), which is the "configured but failing" case for an
		// auto-discovered install.
		return AvailabilityState{State: AvailabilityConfiguredFailed, Err: err}
	}
}
