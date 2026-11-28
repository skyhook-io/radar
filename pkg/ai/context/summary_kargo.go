package context

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/conditions"
	"github.com/skyhook-io/radar/pkg/kargo"
)

// KargoSummary is what a Kargo Stage, Freight, Promotion or Warehouse says
// about versions and where they come from. Freight names are content hashes,
// so Versions and Alias are how a person recognizes a version.
type KargoSummary struct {
	// Alias is a Freight's generated name, such as "singing-octopus".
	Alias string `json:"alias,omitempty"`
	// Versions are the images, commits and charts a Freight carries, or a
	// Stage holds, as short labels ("nginx:1.31.9").
	Versions []string `json:"versions,omitempty"`
	// Origin is the Warehouse a Freight came from.
	Origin string `json:"origin,omitempty"`
	// Stage and Freight are a Promotion's target and the Freight it promotes.
	Stage   string `json:"stage,omitempty"`
	Freight string `json:"freight,omitempty"`
	// Sources are where a Stage takes Freight from: upstream Stages, or
	// "Warehouse/<name>" for a direct source.
	Sources []string `json:"sources,omitempty"`
	// ArgoCDApps are the Argo CD Applications ("namespace/name") a Stage's
	// last Promotion updated, from its argocd-context annotation.
	ArgoCDApps []string `json:"argoCDApps,omitempty"`
	// FailedStep is the step a failed Promotion stopped at.
	FailedStep string `json:"failedStep,omitempty"`
}

func summarizeKargo(obj *unstructured.Unstructured) *ResourceSummary {
	s := &ResourceSummary{Kind: obj.GetKind(), Name: obj.GetName(), Namespace: obj.GetNamespace(), Age: age(obj.GetCreationTimestamp().Time)}
	k := &KargoSummary{}
	switch obj.GetKind() {
	case kargo.KindStage:
		stage := kargo.ParseStage(obj)
		s.Status = kargo.Phase(obj)
		for _, f := range stage.Current {
			k.Versions = append(k.Versions, f.Labels()...)
		}
		for _, req := range stage.Requests {
			k.Sources = append(k.Sources, req.Stages...)
			if req.Direct {
				k.Sources = append(k.Sources, req.Origin.String())
			}
		}
		for _, app := range stage.ArgoCDApps {
			k.ArgoCDApps = append(k.ArgoCDApps, app.Namespace+"/"+app.Name)
		}
		s.Issue = kargoStageIssue(stage, k)
	case kargo.KindFreight:
		f := kargo.ParseFreight(obj)
		k.Alias, k.Versions, k.Origin = f.Alias, f.Labels(), f.Origin.String()
	case kargo.KindPromotion:
		p := kargo.ParsePromotion(obj)
		s.Status = p.Phase
		k.Stage, k.Freight = p.Stage, p.FreightName
		if p.Freight != nil {
			k.Versions = p.Freight.Labels()
		}
		if failure := p.Failure(); failure.Failed {
			k.FailedStep = failure.StepAlias
			s.Issue = cmpOr(failure.Message, "failed without a recorded reason")
		}
	case kargo.KindWarehouse:
		s.Status = extractReadyCondition(obj)
		// A Warehouse that can't discover artifacts says why on Ready.
		if ready, ok := conditions.Find(obj, "Ready"); ok && ready.Status == "False" && !kargo.WaitingReason(ready.Reason) {
			s.Issue = cmpOr(ready.Message, ready.Reason)
		}
	}
	if k.Alias != "" || len(k.Versions) > 0 || k.Origin != "" || k.Stage != "" || len(k.Sources) > 0 || len(k.ArgoCDApps) > 0 || k.FailedStep != "" {
		s.Kargo = k
	}
	return s
}

// kargoStageIssue names what is wrong with a Stage now: its last Promotion
// failed, the latest verification of what it holds failed, or its health
// checks report issues. An aborted Promotion is a cancellation, not an issue.
func kargoStageIssue(stage kargo.Stage, k *KargoSummary) string {
	var issues []string
	if last := stage.LastPromotion; last != nil && kargo.PromotionFailed(last.Phase) {
		failure := last.Failure()
		k.FailedStep = failure.StepAlias
		issues = append(issues, "last promotion "+strings.ToLower(last.Phase)+": "+cmpOr(failure.Message, "no recorded reason"))
	}
	if v := stage.Verification; v != nil && v.Failed() {
		msg := "verification " + strings.ToLower(v.Phase) + ": " + cmpOr(v.Message, "no recorded reason")
		if v.AnalysisRun != nil {
			msg += " (AnalysisRun " + v.AnalysisRun.Name + ")"
		}
		issues = append(issues, msg)
	}
	if h := stage.Health; h != nil && h.Status == "Unhealthy" {
		issues = append(issues, "unhealthy: "+cmpOr(strings.Join(h.Issues, "; "), "no recorded reason"))
	}
	return strings.Join(issues, "; ")
}

func cmpOr(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return strings.TrimSpace(s)
}
