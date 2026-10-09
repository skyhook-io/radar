package auth

// Grant is one Kubernetes RBAC permission a read or an action needs, as the
// caller would have to be given it. An empty Namespace means cluster-wide.
type Grant struct {
	Verb        string `json:"verb"`
	Group       string `json:"group,omitempty"`
	Resource    string `json:"resource"`
	Subresource string `json:"subresource,omitempty"`
	Namespace   string `json:"namespace,omitempty"`
}

// In returns g bound to namespace ("" for cluster-wide).
func (g Grant) In(namespace string) Grant {
	g.Namespace = namespace
	return g
}

// Ref returns a copy of g for an optional field.
func (g Grant) Ref() *Grant {
	return &g
}

// String words the grant for a person: "patch clusters/status
// (postgresql.cnpg.io) in namespace pg", or for a cluster-wide grant "get
// pods/proxy cluster-wide", where the group is written resource.group so the
// text reads without nested parentheses when quoted inside "(needs …)".
func (g Grant) String() string {
	res := g.Resource
	if g.Subresource != "" {
		res += "/" + g.Subresource
	}
	if g.Namespace == "" {
		if g.Group != "" {
			res += "." + g.Group
		}
		return g.Verb + " " + res + " cluster-wide"
	}
	if g.Group != "" {
		res += " (" + g.Group + ")"
	}
	return g.Verb + " " + res + " in namespace " + g.Namespace
}
