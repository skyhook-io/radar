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
	"github.com/skyhook-io/radar/pkg/subject"
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
	Controller    string       `json:"controller,omitempty"`
	ControllerRef *subject.Ref `json:"controllerRef,omitempty"`
	// WizardURL deep-links the Hub's connect wizard with this install's real
	// target, so it renders the existing-install artifact for the right release
	// instead of guessing. Empty when the handoff must go through the CLI —
	// the modal keys the CTA off its presence rather than re-deciding.
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
	generic := cloudConnectSelf{Ownership: "unknown", WizardURL: s.cloudConnectCfg.HubAppURL + "/install?" + cloudFunnelUTM("wizard-generic").Encode()}
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
		self.WizardURL = s.wizardInstallURL(target.Namespace, target.ReleaseName, "helm")
	case cloudinstall.OwnershipGitOpsVerified,
		cloudinstall.OwnershipGitOpsSuspected,
		cloudinstall.OwnershipGitOpsUnreadable,
		cloudinstall.OwnershipGitOpsStale:
		self.Ownership = "gitops"
		// The verified controller, not merely the first one. A stale leftover
		// ref can sort ahead of the real owner — controllerClassification still
		// returns Verified with one verified candidate alongside a stale one —
		// and naming or deep-linking that ref would hand the operator a patch
		// for an object that does not manage this release.
		owner := verifiedController(target.Ownership.Controllers)
		if owner == nil && len(target.Ownership.Controllers) > 0 {
			owner = &target.Ownership.Controllers[0]
		}
		if owner != nil {
			self.Controller = owner.Ref.Kind + " " + owner.Ref.Namespace + "/" + owner.Ref.Name
		}
		// Only a verified controller earns the deep link. Suspected, unreadable,
		// and stale all mean the same thing here: evidence of GitOps we could
		// not confirm against the live object. Handing those a values patch
		// aimed at a controller that may not own this release is the
		// confidently-wrong answer this whole endpoint exists to avoid — they
		// keep the CLI, which inspects before it acts.
		//
		// A release name is required even when verified: the wizard's GitOps
		// artifact is a Helm values patch, so an installation with no Helm
		// release identity has nothing for it to patch.
		if target.Ownership.Classification == cloudinstall.OwnershipGitOpsVerified && owner != nil {
			controllerRef := owner.Ref
			self.ControllerRef = &controllerRef
			if target.ReleaseName != "" {
				if method := wizardMethodFor(owner.Ref); method != "" {
					self.WizardURL = s.wizardInstallURL(target.Namespace, target.ReleaseName, method)
				}
			}
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

// verifiedController returns the candidate whose ownership was confirmed
// against the live object, or nil when none was. Candidate order reflects
// discovery, not confidence.
func verifiedController(candidates []cloudinstall.ControllerCandidate) *cloudinstall.ControllerCandidate {
	for i := range candidates {
		if candidates[i].Verification == cloudinstall.ControllerVerified {
			return &candidates[i]
		}
	}
	return nil
}

// wizardMethodFor maps an owning GitOps object to the Hub wizard's install-method
// tab, so a Flux-managed install lands on a values patch for its HelmRelease
// rather than an imperative command its controller would revert. Empty for
// anything unrecognized — the caller then withholds the link instead of
// guessing a tool.
func wizardMethodFor(ref subject.Ref) string {
	switch {
	case ref.Group == "argoproj.io" && ref.Kind == "Application":
		return "argocd"
	case ref.Group == "helm.toolkit.fluxcd.io" && ref.Kind == "HelmRelease",
		ref.Group == "kustomize.toolkit.fluxcd.io" && ref.Kind == "Kustomization":
		return "flux"
	default:
		return ""
	}
}

// wizardInstallURL deep-links the Hub wizard at this install's real target so
// it renders the existing-install artifact for the right namespace, release,
// and tool instead of guessing any of the three.
func (s *Server) wizardInstallURL(namespace, release, method string) string {
	q := cloudFunnelUTM("wizard-deeplink")
	q.Set("existing", "1")
	q.Set("ns", namespace)
	q.Set("release", release)
	q.Set("method", method)
	return s.cloudConnectCfg.HubAppURL + "/install?" + q.Encode()
}

// cloudFunnelUTM marks a funnel-opened Hub URL with which lane produced it,
// so Hub-side analytics can measure the funnel per lane without Radar itself
// transmitting anything — the Hub only ever sees it when the user actually
// navigates there. Matches the frontend's SIGNUP_QUERY vocabulary.
func cloudFunnelUTM(content string) url.Values {
	return url.Values{
		"utm_source":   {"radar-oss"},
		"utm_medium":   {"app"},
		"utm_campaign": {"cloud-modal"},
		"utm_content":  {content},
	}
}
