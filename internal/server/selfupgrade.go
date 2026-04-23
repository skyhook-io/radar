package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/internal/k8s"
)

// handleSelfUpgrade patches this Radar Deployment's container image so the
// pod restarts on a new version. Called by Radar Cloud's upgrade-agent endpoint
// over the yamux tunnel — no user terminal or cloud credentials needed.
//
// Security: only images under ghcr.io/skyhook-io/radar: are accepted.
// The patch uses the pod's ServiceAccount (not user impersonation) — the SA
// must have patch rights on its own Deployment (Helm rbac.selfUpgrade: true).
// MY_POD_NAMESPACE and MY_DEPLOYMENT_NAME must be set by the chart (downward
// API + static template value respectively) or the endpoint returns 503.
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
	if tag == "" || len(tag) > 64 {
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

	patch := []byte(fmt.Sprintf(
		`{"spec":{"template":{"spec":{"containers":[{"name":"radar","image":%q}]}}}}`,
		req.Image,
	))

	_, err := client.AppsV1().Deployments(ns).Patch(
		r.Context(),
		deployment,
		types.StrategicMergePatchType,
		patch,
		metav1.PatchOptions{},
	)
	if err != nil {
		if apierrors.IsNotFound(err) {
			s.writeError(w, http.StatusNotFound, "deployment not found")
			return
		}
		if apierrors.IsForbidden(err) {
			s.writeError(w, http.StatusForbidden, "SA lacks patch permission on this Deployment (rbac.selfUpgrade=true?)")
			return
		}
		log.Printf("[self-upgrade] patch failed: ns=%s deploy=%s tag=%s err=%v", ns, deployment, tag, err)
		s.writeError(w, http.StatusInternalServerError, "patch failed")
		return
	}

	log.Printf("[self-upgrade] initiated: ns=%s deploy=%s tag=%s", ns, deployment, tag)
	s.writeJSON(w, map[string]string{"status": "upgrade initiated", "image": req.Image})
}
