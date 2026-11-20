package resourcecontext

import "github.com/skyhook-io/radar/pkg/readreason"

// KueuePendingPosition is where Kueue's visibility API
// (visibility.kueue.x-k8s.io, localqueues/pendingworkloads) placed one
// pending Workload when Radar read it, as the caller. The positions are
// Kueue's own snapshot ordering of the queue: it includes Workloads Kueue
// already tried and could not admit, so it is not the order in which
// Workloads will be admitted, nor a start time.
//
// Radar never derives a position itself. Absent on a KueueScheduling means
// this projection carries no visibility result: the host may not read it,
// the Workload may be ineligible, or the result may have been discarded.
type KueuePendingPosition struct {
	Status KueuePendingPositionStatus `json:"status"`
	// AheadInLocalQueue and AheadInClusterQueue are set only when Status is
	// listed: how many Workloads Kueue orders before this one, which is
	// Kueue's own 0-based positionInLocalQueue and positionInClusterQueue.
	// The ClusterQueue count includes Workloads of every namespace; the
	// answer names none of them, and does not say which ClusterQueue Kueue
	// used.
	AheadInLocalQueue   *int32 `json:"aheadInLocalQueue,omitempty"`
	AheadInClusterQueue *int32 `json:"aheadInClusterQueue,omitempty"`
	// LocalQueue is the Workload's spec.queueName, omitted when empty,
	// including when no request was made.
	LocalQueue string `json:"localQueue,omitempty"`
	// Rows is how many pending Workloads Kueue returned for the LocalQueue;
	// set only for a successfully decoded answer. RowLimit is how many Radar
	// asked for, set with Rows; absence does not mean zero.
	Rows     *int `json:"rows,omitempty"`
	RowLimit int  `json:"rowLimit,omitempty"`
	// APIVersion is the supported visibility API version selected for the
	// lookup (e.g. visibility.kueue.x-k8s.io/v1beta2), even if the read could
	// not start. Omitted when none was selected.
	APIVersion string `json:"apiVersion,omitempty"`
	// ReadAt is when Radar made the read (RFC 3339), set whenever it made
	// one. Kueue's answer carries no timestamp of its own.
	ReadAt string `json:"readAt,omitempty"`
	// Code is set only when Status is unavailable.
	Code readreason.Code `json:"code,omitempty"`
	// Message explains unavailable, queue_unknown and unsupported.
	Message string `json:"message,omitempty"`
}

// Clone returns a deep copy; nil stays nil.
func (p *KueuePendingPosition) Clone() *KueuePendingPosition {
	if p == nil {
		return nil
	}
	out := *p
	if p.AheadInLocalQueue != nil {
		v := *p.AheadInLocalQueue
		out.AheadInLocalQueue = &v
	}
	if p.AheadInClusterQueue != nil {
		v := *p.AheadInClusterQueue
		out.AheadInClusterQueue = &v
	}
	if p.Rows != nil {
		v := *p.Rows
		out.Rows = &v
	}
	return &out
}

// KueuePendingPositionStatus is what Radar learned from one read.
type KueuePendingPositionStatus string

const (
	// KueuePositionListed: Kueue's answer for the LocalQueue includes this
	// Workload; the ahead counts are set.
	KueuePositionListed KueuePendingPositionStatus = "listed"
	// KueuePositionNotListed: Kueue returned fewer rows than Radar asked for
	// and none is this Workload. It does not establish that the Workload is
	// not pending: it may have been admitted or deactivated since, Kueue may
	// not have seen it yet, or Kueue may not know the queue's ClusterQueue.
	KueuePositionNotListed KueuePendingPositionStatus = "not_listed"
	// KueuePositionLimitReached: Kueue returned as many rows as Radar asked
	// for and none is this Workload. Its position is unknown.
	KueuePositionLimitReached KueuePendingPositionStatus = "limit_reached"
	// KueuePositionQueueUnknown: Kueue answered that it does not know the
	// Workload's LocalQueue.
	KueuePositionQueueUnknown KueuePendingPositionStatus = "queue_unknown"
	// KueuePositionNoLocalQueue: the Workload names no LocalQueue, so there
	// is nothing to ask.
	KueuePositionNoLocalQueue KueuePendingPositionStatus = "no_local_queue"
	// KueuePositionNotServed: the cluster does not serve Kueue's visibility
	// API.
	KueuePositionNotServed KueuePendingPositionStatus = "not_served"
	// KueuePositionUnsupported: the visibility API is served only at
	// versions Radar does not read.
	KueuePositionUnsupported KueuePendingPositionStatus = "unsupported"
	// KueuePositionUnavailable: Radar could not get an answer; Code says why.
	KueuePositionUnavailable KueuePendingPositionStatus = "unavailable"
)
