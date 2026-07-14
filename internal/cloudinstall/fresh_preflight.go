package cloudinstall

import (
	"context"
	"errors"
	"fmt"
	"strings"

	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/restmapper"
)

const freshInstallPreflightName = "fresh-install preflight"

// FreshInstallPreflightOptions describes the exact non-secret chart target
// rendered by Prepare before Cloud enrollment.
type FreshInstallPreflightOptions struct {
	Namespace      string
	ReleaseName    string
	TargetManifest string
}

// FreshInstallPreflight proves the live Kubernetes mutations for a prepared
// fresh install before Hub creates a cluster or mints a token. When the target
// namespace exists, every rendered object plus the Cloud token and Helm storage
// Secrets receives a real server dry-run create. That makes RBAC bind/escalate
// admission authoritative rather than guessing from broad permission probes.
//
// Kubernetes cannot dry-run a namespaced create against a namespace that does
// not exist because a dry-run Namespace is deliberately not persisted. In that
// case this function still dry-runs the exact Namespace and cluster-scoped
// creates, checks exact resource/verb SSARs for every deferred namespaced
// operation, and returns an advisory that admission and collision checks for
// those namespaced objects must occur during the real install.
//
// The pre-enrollment manifest contains placeholder Cloud URL/cluster values.
// This proves the resource and RBAC mutation shape, not byte-identical
// admission of the two final scalar values; PreparedInstall.Validate performs a
// final server render after approval before any Secret is written.
func FreshInstallPreflight(
	ctx context.Context,
	kc kubernetes.Interface,
	dc dynamic.Interface,
	discoveryClient discovery.DiscoveryInterface,
	opts FreshInstallPreflightOptions,
) (PreflightResult, error) {
	if kc == nil || dc == nil || discoveryClient == nil {
		return PreflightResult{}, errors.New("fresh-install preflight: nil kubernetes, dynamic, or discovery client")
	}
	if strings.TrimSpace(opts.Namespace) == "" || strings.TrimSpace(opts.ReleaseName) == "" {
		return PreflightResult{}, errors.New("fresh-install preflight: namespace and release name are required")
	}
	if strings.TrimSpace(opts.TargetManifest) == "" {
		return PreflightResult{}, errors.New("fresh-install preflight: target manifest is required")
	}

	var result PreflightResult
	if recordHiddenSecretBlocker(opts.TargetManifest, "install", &result) {
		return result, nil
	}
	groupResources, err := restmapper.GetAPIGroupResources(discoveryClient)
	if err != nil {
		return result, fmt.Errorf("fresh-install preflight: discover Kubernetes resources: %w", err)
	}
	mapper := restmapper.NewDiscoveryRESTMapper(groupResources)
	target, err := parseAdoptionManifest(opts.TargetManifest, opts.Namespace, mapper)
	if err != nil {
		if meta.IsNoMatchError(err) {
			result.Blocking = append(result.Blocking, fmt.Sprintf("map target Helm manifest to this cluster: %v", err))
			return result, nil
		}
		return result, fmt.Errorf("fresh-install preflight: parse target Helm manifest: %w", err)
	}
	applyHelmTrackingMetadata(target, opts.ReleaseName, opts.Namespace)

	_, err = kc.CoreV1().Namespaces().Get(ctx, opts.Namespace, metav1.GetOptions{})
	switch {
	case err == nil:
		if err := preflightFreshChartCreates(ctx, dc, target, &result); err != nil {
			return result, err
		}
		if err := preflightTokenSecret(ctx, kc, opts.Namespace, CloudTokenSecretName, &result, freshInstallPreflightName, "failed-install"); err != nil {
			return result, err
		}
		if err := preflightFreshHelmStorage(ctx, kc, opts, true, &result); err != nil {
			return result, err
		}
	case apierrors.IsNotFound(err):
		if err := preflightFreshNamespace(ctx, kc, opts.Namespace, &result); err != nil {
			return result, err
		}
		clusterScoped, namespaced := splitPreflightResources(target)
		if err := preflightFreshChartCreates(ctx, dc, clusterScoped, &result); err != nil {
			return result, err
		}
		if err := preflightDeferredNamespacedCreates(ctx, kc, namespaced, &result); err != nil {
			return result, err
		}
		if err := preflightDeferredTokenSecret(ctx, kc, opts.Namespace, &result); err != nil {
			return result, err
		}
		if err := preflightFreshHelmStorage(ctx, kc, opts, false, &result); err != nil {
			return result, err
		}
		result.Advisory = append(result.Advisory, fmt.Sprintf(
			"target namespace %q does not exist: its create and all cluster-scoped creates were server-dry-run, and exact create permissions were checked for namespaced resources; Kubernetes admission and live name-collision checks for those namespaced objects are deferred until installation creates the namespace",
			opts.Namespace))
	case err != nil:
		return result, fmt.Errorf("fresh-install preflight: inspect target namespace %q: %w", opts.Namespace, err)
	}
	return result, nil
}

// preflightFreshChartCreates sends the create request directly. The preceding
// PrepareFreshInstall server dry-run already ran Helm's existing-resource GETs
// and ownership checks. Repeating them here adds no gate; a server dry-run
// create itself performs authorization, admission, and collision detection.
func preflightFreshChartCreates(
	ctx context.Context,
	dc dynamic.Interface,
	target map[adoptionResourceKey]adoptionResource,
	result *PreflightResult,
) error {
	created := make(map[adoptionResourceKey]bool)
	notedEphemeralRoles := make(map[adoptionResourceKey]bool)
	for _, key := range sortedAdoptionResourceKeys(target) {
		desired := target[key]
		client := adoptionResourceClient(dc, desired)
		_, mutationErr := client.Create(ctx, desired.object.DeepCopy(), metav1.CreateOptions{
			DryRun:       []string{metav1.DryRunAll},
			FieldManager: mutationPreflightFieldManager,
		})
		if mutationErr == nil {
			created[key] = true
			continue
		}

		// A successful dry-run Role create already proves that the caller may
		// grant every rendered rule. Its dry-run object is not persisted, though,
		// so Kubernetes cannot resolve a following generated RoleBinding the way
		// it can during Helm's real kind-ordered create. Suppress only the precise
		// NotFound for that successfully checked prepared role.
		if roleKey, ok := preparedBindingRole(desired, target); ok && created[roleKey] && missingPreparedRole(mutationErr, roleKey) {
			if !notedEphemeralRoles[roleKey] {
				result.Advisory = append(result.Advisory, fmt.Sprintf(
					"create %s: Kubernetes could not complete bind admission because referenced %s %q exists only for the duration of its successful dry-run; Helm creates that proven role before its bindings during the real install",
					desired.description(), roleKey.kind, roleKey.name))
				notedEphemeralRoles[roleKey] = true
			}
			continue
		}
		if err := recordMutationError(result, freshInstallPreflightName, "create "+desired.description(), mutationErr); err != nil {
			return err
		}
	}
	return nil
}

func preparedBindingRole(
	binding adoptionResource,
	target map[adoptionResourceKey]adoptionResource,
) (adoptionResourceKey, bool) {
	if binding.key.kind != "RoleBinding" && binding.key.kind != "ClusterRoleBinding" {
		return adoptionResourceKey{}, false
	}
	apiGroup, found, _ := unstructured.NestedString(binding.object.Object, "roleRef", "apiGroup")
	if !found || apiGroup != "rbac.authorization.k8s.io" {
		return adoptionResourceKey{}, false
	}
	kind, found, _ := unstructured.NestedString(binding.object.Object, "roleRef", "kind")
	if !found || (kind != "Role" && kind != "ClusterRole") {
		return adoptionResourceKey{}, false
	}
	name, found, _ := unstructured.NestedString(binding.object.Object, "roleRef", "name")
	if !found || name == "" {
		return adoptionResourceKey{}, false
	}
	namespace := ""
	if kind == "Role" {
		namespace = binding.key.namespace
	}
	key := adoptionResourceKey{group: apiGroup, kind: kind, namespace: namespace, name: name}
	_, prepared := target[key]
	return key, prepared
}

func missingPreparedRole(err error, role adoptionResourceKey) bool {
	if !apierrors.IsNotFound(err) {
		return false
	}
	var status apierrors.APIStatus
	if !errors.As(err, &status) || status.Status().Details == nil {
		return false
	}
	details := status.Status().Details
	if details.Name != role.name || (details.Group != "" && details.Group != role.group) {
		return false
	}
	got := strings.ToLower(details.Kind)
	want := strings.ToLower(role.kind)
	return got == want || got == want+"s"
}

func preflightFreshNamespace(ctx context.Context, kc kubernetes.Interface, namespace string, result *PreflightResult) error {
	// This matches ensureNamespace, the actual mutation performed immediately
	// before the token Secret is created.
	object := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
	_, err := kc.CoreV1().Namespaces().Create(ctx, object, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
	return recordMutationError(result, freshInstallPreflightName, fmt.Sprintf("create Namespace %q", namespace), err)
}

func splitPreflightResources(resources map[adoptionResourceKey]adoptionResource) (
	map[adoptionResourceKey]adoptionResource,
	map[adoptionResourceKey]adoptionResource,
) {
	clusterScoped := make(map[adoptionResourceKey]adoptionResource)
	namespaced := make(map[adoptionResourceKey]adoptionResource)
	for key, resource := range resources {
		if resource.mapping.Scope.Name() == meta.RESTScopeNameNamespace {
			namespaced[key] = resource
		} else {
			clusterScoped[key] = resource
		}
	}
	return clusterScoped, namespaced
}

func preflightDeferredNamespacedCreates(
	ctx context.Context,
	kc kubernetes.Interface,
	resources map[adoptionResourceKey]adoptionResource,
	result *PreflightResult,
) error {
	for _, key := range sortedAdoptionResourceKeys(resources) {
		resource := resources[key]
		gvr := resource.mapping.Resource
		if err := requireMutationPermission(ctx, kc, result, freshInstallPreflightName, preflightCheck{
			desc:     "create " + resource.description(),
			blocking: true,
			attrs: authv1.ResourceAttributes{
				Namespace: resource.key.namespace,
				Verb:      "create",
				Group:     gvr.Group,
				Version:   gvr.Version,
				Resource:  gvr.Resource,
			},
		}); err != nil {
			return err
		}
	}
	return nil
}

func preflightDeferredTokenSecret(ctx context.Context, kc kubernetes.Interface, namespace string, result *PreflightResult) error {
	for _, check := range []preflightCheck{
		{
			desc:     fmt.Sprintf("create Cloud token Secret %q in namespace %q", CloudTokenSecretName, namespace),
			blocking: true,
			attrs: authv1.ResourceAttributes{
				Namespace: namespace, Resource: "secrets", Verb: "create",
			},
		},
		{
			desc:     fmt.Sprintf("delete Cloud token Secret %q during failed-install cleanup", CloudTokenSecretName),
			blocking: true,
			attrs: authv1.ResourceAttributes{
				Namespace: namespace, Resource: "secrets", Name: CloudTokenSecretName, Verb: "delete",
			},
		},
	} {
		if err := requireMutationPermission(ctx, kc, result, freshInstallPreflightName, check); err != nil {
			return err
		}
	}
	return nil
}

func preflightFreshHelmStorage(
	ctx context.Context,
	kc kubernetes.Interface,
	opts FreshInstallPreflightOptions,
	namespaceExists bool,
	result *PreflightResult,
) error {
	name := helmReleaseSecretName(opts.ReleaseName, 1)
	if namespaceExists {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: opts.Namespace,
				Labels: map[string]string{
					"owner": "helm", "name": opts.ReleaseName,
					"status": "pending-install", "version": "1",
				},
			},
			Type: corev1.SecretType("helm.sh/release.v1"),
			Data: map[string][]byte{"release": []byte("preflight-placeholder")},
		}
		_, err := kc.CoreV1().Secrets(opts.Namespace).Create(ctx, secret, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
		if err := recordMutationError(result, freshInstallPreflightName, fmt.Sprintf("create Helm release Secret %q", name), err); err != nil {
			return err
		}
	} else if err := requireMutationPermission(ctx, kc, result, freshInstallPreflightName, preflightCheck{
		desc:     fmt.Sprintf("create Helm release Secret %q in namespace %q", name, opts.Namespace),
		blocking: true,
		attrs: authv1.ResourceAttributes{
			Namespace: opts.Namespace, Resource: "secrets", Verb: "create",
		},
	}); err != nil {
		return err
	}

	// Helm persists the same revision again as its status moves from pending to
	// deployed or failed. A dry-run-created object is not available to update,
	// so the exact name-scoped SSAR is the authoritative pre-enrollment check.
	return requireMutationPermission(ctx, kc, result, freshInstallPreflightName, preflightCheck{
		desc:     fmt.Sprintf("update Helm release Secret %q", name),
		blocking: true,
		attrs: authv1.ResourceAttributes{
			Namespace: opts.Namespace, Resource: "secrets", Name: name, Verb: "update",
		},
	})
}
