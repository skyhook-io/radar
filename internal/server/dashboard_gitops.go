package server

import (
	"log"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/k8s"
)

// DashboardGitOpsControllers is the Home dashboard summary of in-cluster
// GitOps controller pod health. Nil (not "empty Controllers") when no
// controllers are discovered, so the card disappears on non-GitOps clusters.
//
// Status collapses per-controller rows: per-row "pending" rolls up to
// "degraded" so the card-level tone only branches on healthy/degraded/crashing.
type DashboardGitOpsControllers struct {
	Status      string                      `json:"status"`
	Controllers []DashboardGitOpsController `json:"controllers"`
}

// DashboardGitOpsController is a single controller's pod health row.
// summarizeControllerForDashboard is the sole producer; the Ready/Total
// invariant (0 <= Ready <= Total) is established there.
type DashboardGitOpsController struct {
	Name      string `json:"name"`
	Tool      string `json:"tool"`
	Namespace string `json:"namespace"`
	Ready     int    `json:"ready"`
	Total     int    `json:"total"`
	// Status: one of ctrlStatus*. The aggregate (parent) collapses pending
	// into degraded; per-row keeps them distinct.
	Status string `json:"status"`
	// CrashReason is the pod-level reason (CrashLoopBackOff, Error) when at
	// least one pod is crashing — points the operator at where to dig.
	CrashReason string `json:"crashReason,omitempty"`
}

const (
	ctrlStatusHealthy  = "healthy"
	ctrlStatusDegraded = "degraded"
	ctrlStatusCrashing = "crashing"
	ctrlStatusPending  = "pending"

	ctrlToolArgoCD = "argocd"
	ctrlToolFluxCD = "fluxcd"
)

// gitopsControllerProbe is a label selector + namespace pair to look up.
// Independent of pkg/gitops/insights/finalizers.go's catalog: that one is
// for finalizer resolution, this one for dashboard discovery.
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
	health := summarizeControllerPods(pods)
	status := ctrlStatusHealthy
	switch {
	case health.Crashing > 0:
		status = ctrlStatusCrashing
	case health.Ready < health.Total:
		if health.Pending > 0 && health.Ready == 0 {
			status = ctrlStatusPending
		} else {
			status = ctrlStatusDegraded
		}
	}
	return DashboardGitOpsController{
		Name:        probe.Name,
		Tool:        probe.Tool,
		Namespace:   probe.Namespace,
		Ready:       health.Ready,
		Total:       health.Total,
		Status:      status,
		CrashReason: health.CrashReason,
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
