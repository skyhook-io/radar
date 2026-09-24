package k8s

import (
	"net/url"
	"strings"
)

// SameAPIServer reports whether two Kubernetes API server URLs name the same
// endpoint. Scheme, host (case-insensitive), port (with the scheme's default
// filled in) and path all count: gateways such as Rancher serve several
// clusters from one host, told apart only by path. Query, fragment and
// userinfo don't identify a cluster.
func SameAPIServer(a, b string) bool {
	ka := apiServerKey(a)
	return ka != "" && ka == apiServerKey(b)
}

func apiServerKey(server string) string {
	u, err := url.Parse(strings.TrimSpace(server))
	if err != nil || u.Host == "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && scheme != "http" {
		return ""
	}
	port := u.Port()
	if port == "" {
		port = "443"
		if scheme == "http" {
			port = "80"
		}
	}
	return scheme + "://" + strings.ToLower(u.Hostname()) + ":" + port + strings.TrimRight(u.EscapedPath(), "/")
}

// ContextsForServer returns the kubeconfig contexts, other than the current
// one, whose cluster is served at the given API server URL.
func ContextsForServer(server string) []string {
	if apiServerKey(server) == "" {
		return nil
	}
	contexts, err := GetAvailableContexts()
	if err != nil {
		return nil
	}
	var out []string
	for _, c := range contexts {
		if !c.IsCurrent && SameAPIServer(c.Server, server) {
			out = append(out, c.Name)
		}
	}
	return out
}
