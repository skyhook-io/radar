package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"time"

	"golang.org/x/sync/errgroup"
	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

const ownerAccessReviewScopeLimit = 8

func readOwnerAccessSetup() *k8s.OwnerAccessSetup {
	var setup k8s.OwnerAccessSetup
	if err := json.Unmarshal([]byte(os.Getenv("RADAR_CLOUD_OWNER_ACCESS_SETUP")), &setup); err != nil {
		return nil
	}
	switch setup.Mode {
	case "standard", "custom-owner", "kubernetes", "owner-disabled":
		return &setup
	default:
		return nil
	}
}

type ownerGrantPermission struct {
	resource    string
	subresource string
	verbs       []string
}

type ownerGrantPermissions struct {
	group       string
	permissions []ownerGrantPermission
}

// The chart consistency test checks these against the policy used for the bindings.
var ownerAccessReviewPermissions = map[string]ownerGrantPermissions{
	"argocd": {group: "argoproj.io", permissions: []ownerGrantPermission{
		{resource: "applications", verbs: []string{"get", "patch"}},
	}},
	"cnpg": {group: "postgresql.cnpg.io", permissions: []ownerGrantPermission{
		{resource: "clusters", verbs: []string{"get", "patch"}},
		{resource: "clusters", subresource: "status", verbs: []string{"patch"}},
		{resource: "backups", verbs: []string{"get", "list", "create"}},
		{resource: "scheduledbackups", verbs: []string{"get", "patch"}},
		{resource: "poolers", verbs: []string{"get", "patch"}},
	}},
	"velero": {group: "velero.io", permissions: []ownerGrantPermission{
		{resource: "schedules", verbs: []string{"get", "patch"}},
		{resource: "backups", verbs: []string{"get", "create"}},
	}},
	"velero-download": {group: "velero.io", permissions: []ownerGrantPermission{
		{resource: "downloadrequests", verbs: []string{"create", "get", "delete"}},
	}},
	"velero-delete": {group: "velero.io", permissions: []ownerGrantPermission{
		{resource: "deletebackuprequests", verbs: []string{"list", "create"}},
	}},
}

type ownerAccessReviewResult struct {
	APINotServed      bool `json:"apiNotServed,omitempty"`
	CannotImpersonate bool `json:"cannotImpersonate,omitempty"`
	Verified          int  `json:"verified"`
	Incomplete        int  `json:"incomplete"`
	Unknown           int  `json:"unknown"`
}

type ownerAccountAccessReview struct {
	Added      int `json:"added"`
	Already    int `json:"already"`
	Incomplete int `json:"incomplete"`
	Unknown    int `json:"unknown"`
}

type ownerAccessReviewResponse struct {
	NamespaceScoped   bool                                `json:"namespaceScoped"`
	CheckedScopeCount int                                 `json:"checkedScopeCount"`
	ScopeCount        int                                 `json:"scopeCount"`
	VeleroAPIServed   *bool                               `json:"veleroApiServed,omitempty"`
	Grants            map[string]ownerAccessReviewResult  `json:"grants,omitempty"`
	Account           map[string]ownerAccountAccessReview `json:"account,omitempty"`
}

// The default review checks the grant group alone, so an Owner's independent
// bindings cannot hide a missing managed grant. The optional account review
// compares only the signed-in Owner. Neither accepts arbitrary subjects or verbs.
func (s *Server) handleOwnerAccessReview(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if !cloudMode() || pkgauth.UserTokenMode() || !cloud.OwnerAccessConfigured() {
		s.writeError(w, http.StatusNotFound, "Radar Owner permissions are unavailable")
		return
	}
	if user == nil || user.Username == "" || user.System ||
		!slices.Contains(user.Groups, "radar:owner") && !slices.Contains(user.Groups, "cloud:owner") {
		s.writeError(w, http.StatusForbidden, "Only Cloud Owners can check these permissions")
		return
	}
	if !s.requireConnected(w) {
		return
	}
	conn := k8s.ConnectionFrom(r.Context())
	if conn == nil {
		conn = k8s.Current()
	}
	client := conn.ClientInterface()
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Kubernetes connection unavailable")
		return
	}

	namespaces := k8s.RBACNamespaces()
	if k8s.ForceNamespaceScope {
		namespace := conn.NamespaceScopeTarget()
		if namespace == "" {
			s.writeError(w, http.StatusServiceUnavailable, "Radar namespace scope unavailable")
			return
		}
		namespaces = []string{namespace}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()

	var groups *metav1.APIGroupList
	if discovery := client.Discovery(); discovery != nil && discovery.RESTClient() != nil {
		groups = &metav1.APIGroupList{}
		if err := discovery.RESTClient().Get().AbsPath("/apis").Do(ctx).Into(groups); err != nil {
			groups = nil
		}
	}

	var account *auth.User
	if r.URL.Query().Get("account") == "true" {
		account = user
	}
	result := reviewOwnerAccess(ctx, client, account, cloud.OwnerAccess(), namespaces, groups)
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, result)
}

func reviewOwnerAccess(ctx context.Context, client kubernetes.Interface, user *auth.User, approved, namespaces []string, apiGroups *metav1.APIGroupList) ownerAccessReviewResponse {
	result := ownerAccessReviewResponse{
		NamespaceScoped: len(namespaces) != 0,
	}
	if user == nil {
		result.Grants = make(map[string]ownerAccessReviewResult, len(approved))
		if apiGroups != nil {
			served := slices.ContainsFunc(apiGroups.Groups, func(group metav1.APIGroup) bool {
				return group.Name == ownerAccessReviewPermissions["velero"].group
			})
			result.VeleroAPIServed = &served
		}
	} else {
		result.Account = make(map[string]ownerAccountAccessReview, len(approved))
	}
	scopes := []string{""}
	if result.NamespaceScoped {
		scopes = slices.Clone(namespaces)
		slices.Sort(scopes)
		scopes = slices.Compact(scopes)
	}
	result.ScopeCount = len(scopes)
	scopes = scopes[:min(len(scopes), ownerAccessReviewScopeLimit)]
	result.CheckedScopeCount = len(scopes)

	type scopeReview struct {
		result ownerAccessReviewResult
		votes  []ownerAccessVerdict
	}
	pending := make(map[string]*scopeReview, len(approved))
	var checks errgroup.Group
	checks.SetLimit(6)
	for _, id := range approved {
		grant, known := ownerAccessReviewPermissions[id]
		entry := &scopeReview{}
		pending[id] = entry
		if !known {
			entry.result.Unknown = len(scopes)
			continue
		}
		if apiGroups != nil && !slices.ContainsFunc(apiGroups.Groups, func(group metav1.APIGroup) bool {
			return group.Name == grant.group
		}) {
			entry.result.APINotServed = true
			continue
		}

		group := pkgauth.OwnerAccessGroupPrefix + id
		impersonation, err := client.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authv1.SelfSubjectAccessReview{
			Spec: authv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authv1.ResourceAttributes{
				Verb: "impersonate", Resource: "groups", Name: group,
			}},
		}, metav1.CreateOptions{})
		if err != nil || !impersonation.Status.Allowed && impersonation.Status.EvaluationError != "" {
			entry.result.Unknown = len(scopes)
			continue
		}
		if !impersonation.Status.Allowed {
			entry.result.CannotImpersonate = true
			continue
		}

		username, with := "", []string{group}
		var without []string
		if user != nil {
			username = user.Username
			without = slices.DeleteFunc(slices.Clone(user.Groups), func(value string) bool {
				return value == group
			})
			if !slices.Contains(without, "system:authenticated") {
				without = append(without, "system:authenticated")
			}
			with = append(slices.Clone(without), group)
		}
		entry.votes = make([]ownerAccessVerdict, len(scopes))
		for index, namespace := range scopes {
			checks.Go(func() error {
				entry.votes[index] = reviewOwnerGrant(ctx, client, username, with, without, namespace, grant)
				return nil
			})
		}
	}
	_ = checks.Wait()
	for id, entry := range pending {
		if user != nil {
			if entry.result.APINotServed || entry.result.CannotImpersonate {
				continue
			}
			personal := ownerAccountAccessReview{Unknown: entry.result.Unknown}
			for _, vote := range entry.votes {
				switch vote {
				case ownerAccessAdds:
					personal.Added++
				case ownerAccessAlready:
					personal.Already++
				case ownerAccessIncomplete:
					personal.Incomplete++
				case ownerAccessUnknown:
					personal.Unknown++
				}
			}
			result.Account[id] = personal
			continue
		}

		for _, vote := range entry.votes {
			switch vote {
			case ownerAccessVerified:
				entry.result.Verified++
			case ownerAccessIncomplete:
				entry.result.Incomplete++
			case ownerAccessUnknown:
				entry.result.Unknown++
			}
		}
		result.Grants[id] = entry.result
	}
	return result
}

type ownerAccessVerdict int

const (
	ownerAccessUnknown ownerAccessVerdict = iota
	ownerAccessIncomplete
	ownerAccessVerified
	ownerAccessAlready
	ownerAccessAdds
)

func reviewOwnerGrant(ctx context.Context, client kubernetes.Interface, username string, with, without []string, namespace string, grant ownerGrantPermissions) ownerAccessVerdict {
	verdict := ownerAccessVerified
	if without != nil {
		verdict = ownerAccessAlready
	}
	for _, permission := range grant.permissions {
		for _, verb := range permission.verbs {
			attrs := authv1.ResourceAttributes{
				Group: grant.group, Resource: permission.resource, Subresource: permission.subresource,
				Verb: verb, Namespace: namespace,
			}
			if ctx.Err() != nil {
				return ownerAccessUnknown
			}
			enabled, err := pkgauth.ReviewSubjectAccess(ctx, client, username, with, attrs)
			if err != nil || !enabled.Allowed && enabled.EvaluationError != "" {
				return ownerAccessUnknown
			}
			if !enabled.Allowed {
				return ownerAccessIncomplete
			}
			if without == nil || verdict == ownerAccessAdds {
				continue
			}
			baseline, err := pkgauth.ReviewSubjectAccess(ctx, client, username, without, attrs)
			if err != nil || !baseline.Allowed && baseline.EvaluationError != "" {
				verdict = ownerAccessUnknown
			} else if !baseline.Allowed {
				verdict = ownerAccessAdds
			}
		}
	}
	return verdict
}
