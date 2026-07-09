package cloudinstall

import (
	"context"
	"fmt"

	authv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// InstallPreflight checks whether the caller's kube identity can create
// everything the cloud-enabled Radar chart renders, BEFORE we mint a Cloud token
// — a failed install after approval would orphan a never-connected cluster plus a
// live token. Enabling cloud mode requires provisioning the impersonation RBAC
// (a ClusterRole with `impersonate` + ClusterRoleBindings to admin/edit/view),
// which K8s privilege-escalation prevention gates behind `escalate`/`bind`. The
// only identity that can legitimately do this is the operator's own kubeconfig —
// which is exactly what the `radar cloud install` driver runs as.
//
// The create-verb checks are authoritative (a plain user is caught cleanly).
// The escalate/bind checks are advisory: a full cluster-admin can create a
// binding to a role it fully possesses WITHOUT holding the `bind` verb, so a
// denied `bind`/`escalate` SSAR is a false negative for such users. The
// definitive escalation answer therefore comes from the install attempt itself
// (its apiserver RBAC error), not from these advisory probes.

// PreflightResult splits blocking failures (the caller can't create the chart's
// objects at all) from advisory ones (escalation probes that may false-negative).
type PreflightResult struct {
	// Blocking lists denied create permissions — a hard stop; do not mint a token.
	Blocking []string
	// Advisory lists denied escalate/bind probes. Non-fatal on their own; surfaced
	// so the operator understands a later "cannot bind/escalate" install error.
	Advisory []string
}

// OK reports whether the install may proceed (no blocking denials).
func (r PreflightResult) OK() bool { return len(r.Blocking) == 0 }

type preflightCheck struct {
	desc     string
	blocking bool
	attrs    authv1.ResourceAttributes
}

// installPreflightChecks is the permission set the cloud-enabled chart needs.
// nsExists suppresses the namespace-create check when the target namespace is
// already present (the caller may hold create-in-ns without create-namespaces).
func installPreflightChecks(namespace string, nsExists bool) []preflightCheck {
	rbacGroup := "rbac.authorization.k8s.io"
	checks := []preflightCheck{
		{"create Deployments", true, authv1.ResourceAttributes{Namespace: namespace, Group: "apps", Resource: "deployments", Verb: "create"}},
		{"create ServiceAccounts", true, authv1.ResourceAttributes{Namespace: namespace, Resource: "serviceaccounts", Verb: "create"}},
		{"create Services", true, authv1.ResourceAttributes{Namespace: namespace, Resource: "services", Verb: "create"}},
		{"create Secrets", true, authv1.ResourceAttributes{Namespace: namespace, Resource: "secrets", Verb: "create"}},
		// upsertTokenSecret does get+update only when the token Secret already
		// exists (a re-run after an uninstalled-but-not-purged release) — advisory
		// so a fresh install (the common case, create-only) isn't false-blocked.
		{"update Secrets (only if a prior token Secret exists)", false, authv1.ResourceAttributes{Namespace: namespace, Resource: "secrets", Verb: "update"}},
		{"create ClusterRoles", true, authv1.ResourceAttributes{Group: rbacGroup, Resource: "clusterroles", Verb: "create"}},
		{"create ClusterRoleBindings", true, authv1.ResourceAttributes{Group: rbacGroup, Resource: "clusterrolebindings", Verb: "create"}},
		// rbac.selfUpgrade=true (which the driver sets) renders a namespaced
		// Role + RoleBinding, so the caller needs to create those too.
		{"create Roles", true, authv1.ResourceAttributes{Namespace: namespace, Group: rbacGroup, Resource: "roles", Verb: "create"}},
		{"create RoleBindings", true, authv1.ResourceAttributes{Namespace: namespace, Group: rbacGroup, Resource: "rolebindings", Verb: "create"}},
		// Privilege-escalation gates (advisory — see file header): the chart's
		// ClusterRole carries `impersonate`, and the cloud bindings reference
		// admin/edit/view, so a non-admin caller needs escalate + bind.
		{"escalate ClusterRoles (for the impersonation role)", false, authv1.ResourceAttributes{Group: rbacGroup, Resource: "clusterroles", Verb: "escalate"}},
		{"bind the admin ClusterRole", false, authv1.ResourceAttributes{Group: rbacGroup, Resource: "clusterroles", Name: "admin", Verb: "bind"}},
		{"bind the edit ClusterRole", false, authv1.ResourceAttributes{Group: rbacGroup, Resource: "clusterroles", Name: "edit", Verb: "bind"}},
		{"bind the view ClusterRole", false, authv1.ResourceAttributes{Group: rbacGroup, Resource: "clusterroles", Name: "view", Verb: "bind"}},
	}
	if !nsExists {
		checks = append(checks, preflightCheck{"create the target Namespace", true, authv1.ResourceAttributes{Resource: "namespaces", Verb: "create"}})
	}
	return checks
}

// InstallPreflight runs the SSAR batch. A non-nil error means an SSAR call itself
// failed (API unreachable / RBAC to even self-review) — distinct from a clean
// "denied" result carried in PreflightResult.
func InstallPreflight(ctx context.Context, client kubernetes.Interface, namespace string) (PreflightResult, error) {
	if client == nil {
		return PreflightResult{}, fmt.Errorf("preflight: nil kubernetes client")
	}
	// Skip the namespace-create check if it already exists — the caller may be
	// allowed to create objects inside it without holding create-namespaces.
	// A get error other than NotFound is ignored (treated as "exists"): we don't
	// want a transient read blip to demand a create permission the user may lack.
	nsExists := true
	if _, err := client.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{}); err != nil && apierrors.IsNotFound(err) {
		nsExists = false
	}
	var res PreflightResult
	for _, c := range installPreflightChecks(namespace, nsExists) {
		attrs := c.attrs
		review := &authv1.SelfSubjectAccessReview{
			Spec: authv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &attrs},
		}
		out, err := client.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
		if err != nil {
			return PreflightResult{}, fmt.Errorf("preflight SSAR failed for %q: %w", c.desc, err)
		}
		if out.Status.Allowed {
			continue
		}
		if c.blocking {
			res.Blocking = append(res.Blocking, c.desc)
		} else {
			res.Advisory = append(res.Advisory, c.desc)
		}
	}
	return res, nil
}
