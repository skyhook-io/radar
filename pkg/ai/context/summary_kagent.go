package context

import (
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/kagent"
)

// summarizeKagent summarizes a kagent kind that reports status with the
// reading Radar's Issues and UI use (pkg/kagent), so an agent is not called
// healthy because Ready=True while its egress grants are not applied, nor
// failed while it is still preparing. Literal header values and environment
// values never appear: the summary names the model, URL or image only.
func summarizeKagent(obj *unstructured.Unstructured) *ResourceSummary {
	s := &ResourceSummary{
		Kind:      obj.GetKind(),
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		Age:       age(obj.GetCreationTimestamp().Time),
	}
	if r, ok := kagent.Read(obj, time.Now()); ok {
		s.Status, s.Ready = r.Text, r.Summary
		if findings := r.Findings(); len(findings) > 0 {
			s.Issue = findings[0].Reason
		}
	}
	str := func(path ...string) string {
		v, _, _ := unstructured.NestedString(obj.Object, path...)
		return v
	}
	switch obj.GetKind() {
	case kagent.KindAgent:
		rev := kagent.ReadAgentRevisions(obj)
		s.Version = kagent.ShortRevision(rev.LatestSuccessful)
		s.Image = rev.WorkloadImage
	case kagent.KindSandboxTemplate:
		s.Image = str("spec", "workload", "image")
	case kagent.KindModelConfig:
		s.Type, s.Target = str("spec", "provider"), str("spec", "model")
	case kagent.KindModelProviderConfig:
		s.Type, s.Target = str("spec", "type"), RedactSecrets(str("spec", "endpoint"))
	case kagent.KindRemoteMCPServer:
		s.Type, s.Target = str("spec", "protocol"), RedactSecrets(str("spec", "url"))
	}
	return s
}
