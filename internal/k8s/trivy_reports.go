package k8s

import (
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"

	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
)

// TrivyReportGVR resolves a read of kind (with an optional API group) the way
// the dynamic list and get paths do, and reports whether it reads one of Trivy
// Operator's reports. Read paths authorize these per kind, like Secrets: being
// able to see a namespace doesn't mean being allowed to read its scan
// findings, and an ExposedSecretReport carries the matched secret itself.
func TrivyReportGVR(kind, group string) (schema.GroupVersionResource, bool) {
	trivyGroup := strings.EqualFold(group, aicontext.TrivyGroup)
	if group != "" && !trivyGroup {
		return schema.GroupVersionResource{}, false
	}
	gvr, err := resolveDynamicGVR(kind, group)
	if err != nil {
		if trivyGroup {
			// Discovery doesn't know the kind yet, but a refresh could land
			// before the read resolves it again. Check the name as given, so
			// the read is authorized rather than skipped.
			return schema.GroupVersionResource{Group: aicontext.TrivyGroup, Resource: strings.ToLower(kind)}, true
		}
		return schema.GroupVersionResource{}, false
	}
	if gvr.Group != aicontext.TrivyGroup {
		return schema.GroupVersionResource{}, false
	}
	return gvr, true
}
