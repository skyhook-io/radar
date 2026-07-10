package server

import "strings"

// sanitizeForLog replaces CR/LF/tab with U+FFFD so user-controlled values
// (URL params like kind/namespace/name, and error strings that wrap them)
// can't forge log lines when shipped to shared aggregators in in-cluster
// deployments. Other characters pass through so legitimate names log readably.
// Uses strings.ReplaceAll on the line-terminator runes specifically so static
// analysis recognizes it as a log-injection barrier.
// normalizeNamespaceParam converts the "_" placeholder the web client uses for
// an empty namespace path segment back to "", so RBAC checks and upstream
// lookups don't treat "_" as a real namespace.
func normalizeNamespaceParam(ns string) string {
	if ns == "_" {
		return ""
	}
	return ns
}

func sanitizeForLog(s string) string {
	if s == "" {
		return s
	}
	s = strings.ReplaceAll(s, "\r", "�")
	s = strings.ReplaceAll(s, "\n", "�")
	s = strings.ReplaceAll(s, "\t", "�")
	return s
}

// GitOpsResourceRef identifies a GitOps resource
type GitOpsResourceRef struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

// GitOpsOperationResponse is the standardized response format for all GitOps operations
type GitOpsOperationResponse struct {
	Message     string             `json:"message"`
	Operation   string             `json:"operation"` // "sync", "refresh", "terminate", "suspend", "resume", "reconcile"
	Tool        string             `json:"tool"`      // "argocd" or "fluxcd"
	Resource    GitOpsResourceRef  `json:"resource"`
	RequestedAt string             `json:"requestedAt,omitempty"`
	Source      *GitOpsResourceRef `json:"source,omitempty"` // For sync-with-source operations
}
