package audit

import "github.com/skyhook-io/radar/pkg/configrefs"

// ConfigReferenceEvidence separates observed use from the inventories needed
// to assert absence. An omitted namespace is unknown, never proof of no use.
// Refs and Objects supplement the subject input with additional observations.
// ReflectionsComplete additionally covers potential consumers of remote mirrors.
type ConfigReferenceEvidence struct {
	Refs                []ConfigObjectRef
	Objects             []configrefs.Object
	CompleteNamespaces  map[string][]string
	ReflectionsComplete map[string]bool
}
