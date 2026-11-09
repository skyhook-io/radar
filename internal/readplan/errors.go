package readplan

import (
	"errors"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	case errors.Is(err, k8s.ErrUnknownDynamicKind):
		return readreason.KindUnknown, true
	case errors.Is(err, k8s.ErrDynamicNotReady):
		return readreason.KindSyncPending, true
	default:
		return "", false
	}
}
