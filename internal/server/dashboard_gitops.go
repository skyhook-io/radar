package server

import (
	"log"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/k8s"
)

// DashboardGitOpsControllers summarizes the health of GitOps controller
// pods discovered in the cluster. Surfaced on the Home dashboard so an
// operator can spot "source-controller is CrashLoopBackOff" before
// drilling into individual GitOps applications and seeing the
// downstream symptoms.
//
// The aggregate Status field collapses per-controller statuses to one
// of three tones; the whole struct is nil (not "missing") when no
// controllers were detected at all, so the home dashboard can suppress
// the card entirely on non-GitOps clusters.
type DashboardGitOpsControllers struct {
	// Status is the worst-case aggregate across all controllers,
	// normalized for the card's overall tone:
	//   ctrlStatusHealthy  — all controllers have all expected pods Ready
	//   ctrlStatusDegraded — any controller has fewer Ready pods than total,
	//                        or any pod is Pending
	//   ctrlStatusCrashing — any controller pod is CrashLoopBackOff/Error
	// Per-controller "pending" rolls up to "degraded" at this level so the
	// frontend only branches on three tones.
	Status string `json:"status"`
	// Controllers lists each discovered controller. When empty, the parent
	// payload is set to nil rather than emitted as an empty card.
	Controllers []DashboardGitOpsController `json:"controllers"`
}

// DashboardGitOpsController is a single controller's pod health row.
type DashboardGitOpsController struct {
	// Name is the controller's pod label value, used as a stable
	// identifier. Examples: "argocd-application-controller",
	// "kustomize-controller", "source-controller".
	Name string `json:"name"`
	// Tool identifies the GitOps system: ctrlToolArgoCD or ctrlToolFluxCD.
	// Frontend branches on this to label the section ("Argo CD" vs "Flux CD").
	Tool string `json:"tool"`
	// Namespace where the controller's pods were found.
	Namespace string `json:"namespace"`
	// Ready is the count of pods that are running and Ready. Invariant:
	// 0 <= Ready <= Total. Caller (summarizeControllerForDashboard) is the
	// sole producer; callers should not set these fields directly.
	Ready int `json:"ready"`
	// Total is the total pod count for this controller. Argo controllers
	// often have 2 (HA), Flux controllers typically 1.
	Total int `json:"total"`
	// Status is one of: ctrlStatusHealthy, ctrlStatusDegraded,
	// ctrlStatusCrashing, ctrlStatusPending. Aggregate Status normalizes
	// pending → degraded (see DashboardGitOpsControllers.Status); per-row
	// it stays distinct for finer-grained UI.
	Status string `json:"status"`
	// CrashReason is set when at least one pod is in CrashLoopBackOff or
	// Error; identifies the kind of crash so the operator knows where to
	// start digging.
	CrashReason string `json:"crashReason,omitempty"`
}

// Status + tool string constants. Keeping these as named values rather
// than free strings catches typo-class regressions at compile time and
// gives a single place to grep when wiring the matching TS union literal
// in packages/k8s-ui/src/api/client.ts.
const (
	ctrlStatusHealthy  = "healthy"
	ctrlStatusDegraded = "degraded"
	ctrlStatusCrashing = "crashing"
	ctrlStatusPending  = "pending"

	ctrlToolArgoCD = "argocd"
	ctrlToolFluxCD = "fluxcd"
)

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
		Name: "argocd-application-controller", Tool: ctrlToolArgoCD, Namespace: "argocd",
		LabelKey: "app.kubernetes.io/name", LabelVal: "argocd-application-controller",
	},
	// Argo CD: server (API + UI). Without it, controller still reconciles
	// but the Argo CLI/UI is unreachable — non-fatal but worth surfacing.
	{
		Name: "argocd-server", Tool: ctrlToolArgoCD, Namespace: "argocd",
		LabelKey: "app.kubernetes.io/name", LabelVal: "argocd-server",
	},
	// Argo CD: repo-server (manifest rendering / git-clone). Load-bearing —
	// without it, no sync attempts succeed because manifests can't be rendered.
	{
		Name: "argocd-repo-server", Tool: ctrlToolArgoCD, Namespace: "argocd",
		LabelKey: "app.kubernetes.io/name", LabelVal: "argocd-repo-server",
	},
	// Flux: per-controller catalog. The operator's actual install may
	// not include all of them (e.g. notification-controller is optional);
	// missing controllers are simply omitted from the summary.
	{Name: "source-controller", Tool: ctrlToolFluxCD, Namespace: "flux-system", LabelKey: "app", LabelVal: "source-controller"},
	{Name: "kustomize-controller", Tool: ctrlToolFluxCD, Namespace: "flux-system", LabelKey: "app", LabelVal: "kustomize-controller"},
	{Name: "helm-controller", Tool: ctrlToolFluxCD, Namespace: "flux-system", LabelKey: "app", LabelVal: "helm-controller"},
	{Name: "notification-controller", Tool: ctrlToolFluxCD, Namespace: "flux-system", LabelKey: "app", LabelVal: "notification-controller"},
	{Name: "image-reflector-controller", Tool: ctrlToolFluxCD, Namespace: "flux-system", LabelKey: "app", LabelVal: "image-reflector-controller"},
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
			// Distinguish RBAC denial from other lookup failures. Both
			// paths skip the probe (the card silently misses controllers
			// the operator can't see), but logging the RBAC case gives
			// ops a way to discover that GitOps controllers exist but
			// the user's token can't reach their namespace — otherwise
			// "card hidden" reads identically to "no GitOps installed".
			if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
				log.Printf("[dashboard/gitops] RBAC denied listing pods in %s for controller probe %s — controller may be running but user lacks namespace access", probe.Namespace, probe.Name)
			} else {
				log.Printf("[dashboard/gitops] Failed to list pods in %s for controller probe %s: %v", probe.Namespace, probe.Name, err)
			}
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
	status := ctrlStatusHealthy
	switch {
	case crashing > 0:
		status = ctrlStatusCrashing
	case ready < len(pods):
		if pending > 0 && ready == 0 {
			status = ctrlStatusPending
		} else {
			status = ctrlStatusDegraded
		}
	}
	// Defensive cap: Ready should never exceed total pod count, but a
	// future double-count bug in the loop above (e.g. counting a pod
	// once for being Running and again for being Ready) would silently
	// emit Ready > Total. The frontend renders "ready/total" verbatim,
	// so 4/2 would ship straight to users. Trivial guard.
	if ready > len(pods) {
		ready = len(pods)
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
	worst := ctrlStatusHealthy
	rank := func(s string) int {
		switch s {
		case ctrlStatusCrashing:
			return 3
		case ctrlStatusDegraded, ctrlStatusPending:
			return 2
		case ctrlStatusHealthy:
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
			if worst == ctrlStatusPending {
				worst = ctrlStatusDegraded
			}
		}
	}
	return worst
}
