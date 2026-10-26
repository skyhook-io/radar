package server

import (
	"fmt"
	"net/http"
	"reflect"
	"strconv"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

const resourceListHardLimit = 50000

func parseResourceListLimit(r *http.Request, summary bool) (int, error) {
	limit := 25000
	if summary {
		limit = resourceListHardLimit
	}
	if value := r.URL.Query().Get("maxItems"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > resourceListHardLimit {
			return 0, fmt.Errorf("maxItems must be between 1 and %d", resourceListHardLimit)
		}
		limit = parsed
	}
	return limit, nil
}

func (s *Server) checkResourceListSize(w http.ResponseWriter, count, limit int, target listTarget, gaps []resourceapi.Gap) bool {
	if count <= limit {
		return true
	}
	s.writeReadFailure(w, "", &resourceReadFailure{
		code:    readreason.ListTooLarge,
		message: "Too many resources to load; select fewer namespaces",
		gaps:    gaps, count: count, limit: limit,
	}, &target)
	return false
}

func listSize(items any) int {
	if items == nil {
		return 0
	}
	value := reflect.ValueOf(items)
	if value.Kind() == reflect.Slice {
		return value.Len()
	}
	return 0
}

func typedListCount(cache *k8s.ResourceCache, resource string, namespaces []string) int {
	// ListCountNamespaced reads an empty slice as every namespace.
	if namespaces != nil && len(namespaces) == 0 {
		return 0
	}
	for _, kind := range k8score.AllKindListers() {
		if informerKeyForKind(kind.Kind()) == resource {
			return k8score.ListCountNamespaced(kind.Lister()(cache.ResourceCache), namespaces)
		}
	}
	return 0
}
