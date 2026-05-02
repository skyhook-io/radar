package server

import "strings"

// sanitizeForLog strips characters that would let a caller forge log lines
// by submitting names/namespaces containing CR/LF or other control bytes.
// This addresses CodeQL log-injection findings on the GitOps handlers,
// which take user-controlled URL params and write them into log.Printf
// for diagnostics. Local Radar binaries are low-risk (single user, single
// terminal); in-cluster deployments shipping logs to shared aggregators
// are not, so we sanitize at the choke point rather than per-call.
//
// Replaces CR, LF, and tab with U+FFFD; leaves printable ASCII + UTF-8
// alone so legitimate names with spaces or unicode still log readably.
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
