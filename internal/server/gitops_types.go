package server

import "strings"

// sanitizeForLog replaces CR/LF/tab with U+FFFD so user-controlled URL
// params (kind, namespace, name) can't forge log lines when shipped to
// shared aggregators in in-cluster deployments. Other characters pass
// through so legitimate names log readably.
func sanitizeForLog(s string) string {
	if s == "" {
		return s
	}
	if !strings.ContainsAny(s, "\r\n\t") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\r', '\n', '\t':
			b.WriteRune('�')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// GitOpsResourceRef identifies a GitOps resource
type GitOpsResourceRef struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

// GitOpsOperationResponse is the standardized response format for all GitOps operations
type GitOpsOperationResponse struct {
	Message     string            `json:"message"`
	Operation   string            `json:"operation"`             // "sync", "refresh", "terminate", "suspend", "resume", "reconcile"
	Tool        string            `json:"tool"`                  // "argocd" or "fluxcd"
	Resource    GitOpsResourceRef `json:"resource"`
	RequestedAt string            `json:"requestedAt,omitempty"`
	Source      *GitOpsResourceRef `json:"source,omitempty"`     // For sync-with-source operations
}
