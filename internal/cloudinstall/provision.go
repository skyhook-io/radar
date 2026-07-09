package cloudinstall

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/skyhook-io/radar/internal/helm"
)

// Provision installs the Radar chart into a cluster with Cloud mode enabled, so
// the in-cluster Radar comes up already connected to the hub with full per-user
// RBAC (impersonation). It runs as the caller's own kube identity — the only
// credential that can legitimately provision the impersonation RBAC (see
// preflight.go). This backs the local `radar cloud install` driver.
//
// The Cloud token is delivered via a pre-created Secret (referenced through
// cloud.existingSecret), NOT inlined into the Deployment manifest — matching the
// install wizard and keeping the token out of the Helm release.

const (
	// DefaultInstallNamespace / DefaultReleaseName match the install wizard's
	// `-n radar` + release `radar` so a driver install and a wizard install are
	// the same object.
	DefaultInstallNamespace = "radar"
	DefaultReleaseName      = "radar"
	// CloudTokenSecretName is the Secret the chart reads the token from via
	// cloud.existingSecret; cloudTokenSecretKey is its data key.
	CloudTokenSecretName = "radar-cloud-config"
	cloudTokenSecretKey  = "token"
	// chartRepo/chartName resolve the PUBLISHED chart (what `helm repo add
	// skyhook` serves) so the driver installs exactly what users get today.
	chartRepo = "https://skyhook-io.github.io/helm-charts"
	chartName = "radar"
)

// ProvisionConfig is the install driver's input. Namespace/ReleaseName default
// to the wizard's when empty.
type ProvisionConfig struct {
	Namespace    string
	ReleaseName  string
	CloudURL     string // wss://api.radarhq.io/agent — the hub agent endpoint
	ClusterID    string // hub-assigned cluster id (→ cloud.clusterName)
	Token        string // rhc_ cluster token minted by the device-flow approve
	ChartVersion string // "" / "latest" → newest published
}

func (c ProvisionConfig) namespace() string {
	if c.Namespace != "" {
		return c.Namespace
	}
	return DefaultInstallNamespace
}

func (c ProvisionConfig) releaseName() string {
	if c.ReleaseName != "" {
		return c.ReleaseName
	}
	return DefaultReleaseName
}

// ErrReleaseExists signals that a Radar release is already installed in the
// target namespace, so the driver should hand off to a guided `helm upgrade`
// rather than a fresh install (the MVP does not do in-place value merges).
var ErrReleaseExists = errors.New("radar is already installed in this namespace")

// Provision creates the token Secret and installs the cloud-enabled chart.
// Callers MUST run InstallPreflight first (before minting the token).
func Provision(ctx context.Context, hc *helm.Client, kc kubernetes.Interface, cfg ProvisionConfig) error {
	if hc == nil || kc == nil {
		return fmt.Errorf("provision: nil helm or kubernetes client")
	}
	if cfg.Token == "" || cfg.CloudURL == "" || cfg.ClusterID == "" {
		return fmt.Errorf("provision: token, cloud URL, and cluster id are required")
	}
	ns := cfg.namespace()

	// Gate before ANY side effect (namespace, Secret) — a fresh install can't
	// replace a deployed release. The CLI checks this pre-mint too; this catches
	// a release that appeared in between (race), so we never write an orphaned
	// token Secret for an install that will fail.
	if blocked, err := hc.ReleaseInstallBlocked(ns, cfg.releaseName()); err != nil {
		return fmt.Errorf("inspect existing release: %w", err)
	} else if blocked {
		return ErrReleaseExists
	}

	if err := ensureNamespace(ctx, kc, ns); err != nil {
		return fmt.Errorf("ensure namespace %q: %w", ns, err)
	}
	if err := upsertTokenSecret(ctx, kc, ns, cfg.Token); err != nil {
		return fmt.Errorf("write token secret: %w", err)
	}

	req := &helm.InstallRequest{
		ReleaseName:     cfg.releaseName(),
		Namespace:       ns,
		ChartName:       chartName,
		Version:         cfg.ChartVersion,
		Repository:      chartRepo,
		CreateNamespace: true,
		Values:          cloudInstallValues(cfg.CloudURL, cfg.ClusterID),
	}
	if _, err := hc.Install(req); err != nil {
		// Belt-and-suspenders for a race that slips past the pre-check.
		var exists *helm.ReleaseExistsError
		if errors.As(err, &exists) {
			return ErrReleaseExists
		}
		return fmt.Errorf("helm install: %w", err)
	}
	return nil
}

// cloudInstallValues mirrors the wizard's fresh-install --set flags
// (installCommand.ts): cloud.* + the rbac.* surface a cloud install needs.
func cloudInstallValues(cloudURL, clusterID string) map[string]any {
	return map[string]any{
		"cloud": map[string]any{
			"enabled":        true,
			"url":            cloudURL,
			"clusterName":    clusterID, // carries the hub cluster id
			"existingSecret": CloudTokenSecretName,
		},
		"auth": map[string]any{"mode": "proxy"},
		"rbac": map[string]any{
			"helm":        true,
			"secrets":     true,
			"podExec":     true,
			"portForward": true,
			"metrics":     true,
			"selfUpgrade": true,
		},
	}
}

func ensureNamespace(ctx context.Context, kc kubernetes.Interface, ns string) error {
	_, err := kc.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	_, err = kc.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: ns},
	}, metav1.CreateOptions{})
	if err != nil && apierrors.IsAlreadyExists(err) {
		return nil // raced with another writer — fine
	}
	return err
}

// upsertTokenSecret creates or updates the token Secret idempotently so re-runs
// (e.g. after a rotated token) don't fail on a pre-existing Secret.
func upsertTokenSecret(ctx context.Context, kc kubernetes.Interface, ns, token string) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: CloudTokenSecretName, Namespace: ns},
		Type:       corev1.SecretTypeOpaque,
		StringData: map[string]string{cloudTokenSecretKey: token},
	}
	_, err := kc.CoreV1().Secrets(ns).Create(ctx, secret, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return err
	}
	// Exists → patch the token key in place.
	existing, gerr := kc.CoreV1().Secrets(ns).Get(ctx, CloudTokenSecretName, metav1.GetOptions{})
	if gerr != nil {
		return gerr
	}
	if existing.Data == nil {
		existing.Data = map[string][]byte{}
	}
	existing.StringData = map[string]string{cloudTokenSecretKey: token}
	_, uerr := kc.CoreV1().Secrets(ns).Update(ctx, existing, metav1.UpdateOptions{})
	return uerr
}
