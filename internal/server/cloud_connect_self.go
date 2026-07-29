package server

// In-cluster Cloud Connect: a Radar pod cannot install its own connection —
// the ServiceAccount cannot self-grant the impersonation RBAC (Kubernetes
// escalation prevention), and a successful `helm upgrade` restarts the very
// pod serving the flow. So the in-cluster lane hands off to the Hub wizard.
//
// What it can do is stop making the operator work out their own situation.
// This Radar IS the installation: it knows its namespace, Helm release, chart,
// and whether it is managed natively by Helm or owned by Argo/Flux. The Hub
// wizard otherwise assumes the documented `radar/radar` layout and tells
// anyone else to adjust by hand — and its imperative command is simply wrong
// for a GitOps-managed install, which would drift or be reverted.

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/skyhook-io/radar/internal/cloudinstall"
	"github.com/skyhook-io/radar/internal/k8s"
)

// cloudConnectSelf describes the in-cluster Radar to the funnel modal so it can
// route the operator to the right next step.
type cloudConnectSelf struct {
	// Ownership is helm | gitops | ambiguous | unknown — which handoff applies.
	Ownership      string `json:"ownership"`
	Namespace      string `json:"namespace,omitempty"`
	Release        string `json:"release,omitempty"`
	DeploymentName string `json:"deploymentName,omitempty"`
	Chart          string `json:"chart,omitempty"`
	// Controller names the GitOps object that owns this install, when one does.
	Controller string `json:"controller,omitempty"`
	// WizardURL deep-links the Hub's connect wizard with this install's real
	// target, so it renders the existing-install command for the right release
	// instead of guessing. Empty for GitOps (its command would drift).
	WizardURL string `json:"wizardUrl,omitempty"`
}

const cloudConnectSelfTimeout = 15 * time.Second

// handleCloudConnectSelf reports what this in-cluster Radar knows about its own
// installation. Read-only: it never contacts the Hub and never mutates.
func (s *Server) handleCloudConnectSelf(w http.ResponseWriter, r *http.Request) {
	if cloudMode() || cloudConnectDeploymentMode() != k8s.DeploymentModeInCluster {
		s.writeError(w, http.StatusNotFound, "in-cluster Cloud connect details are not available on this deployment")
		return
	}
	if !s.requireConnected(w) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), cloudConnectSelfTimeout)
	defer cancel()

	self := s.inspectSelfInstall(ctx, r, os.Getenv("MY_POD_NAMESPACE"), os.Getenv("MY_DEPLOYMENT_NAME"))
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, self)
}

// inspectSelfInstall discovers the Radar installation in the pod's own
// namespace and classifies how it is managed. Every failure degrades to
// ownership "unknown" with a generic wizard link — a wrong-but-confident
// answer here would send an operator to a command that damages their install.
func (s *Server) inspectSelfInstall(ctx context.Context, r *http.Request, namespace, deploymentName string) cloudConnectSelf {
	generic := cloudConnectSelf{Ownership: "unknown", WizardURL: s.cloudConnectCfg.HubAppURL + "/install"}
	if namespace == "" || deploymentName == "" {
		return generic
	}
	kc := s.getClientForRequest(r)
	dc, _ := s.getDynamicClientSnapshotForRequest(r)
	if kc == nil || dc == nil {
		return generic
	}

	result, err := cloudinstall.DiscoverRadarTargets(ctx, kc, dc, cloudinstall.DiscoveryOptions{
		Namespace: namespace, ReleaseName: cloudinstall.DefaultReleaseName,
	})
	if err != nil {
		return generic
	}
	// Match this pod's own Deployment by name. "The only Radar-labelled
	// Deployment in my namespace" is not proof that it is the one serving this
	// request — a sibling install would produce a confident, wrong deep link.
	var target cloudinstall.RadarTarget
	found := false
	for _, candidate := range cloudinstall.DiscoveredTargets(result, false) {
		if candidate.DeploymentName == deploymentName {
			target, found = candidate, true
			break
		}
	}
	if !found {
		return generic
	}

	self := cloudConnectSelf{
		Namespace:      target.Namespace,
		Release:        target.ReleaseName,
		DeploymentName: target.DeploymentName,
		Chart:          target.Chart,
	}
	switch target.Ownership.Classification {
	case cloudinstall.OwnershipNativeHelm:
		if target.ReleaseName == "" {
			return generic
		}
		self.Ownership = "helm"
		self.WizardURL = s.wizardInstallURL(target.Namespace, target.ReleaseName)
	case cloudinstall.OwnershipGitOpsVerified,
		cloudinstall.OwnershipGitOpsSuspected,
		cloudinstall.OwnershipGitOpsUnreadable,
		cloudinstall.OwnershipGitOpsStale:
		self.Ownership = "gitops"
		if len(target.Ownership.Controllers) > 0 {
			ref := target.Ownership.Controllers[0].Ref
			self.Controller = ref.Kind + " " + ref.Namespace + "/" + ref.Name
		}
	case cloudinstall.OwnershipAmbiguous:
		// Conflicting Helm and GitOps evidence: cloudinstall.ClassifyInstallPlan
		// refuses to act on this, so neither should the handoff — an imperative
		// upgrade could fight whatever else manages the release.
		self.Ownership = "ambiguous"
	default:
		self.Ownership = "unknown"
		self.WizardURL = generic.WizardURL
	}
	return self
}

// wizardInstallURL deep-links the Hub wizard at this install's real target so
// it renders the existing-install command for the right namespace and release.
func (s *Server) wizardInstallURL(namespace, release string) string {
	q := url.Values{"existing": {"1"}, "ns": {namespace}, "release": {release}}
	return s.cloudConnectCfg.HubAppURL + "/install?" + q.Encode()
}
