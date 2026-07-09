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

// RevisionMetadata is the Git commit metadata for a deployed revision, from
// GET /api/v1/applications/{name}/revisions/{revision}/metadata. Every field is
// best-effort: they vary across Argo CD versions (signatureInfo is deprecated
// upstream in favor of a structured source-integrity result) and any may be
// empty — consumers must tolerate missing fields.
type RevisionMetadata struct {
	Author  string   `json:"author,omitempty"`
	Date    string   `json:"date,omitempty"`
	Tags    []string `json:"tags,omitempty"`
	Message string   `json:"message,omitempty"`
	// SignatureInfo is the raw GPG verification line. Non-empty means Argo
	// checked a signature; treat it as opaque — presence, not content, is what
	// the UI renders (a signed/unverified chip). Empty means no signature check.
	SignatureInfo string `json:"signatureInfo,omitempty"`
}

// RevisionMetadataQuery identifies a revision to look up. AppName + Revision are
// required; AppNamespace/Project/SourceIndex disambiguate multi-source apps and
// satisfy Argo CD's project-scoped identity check.
type RevisionMetadataQuery struct {
	AppName      string
	Revision     string
	AppNamespace string
	Project      string
	SourceIndex  string // stringified source index; omitted when empty
}
