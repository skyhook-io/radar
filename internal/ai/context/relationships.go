package context

import (
	"fmt"
	"strings"
)

// StatusFunc returns a short health status string for a given resource.
// Returns empty string if status is unknown or unavailable.
type StatusFunc func(kind, namespace, name string) string

// SummarizeRelationships renders a resource's relationships as a concise string for LLM context.
// statusFn is optional — if nil, no health status is included.
func SummarizeRelationships(rels *Relationships, statusFn StatusFunc) string {
	if rels == nil {
		return "No relationships found."
	}

	var b strings.Builder

	if rels.Owner != nil {
		fmt.Fprintf(&b, "Owner: %s/%s", rels.Owner.Kind, rels.Owner.Name)
		if statusFn != nil {
			if s := statusFn(rels.Owner.Kind, rels.Owner.Namespace, rels.Owner.Name); s != "" {
				fmt.Fprintf(&b, " [%s]", s)
			}
		}
		b.WriteByte('\n')
	}

	writeRefList(&b, "Children", rels.Children, statusFn)
	writeRefList(&b, "Services", rels.Services, statusFn)
	writeRefList(&b, "Ingresses", rels.Ingresses, statusFn)
	writeRefList(&b, "Gateways", rels.Gateways, statusFn)
	writeRefList(&b, "Routes", rels.Routes, statusFn)
	writeRefList(&b, "Pods", rels.Pods, statusFn)

	// ConfigRefs: names only, no status
	if len(rels.ConfigRefs) > 0 {
		names := make([]string, len(rels.ConfigRefs))
		for i, ref := range rels.ConfigRefs {
			names[i] = fmt.Sprintf("%s/%s", ref.Kind, ref.Name)
		}
		fmt.Fprintf(&b, "Config: %s\n", strings.Join(names, ", "))
	}

	if len(rels.Scalers) > 0 {
		names := make([]string, len(rels.Scalers))
		for i, ref := range rels.Scalers {
			names[i] = fmt.Sprintf("%s/%s", ref.Kind, ref.Name)
		}
		fmt.Fprintf(&b, "Scalers: %s\n", strings.Join(names, ", "))
	}

	if rels.ScaleTarget != nil {
		fmt.Fprintf(&b, "Scale target: %s/%s\n", rels.ScaleTarget.Kind, rels.ScaleTarget.Name)
	}

	if b.Len() == 0 {
		return "No relationships found."
	}

	return b.String()
}

func writeRefList(b *strings.Builder, label string, refs []ResourceRef, statusFn StatusFunc) {
	if len(refs) == 0 {
		return
	}
	fmt.Fprintf(b, "%s: ", label)
	for i, ref := range refs {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(b, "%s/%s", ref.Kind, ref.Name)
		if statusFn != nil {
			if s := statusFn(ref.Kind, ref.Namespace, ref.Name); s != "" {
				fmt.Fprintf(b, " [%s]", s)
			}
		}
	}
	b.WriteByte('\n')
}
