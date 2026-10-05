package context

import (
	"github.com/skyhook-io/radar/pkg/datum"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"strings"
)

func summarizeDatum(u *unstructured.Unstructured) *ResourceSummary {
	s := summarizeGenericCRD(u)
	s.Status = datum.State(u)
	s.Hosts = datum.Hosts(u)
	var reasons []string
	for _, c := range datum.Failures(u) {
		reasons = append(reasons, strings.TrimSpace(c.Scope+" "+c.Type+": "+c.Reason))
	}
	s.Issue = strings.Join(reasons, "; ")
	var targets []string
	for _, ref := range datum.References(u) {
		targets = append(targets, ref.Ref.Kind+"."+ref.Ref.Group+"/"+ref.Ref.Name)
	}
	s.Target = strings.Join(targets, ", ")
	return s
}
func redactDatum(u *unstructured.Unstructured) *unstructured.Unstructured {
	if _, ok := datum.Lookup(u.GroupVersionKind().Group, u.GetKind()); !ok {
		return u
	}
	u = u.DeepCopy()
	RedactInlineSecrets(u.Object)
	switch u.GetKind() {
	case "Domain":
		unstructured.RemoveNestedField(u.Object, "status", "verification")
		unstructured.RemoveNestedField(u.Object, "status", "registration", "contacts")
		unstructured.RemoveNestedField(u.Object, "status", "registration", "abuse")
	case "Connector":
		unstructured.RemoveNestedField(u.Object, "status", "connectionDetails")
	}
	return u
}
