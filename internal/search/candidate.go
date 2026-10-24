package search

import (
	"fmt"
	"sort"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
)

// fromObject builds a candidate from a typed K8s object. Returns ok=false
// when the object doesn't expose ObjectMeta (shouldn't happen for cache
// objects but we guard anyway).
func fromObject(obj runtime.Object, kind string) (candidate, bool) {
	m, err := meta.Accessor(obj)
	if err != nil {
		return candidate{}, false
	}
	c := candidate{
		Kind:        kind,
		Namespace:   m.GetNamespace(),
		Name:        m.GetName(),
		Labels:      m.GetLabels(),
		Annotations: m.GetAnnotations(),
	}
	c.Images = imagesForTyped(obj)
	c.Content = contentForTyped(obj, kind)
	return c, true
}

// identityFromUnstructured builds a CRD candidate without Content: enough to
// evaluate modifiers, so a candidate they reject never pays for content
// extraction (addUnstructuredContent). The kind is already known by the
// caller (we don't trust the unstructured's Kind since informers strip
// TypeMeta). A Trivy report's scanned image is part of identity, for image:
// modifiers.
func identityFromUnstructured(u *unstructured.Unstructured, kind, group string) candidate {
	c := candidate{
		Kind:        kind,
		Group:       group,
		Namespace:   u.GetNamespace(),
		Name:        u.GetName(),
		Labels:      u.GetLabels(),
		Annotations: u.GetAnnotations(),
	}
	c.Images = imagesFromUnstructured(u)
	if group == trivyGroup {
		if img := aicontext.TrivyArtifactImage(asMap(u.Object["report"])); img != "" {
			c.Images = append(c.Images, img)
		}
	}
	return c
}

// addUnstructuredContent fills Content, which only free tokens read.
func addUnstructuredContent(c *candidate, u *unstructured.Unstructured, findings *findingsScan) {
	c.Content = contentFromUnstructured(u, c.Kind)
	if c.Group == trivyGroup && findings.allow() {
		c.report = asMap(u.Object["report"])
		c.findings = findings
	}
}

func contentForTyped(obj runtime.Object, kind string) []ContentField {
	if obj == nil {
		return nil
	}
	// Secrets are intentionally not content-indexed. Search may expose Secret
	// names to callers with Secret RBAC, but matching/snippeting data values
	// would turn search into a secret-value disclosure path.
	if kind == "Secret" {
		return nil
	}
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil
	}
	return contentFromMap(m, kind)
}

func contentFromUnstructured(u *unstructured.Unstructured, kind string) []ContentField {
	if u == nil || u.Object == nil || kind == "Secret" {
		return nil
	}
	return contentFromMap(u.Object, kind)
}

func contentFromMap(obj map[string]any, kind string) []ContentField {
	var out []ContentField
	if kind == "ConfigMap" {
		walkContent("data", obj["data"], &out)
		walkContent("binaryData", obj["binaryData"], &out)
		return out
	}
	// These roots capture the useful grep-like surface without indexing noisy
	// metadata such as managedFields or leaking Secret data values.
	walkContent("spec", obj["spec"], &out)
	walkContent("status", obj["status"], &out)
	walkContent("data", obj["data"], &out)
	// Admission webhook configs keep their content in a top-level list.
	if kind == "MutatingWebhookConfiguration" || kind == "ValidatingWebhookConfiguration" {
		walkContent("webhooks", obj["webhooks"], &out)
	}
	return out
}

func walkContent(path string, v any, out *[]ContentField) {
	switch x := v.(type) {
	case nil:
		return
	case string:
		if x != "" {
			*out = append(*out, ContentField{Path: path, Value: x})
		}
	case bool:
		*out = append(*out, ContentField{Path: path, Value: strconv.FormatBool(x)})
	case int:
		*out = append(*out, ContentField{Path: path, Value: strconv.Itoa(x)})
	case int64:
		*out = append(*out, ContentField{Path: path, Value: strconv.FormatInt(x, 10)})
	case float64:
		*out = append(*out, ContentField{Path: path, Value: strconv.FormatFloat(x, 'f', -1, 64)})
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			// A base64 CA bundle is kilobytes of noise that substring-matches
			// short tokens like "api" or "ops".
			if k == "caBundle" {
				continue
			}
			walkContent(path+"."+k, x[k], out)
		}
	case []any:
		for i, item := range x {
			walkContent(fmt.Sprintf("%s[%d]", path, i), item, out)
		}
	}
}

func imagesForTyped(obj runtime.Object) []string {
	switch o := obj.(type) {
	case *corev1.Pod:
		return collectFromPodSpec(&o.Spec)
	case *appsv1.Deployment:
		return collectFromPodSpec(&o.Spec.Template.Spec)
	case *appsv1.DaemonSet:
		return collectFromPodSpec(&o.Spec.Template.Spec)
	case *appsv1.StatefulSet:
		return collectFromPodSpec(&o.Spec.Template.Spec)
	case *appsv1.ReplicaSet:
		return collectFromPodSpec(&o.Spec.Template.Spec)
	case *batchv1.Job:
		return collectFromPodSpec(&o.Spec.Template.Spec)
	case *batchv1.CronJob:
		return collectFromPodSpec(&o.Spec.JobTemplate.Spec.Template.Spec)
	}
	return nil
}

func collectFromPodSpec(spec *corev1.PodSpec) []string {
	if spec == nil {
		return nil
	}
	out := make([]string, 0, len(spec.Containers)+len(spec.InitContainers)+len(spec.EphemeralContainers))
	for _, c := range spec.Containers {
		if c.Image != "" {
			out = append(out, c.Image)
		}
	}
	for _, c := range spec.InitContainers {
		if c.Image != "" {
			out = append(out, c.Image)
		}
	}
	for _, c := range spec.EphemeralContainers {
		if c.Image != "" {
			out = append(out, c.Image)
		}
	}
	return out
}

// imagesFromUnstructured walks common pod-template paths in CRDs.
// We try spec.template.spec.containers first (most workload-shaped CRDs),
// then spec.containers (Pod-shaped), then leave it. Any miss is fine —
// the candidate just won't have images, which only matters for image:
// queries against that CRD.
func imagesFromUnstructured(u *unstructured.Unstructured) []string {
	if u == nil || u.Object == nil {
		return nil
	}
	if imgs := containersAt(u.Object, "spec", "template", "spec"); imgs != nil {
		return imgs
	}
	if imgs := containersAt(u.Object, "spec"); imgs != nil {
		return imgs
	}
	return nil
}

func containersAt(root map[string]any, path ...string) []string {
	cur := root
	for _, k := range path {
		next, ok := cur[k].(map[string]any)
		if !ok {
			return nil
		}
		cur = next
	}
	out := imagesFromContainerList(cur["containers"])
	out = append(out, imagesFromContainerList(cur["initContainers"])...)
	if len(out) == 0 {
		return nil
	}
	return out
}

func imagesFromContainerList(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range list {
		c, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if img, ok := c["image"].(string); ok && img != "" {
			out = append(out, img)
		}
	}
	return out
}
