package cnpg

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	integration "github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
)

// CNPGCatalogUser is one Cluster pinned to an image catalog.
type CNPGCatalogUser struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// Major is the PostgreSQL major the Cluster asks the catalog for. A
	// reference without one lands here as 0, which the screen must not describe
	// as "asks for PostgreSQL 0".
	Major int `json:"major,omitempty"`
	// Image the Cluster resolved and is running now. A cluster pinned to a
	// catalog carries no spec.imageName, so this is the only place the running
	// image appears.
	Image string `json:"image,omitempty"`
}

// CNPGCatalogUsersResponse lists the Clusters referencing one image catalog.
type CNPGCatalogUsersResponse struct {
	Clusters []CNPGCatalogUser `json:"clusters"`
}

// catalogRefMatches reports whether a Cluster's imageCatalogRef names this
// catalog.
//
// CloudNativePG defaults an omitted `kind` to the namespaced ImageCatalog, so a
// reference without one must not be counted against a ClusterImageCatalog of the
// same name — the two are different objects and may both exist.
func catalogRefMatches(ref map[string]interface{}, name, wantKind string) bool {
	if refName, _ := ref["name"].(string); refName != name {
		return false
	}
	refKind, _ := ref["kind"].(string)
	if refKind == "" {
		refKind = "ImageCatalog"
	}
	return refKind == wantKind
}

// catalogRefMajor reads the PostgreSQL major from a catalog reference.
//
// Read tolerantly: the same field arrives as int64 or float64 depending on how
// the object entered the dynamic cache, and NestedInt64 alone misses the float64
// shape — which would drop a real major to zero, a state the screen treats as
// "the reference carries no major at all".
func catalogRefMajor(ref map[string]interface{}) int {
	switch v := ref["major"].(type) {
	case int64:
		return int(v)
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

func (s *Reader) CatalogUsers(ctx context.Context, cache *k8s.ResourceCache, namespace, name, wantKind string) (*CNPGCatalogUsersResponse, error) {
	// A namespaced catalog can only be referenced from its own namespace; a
	// cluster-scoped one from anywhere.
	items, err := s.Observations.DynamicList(ctx, cache, "Cluster", Group, namespace)
	switch {
	case err == nil:
	case errors.Is(err, k8s.ErrUnknownDynamicKind):
		// No CloudNativePG on this cluster, so nothing can be pinned to a catalog.
		return &CNPGCatalogUsersResponse{Clusters: []CNPGCatalogUser{}}, nil
	case errors.Is(err, integration.ErrDynamicNotSynced):
		return nil, &ReadFailure{Status: http.StatusServiceUnavailable, Message: "clusters are still loading"}
	default:
		// "No cluster uses this catalog" is the sentence someone reads before
		// editing it. Never say it because the lookup failed.
		log.Printf("[cnpg] Failed to list Clusters for catalog %s/%s: %v", k8s.SanitizeForLog(namespace), k8s.SanitizeForLog(name), err)
		return nil, &ReadFailure{Status: http.StatusServiceUnavailable, Message: "could not read CloudNativePG clusters"}
	}

	resp := CNPGCatalogUsersResponse{Clusters: []CNPGCatalogUser{}}
	for _, u := range items {
		if u == nil {
			continue
		}
		ref, found, _ := unstructured.NestedMap(u.Object, "spec", "imageCatalogRef")
		if !found {
			continue
		}
		if !catalogRefMatches(ref, name, wantKind) {
			continue
		}
		user := CNPGCatalogUser{Namespace: u.GetNamespace(), Name: u.GetName()}
		user.Major = catalogRefMajor(ref)
		if img, _, _ := unstructured.NestedString(u.Object, "status", "image"); img != "" {
			user.Image = img
		}
		resp.Clusters = append(resp.Clusters, user)
	}
	sort.Slice(resp.Clusters, func(i, j int) bool {
		if resp.Clusters[i].Namespace != resp.Clusters[j].Namespace {
			return resp.Clusters[i].Namespace < resp.Clusters[j].Namespace
		}
		return resp.Clusters[i].Name < resp.Clusters[j].Name
	})

	return &resp, nil
}
