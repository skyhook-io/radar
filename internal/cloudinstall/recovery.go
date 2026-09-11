package cloudinstall

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/helm"
)

// CommandTarget keeps copy-paste follow-up commands on the same cluster the
// installer inspected, even when --context or a kubeconfig override differs
// from the user's later current context.
type CommandTarget struct {
	Context    string
	Kubeconfig string
}

func (t CommandTarget) Kubectl() string {
	command := "kubectl"
	if t.Kubeconfig != "" {
		command += " --kubeconfig " + ShellArgument(t.Kubeconfig)
	}
	if t.Context != "" {
		command += " --context " + ShellArgument(t.Context)
	}
	return command
}

func (t CommandTarget) Helm() string {
	command := "helm"
	if t.Kubeconfig != "" {
		command += " --kubeconfig " + ShellArgument(t.Kubeconfig)
	}
	if t.Context != "" {
		command += " --kube-context " + ShellArgument(t.Context)
	}
	return command
}

func (t CommandTarget) CloudStatus(namespace, release string) string {
	command := "radar cloud status"
	if t.Context != "" {
		command += " --context " + ShellArgument(t.Context)
	}
	return command + " --namespace " + ShellArgument(namespace) + " --release " + ShellArgument(release)
}

func ShellArgument(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

// ProvisionRecovery is the release identity a failed provision must be
// recovered against, captured from the prepared plan before mutation.
type ProvisionRecovery struct {
	Mode            ProvisionMode
	ReleaseName     string
	Namespace       string
	Deployment      helm.DeploymentRef
	CurrentRevision int
}

func ProvisionRecoveryFrom(prepared *PreparedProvision) ProvisionRecovery {
	return ProvisionRecovery{
		Mode:            prepared.Mode(),
		ReleaseName:     prepared.ReleaseName(),
		Namespace:       prepared.Namespace(),
		Deployment:      prepared.Deployment(),
		CurrentRevision: prepared.CurrentRevision(),
	}
}

// RecoveryGuidance is post-approval recovery guidance rendered identically by
// the CLI printers and the Cloud install flow's status API — single-sourced so
// the two presenters can't drift on the safety-critical instructions.
type RecoveryGuidance struct {
	Summary    string   `json:"summary"`
	Lines      []string `json:"lines,omitempty"`
	Inspect    []string `json:"inspect,omitempty"`
	ClusterURL string   `json:"clusterUrl,omitempty"`
}

// CanceledAfterApprovalGuidance covers cancellation between Hub approval and
// the first Kubernetes mutation. retryHint carries the presenter's own retry
// instruction ("Rerun `radar cloud install`…" / "Start a new connection…").
func CanceledAfterApprovalGuidance(clusterID, clusterURL, retryHint string) RecoveryGuidance {
	return RecoveryGuidance{
		Summary: fmt.Sprintf("The Hub approved cluster %q, but the connection was canceled before Kubernetes provisioning began.", clusterID),
		Lines: []string{
			"No token Secret or Helm release was written. " + retryHint,
			"The previous approval may remain as a pending cluster in Radar. An organization owner can delete it later:",
		},
		ClusterURL: clusterURL,
	}
}

// PostApprovalProvisionGuidance covers a failed provision after the Hub
// cluster exists. It mirrors the CLI's typed-error distinctions exactly.
func PostApprovalProvisionGuidance(clusterID, clusterURL string, recovery ProvisionRecovery, provisionErr error, target CommandTarget) RecoveryGuidance {
	g := RecoveryGuidance{
		Summary:    fmt.Sprintf("Hub cluster %q already exists. Do not rerun the installer; first inspect the existing attempt.", clusterID),
		ClusterURL: clusterURL,
	}
	if recovery.Mode == ProvisionAdopt {
		var adoptionErr *AdoptionUpgradeError
		var preMutationErr *ProvisionPreMutationError
		switch {
		case errors.As(provisionErr, &preMutationErr):
			g.Lines = append(g.Lines, "The Helm upgrade did not start, so there was no rollback to verify.")
			if preMutationErr.TokenSecretMayExist {
				g.Lines = append(g.Lines, "Kubernetes did not confirm whether the Cloud token Secret was created. Inspect the fixed Secret name before deciding how to recover this Hub cluster.")
			} else {
				g.Lines = append(g.Lines,
					"This attempt created no Cloud token Secret and made no change to the existing Helm release.",
					"Use this Hub cluster's owner-only Resume install flow to generate fresh credentials, or delete it before deliberately starting over.",
				)
			}
		case errors.As(provisionErr, &adoptionErr) && adoptionErr.RollbackVerified:
			g.Lines = append(g.Lines, fmt.Sprintf("Helm's atomic rollback restored the original release after the failed adoption (the pre-adoption revision was %d).", recovery.CurrentRevision))
			if adoptionErr.TokenSecretPreserved {
				g.Lines = append(g.Lines, "The Cloud token Secret could not be safely removed; keep it while you inspect the release and recover this same Hub cluster.")
			} else {
				g.Lines = append(g.Lines, "The unchanged Cloud token Secret created by this attempt was removed after rollback verification.")
			}
		default:
			g.Lines = append(g.Lines, "Radar could not prove that the original release was fully restored, so it preserved the Cloud token Secret. Inspect and recover this same Hub cluster before any cleanup.")
		}
		g.Inspect = []string{
			fmt.Sprintf("%s status %s -n %s", target.Helm(), recovery.ReleaseName, recovery.Namespace),
			fmt.Sprintf("%s history %s -n %s", target.Helm(), recovery.ReleaseName, recovery.Namespace),
			fmt.Sprintf("%s -n %s get secret/%s", target.Kubectl(), recovery.Namespace, CloudTokenSecretName),
			fmt.Sprintf("%s -n %s get deployment/%s", target.Kubectl(), recovery.Deployment.Namespace, recovery.Deployment.Name),
		}
		return g
	}

	g.Inspect = []string{
		fmt.Sprintf("%s status %s -n %s", target.Helm(), recovery.ReleaseName, recovery.Namespace),
		fmt.Sprintf("%s -n %s get secret/%s", target.Kubectl(), recovery.Namespace, CloudTokenSecretName),
		fmt.Sprintf("%s -n %s get deployment/%s", target.Kubectl(), recovery.Deployment.Namespace, recovery.Deployment.Name),
	}
	g.Lines = append(g.Lines,
		"The installer removes only the unchanged token Secret it created when a Helm failure can be cleaned up safely; verify the actual release and Secret state.",
		"If the token Secret remains, recover the partial install with this Hub cluster. If the Secret was cleaned up, the token is no longer recoverable: clean up any partial Helm release, then delete this Hub cluster before starting a fresh flow.",
	)
	return g
}

// TunnelConfirmationGuidance covers a provisioned install whose tunnel did not
// confirm within the wait window.
func TunnelConfirmationGuidance(err error, clusterID, cloudURL, clusterURL string, deployment helm.DeploymentRef, target CommandTarget) RecoveryGuidance {
	reason := "confirmation failed"
	if err != nil {
		reason = err.Error()
	}
	switch {
	case errors.Is(err, cloud.ErrConnectConsumptionTimeout):
		reason = "the five-minute confirmation window elapsed"
	case errors.Is(err, cloud.ErrConnectPickupExpired):
		reason = "the Hub stopped reporting the approved request before the in-cluster Radar connected"
	case errors.Is(err, context.Canceled):
		reason = "confirmation was canceled"
	}

	return RecoveryGuidance{
		Summary: fmt.Sprintf("Radar was provisioned and Hub cluster %q already exists, but its tunnel could not be confirmed: %s.", clusterID, reason),
		Lines: []string{
			"Do not rerun the installer or delete the cluster by default; the installed Radar can still connect after you resolve its startup or egress issue.",
			fmt.Sprintf("Verify cluster DNS and outbound WSS/HTTPS access to %s.", cloudURL),
			"Keep using this Hub cluster and token Secret for recovery. Only if you deliberately abandon the installation should you clean up Helm and the Secret, then delete the Hub cluster before starting a fresh flow.",
		},
		Inspect: []string{
			fmt.Sprintf("%s -n %s rollout status deployment/%s", target.Kubectl(), deployment.Namespace, deployment.Name),
			fmt.Sprintf("%s -n %s logs deployment/%s --all-containers=true --tail=200", target.Kubectl(), deployment.Namespace, deployment.Name),
		},
		ClusterURL: clusterURL,
	}
}

// AdoptionRollbackGuidance tells a successfully adopted install how to undo
// the adoption deliberately later.
func AdoptionRollbackGuidance(recovery ProvisionRecovery, clusterURL string, target CommandTarget) RecoveryGuidance {
	// Inspect holds only runnable commands — both presenters render it as a
	// copyable shell block, so prose there would be pasted into a terminal.
	return RecoveryGuidance{
		Summary: "To deliberately undo this adoption later:",
		Inspect: []string{
			fmt.Sprintf("%s rollback %s %d -n %s", target.Helm(), recovery.ReleaseName, recovery.CurrentRevision, recovery.Namespace),
			fmt.Sprintf("%s -n %s delete secret/%s", target.Kubectl(), recovery.Namespace, CloudTokenSecretName),
		},
		// Order-independent prose: the CLI prints the commands above these
		// lines and the UI prints them below, so nothing here may lean on
		// "then" or "the steps above" to make sense.
		Lines: []string{
			"The Helm rollback restores the exact pre-adoption chart, image pin, values, and RBAC. Delete the Secret only once that rollback has succeeded.",
			"An organization owner must also delete the connected cluster in the Hub — until they do, its fleet row shows as disconnected.",
		},
		ClusterURL: clusterURL,
	}
}
