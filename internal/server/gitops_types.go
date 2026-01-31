package server

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
