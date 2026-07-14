package cloudinstall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	helmkube "helm.sh/helm/v3/pkg/kube"
	helmreleaseutil "helm.sh/helm/v3/pkg/releaseutil"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apiextv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/jsonmergepatch"
	"k8s.io/apimachinery/pkg/util/mergepatch"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/cli-runtime/pkg/resource"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/restmapper"
)

const (
	mutationPreflightFieldManager = "radar"
	adoptionPreflightName         = "adoption preflight"
)

// AdoptionPreflightOptions describes the exact Helm release transition that
// was prepared before Cloud enrollment. CurrentManifest and TargetManifest
// must come from the same PreparedUpgrade. Enrollment may replace the prepared
// Cloud URL/cluster placeholder strings, but must not change the resource or
// RBAC shape checked here.
type AdoptionPreflightOptions struct {
	Namespace       string
	ReleaseName     string
	CurrentRevision int
	CurrentManifest string
	TargetManifest  string
}

// AdoptionPreflight proves that the caller's kube identity can perform the
// exact live mutations in a prepared Helm adoption before Hub creates a cluster
// or mints a token. Helm's server dry-run validates rendered objects, but does
// not authorize the create/patch/delete calls made by a real upgrade.
//
// Chart hooks are not part of release.Manifest, and PreparedUpgrade currently
// renders with HideSecret. The Radar chart has neither hooks nor rendered
// Secrets in this flow (the Cloud token and Helm storage Secrets are checked
// explicitly below). If the chart gains either, PreparedUpgrade must expose
// those objects and this preflight must include them before adoption remains
// safe. A HideSecret marker fails closed so that a future rendered Secret is
// not silently omitted.
func AdoptionPreflight(
	ctx context.Context,
	kc kubernetes.Interface,
	dc dynamic.Interface,
	discoveryClient discovery.DiscoveryInterface,
	opts AdoptionPreflightOptions,
) (PreflightResult, error) {
	if kc == nil || dc == nil || discoveryClient == nil {
		return PreflightResult{}, errors.New("adoption preflight: nil kubernetes, dynamic, or discovery client")
	}
	if strings.TrimSpace(opts.Namespace) == "" || strings.TrimSpace(opts.ReleaseName) == "" || opts.CurrentRevision < 1 {
		return PreflightResult{}, errors.New("adoption preflight: namespace, release name, and a positive current revision are required")
	}
	if strings.TrimSpace(opts.CurrentManifest) == "" || strings.TrimSpace(opts.TargetManifest) == "" {
		return PreflightResult{}, errors.New("adoption preflight: current and target manifests are required")
	}
	var result PreflightResult
	if recordHiddenSecretBlocker(opts.TargetManifest, "upgrade", &result) {
		return result, nil
	}

	groupResources, err := restmapper.GetAPIGroupResources(discoveryClient)
	if err != nil {
		return result, fmt.Errorf("adoption preflight: discover Kubernetes resources: %w", err)
	}
	mapper := restmapper.NewDiscoveryRESTMapper(groupResources)
	current, err := parseAdoptionManifest(opts.CurrentManifest, opts.Namespace, mapper)
	if err != nil {
		if meta.IsNoMatchError(err) {
			result.Blocking = append(result.Blocking, fmt.Sprintf("map current Helm manifest to this cluster: %v", err))
			return result, nil
		}
		return result, fmt.Errorf("adoption preflight: parse current Helm manifest: %w", err)
	}
	target, err := parseAdoptionManifest(opts.TargetManifest, opts.Namespace, mapper)
	if err != nil {
		if meta.IsNoMatchError(err) {
			result.Blocking = append(result.Blocking, fmt.Sprintf("map target Helm manifest to this cluster: %v", err))
			return result, nil
		}
		return result, fmt.Errorf("adoption preflight: parse target Helm manifest: %w", err)
	}
	applyHelmTrackingMetadata(target, opts.ReleaseName, opts.Namespace)

	forward, err := preflightChartMutations(ctx, dc, current, target, &result, adoptionPreflightName)
	if err != nil {
		return result, err
	}
	if err := preflightAtomicRollbackPermissions(ctx, kc, forward, &result); err != nil {
		return result, err
	}
	if err := preflightTokenSecret(ctx, kc, opts.Namespace, CloudTokenSecretName, &result, adoptionPreflightName, "failed-upgrade"); err != nil {
		return result, err
	}
	if err := preflightHelmStorage(ctx, kc, opts, &result); err != nil {
		return result, err
	}
	if err := requireMutationPermission(ctx, kc, &result, adoptionPreflightName, preflightCheck{
		desc:     "list Pods in the target namespace (required by Helm's atomic readiness wait)",
		blocking: true,
		attrs: authv1.ResourceAttributes{
			Namespace: opts.Namespace,
			Resource:  "pods",
			Verb:      "list",
		},
	}); err != nil {
		return result, err
	}
	return result, nil
}

func recordHiddenSecretBlocker(manifest, mutationKind string, result *PreflightResult) bool {
	if !strings.Contains(manifest, "# HIDDEN: The Secret output has been suppressed") {
		return false
	}
	result.Blocking = append(result.Blocking,
		fmt.Sprintf("inspect rendered chart Secrets: the prepared target hid at least one Secret, so Radar cannot prove the exact %s mutations", mutationKind))
	return true
}

type adoptionResourceKey struct {
	group     string
	kind      string
	namespace string
	name      string
}

type adoptionResource struct {
	key     adoptionResourceKey
	mapping *meta.RESTMapping
	object  *unstructured.Unstructured
}

func (r adoptionResource) description() string {
	if r.key.namespace == "" {
		return fmt.Sprintf("%s %q", r.key.kind, r.key.name)
	}
	return fmt.Sprintf("%s %q in namespace %q", r.key.kind, r.key.name, r.key.namespace)
}

func parseAdoptionManifest(manifest, defaultNamespace string, mapper meta.RESTMapper) (map[adoptionResourceKey]adoptionResource, error) {
	resources := make(map[adoptionResourceKey]adoptionResource)
	for source, document := range helmreleaseutil.SplitManifests(manifest) {
		document = strings.TrimSpace(document)
		if document == "" {
			continue
		}
		jsonDocument, err := yaml.ToJSON([]byte(document))
		if err != nil {
			return nil, fmt.Errorf("%s: convert YAML to JSON: %w", source, err)
		}
		var object unstructured.Unstructured
		if err := json.Unmarshal(jsonDocument, &object.Object); err != nil {
			return nil, fmt.Errorf("%s: decode object: %w", source, err)
		}
		gvk := object.GroupVersionKind()
		if gvk.Empty() || object.GetName() == "" {
			return nil, fmt.Errorf("%s: object must have apiVersion, kind, and metadata.name", source)
		}
		mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			return nil, fmt.Errorf("%s (%s %q): %w", source, gvk.Kind, object.GetName(), err)
		}
		namespace := object.GetNamespace()
		if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
			if namespace == "" {
				namespace = defaultNamespace
				object.SetNamespace(namespace)
			}
		} else {
			namespace = ""
			object.SetNamespace("")
		}
		key := adoptionResourceKey{group: gvk.Group, kind: gvk.Kind, namespace: namespace, name: object.GetName()}
		if _, exists := resources[key]; exists {
			return nil, fmt.Errorf("%s: duplicate %s %q in namespace %q", source, gvk.Kind, object.GetName(), namespace)
		}
		resources[key] = adoptionResource{key: key, mapping: mapping, object: &object}
	}
	return resources, nil
}

// applyHelmTrackingMetadata mirrors action.setMetadataVisitor(..., force=true),
// which mutates every rendered object before Helm sends it to Kubernetes. The
// release manifest itself does not include these additions.
func applyHelmTrackingMetadata(resources map[adoptionResourceKey]adoptionResource, releaseName, releaseNamespace string) {
	for key, resource := range resources {
		labels := resource.object.GetLabels()
		if labels == nil {
			labels = make(map[string]string)
		}
		labels["app.kubernetes.io/managed-by"] = "Helm"
		resource.object.SetLabels(labels)

		annotations := resource.object.GetAnnotations()
		if annotations == nil {
			annotations = make(map[string]string)
		}
		annotations["meta.helm.sh/release-name"] = releaseName
		annotations["meta.helm.sh/release-namespace"] = releaseNamespace
		resource.object.SetAnnotations(annotations)
		resources[key] = resource
	}
}

func preflightChartMutations(
	ctx context.Context,
	dc dynamic.Interface,
	current, target map[adoptionResourceKey]adoptionResource,
	result *PreflightResult,
	preflightName string,
) (chartMutationProof, error) {
	proof := chartMutationProof{}
	created := make(map[adoptionResourceKey]bool)
	notedEphemeralRoles := make(map[adoptionResourceKey]bool)
	for _, key := range sortedAdoptionResourceKeys(target) {
		desired := target[key]
		client := adoptionResourceClient(dc, desired)
		live, err := client.Get(ctx, desired.key.name, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			_, mutationErr := client.Create(ctx, desired.object.DeepCopy(), metav1.CreateOptions{
				DryRun:       []string{metav1.DryRunAll},
				FieldManager: mutationPreflightFieldManager,
			})
			if mutationErr == nil {
				created[key] = true
				if _, existedInCurrent := current[key]; !existedInCurrent {
					proof.created = append(proof.created, desired)
				}
			}
			if roleKey, ok := preparedBindingRole(desired, target); ok && created[roleKey] && missingPreparedRole(mutationErr, roleKey) {
				if !notedEphemeralRoles[roleKey] {
					result.Advisory = append(result.Advisory, fmt.Sprintf(
						"create %s: Kubernetes could not complete bind admission because referenced %s %q exists only for the duration of its successful dry-run; Helm creates that proven role before its bindings during the real upgrade",
						desired.description(), roleKey.kind, roleKey.name))
					notedEphemeralRoles[roleKey] = true
				}
				created[key] = true
				if _, existedInCurrent := current[key]; !existedInCurrent {
					proof.created = append(proof.created, desired)
				}
				continue
			}
			if err := recordMutationError(result, preflightName, "create "+desired.description(), mutationErr); err != nil {
				return proof, err
			}
		case err != nil:
			if err := recordMutationError(result, preflightName, "read "+desired.description()+" before the planned mutation", err); err != nil {
				return proof, err
			}
		case current[key].object == nil:
			result.Blocking = append(result.Blocking,
				fmt.Sprintf("create %s: an object already exists but is not owned by the current Helm release", desired.description()))
		default:
			original := current[key]
			patch, patchType, err := helmThreeWayPatch(original.object, desired.object, live, desired.mapping)
			if err != nil {
				return proof, fmt.Errorf("adoption preflight: build Helm patch for %s: %w", desired.description(), err)
			}
			if isEmptyPatch(patch) {
				continue
			}
			_, mutationErr := client.Patch(ctx, desired.key.name, patchType, patch, metav1.PatchOptions{
				DryRun:       []string{metav1.DryRunAll},
				FieldManager: mutationPreflightFieldManager,
			})
			if err := recordMutationError(result, preflightName, "patch "+desired.description(), mutationErr); err != nil {
				return proof, err
			}
		}
	}

	background := metav1.DeletePropagationBackground
	for _, key := range sortedAdoptionResourceKeys(current) {
		if _, retained := target[key]; retained {
			continue
		}
		obsolete := current[key]
		client := adoptionResourceClient(dc, obsolete)
		live, err := client.Get(ctx, obsolete.key.name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			// The forward upgrade has nothing to delete, but Helm's atomic
			// rollback can still recreate this current-manifest resource. Keep
			// it in the inverse-permission proof even though it already drifted
			// out of the live cluster.
			proof.deleted = append(proof.deleted, obsolete)
			continue
		} else if err != nil {
			if err := recordMutationError(result, preflightName, "read "+obsolete.description()+" before the planned delete", err); err != nil {
				return proof, err
			}
			continue
		}
		// Helm refreshes the original object before honoring resource-policy,
		// so use the live annotation rather than only the historical manifest.
		if live.GetAnnotations()["helm.sh/resource-policy"] == "keep" {
			continue
		}
		err = client.Delete(ctx, obsolete.key.name, metav1.DeleteOptions{
			DryRun:            []string{metav1.DryRunAll},
			PropagationPolicy: &background,
		})
		if err := recordMutationError(result, preflightName, "delete "+obsolete.description(), err); err != nil {
			return proof, err
		}
		if err == nil {
			proof.deleted = append(proof.deleted, obsolete)
		}
	}
	return proof, nil
}

type chartMutationProof struct {
	// created contains target-only resources whose forward create was proven.
	// Atomic rollback must be allowed to delete them.
	created []adoptionResource
	// deleted contains current-only resources that an atomic rollback may need
	// to recreate: either their forward delete was proven, or they were already
	// absent from the live cluster when preflight ran.
	deleted []adoptionResource
}

func preflightAtomicRollbackPermissions(
	ctx context.Context,
	kc kubernetes.Interface,
	forward chartMutationProof,
	result *PreflightResult,
) error {
	for _, resource := range forward.created {
		gvr := resource.mapping.Resource
		if err := requireMutationPermission(ctx, kc, result, adoptionPreflightName, preflightCheck{
			desc:     "delete " + resource.description() + " during Helm's atomic rollback",
			blocking: true,
			attrs: authv1.ResourceAttributes{
				Namespace: resource.key.namespace,
				Verb:      "delete",
				Group:     gvr.Group,
				Version:   gvr.Version,
				Resource:  gvr.Resource,
				Name:      resource.key.name,
			},
		}); err != nil {
			return err
		}
	}
	for _, resource := range forward.deleted {
		gvr := resource.mapping.Resource
		if err := requireMutationPermission(ctx, kc, result, adoptionPreflightName, preflightCheck{
			desc:     "recreate " + resource.description() + " during Helm's atomic rollback",
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

func sortedAdoptionResourceKeys(resources map[adoptionResourceKey]adoptionResource) []adoptionResourceKey {
	keys := make([]adoptionResourceKey, 0, len(resources))
	for key := range resources {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		return a.group+"\x00"+a.kind+"\x00"+a.namespace+"\x00"+a.name <
			b.group+"\x00"+b.kind+"\x00"+b.namespace+"\x00"+b.name
	})
	return keys
}

func adoptionResourceClient(dc dynamic.Interface, resource adoptionResource) dynamic.ResourceInterface {
	client := dc.Resource(resource.mapping.Resource)
	if resource.mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		return client.Namespace(resource.key.namespace)
	}
	return client
}

func helmThreeWayPatch(original, target, live *unstructured.Unstructured, mapping *meta.RESTMapping) ([]byte, types.PatchType, error) {
	oldData, err := json.Marshal(original.Object)
	if err != nil {
		return nil, "", fmt.Errorf("marshal original object: %w", err)
	}
	newData, err := json.Marshal(target.Object)
	if err != nil {
		return nil, "", fmt.Errorf("marshal target object: %w", err)
	}
	currentData, err := json.Marshal(live.Object)
	if err != nil {
		return nil, "", fmt.Errorf("marshal live object: %w", err)
	}

	// Reuse Helm's native-scheme conversion instead of consulting the process's
	// global scheme. This is the same type-selection step Helm uses for its real
	// three-way upgrade patch and deliberately leaves custom resources
	// unstructured.
	versioned := helmkube.AsVersioned(&resource.Info{Object: target, Mapping: mapping})
	_, unstructuredType := versioned.(runtime.Unstructured)
	_, legacyCRDType := versioned.(*apiextv1beta1.CustomResourceDefinition)
	if !unstructuredType && !legacyCRDType {
		patchMeta, err := strategicpatch.NewPatchMetaFromStruct(versioned)
		if err != nil {
			return nil, "", fmt.Errorf("create strategic patch metadata: %w", err)
		}
		patch, err := strategicpatch.CreateThreeWayMergePatch(oldData, newData, currentData, patchMeta, true)
		if err != nil {
			return nil, "", fmt.Errorf("create strategic patch for %T: %w", versioned, err)
		}
		return patch, types.StrategicMergePatchType, nil
	}

	preconditions := []mergepatch.PreconditionFunc{
		mergepatch.RequireKeyUnchanged("apiVersion"),
		mergepatch.RequireKeyUnchanged("kind"),
		mergepatch.RequireMetadataKeyUnchanged("name"),
	}
	patch, err := jsonmergepatch.CreateThreeWayJSONMergePatch(oldData, newData, currentData, preconditions...)
	return patch, types.MergePatchType, err
}

func isEmptyPatch(patch []byte) bool {
	trimmed := bytes.TrimSpace(patch)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("{}"))
}

func preflightTokenSecret(ctx context.Context, kc kubernetes.Interface, namespace, name string, result *PreflightResult, preflightName, cleanupKind string) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: namespace,
			Annotations: map[string]string{secretAttemptKey: "preflight-placeholder"},
		},
		Type:       corev1.SecretTypeOpaque,
		StringData: map[string]string{cloudTokenSecretKey: "preflight-placeholder"},
	}
	_, err := kc.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
	if err := recordMutationError(result, preflightName, fmt.Sprintf("create Cloud token Secret %q in namespace %q", name, namespace), err); err != nil {
		return err
	}
	return requireMutationPermission(ctx, kc, result, preflightName, preflightCheck{
		desc:     fmt.Sprintf("delete Cloud token Secret %q during %s cleanup", name, cleanupKind),
		blocking: true,
		attrs: authv1.ResourceAttributes{
			Namespace: namespace,
			Resource:  "secrets",
			Name:      name,
			Verb:      "delete",
		},
	})
}

func preflightHelmStorage(ctx context.Context, kc kubernetes.Interface, opts AdoptionPreflightOptions, result *PreflightResult) error {
	currentName := helmReleaseSecretName(opts.ReleaseName, opts.CurrentRevision)
	current, err := kc.CoreV1().Secrets(opts.Namespace).Get(ctx, currentName, metav1.GetOptions{})
	if err != nil {
		if err := recordMutationError(result, adoptionPreflightName, fmt.Sprintf("read current Helm release Secret %q", currentName), err); err != nil {
			return err
		}
	} else {
		_, updateErr := kc.CoreV1().Secrets(opts.Namespace).Update(ctx, current.DeepCopy(), metav1.UpdateOptions{
			DryRun: []string{metav1.DryRunAll},
		})
		if err := recordMutationError(result, adoptionPreflightName, fmt.Sprintf("update Helm release Secret %q", currentName), updateErr); err != nil {
			return err
		}
	}

	// Helm persists the attempted upgrade at n+1. If Atomic rolls it back, the
	// rollback is itself a Helm release at n+2. Prove admission for both exact
	// storage names before enrollment, then prove the status updates each record
	// receives as it moves from pending to its terminal state.
	for offset, status := range []string{"pending-upgrade", "pending-rollback"} {
		revision := opts.CurrentRevision + offset + 1
		name := helmReleaseSecretName(opts.ReleaseName, revision)
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: opts.Namespace,
				Labels: map[string]string{
					"owner":   "helm",
					"name":    opts.ReleaseName,
					"status":  status,
					"version": fmt.Sprintf("%d", revision),
				},
			},
			Type: corev1.SecretType("helm.sh/release.v1"),
			Data: map[string][]byte{"release": []byte("preflight-placeholder")},
		}
		_, err = kc.CoreV1().Secrets(opts.Namespace).Create(ctx, secret, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
		if err := recordMutationError(result, adoptionPreflightName, fmt.Sprintf("create Helm release Secret %q", name), err); err != nil {
			return err
		}
		if err := requireMutationPermission(ctx, kc, result, adoptionPreflightName, preflightCheck{
			desc:     fmt.Sprintf("update Helm release Secret %q", name),
			blocking: true,
			attrs: authv1.ResourceAttributes{
				Namespace: opts.Namespace,
				Resource:  "secrets",
				Name:      name,
				Verb:      "update",
			},
		}); err != nil {
			return err
		}
	}

	// Atomic failure cleanup and MaxHistory pruning can delete a release
	// revision whose exact name is not knowable from only the active revision.
	// An unscoped SSAR accurately catches a policy that cannot delete Helm's
	// release Secrets without pretending a dry-run-created Secret persisted.
	return requireMutationPermission(ctx, kc, result, adoptionPreflightName, preflightCheck{
		desc:     "delete Helm release Secrets (required by atomic cleanup and history pruning)",
		blocking: true,
		attrs: authv1.ResourceAttributes{
			Namespace: opts.Namespace,
			Resource:  "secrets",
			Verb:      "delete",
		},
	})
}

func helmReleaseSecretName(releaseName string, revision int) string {
	return fmt.Sprintf("sh.helm.release.v1.%s.v%d", releaseName, revision)
}

func requireMutationPermission(ctx context.Context, kc kubernetes.Interface, result *PreflightResult, preflightName string, check preflightCheck) error {
	attrs := check.attrs
	review := &authv1.SelfSubjectAccessReview{
		Spec: authv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &attrs},
	}
	out, err := kc.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("%s SSAR failed for %q: %w", preflightName, check.desc, err)
	}
	if out.Status.Allowed {
		return nil
	}
	detail := check.desc
	if reason := strings.TrimSpace(out.Status.Reason); reason != "" {
		detail += ": " + reason
	}
	if check.blocking {
		result.Blocking = append(result.Blocking, detail)
	} else {
		result.Advisory = append(result.Advisory, detail)
	}
	return nil
}

func recordMutationError(result *PreflightResult, preflightName, description string, err error) error {
	if err == nil {
		return nil
	}
	if isActionableKubernetesError(err) {
		result.Blocking = append(result.Blocking, fmt.Sprintf("%s: %v", description, err))
		return nil
	}
	return fmt.Errorf("%s: %s: %w", preflightName, description, err)
}

func isActionableKubernetesError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var status apierrors.APIStatus
	if !errors.As(err, &status) {
		return false
	}
	code := status.Status().Code
	return code >= 400 && code < 500 && code != 408 && code != 429
}
