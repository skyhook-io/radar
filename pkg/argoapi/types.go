package argoapi

// ResourceDiff is one entry from the managed-resources endpoint. All *State
// fields are JSON-serialized manifest strings, as the Argo CD API returns
// them.
type ResourceDiff struct {
	Group     string `json:"group,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
	// TargetState is the desired manifest from the source repo.
	TargetState string `json:"targetState,omitempty"`
	// LiveState is the manifest currently in the cluster.
	LiveState string `json:"liveState,omitempty"`
	// Diff is deprecated upstream in favor of the normalized/predicted states.
	Diff string `json:"diff,omitempty"`
	Hook bool   `json:"hook,omitempty"`
	// NormalizedLiveState is LiveState after Argo CD's diff normalizations.
	NormalizedLiveState string `json:"normalizedLiveState,omitempty"`
	// PredictedLiveState is the expected post-sync state (server-side dry-run).
	PredictedLiveState string `json:"predictedLiveState,omitempty"`
	Modified           bool   `json:"modified,omitempty"`
}

// UserInfo is the response of /api/v1/session/userinfo.
type UserInfo struct {
	LoggedIn bool     `json:"loggedIn"`
	Username string   `json:"username"`
	Iss      string   `json:"iss"`
	Groups   []string `json:"groups"`
}

// ManagedResourcesQuery filters the managed-resources call. AppName is
// required; all other fields are optional and omitted from the request when
// empty.
type ManagedResourcesQuery struct {
	AppName      string
	AppNamespace string
	Project      string
	Group        string
	Kind         string
	Namespace    string
	Name         string
}
