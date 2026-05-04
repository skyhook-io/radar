package server

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/k8s"
)

// DashboardGitOpsControllers summarizes the health of GitOps controller
// pods discovered in the cluster. Surfaced on the Home dashboard so an
// operator can spot "source-controller is CrashLoopBackOff" before
// drilling into individual GitOps applications and seeing the
// downstream symptoms.
//
// Status is the aggregate roll-up across all detected controllers:
// "healthy" when all controllers have all expected pods Ready;
// "degraded" when any controller has fewer Ready pods than total;
// "missing" when no controllers are detected at all (rare — caller
// suppresses the entire payload in that case so the card disappears).
type DashboardGitOpsControllers struct {
	// Status is the worst-case aggregate across all controllers:
	// "healthy" | "degraded" | "missing". Drives the card's overall tone.
	Status string `json:"status"`
	// Controllers lists each discovered controller. Empty slice means
	// nothing was detected — caller should set GitOpsControllers to nil
	// instead of emitting an empty card.
	Controllers []DashboardGitOpsController `json:"controllers"`
}

// DashboardGitOpsController is a single controller's pod health row.
type DashboardGitOpsController struct {
	// Name is the controller's pod label value, used as a stable
	// identifier. Examples: "argocd-application-controller",
	// "kustomize-controller", "source-controller".
	Name string `json:"name"`
	// Tool identifies the GitOps system: "argocd" or "flux".
	Tool string `json:"tool"`
	// Namespace where the controller's pods were found.
	Namespace string `json:"namespace"`
	// Ready is the count of pods that are running and Ready.
	Ready int `json:"ready"`
	// Total is the total pod count for this controller. Argo controllers
	// often have 2 (HA), Flux controllers typically 1.
	Total int `json:"total"`
	// Status is "healthy" (all Ready), "degraded" (some Ready, some not),
	// "crashing" (any pod in CrashLoopBackOff), or "pending" (pods exist
	// but none Ready and none crashing).
	Status string `json:"status"`
	// CrashReason is set when at least one pod is in CrashLoopBackOff or
	// Error; identifies the kind of crash so the operator knows where to
	// start digging.
	CrashReason string `json:"crashReason,omitempty"`
}

// gitopsControllerProbe describes what to look for: a label selector
// (key=value) in a typical install namespace. Mirrors the catalog in
// pkg/gitops/insights/finalizers.go but kept independent — that file
// targets finalizer resolution while this one targets dashboard
// discovery; the duplication is small and keeps the two surfaces
// independently evolvable.
type gitopsControllerProbe struct {
	Name      string
	Tool      string
	Namespace string
	LabelKey  string
	LabelVal  string
}

var gitopsControllerProbes = []gitopsControllerProbe{
	// Argo CD: single application-controller (often deployed as a
	// 2-replica StatefulSet for HA in larger installs).
	{
		Name: "argocd-application-controller", Tool: "argocd", Namespace: "argocd",
		LabelKey: "app.kubernetes.io/name", LabelVal: "argocd-application-controller",
	},
	// Argo CD: server (the API/UI). Optional but useful — without it,
	// kubectl-only operations work but the UI/CLI commands fail.
	{
		Name: "argocd-server", Tool: "argocd", Namespace: "argocd",
		LabelKey: "app.kubernetes.io/name", LabelVal: "argocd-server",
	},
	// Argo CD: repo-server (does the manifest rendering).
	{
		Name: "argocd-repo-server", Tool: "argocd", Namespace: "argocd",
		LabelKey: "app.kubernetes.io/name", LabelVal: "argocd-repo-server",
	},
	// Flux: per-controller catalog. The operator's actual install may
	// not include all of them (e.g. notification-controller is optional);
	// missing controllers are simply omitted from the summary.
	{Name: "source-controller", Tool: "flux", Namespace: "flux-system", LabelKey: "app", LabelVal: "source-controller"},
	{Name: "kustomize-controller", Tool: "flux", Namespace: "flux-system", LabelKey: "app", LabelVal: "kustomize-controller"},
	{Name: "helm-controller", Tool: "flux", Namespace: "flux-system", LabelKey: "app", LabelVal: "helm-controller"},
	{Name: "notification-controller", Tool: "flux", Namespace: "flux-system", LabelKey: "app", LabelVal: "notification-controller"},
	{Name: "image-reflector-controller", Tool: "flux", Namespace: "flux-system", LabelKey: "app", LabelVal: "image-reflector-controller"},
}

// getDashboardGitOpsControllers walks the static probe catalog, queries
// matching pods from the cache, and rolls up the per-controller health
// into a single response. Returns nil when no controllers are detected
// — the home dashboard suppresses the card on non-GitOps clusters
// rather than rendering an empty placeholder.
//
// RBAC note: the call uses the regular pod lister, which respects the
// caller's namespace allowlist. Operators with no access to argocd /
// flux-system will see the card hidden — preferable to showing
// "controllers missing" when really we just can't see them.
func (s *Server) getDashboardGitOpsControllers(cache *k8s.ResourceCache, allowedNamespaces []string) *DashboardGitOpsControllers {
	if cache == nil || cache.Pods() == nil {
		return nil
	}
	allowed := map[string]bool{}
	allowAll := allowedNamespaces == nil
	for _, ns := range allowedNamespaces {
		allowed[ns] = true
	}

	out := &DashboardGitOpsControllers{}
	for _, probe := range gitopsControllerProbes {
		if !allowAll && !allowed[probe.Namespace] {
			continue
		}
		pods, err := cache.Pods().Pods(probe.Namespace).List(labels.Everything())
		if err != nil {
			continue
		}
		var matched []*corev1.Pod
		for _, p := range pods {
			if p.Labels[probe.LabelKey] == probe.LabelVal {
				matched = append(matched, p)
			}
		}
		if len(matched) == 0 {
			continue
		}
		ctrl := summarizeControllerForDashboard(probe, matched)
		out.Controllers = append(out.Controllers, ctrl)
	}

	if len(out.Controllers) == 0 {
		return nil
	}
	out.Status = aggregateControllerStatus(out.Controllers)
	return out
}

// summarizeControllerForDashboard distills the pod slice into the
// per-controller card row. Logic mirrors summarizeControllerHealth in
// gitops_handlers.go but emits structured fields rather than a string
// (the dashboard card renders bespoke chrome around the data).
func summarizeControllerForDashboard(probe gitopsControllerProbe, pods []*corev1.Pod) DashboardGitOpsController {
	ready := 0
	crashing := 0
	pending := 0
	var crashReason string
	for _, p := range pods {
		for _, cs := range p.Status.ContainerStatuses {
			if cs.State.Waiting != nil && (cs.State.Waiting.Reason == "CrashLoopBackOff" || cs.State.Waiting.Reason == "Error") {
				crashing++
				if crashReason == "" {
					crashReason = cs.State.Waiting.Reason
				}
				break
			}
		}
		if isPodReady(p) {
			ready++
		}
		if p.Status.Phase == corev1.PodPending {
			pending++
		}
	}
	status := "healthy"
	switch {
	case crashing > 0:
		status = "crashing"
	case ready < len(pods):
		if pending > 0 && ready == 0 {
			status = "pending"
		} else {
			status = "degraded"
		}
	}
	return DashboardGitOpsController{
		Name:        probe.Name,
		Tool:        probe.Tool,
		Namespace:   probe.Namespace,
		Ready:       ready,
		Total:       len(pods),
		Status:      status,
		CrashReason: crashReason,
	}
}

// aggregateControllerStatus rolls up multiple controller statuses into
// one card-level status. The worst per-controller state dominates: any
// crashing controller drives the card to "crashing"; any degraded /
// pending controller drives "degraded"; otherwise "healthy".
//
// We distinguish "crashing" from "degraded" at the aggregate level so
// the home card's tone (red vs amber) matches the severity an operator
// expects when scanning the dashboard at a glance.
func aggregateControllerStatus(ctrls []DashboardGitOpsController) string {
	worst := "healthy"
	rank := func(s string) int {
		switch s {
		case "crashing":
			return 3
		case "degraded", "pending":
			return 2
		case "healthy":
			return 1
		default:
			return 0
		}
	}
	for _, c := range ctrls {
		if rank(c.Status) > rank(worst) {
			worst = c.Status
			// Normalize "pending" to "degraded" at the card level —
			// operationally the same triage path (look at the pod) and
			// keeping the aggregate vocabulary tight prevents the
			// frontend from needing four separate tone branches.
			if worst == "pending" {
				worst = "degraded"
			}
		}
	}
	return worst
}
