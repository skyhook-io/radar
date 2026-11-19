package readplan

import (
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/readreason"
)

// CollectorErrorCode classifies unambiguous collector refusals and sentinels;
// see k8s.CollectorErrorCode.
func CollectorErrorCode(err error) (readreason.Code, bool) {
	return k8s.CollectorErrorCode(err)
}
