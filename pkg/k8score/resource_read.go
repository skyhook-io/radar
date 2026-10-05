package k8score

import (
	"fmt"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

type ResourceReadError struct {
	Code     string
	Resource string
	Reason   string
}

func (e *ResourceReadError) Error() string {
	switch e.Code {
	case "kind_not_served":
		return fmt.Sprintf("%s API is not served on this connection; specify an API group to disambiguate colliding kinds", e.Resource)
	case "kind_sync_failed":
		return fmt.Sprintf("%s failed to load: %s; retry or reconnect", e.Resource, e.Reason)
	default:
		return fmt.Sprintf("%s initial inventory is still loading; retry shortly", e.Resource)
	}
}

func initialListErrorReason(err error) string {
	if status, ok := err.(apierrors.APIStatus); ok {
		return fmt.Sprintf("API returned %s (%d)", status.Status().Reason, status.Status().Code)
	}
	return "initial LIST or WATCH failed (see local Radar logs)"
}
