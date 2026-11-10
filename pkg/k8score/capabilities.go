package k8score

import (
	"context"
	"log"
	"strings"

	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// CanI checks if the current user/service account can perform an action using
// SelfSubjectAccessReview. The group parameter specifies the API group
// (empty string for core resources like pods, secrets).
// Returns (allowed, apiErr) where apiErr=true means the API call itself failed
// (distinct from RBAC denial where allowed=false, apiErr=false).
func CanI(ctx context.Context, client kubernetes.Interface, namespace, group, resource, verb string) (allowed bool, apiErr bool) {
	if ctx.Err() != nil {
		return false, true
	}

	if client == nil {
		log.Printf("Warning: K8s client nil in CanI check for %s %s", verb, resource)
		return false, true
	}

	// "pods/exec" names a subresource. RBAC matches either spelling, but webhook
	// authorizers (e.g. GKE IAM) only recognize the subresource in its own field.
	subresource := ""
	if i := strings.IndexByte(resource, '/'); i >= 0 {
		resource, subresource = resource[:i], resource[i+1:]
	}
	review := &authv1.SelfSubjectAccessReview{
		Spec: authv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authv1.ResourceAttributes{
				Namespace:   namespace, // Empty = cluster-wide
				Group:       group,     // API group (empty = core)
				Verb:        verb,
				Resource:    resource,
				Subresource: subresource,
			},
		},
	}

	result, err := client.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("Warning: SelfSubjectAccessReview failed for %s %s: %v", verb, resource, err)
		}
		return false, true
	}

	return result.Status.Allowed, false
}
