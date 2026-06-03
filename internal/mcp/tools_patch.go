package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/skyhook-io/radar/internal/k8s"
)

type patchResourceInput struct {
	Kind      string `json:"kind" jsonschema:"resource kind, e.g. Deployment, Service, ConfigMap"`
	Group     string `json:"group,omitempty" jsonschema:"API group when the kind is ambiguous, e.g. apps for Deployment or serving.knative.dev for Knative Service"`
	Namespace string `json:"namespace,omitempty" jsonschema:"namespace for namespaced resources; omit for cluster-scoped resources"`
	Name      string `json:"name" jsonschema:"resource name"`
	PatchType string `json:"patch_type,omitempty" jsonschema:"json (default, RFC 6902 JSON Patch array), merge (JSON Merge Patch object), or strategic (built-in Kubernetes kinds only)"`
	Patch     string `json:"patch" jsonschema:"JSON patch body. For patch_type=json, pass an array like [{\"op\":\"remove\",\"path\":\"/spec/template/spec/dnsConfig\"}]. For merge/strategic, pass an object."`
	DryRun    bool   `json:"dry_run,omitempty" jsonschema:"validate and preview the server-side result without persisting changes"`
	Verify    *bool  `json:"verify,omitempty" jsonschema:"return compact post-patch state; on dry_run return a preview diff. JSON Patch calls also include field checks. Default true; set false for a terse write result."`
}

type jsonPatchOperation struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value,omitempty"`
}

func handlePatchResource(ctx context.Context, req *mcp.CallToolRequest, input patchResourceInput) (*mcp.CallToolResult, any, error) {
	kind := strings.TrimSpace(input.Kind)
	name := strings.TrimSpace(input.Name)
	namespace := strings.TrimSpace(input.Namespace)
	group := strings.TrimSpace(input.Group)
	if kind == "" {
		return nil, nil, fmt.Errorf("kind is required")
	}
	if name == "" {
		return nil, nil, fmt.Errorf("name is required")
	}

	patchBody := strings.TrimSpace(input.Patch)
	if patchBody == "" {
		return nil, nil, fmt.Errorf("patch is required")
	}
	if !json.Valid([]byte(patchBody)) {
		return nil, nil, fmt.Errorf("patch must be valid JSON")
	}

	gvr, namespaced, err := resolveMutationGVR(kind, group)
	if err != nil {
		return nil, nil, err
	}
	if namespaced && namespace == "" {
		return nil, nil, fmt.Errorf("namespace is required for namespaced kind %q", kind)
	}
	if !namespaced && namespace != "" {
		return nil, nil, fmt.Errorf("namespace must be empty for cluster-scoped kind %q", kind)
	}

	dynClient := k8s.DynamicClientFromContext(ctx)
	if dynClient == nil {
		return nil, nil, fmt.Errorf("not connected to cluster")
	}

	patchType, err := parsePatchType(input.PatchType)
	if err != nil {
		return nil, nil, err
	}
	if patchType == types.StrategicMergePatchType && !strategicPatchSupported(kind, group, gvr) {
		return nil, nil, fmt.Errorf("patch_type=strategic is only supported for Kubernetes built-in resources discovered as non-CRD; use patch_type=merge or patch_type=json for CRDs or unknown resources")
	}

	var resClient = dynClient.Resource(gvr)
	var client dynamic.ResourceInterface = resClient
	if namespace != "" {
		client = resClient.Namespace(namespace)
	}

	verify := input.Verify == nil || *input.Verify

	var before *unstructured.Unstructured
	var beforeErr string
	if verify {
		got, err := client.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			beforeErr = err.Error()
		} else {
			before = got
		}
	}

	opts := metav1.PatchOptions{}
	if input.DryRun {
		opts.DryRun = []string{metav1.DryRunAll}
	}
	patched, err := client.Patch(ctx, name, patchType, []byte(patchBody), opts)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to patch resource: %w", err)
	}

	result := map[string]any{
		"status":     "ok",
		"message":    fmt.Sprintf("Successfully patched %s %s", kind, resourceDisplayName(namespace, name)),
		"kind":       kind,
		"group":      gvr.Group,
		"namespace":  namespace,
		"name":       name,
		"patch_type": patchTypeName(patchType),
		"dry_run":    input.DryRun,
	}
	if rv := patched.GetResourceVersion(); rv != "" {
		result["resourceVersion"] = rv
	}
	if gen := patched.GetGeneration(); gen != 0 {
		result["generation"] = gen
	}

	if verify {
		var ops []jsonPatchOperation
		if patchType == types.JSONPatchType {
			ops = parseJSONPatchOperations([]byte(patchBody))
		}
		result["verification"] = buildMutationVerification(ctx, dynClient, mutationVerificationOptions{
			Kind:         kind,
			Group:        group,
			Namespace:    namespace,
			Name:         name,
			GVR:          gvr,
			Post:         patched,
			Before:       before,
			BeforeErr:    beforeErr,
			JSONPatchOps: ops,
			PreviewDiff:  input.DryRun,
		})
	}

	return toJSONResult(result)
}

func resourceDisplayName(namespace, name string) string {
	if namespace == "" {
		return name
	}
	return namespace + "/" + name
}

func parsePatchType(input string) (types.PatchType, error) {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "", "json", "jsonpatch", "json_patch":
		return types.JSONPatchType, nil
	case "merge", "jsonmerge", "merge_patch", "json_merge":
		return types.MergePatchType, nil
	case "strategic", "strategicmerge", "strategic_merge", "strategic-merge":
		return types.StrategicMergePatchType, nil
	default:
		return "", fmt.Errorf("patch_type must be 'json', 'merge', or 'strategic', got %q", input)
	}
}

func patchTypeName(patchType types.PatchType) string {
	switch patchType {
	case types.JSONPatchType:
		return "json"
	case types.MergePatchType:
		return "merge"
	case types.StrategicMergePatchType:
		return "strategic"
	default:
		return string(patchType)
	}
}

func strategicPatchSupported(kind, group string, gvr schema.GroupVersionResource) bool {
	if discovery := k8s.GetResourceDiscovery(); discovery != nil {
		if res, ok := discovery.GetResourceWithGroup(kind, gvr.Group); ok && res.Name == gvr.Resource {
			return !res.IsCRD
		}
	}
	if _, ok := k8s.BuiltinGVR(kind, group); ok {
		return true
	}
	if group == "" {
		if builtin, ok := k8s.BuiltinGVRAnyGroup(kind); ok {
			return builtin.Group == gvr.Group && builtin.Resource == gvr.Resource
		}
	}
	return false
}

func parseJSONPatchOperations(raw []byte) []jsonPatchOperation {
	var ops []jsonPatchOperation
	if err := json.Unmarshal(raw, &ops); err != nil {
		return nil
	}
	return ops
}

func verifyJSONPatchOperations(before, after *unstructured.Unstructured, ops []jsonPatchOperation, beforeErr string) []map[string]any {
	results := make([]map[string]any, 0, len(ops))
	for _, op := range ops {
		entry := map[string]any{
			"op":   op.Op,
			"path": op.Path,
		}
		beforeVal, beforeFound := getJSONPointerValue(before, op.Path)
		afterVal, afterFound := getJSONPointerValue(after, op.Path)
		entry["before_found"] = beforeFound
		entry["after_found"] = afterFound

		switch strings.ToLower(op.Op) {
		case "remove":
			switch {
			case beforeErr != "":
				entry["status"] = "unknown_before"
				entry["before_error"] = beforeErr
			case beforeFound && !afterFound:
				entry["status"] = "removed"
			case beforeFound && afterFound && !reflect.DeepEqual(beforeVal, afterVal):
				entry["status"] = "changed_at_path"
			case beforeFound:
				entry["status"] = "unchanged"
			default:
				entry["status"] = "missing_before"
			}
		case "replace", "add":
			if strings.ToLower(op.Op) == "add" && strings.HasSuffix(op.Path, "/-") {
				entry["status"] = "array_append_not_checked"
				entry["note"] = "JSON Patch append paths ending in /- cannot be resolved as a stable post-mutation JSON pointer"
				break
			}
			var want any
			haveWant := len(op.Value) > 0 && json.Unmarshal(op.Value, &want) == nil
			matched := afterFound && haveWant && reflect.DeepEqual(normalizeJSONNumber(afterVal), normalizeJSONNumber(want))
			if haveWant {
				entry["value_matched"] = matched
			}
			switch {
			case !afterFound:
				entry["status"] = "missing_after"
			case haveWant && !matched:
				// Path exists but the live value differs from what we set
				// (defaulting webhook, another field manager, etc.) — don't let
				// "present" read as a confirmed apply.
				entry["status"] = "present_value_mismatch"
			default:
				entry["status"] = "present"
			}
		default:
			entry["status"] = "not_checked"
		}
		results = append(results, entry)
	}
	return results
}

func getJSONPointerValue(obj *unstructured.Unstructured, pointer string) (any, bool) {
	if obj == nil || pointer == "" || pointer[0] != '/' {
		return nil, false
	}
	var cur any = obj.Object
	for _, rawPart := range strings.Split(pointer[1:], "/") {
		part := strings.ReplaceAll(strings.ReplaceAll(rawPart, "~1", "/"), "~0", "~")
		switch typed := cur.(type) {
		case map[string]any:
			next, ok := typed[part]
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			if part == "-" {
				return nil, false
			}
			idx, err := strconv.Atoi(part)
			if err != nil || idx < 0 || idx >= len(typed) {
				return nil, false
			}
			cur = typed[idx]
		default:
			return nil, false
		}
	}
	return cur, true
}

func normalizeJSONNumber(v any) any {
	switch n := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(n))
		for k, v := range n {
			out[k] = normalizeJSONNumber(v)
		}
		return out
	case []any:
		out := make([]any, len(n))
		for i, v := range n {
			out[i] = normalizeJSONNumber(v)
		}
		return out
	case int:
		return float64(n)
	case int32:
		return float64(n)
	case int64:
		return float64(n)
	default:
		return v
	}
}
