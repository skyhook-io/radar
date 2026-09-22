package gitops

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// StringValue returns v as a string, or "" if v is not a string.
// Convenience helper for unstructured map[string]any access where typed
// assertions would otherwise litter the call sites.
func StringValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// GroupFromAPIVersion extracts the API group from a Kubernetes apiVersion
// string. Returns "" for the core group ("v1" or empty input).
func GroupFromAPIVersion(apiVersion string) string {
	if apiVersion == "" || apiVersion == "v1" {
		return ""
	}
	if before, _, ok := strings.Cut(apiVersion, "/"); ok {
		return before
	}
	return apiVersion
}

// IsInClusterDestination reports whether an Argo Application deploys to the
// cluster Radar is connected to — spec.destination is the local API server or
// the "in-cluster" name — as opposed to a remote hub-spoke destination. Radar's
// per-user SARs authorize against the local cluster only, so the desired/live
// manifests of a remote destination cannot be authorized here. A missing/empty
// destination is Argo's degenerate local default; an explicit remote server or
// name is not. Fail closed: a nil Application is treated as not-in-cluster.
func IsInClusterDestination(app *unstructured.Unstructured) bool {
	if app == nil {
		return false
	}
	name, _, _ := unstructured.NestedString(app.Object, "spec", "destination", "name")
	server, _, _ := unstructured.NestedString(app.Object, "spec", "destination", "server")
	name = strings.TrimSpace(name)
	server = strings.TrimSpace(server)
	if name == "" && server == "" {
		return true
	}
	if strings.EqualFold(name, "in-cluster") {
		return true
	}
	return isLocalAPIServer(server)
}

// isLocalAPIServer matches the in-cluster Kubernetes API server URL Argo records
// for a same-cluster destination (kubernetes.default.svc, with or without a
// scheme, port, or trailing dot). Any other host is a remote cluster.
func isLocalAPIServer(server string) bool {
	if server == "" {
		return false
	}
	h := server
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	if i := strings.IndexAny(h, "/?#"); i >= 0 {
		h = h[:i]
	}
	if i := strings.LastIndex(h, ":"); i >= 0 {
		h = h[:i]
	}
	h = strings.TrimSuffix(strings.ToLower(h), ".")
	// Accept every in-cluster form: the short names and the service FQDN with any
	// cluster domain (kubernetes.default.svc.cluster.local, or a custom domain).
	return h == "kubernetes" || h == "kubernetes.default" ||
		h == "kubernetes.default.svc" || strings.HasPrefix(h, "kubernetes.default.svc.")
}
