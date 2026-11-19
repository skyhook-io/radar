package k8s

import (
	"errors"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
)

// CollectorErrorCode classifies unambiguous collector refusals and sentinels.
// Deadlines and cancellation belong to the caller's execution policy.
func CollectorErrorCode(err error) (readreason.Code, bool) {
	switch {
	case errors.Is(err, k8score.ErrResourceListTooLarge):
		return readreason.ListTooLarge, true
	case errors.Is(err, k8score.ErrCollectionNotSupported):
		return readreason.KindNotWatchable, true
	case apierrors.IsUnauthorized(err):
		return readreason.RadarCredentialsRejected, true
	case apierrors.IsForbidden(err):
		return readreason.RadarForbidden, true
	case errors.Is(err, ErrUnknownDynamicKind):
		return readreason.KindUnknown, true
	case errors.Is(err, ErrDynamicNotReady):
		return readreason.KindSyncPending, true
	default:
		return "", false
	}
}
