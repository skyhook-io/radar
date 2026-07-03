package server

import (
	"net/http"
	"net/url"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// GET /api/argo/destinations — the Argo CD cluster-secret registry as plain
// topology facts: destination name + API server URL, nothing else. Exists so
// fleet consumers (Radar Cloud's destination mapping) never have to pull full
// cluster Secrets — whose .data carries destination-cluster credentials —
// through an aggregator to read two fields.
//
// RBAC stance (deliberate, product-approved): SA-backed inventory. The
// response is derived from Secrets, but emits ONLY the destination name and
// server URL — the same facts any Argo user sees in its UI — so it is not
// gated on the caller's own secret-read RBAC. .data never crosses the wire.
//
// Argo's implicit in-cluster destination has no cluster Secret; consumers
// resolve in-cluster separately (as the hub already does).

const argoClusterSecretLabel = "argocd.argoproj.io/secret-type"

const maxDestinationFieldLen = 512

func validDestinationName(name string) bool {
	if name == "" || len(name) > maxDestinationFieldLen {
		return false
	}
	return !strings.ContainsAny(name, "\n\r")
}

// sanitizeDestinationServer parses a cluster-secret server value and
// reconstructs it as scheme://host[:port] — nothing else survives. Query
// strings, fragments, paths, and userinfo are all places credential
// material can hide in Secret data, and this endpoint deliberately serves
// callers who cannot read Secrets, so the only safe output is a rebuilt
// URL, never reflected bytes.
func sanitizeDestinationServer(server string) (string, bool) {
	if server == "" || len(server) > maxDestinationFieldLen {
		return "", false
	}
	u, err := url.Parse(server)
	if err != nil {
		return "", false
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return "", false
	}
	return u.Scheme + "://" + u.Host, true
}

func secretsScopeDisabled() bool {
	perm := k8s.GetCachedPermissionResult()
	if perm == nil {
		return false
	}
	scope, ok := perm.Scopes[k8score.Secrets]
	return ok && !scope.Enabled
}

type ArgoDestination struct {
	Name   string `json:"name"`
	Server string `json:"server"`
	// Backing cluster Secret identity — source attribution and the only
	// complete disambiguator when two secrets claim the same destination name.
	SecretNamespace string `json:"secretNamespace"`
	SecretName      string `json:"secretName"`
}

type ArgoDestinationsCompleteness struct {
	// Secrets informer is still syncing (deferred kind) — retry shortly.
	Syncing bool `json:"syncing"`
	// The SA cannot list Secrets at all — destinations unknowable.
	Restricted bool `json:"restricted"`
	// Secrets are permitted but the informer failed to sync (timeout/abort) —
	// distinct from RBAC restriction; a retry may recover.
	Unavailable bool `json:"unavailable"`
	// Secrets are cached from a single namespace only (cluster-wide list was
	// denied at startup); destinations outside it are invisible.
	ScopedToNamespace string `json:"scopedToNamespace,omitempty"`
	Complete          bool   `json:"complete"`
}

type ArgoDestinationsResponse struct {
	Destinations []ArgoDestination            `json:"destinations"`
	Completeness ArgoDestinationsCompleteness `json:"completeness"`
}

func (s *Server) handleArgoDestinations(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster cache not ready")
		return
	}

	resp := ArgoDestinationsResponse{Destinations: []ArgoDestination{}}

	secretLister := cache.Secrets()
	if secretLister == nil {
		// Distinguish three nil-lister causes: still syncing (deferred kind),
		// RBAC-disabled at startup probe, or permitted-but-sync-failed.
		switch {
		case cache.IsDeferredPending(k8score.Secrets):
			resp.Completeness.Syncing = true
		case secretsScopeDisabled():
			resp.Completeness.Restricted = true
		default:
			resp.Completeness.Unavailable = true
		}
		s.writeJSON(w, resp)
		return
	}

	// A non-nil lister can still be namespace-scoped: startup probes fall
	// back to a single namespace when cluster-wide secret list is denied.
	if perm := k8s.GetCachedPermissionResult(); perm != nil {
		if scope, ok := perm.Scopes[k8score.Secrets]; ok && scope.Enabled && scope.Namespace != "" {
			resp.Completeness.ScopedToNamespace = scope.Namespace
		}
	}

	selector := labels.SelectorFromSet(labels.Set{argoClusterSecretLabel: "cluster"})
	secrets, err := secretLister.List(selector)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "listing argo cluster secrets: "+err.Error())
		return
	}
	for _, sec := range secrets {
		name := string(sec.Data["name"])
		server := string(sec.Data["server"])
		// The label is mutable and the exported keys come from Secret data —
		// validate before reflecting: server must be a bounded http(s) URL
		// with a host, name a bounded single-line string. Anything else is a
		// malformed or hostile row, skipped.
		sanitized, ok := sanitizeDestinationServer(server)
		if !validDestinationName(name) || !ok {
			continue
		}
		resp.Destinations = append(resp.Destinations, ArgoDestination{
			Name:            name,
			Server:          sanitized,
			SecretNamespace: sec.Namespace,
			SecretName:      sec.Name,
		})
	}
	sort.Slice(resp.Destinations, func(i, j int) bool {
		a, b := resp.Destinations[i], resp.Destinations[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.SecretNamespace != b.SecretNamespace {
			return a.SecretNamespace < b.SecretNamespace
		}
		if a.SecretName != b.SecretName {
			return a.SecretName < b.SecretName
		}
		return a.Server < b.Server
	})

	resp.Completeness.Complete = !resp.Completeness.Syncing &&
		!resp.Completeness.Restricted && !resp.Completeness.Unavailable &&
		resp.Completeness.ScopedToNamespace == ""

	s.writeJSON(w, resp)
}
