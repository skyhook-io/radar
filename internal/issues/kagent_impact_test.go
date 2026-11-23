package issues

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/kagent"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func kagentImpactProvider(n int, templated bool) *fakeProvider {
	models := schema.GroupVersionResource{Group: kagent.Group, Version: "v1alpha3", Resource: "modelconfigs"}
	templates := schema.GroupVersionResource{Group: kagent.Group, Version: "v1alpha3", Resource: "agenttemplates"}
	p := &fakeProvider{dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{}, kinds: map[schema.GroupVersionResource]string{kagentAgents: "Agent", models: "ModelConfig", templates: "AgentTemplate"}, namespaced: map[schema.GroupVersionResource]bool{kagentAgents: true, models: true, templates: true}}
	m := kagentObject("model", kagentCondition("Accepted", "True", "Accepted", "", time.Hour), kagentCondition("ResolvedRefs", "False", "APIKeySecretNotFound", "secret missing not found", time.Hour))
	m.SetAPIVersion(kagent.Group + "/v1alpha3")
	m.SetKind("ModelConfig")
	m.Object["spec"] = map[string]any{"apiKeySecret": "missing", "apiKeySecretKey": "key"}
	p.dynamic[models] = []*unstructured.Unstructured{m}
	inline := map[string]any{"modelConfig": map[string]any{"name": "model"}}
	template := kagentObject("base")
	template.SetAPIVersion(kagent.Group + "/v1alpha3")
	template.SetKind("AgentTemplate")
	template.Object["spec"] = inline
	p.dynamic[templates] = []*unstructured.Unstructured{template}
	for i := 0; i < n; i++ {
		a := kagentObject(fmt.Sprintf("agent-%04d", i), kagentCondition("ResolvedRefs", "False", "ReferenceResolutionFailed", `resolve ModelConfig "agents/model": secret missing not found`, time.Hour), kagentCondition("Ready", "False", "Blocked", "blocked by ResolvedRefs", time.Hour))
		a.SetAPIVersion(kagent.Group + "/v1alpha3")
		a.SetKind("Agent")
		spec := map[string]any{"inline": inline}
		if templated {
			spec = map[string]any{"ref": map[string]any{"name": "base"}}
		}
		a.Object["spec"] = map[string]any{"template": spec}
		p.dynamic[kagentAgents] = append(p.dynamic[kagentAgents], a)
	}
	return p
}

func TestKagentImpactIssuesKeepCriticalAndUncappedMembership(t *testing.T) {
	p := kagentImpactProvider(kagent.UsedByCap+5, false)
	all := Compose(p, Filters{Limit: NoLimit, CanReadEvidence: func(EvidenceRead) bool { return true }})
	var dep Issue
	critical := 0
	for _, iss := range all {
		if iss.Kind == "ModelConfig" {
			dep = iss
		}
		if iss.Kind == "Agent" && iss.Severity == SeverityCritical {
			critical++
		}
	}
	if critical != kagent.UsedByCap+5 || len(dep.AffectedRefs) != kagent.UsedByCap+5 || dep.Action == "" {
		t.Fatalf("critical=%d refs=%d dependency=%+v", critical, len(dep.AffectedRefs), dep)
	}
	opts := RelatedIssueOptions{CanReadRelated: func(Ref) bool { return true }, CanReadEvidence: func(EvidenceRead) bool { return true }}
	related := RelatedIssuesFrom(all, GroupIssues(all), opts, kagent.Group, "Agent", "agents", "agent-0504")
	if len(related) != 2 {
		t.Fatalf("last Agent related issues=%d, want own and dependency", len(related))
	}
	opts.CanReadEvidence = func(EvidenceRead) bool { return false }
	related = RelatedIssuesFrom(all, GroupIssues(all), opts, kagent.Group, "Agent", "agents", "agent-0504")
	if len(related) != 1 || related[0].Kind != "Agent" {
		t.Fatalf("denied related=%+v", related)
	}
	base, ok := ForEvidence(dep, opts.CanReadEvidence, false)
	if !ok || base.Message != dep.Message || base.Action != dep.Action || len(base.AffectedRefs) != 0 || base.DiagnosticContext != nil || !base.FirstSeen.Equal(dep.FirstSeen) {
		t.Fatalf("denied enrichment altered the underlying Issue: %+v", base)
	}
}

func TestKagentImpactDeniedIntermediateLeavesIssueUnchanged(t *testing.T) {
	p := kagentImpactProvider(2, true)
	baseline := Compose(p, Filters{Limit: NoLimit})
	denied := Compose(p, Filters{Limit: NoLimit, CanReadEvidence: func(r EvidenceRead) bool { return r.Resource != "agenttemplates" }})
	for _, list := range [][]Issue{baseline, denied} {
		for _, iss := range list {
			if iss.Kind == "ModelConfig" && (len(iss.AffectedRefs) != 0 || iss.DiagnosticContext != nil || len(iss.RequiredReads) != 0) {
				t.Fatalf("denied impact enriched: %+v", iss)
			}
		}
	}
	allowed := Compose(p, Filters{Limit: NoLimit, CanReadEvidence: func(r EvidenceRead) bool { return r.Exact && r.Verb == "list" }})
	for _, iss := range allowed {
		if iss.Kind == "ModelConfig" && len(iss.AffectedRefs) != 2 {
			t.Fatalf("allowed impact=%+v", iss)
		}
	}
	if len(baseline) != len(denied) {
		t.Fatal("impact read denial removed an Issue")
	}
}

func TestKagentImpactPartlyReadable(t *testing.T) {
	p := kagentImpactProvider(2, false)
	a := p.dynamic[kagentAgents][1]
	a.SetNamespace("hidden")
	unstructured.SetNestedField(a.Object, "agents", "spec", "template", "inline", "modelConfig", "namespace")
	got := Compose(p, Filters{Limit: NoLimit, CanReadEvidence: func(r EvidenceRead) bool { return r.Namespace != "hidden" }})
	for _, iss := range got {
		if iss.Kind == "ModelConfig" && !reflect.DeepEqual(iss.AffectedRefs, []Ref{{Group: kagent.Group, Kind: "Agent", Namespace: "agents", Name: "agent-0000"}}) {
			t.Fatalf("impact=%+v", iss.AffectedRefs)
		}
	}
}

func TestKagentImpactKindFilterIncludesDependencyIssues(t *testing.T) {
	p := kagentImpactProvider(2, false)
	got := Compose(p, Filters{Kinds: []string{"Agent"}, Limit: NoLimit, CanReadEvidence: func(EvidenceRead) bool { return true }})
	if len(got) != 3 {
		t.Fatalf("Agent kind filter returned %d findings, want two Agents and their ModelConfig", len(got))
	}
	denied := Compose(p, Filters{Kinds: []string{"Agent"}, Limit: NoLimit, CanReadEvidence: func(EvidenceRead) bool { return false }})
	if len(denied) != 2 {
		t.Fatalf("denied impact returned %d findings, want only the two Agents", len(denied))
	}
	for _, iss := range denied {
		if iss.Kind != "Agent" || iss.Severity != SeverityCritical {
			t.Fatalf("Agent Issue lost critical: %+v", iss)
		}
	}
}
