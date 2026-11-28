package issues

import (
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/conditions"
	"github.com/skyhook-io/radar/pkg/gitops"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/kargo"
)

const (
	ReasonKargoPromotionFailed    = "KargoPromotionFailed"
	ReasonKargoVerificationFailed = "KargoVerificationFailed"
)

const (
	argoProjGroup   = "argoproj.io"
	kindAnalysisRun = "AnalysisRun"
	kindApplication = "Application"
	// factKargoVerification links an AnalysisRun's rows, and its Jobs', to
	// the Kargo verification that ran it.
	factKargoVerification = "kargo_verification"
)

// KargoStageIssues are the current problems a Kargo Stage records: its last
// Promotion Failed or Errored, and the latest verification of the Freight it
// holds Failed or Errored. Both are read from the Stage, which keeps a copy
// of its last Promotion's status, so a Promotion Kargo has already pruned
// still shows. Older failed Promotions are history, and an Aborted one is a
// person's cancellation, so neither is an issue. Both stop Freight from
// moving on through the pipeline, so both are critical.
func KargoStageIssues(gvr schema.GroupVersionResource, u *unstructured.Unstructured) []Issue {
	if !kargo.Is(u, kargo.KindStage) || u.GetDeletionTimestamp() != nil {
		return nil
	}
	stage := kargo.ParseStage(u)
	var out []Issue
	if last := stage.LastPromotion; last != nil && kargo.PromotionFailed(last.Phase) {
		out = append(out, kargoPromotionIssue(gvr, u, stage, *last))
	}
	if v := stage.Verification; v != nil && v.Failed() {
		out = append(out, kargoVerificationIssue(gvr, u, stage, *v))
	}
	return out
}

func kargoPromotionIssue(gvr schema.GroupVersionResource, u *unstructured.Unstructured, stage kargo.Stage, last kargo.PromotionRecord) Issue {
	failure := last.Failure()
	verb := "failed"
	if last.Phase == kargo.PhaseErrored {
		verb = "errored"
	}
	msg := "Promotion" + freightPhrase(last.Freight) + " to stage " + stage.Name + " " + verb
	if failure.StepAlias != "" {
		msg += " at step " + failure.StepAlias
	}
	if failure.Message != "" {
		msg += ": " + strings.TrimSpace(failure.Message)
	} else {
		msg += ". Kargo recorded no reason."
	}
	promotions := gvr
	promotions.Resource = "promotions"
	issue := newConditionIssue(promotions, kargo.KindPromotion, stage.Namespace, last.Name, SeverityCritical, ReasonKargoPromotionFailed,
		msg, last.FinishedAt, !last.FinishedAt.IsZero(), ReasonKargoPromotionFailed, u.GetCreationTimestamp().Time)
	issue.RequiredReads = kargoEvidence(gvr, stage.Namespace)
	// Its Stage's related Issues and a kind=Stage list find it through here.
	issue.AffectedRefs = []Ref{{Group: kargo.Group, Kind: kargo.KindStage, Namespace: stage.Namespace, Name: stage.Name}}
	return issue
}

func kargoVerificationIssue(gvr schema.GroupVersionResource, u *unstructured.Unstructured, stage kargo.Stage, v kargo.Verification) Issue {
	msg := "Verification" + freightsPhrase(stage.Current) + " in stage " + stage.Name + " failed"
	if v.Phase == "Error" {
		msg = "Verification" + freightsPhrase(stage.Current) + " in stage " + stage.Name + " couldn't run"
	}
	if v.Message != "" {
		msg += ": " + strings.TrimSpace(v.Message)
	} else {
		msg += "."
	}
	if v.AnalysisRun != nil {
		msg = strings.TrimSuffix(msg, ".") + ". AnalysisRun " + v.AnalysisRun.Name + " has the details."
	}
	at, known := v.FinishTime, !v.FinishTime.IsZero()
	if !known {
		at, known = v.StartTime, !v.StartTime.IsZero()
	}
	issue := newConditionIssue(gvr, kargo.KindStage, stage.Namespace, stage.Name, SeverityCritical, ReasonKargoVerificationFailed,
		msg, at, known, ReasonKargoVerificationFailed, u.GetCreationTimestamp().Time)
	issue.RequiredReads = kargoEvidence(gvr, stage.Namespace)
	// The AnalysisRun's related Issues find the verification through here,
	// and its rows and its Job's are linked back to the Stage.
	if run := v.AnalysisRun; run != nil && run.Name != "" {
		namespace := run.Namespace
		if namespace == "" {
			namespace = stage.Namespace
		}
		issue.AffectedRefs = []Ref{{Group: argoProjGroup, Kind: kindAnalysisRun, Namespace: namespace, Name: run.Name}}
	}
	return issue
}

// kargoEvidence is the read a finding about a namespaced Kargo object rests
// on: listing that kind (Stages for both Kargo findings). Exact, so a caller
// who sees the namespace but may not list the kind doesn't get the failure
// messages and versions its status records.
func kargoEvidence(gvr schema.GroupVersionResource, namespace string) []EvidenceRead {
	return []EvidenceRead{{Group: kargo.Group, Resource: gvr.Resource, Namespace: namespace, Verb: "list", Exact: true}}
}

func freightPhrase(f *kargo.FreightRef) string {
	if f == nil {
		return ""
	}
	return freightsPhrase([]kargo.FreightRef{*f})
}

func freightsPhrase(freight []kargo.FreightRef) string {
	var labels []string
	for _, f := range freight {
		labels = append(labels, f.Labels()...)
	}
	if len(labels) == 0 {
		return ""
	}
	return " of " + strings.Join(labels, ", ")
}

// kargoGenericConditionTypes narrows the generic condition walk for a Kargo
// Stage. Kargo copies the last Promotion's or verification's outcome onto
// Ready; KargoStageIssues already reports the failures, and an Aborted one
// is a cancellation, so that Ready is left out. Healthy and any other Ready
// reason are still read.
func kargoGenericConditionTypes(u *unstructured.Unstructured, condTypes []string) []string {
	ready, ok := conditions.Find(u, "Ready")
	if !ok || ready.Status != "False" {
		return condTypes
	}
	switch ready.Reason {
	case "LastPromotionFailed", "LastPromotionErrored", "LastPromotionAborted",
		"VerificationFailed", "VerificationError", "VerificationAborted":
		out := make([]string, 0, len(condTypes))
		for _, t := range condTypes {
			if t != "Ready" {
				out = append(out, t)
			}
		}
		return out
	}
	return condTypes
}

// kargoStagesDeployingTo are the Stages outside namespaces whose Argo CD
// Applications deploy into one of them, so a view of an app namespace shows
// the Kargo findings about what is deployed there. Each Stage is returned
// once. The findings keep the Stage's own namespace and evidence read, so a
// caller who can't list Stages there still doesn't get them.
func kargoStagesDeployingTo(p Provider, stageGVR schema.GroupVersionResource, namespaces []string) []*unstructured.Unstructured {
	appGVR, ok := argoCDApplicationGVR(p)
	if !ok {
		return nil
	}
	stages, err := p.ListDynamicAllNamespaces(stageGVR)
	if err != nil {
		return nil
	}
	appsByNamespace := map[string]map[string]string{}
	destination := func(app kargo.ObjectRef) string {
		apps, read := appsByNamespace[app.Namespace]
		if !read {
			apps = map[string]string{}
			items, err := p.ListDynamic(appGVR, app.Namespace)
			if err == nil {
				for _, item := range items {
					if !gitops.IsInClusterDestination(item) {
						continue
					}
					ns, _, _ := unstructured.NestedString(item.Object, "spec", "destination", "namespace")
					apps[item.GetName()] = ns
				}
			}
			appsByNamespace[app.Namespace] = apps
		}
		return apps[app.Name]
	}
	var out []*unstructured.Unstructured
	for _, u := range stages {
		if !kargo.Is(u, kargo.KindStage) || slices.Contains(namespaces, u.GetNamespace()) {
			continue
		}
		apps, _ := kargo.ArgoCDApps(u)
		if slices.ContainsFunc(apps, func(app kargo.ObjectRef) bool {
			ns := destination(app)
			return ns != "" && slices.Contains(namespaces, ns)
		}) {
			out = append(out, u)
		}
	}
	return out
}

func argoCDApplicationGVR(p Provider) (schema.GroupVersionResource, bool) {
	for _, gvr := range p.WatchedDynamic() {
		if gvr.Group == argoProjGroup && p.KindForGVR(gvr) == kindApplication {
			return gvr, true
		}
	}
	return schema.GroupVersionResource{}, false
}

// jobControllerProvider resolves a Job's controller, which a Job's own issue
// row doesn't carry.
type jobControllerProvider interface {
	JobController(namespace, name string) (Ref, bool)
}

// addKargoVerificationEdges points the rows of the AnalysisRun a failed Kargo
// verification ran (the run itself, the Pods it rolls up, and its Jobs) at
// the verification, so each says which Stage's check it is.
func addKargoVerificationEdges(verification Issue, edges *[]incidentEdge, p Provider, flat []Issue, groupedByID map[string]Issue) {
	if verification.Group != kargo.Group || verification.Reason != ReasonKargoVerificationFailed {
		return
	}
	jobs, _ := p.(jobControllerProvider)
	for _, run := range verification.AffectedRefs {
		if run.Group != argoProjGroup || run.Kind != kindAnalysisRun {
			continue
		}
		_, _, _, ids := collectRelated(groupedByID, func(yield func(Ref, Issue)) {
			for _, f := range flat {
				own := Ref{Group: f.Group, Kind: f.Kind, Namespace: f.Namespace, Name: f.Name}
				switch {
				case own == run, f.Owner == run:
				case jobs != nil && f.Group == "batch" && f.Kind == "Job":
					if owner, ok := jobs.JobController(f.Namespace, f.Name); !ok || owner != run {
						continue
					}
				default:
					continue
				}
				yield(own, f)
			}
		})
		recordIncidentEdges(edges, verification, factKargoVerification, issuesapi.ConfidenceHigh, ids)
	}
}
