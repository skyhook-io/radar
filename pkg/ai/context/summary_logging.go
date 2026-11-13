package context

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/logging"
)

func isLoggingOutput(obj *unstructured.Unstructured) bool {
	kind := obj.GetKind()
	return obj.GroupVersionKind().Group == logging.Group && (kind == "Output" || kind == "ClusterOutput")
}

// summarizeLoggingOutput names the output type only: the rest of an output
// block (hosts, tenants, token files, inline secrets) never reaches AI context.
func summarizeLoggingOutput(obj *unstructured.Unstructured) *ResourceSummary {
	out := logging.OutputFrom(obj)
	s := &ResourceSummary{Kind: obj.GetKind(), Name: obj.GetName(), Namespace: obj.GetNamespace(), Age: age(obj.GetCreationTimestamp().Time), Type: out.Type, Status: "Inactive"}
	if out.Active {
		s.Status = "Active"
	}
	if out.ProblemsCount > 0 {
		s.Status = fmt.Sprintf("%d problems", out.ProblemsCount)
	}
	return s
}

// projectLoggingOutput replaces the output block under spec with its redacted
// address, keeping the block's key (the output type) and the common keys.
func projectLoggingOutput(obj map[string]any) {
	out := logging.OutputFrom(&unstructured.Unstructured{Object: obj})
	spec, ok := obj["spec"].(map[string]any)
	if !ok {
		return
	}
	projected := map[string]any{}
	for _, k := range []string{"loggingRef", "protected", "enabledNamespaces"} {
		if v, ok := spec[k]; ok {
			projected[k] = v
		}
	}
	if out.Type != "" {
		block := map[string]any{}
		if out.Address != "" {
			block["address"] = out.Address
		}
		projected[out.Type] = block
	}
	obj["spec"] = projected
}
