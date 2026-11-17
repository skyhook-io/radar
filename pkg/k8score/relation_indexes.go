package k8score

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	"github.com/skyhook-io/radar/pkg/relations"
)

// relationIndexers are the relation families' reverse indexes on the typed
// kinds that are a family's source (relations.ReferrerIndexers); dynamic
// informers take theirs by GroupResource when they start.
var relationIndexers = map[string]cache.Indexers{
	Secrets:    relations.ReferrerIndexers[schema.GroupResource{Resource: "secrets"}],
	ConfigMaps: relations.ReferrerIndexers[schema.GroupResource{Resource: "configmaps"}],
}
