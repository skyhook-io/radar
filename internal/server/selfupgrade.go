package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"

	"helm.sh/helm/v3/pkg/releaseutil"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"

	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/logsafe"
)

var radarImageTagPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,63}$`)

// selfUpgradePatchOptions returns the PatchOptions used by the self-upgrade
// endpoint. The patch is Server-Side Apply using Helm's field manager and the
// full Deployment manifest from the Helm release, not a tiny image-only object.
// SSA treats each apply as the manager's full desired field set, so applying
// only `.image` would make Helm drop ownership of selector/template fields and
// fail on immutable-field validation. Force reclaims `.image` from stale
// strategic-merge self-upgrades that recorded ownership as (helm, Update).
//
// Extracted for tripwire test; if a refactor reverts these values, the
// test in selfupgrade_test.go fails before the bug ships.
func selfUpgradePatchOptions() metav1.PatchOptions {
	force := true
	return metav1.PatchOptions{FieldManager: "helm", Force: &force}
}

// handleSelfUpgrade patches this Radar Deployment's container image so the
// pod restarts on a new version. Called by Radar Cloud's upgrade-agent endpoint
// over the yamux tunnel — no user terminal or cloud credentials needed.
//
// Security: only images under ghcr.io/skyhook-io/radar: are accepted. The
// apply body is the Helm release's rendered Deployment with only that image
// swapped, so the SA must also be able to read Helm release storage and patch
// its own Deployment (Helm rbac.selfUpgrade: true). MY_POD_NAMESPACE and
// MY_DEPLOYMENT_NAME must be set by the chart (downward API + static template
// value respectively) or the endpoint returns 503.
func (s *Server) handleSelfUpgrade(w http.ResponseWriter, r *http.Request) {
	ns := os.Getenv("MY_POD_NAMESPACE")
	deployment := os.Getenv("MY_DEPLOYMENT_NAME")
	if ns == "" || deployment == "" {
		s.writeError(w, http.StatusServiceUnavailable,
			"self-upgrade not configured (set rbac.selfUpgrade=true in Helm values)")
		return
	}

	var req struct {
		Image string `json:"image"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	const allowedRepo = "ghcr.io/skyhook-io/radar:"
	if !strings.HasPrefix(req.Image, allowedRepo) {
		s.writeError(w, http.StatusBadRequest, "image must be from ghcr.io/skyhook-io/radar")
		return
	}
	tag := strings.TrimPrefix(req.Image, allowedRepo)
	if !isValidRadarImageTag(tag) {
		s.writeError(w, http.StatusBadRequest, "invalid image tag")
		return
	}

	// Use the SA's ambient client, not the impersonated user client.
	// The SA has patch rights on its own Deployment; a hub-forwarded user
	// identity is a Cloud user ID, not a K8s principal, so impersonation
	// would fail anyway.
	client := k8s.GetClient()
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "k8s client not available")
		return
	}

	deploy, err := client.AppsV1().Deployments(ns).Get(r.Context(), deployment, metav1.GetOptions{})
	if err != nil {
		switch {
		case apierrors.IsNotFound(err):
			s.writeError(w, http.StatusNotFound, "deployment not found")
		case apierrors.IsForbidden(err):
			s.writeError(w, http.StatusForbidden, "SA lacks get permission on this Deployment (rbac.selfUpgrade=true?)")
		default:
			log.Printf("[self-upgrade] get deployment failed: ns=%s deploy=%s tag=%s err=%v", ns, deployment, logsafe.Sanitize(tag), err)
			s.writeError(w, http.StatusInternalServerError, "deployment lookup failed")
		}
		return
	}
	releaseName := deploy.Annotations["meta.helm.sh/release-name"]
	if releaseName == "" {
		releaseName = deployment
	}

	patch, err := selfUpgradeApplyPatch(ns, releaseName, deployment, req.Image)
	if err != nil {
		log.Printf("[self-upgrade] failed to build apply patch: ns=%s deploy=%s tag=%s err=%v", ns, deployment, logsafe.Sanitize(tag), err)
		s.writeError(w, http.StatusServiceUnavailable, "helm release manifest not available")
		return
	}

	_, err = client.AppsV1().Deployments(ns).Patch(
		r.Context(),
		deployment,
		types.ApplyPatchType,
		patch,
		selfUpgradePatchOptions(),
	)
	if err != nil {
		switch {
		case apierrors.IsNotFound(err):
			s.writeError(w, http.StatusNotFound, "deployment not found")
		case apierrors.IsForbidden(err):
			s.writeError(w, http.StatusForbidden, "SA lacks patch permission on this Deployment (rbac.selfUpgrade=true?)")
		case apierrors.IsConflict(err):
			// Force=true handles field-manager conflicts. A remaining
			// conflict here means a concurrent write raced this apply.
			s.writeError(w, http.StatusConflict, "concurrent modification, retry")
		case apierrors.IsTooManyRequests(err) || apierrors.IsServerTimeout(err):
			s.writeError(w, http.StatusServiceUnavailable, "apiserver throttled, retry")
		case apierrors.IsInvalid(err):
			s.writeError(w, http.StatusBadRequest, "invalid patch")
		default:
			log.Printf("[self-upgrade] patch failed: ns=%s deploy=%s tag=%s err=%v", ns, deployment, logsafe.Sanitize(tag), err)
			s.writeError(w, http.StatusInternalServerError, "patch failed")
		}
		return
	}

	log.Printf("[self-upgrade] initiated: ns=%s deploy=%s tag=%s", ns, deployment, logsafe.Sanitize(tag))
	s.writeJSON(w, map[string]string{"status": "upgrade initiated", "image": req.Image})
}

func isValidRadarImageTag(tag string) bool {
	return radarImageTagPattern.MatchString(tag)
}

func selfUpgradeApplyPatch(namespace, releaseName, deployment, image string) ([]byte, error) {
	helmClient := helm.GetClient()
	if helmClient == nil {
		return nil, fmt.Errorf("helm client not initialized")
	}

	manifest, err := helmClient.GetManifest(namespace, releaseName, 0)
	if err != nil {
		return nil, fmt.Errorf("get release manifest: %w", err)
	}

	return buildSelfUpgradeApplyPatch(manifest, namespace, releaseName, deployment, image)
}

func buildSelfUpgradeApplyPatch(manifest, namespace, releaseName, deployment, image string) ([]byte, error) {
	for _, doc := range releaseutil.SplitManifests(manifest) {
		var obj map[string]any
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			return nil, fmt.Errorf("parse manifest document: %w", err)
		}
		if len(obj) == 0 || obj["apiVersion"] != "apps/v1" || obj["kind"] != "Deployment" {
			continue
		}

		metadata, ok := obj["metadata"].(map[string]any)
		if !ok || metadata["name"] != deployment {
			continue
		}
		if manifestNamespace, ok := metadata["namespace"].(string); ok && manifestNamespace != "" && manifestNamespace != namespace {
			continue
		}

		if err := setContainerImage(obj, "radar", image); err != nil {
			return nil, err
		}

		metadata["namespace"] = namespace
		annotations, ok := metadata["annotations"].(map[string]any)
		if !ok {
			annotations = map[string]any{}
			metadata["annotations"] = annotations
		}
		annotations["meta.helm.sh/release-name"] = releaseName
		annotations["meta.helm.sh/release-namespace"] = namespace
		return json.Marshal(obj)
	}

	return nil, fmt.Errorf("deployment %s/%s not found in helm manifest", namespace, deployment)
}

func setContainerImage(obj map[string]any, containerName, image string) error {
	containers, ok, err := nestedSlice(obj, "spec", "template", "spec", "containers")
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("deployment manifest has no containers")
	}

	for _, item := range containers {
		container, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("deployment manifest contains a non-object container")
		}
		if container["name"] == containerName {
			container["image"] = image
			return nil
		}
	}

	return fmt.Errorf("container %q not found in deployment manifest", containerName)
}

func nestedSlice(obj map[string]any, fields ...string) ([]any, bool, error) {
	var current any = obj
	for _, field := range fields {
		currentMap, ok := current.(map[string]any)
		if !ok {
			return nil, false, fmt.Errorf("manifest path %q is not an object", field)
		}
		current, ok = currentMap[field]
		if !ok {
			return nil, false, nil
		}
	}

	items, ok := current.([]any)
	if !ok {
		return nil, false, fmt.Errorf("manifest path %q is not a list", strings.Join(fields, "."))
	}
	return items, true, nil
}
