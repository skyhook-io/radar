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
	// SignatureInfo is the raw GPG verification line Argo emits. Empty means no
	// signature check ran; non-empty means it did, and the UI reads the line to
	// distinguish a good signature ("good signature" → verified) from a failed
	// one (any other content → unverified). Not machine-parsed beyond that.
	SignatureInfo string `json:"signatureInfo,omitempty"`
}

// Repository is one row from GET /api/v1/repositories — a configured Git source
// and Argo CD's cached connection state for it.
type Repository struct {
	Repo string `json:"repo"`
	Type string `json:"type,omitempty"`
	Name string `json:"name,omitempty"`
	// Project scopes the repo to an AppProject; empty means a global repo. Argo
	// allows the same URL registered under different projects, so matching an
	// Application's source must respect this to avoid cross-project confusion.
	Project         string          `json:"project,omitempty"`
	ConnectionState ConnectionState `json:"connectionState"`
}

// ConnectionState is Argo CD's cached health for a repository connection. Status
// is "Successful" | "Failed" | "Unknown" (best-effort — treat unknown values as
// non-failing).
type ConnectionState struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
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

// ApplicationQuery identifies an Application for the health calls. AppName
// is required; AppNamespace is sent as `appNamespace` when non-empty.
type ApplicationQuery struct {
	AppName      string
	AppNamespace string
	Project      string
}

// ResourceHealth is one managed resource's health as the Argo CD API server
// reports it. Health is Argo's vocabulary (Healthy / Progressing / Degraded /
// Suspended / Missing / Unknown) or "" when Argo has no check for the kind.
type ResourceHealth struct {
	Group     string
	Kind      string
	Namespace string
	Name      string
	Health    string
	Message   string
}

// ApplicationHealth is the per-resource health of one Application, read from
// argocd-server rather than the Application object. UID identifies which
// Application the server answered for, so a caller can refuse an answer
// about a same-named app from a different Argo install.
type ApplicationHealth struct {
	UID string
	// ResourceHealthSource mirrors status.resourceHealthSource: "" when the
	// controller persists per-resource health inline, "appTree" when not.
	ResourceHealthSource string
	Resources            []ResourceHealth
}

// HasHealth reports whether at least one resource carries a health value —
// the difference between "the server answered" and "the server answered
// with Argo's verdicts".
func (a *ApplicationHealth) HasHealth() bool {
	if a == nil {
		return false
	}
	for _, r := range a.Resources {
		if r.Health != "" {
			return true
		}
	}
	return false
}
