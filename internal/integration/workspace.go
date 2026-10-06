package integration

// workspaceKind is one Kind a workspace lists from Radar's dynamic cache.
type WorkspaceKind struct {
	Key           string
	Group         string
	Kind          string
	Resource      string
	ClusterScoped bool
}
