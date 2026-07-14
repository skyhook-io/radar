package cloudinstall

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/kubernetes"

	"github.com/skyhook-io/radar/internal/cloud"
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
	// MinimumCloudChartVersion is the first chart that turns Cloud's forwarded
	// identity into enforced Kubernetes impersonation RBAC.
	MinimumCloudChartVersion = "1.5.4"

	preflightCloudURL  = "wss://preflight.invalid/agent"
	preflightClusterID = "preflight-cluster-id"
	secretAttemptKey   = "radarhq.io/cloud-install-attempt"
	secretCleanupLimit = 10 * time.Second
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

// PrepareConfig is the non-secret portion of a fresh install or native Helm
// adoption. Prepare runs before any Hub request or token mint.
type PrepareConfig struct {
	Namespace           string
	ReleaseName         string
	ChartVersion        string
	AdoptExisting       bool
	EnableCloudFeatures bool
	DisableSelfUpgrade  bool
}

func (c PrepareConfig) namespace() string {
	if c.Namespace != "" {
		return c.Namespace
	}
	return DefaultInstallNamespace
}

func (c PrepareConfig) releaseName() string {
	if c.ReleaseName != "" {
		return c.ReleaseName
	}
	return DefaultReleaseName
}

// ResolveCloudChartSummary resolves the same verified stable chart used by
// fresh installs and native Helm adoption without inspecting a Helm release.
// GitOps handoff uses it to pin an exact target in the user's source of truth.
func ResolveCloudChartSummary(ctx context.Context, hc *helm.Client, chartVersion string) (helm.PreparedChartSummary, error) {
	if hc == nil {
		return helm.PreparedChartSummary{}, errors.New("resolve cloud chart: nil helm client")
	}
	return hc.ResolvePreparedChartSummary(ctx, &helm.InstallRequest{
		ReleaseName: DefaultReleaseName,
		Namespace:   DefaultInstallNamespace,
		ChartName:   chartName,
		Version:     chartVersion,
		Repository:  chartRepo,
	}, MinimumCloudChartVersion)
}

// PreparedProvision pins the exact chart and rendered workload across the Hub
// approval flow. Runtime Cloud URL/cluster ID values are supplied only after
// approval; they are server-dry-run again before the token Secret is created.
type PreparedProvision struct {
	install             *helm.PreparedInstall
	upgrade             *helm.PreparedUpgrade
	enableCloudFeatures bool
	disableSelfUpgrade  bool
}

type ProvisionMode string

const (
	ProvisionFresh ProvisionMode = "fresh"
	ProvisionAdopt ProvisionMode = "adopt"
)

func (p *PreparedProvision) Mode() ProvisionMode {
	if p != nil && p.upgrade != nil {
		return ProvisionAdopt
	}
	return ProvisionFresh
}

func (p *PreparedProvision) ChartVersion() string {
	if p == nil {
		return ""
	}
	if p.upgrade != nil {
		return p.upgrade.ChartVersion()
	}
	if p.install == nil {
		return ""
	}
	return p.install.ChartVersion()
}

func (p *PreparedProvision) AppVersion() string {
	if p == nil {
		return ""
	}
	if p.upgrade == nil {
		if p.install == nil {
			return ""
		}
		return p.install.AppVersion()
	}
	return p.upgrade.AppVersion()
}

func (p *PreparedProvision) CurrentChartVersion() string {
	if p == nil || p.upgrade == nil {
		return ""
	}
	return p.upgrade.CurrentChartVersion()
}

func (p *PreparedProvision) CurrentRevision() int {
	if p == nil || p.upgrade == nil {
		return 0
	}
	return p.upgrade.CurrentRevision()
}

func (p *PreparedProvision) CurrentValues() helm.CloudUpgradeValuesSummary {
	if p == nil || p.upgrade == nil {
		return helm.CloudUpgradeValuesSummary{}
	}
	return p.upgrade.CurrentValues()
}

func (p *PreparedProvision) CurrentManifest() string {
	if p == nil || p.upgrade == nil {
		return ""
	}
	return p.upgrade.CurrentManifest()
}

func (p *PreparedProvision) TargetManifest() string {
	if p == nil {
		return ""
	}
	if p.upgrade == nil {
		if p.install == nil {
			return ""
		}
		return p.install.TargetManifest()
	}
	return p.upgrade.TargetManifest()
}

func (p *PreparedProvision) Deployment() helm.DeploymentRef {
	if p == nil {
		return helm.DeploymentRef{}
	}
	if p.upgrade != nil {
		return p.upgrade.Deployment()
	}
	if p.install == nil {
		return helm.DeploymentRef{}
	}
	return p.install.Deployment()
}

func (p *PreparedProvision) Namespace() string {
	if p == nil {
		return ""
	}
	if p.upgrade != nil {
		return p.upgrade.Namespace()
	}
	if p.install == nil {
		return ""
	}
	return p.install.Namespace()
}

func (p *PreparedProvision) ReleaseName() string {
	if p == nil {
		return ""
	}
	if p.upgrade != nil {
		return p.upgrade.ReleaseName()
	}
	if p.install == nil {
		return ""
	}
	return p.install.ReleaseName()
}

func (p *PreparedProvision) values(cloudURL, clusterID string) map[string]any {
	if p.Mode() == ProvisionAdopt {
		return cloudAdoptionValues(cloudURL, clusterID, p.enableCloudFeatures, p.disableSelfUpgrade)
	}
	return cloudInstallValues(cloudURL, clusterID, p.disableSelfUpgrade)
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

// TokenSecretExistsError prevents a fresh enrollment from adopting or
// overwriting credentials left by another install attempt.
type TokenSecretExistsError struct {
	Namespace       string
	Name            string
	UID             types.UID
	ResourceVersion string
}

// ExistingReleaseIncompatibleError reports an existing Radar configuration
// that Cloud adoption cannot safely reinterpret automatically.
type ExistingReleaseIncompatibleError struct {
	Namespace string
	Release   string
	Reason    string
}

func (e *ExistingReleaseIncompatibleError) Error() string {
	return fmt.Sprintf("Helm release %q in namespace %q cannot be adopted: %s", e.Release, e.Namespace, e.Reason)
}

// AdoptionUpgradeError preserves whether the one-time token Secret remains
// after a failed atomic upgrade so CLI recovery copy never guesses.
type AdoptionUpgradeError struct {
	Err                  error
	TokenSecretPreserved bool
	RollbackVerified     bool
}

func (e *AdoptionUpgradeError) Error() string { return e.Err.Error() }
func (e *AdoptionUpgradeError) Unwrap() error { return e.Err }

// ProvisionPreMutationError identifies an adoption failure before Helm's
// upgrade mutation began. TokenSecretMayExist is true only when Kubernetes did
// not conclusively answer the Secret create request; callers must inspect the
// fixed Secret name rather than claiming it was either created or absent.
type ProvisionPreMutationError struct {
	Err                 error
	TokenSecretMayExist bool
}

func (e *ProvisionPreMutationError) Error() string { return e.Err.Error() }
func (e *ProvisionPreMutationError) Unwrap() error { return e.Err }

func (e *TokenSecretExistsError) Error() string {
	return fmt.Sprintf("Secret %q already exists in namespace %q; inspect and remove or recover that installation before retrying",
		e.Name, e.Namespace)
}

// Prepare performs all chart/release/Secret checks that can run before Hub
// enrollment. It has no Kubernetes write side effects.
func Prepare(ctx context.Context, hc *helm.Client, kc kubernetes.Interface, cfg PrepareConfig) (*PreparedProvision, error) {
	if hc == nil || kc == nil {
		return nil, fmt.Errorf("prepare cloud install: nil helm or kubernetes client")
	}
	namespace := cfg.namespace()
	if err := CheckTokenSecretAvailable(ctx, kc, namespace); err != nil {
		return nil, err
	}
	values := cloudInstallValues(preflightCloudURL, preflightClusterID, cfg.DisableSelfUpgrade)
	if cfg.AdoptExisting {
		values = cloudAdoptionValues(preflightCloudURL, preflightClusterID, cfg.EnableCloudFeatures, cfg.DisableSelfUpgrade)
		prepared, err := hc.PrepareCloudUpgrade(ctx, &helm.InstallRequest{
			ReleaseName: cfg.releaseName(), Namespace: namespace,
			ChartName: chartName, Version: cfg.ChartVersion, Repository: chartRepo,
			Values: values,
		}, MinimumCloudChartVersion)
		if err != nil {
			return nil, err
		}
		current := prepared.CurrentValues()
		if current.CloudEnabled || current.CloudTokenSet || current.CloudExistingSecret != "" {
			return nil, &ExistingReleaseIncompatibleError{
				Namespace: namespace, Release: cfg.releaseName(),
				Reason: "Cloud connection values are already configured; recover or disconnect the existing pairing instead of replacing it",
			}
		}
		if current.AuthMode != "none" && current.AuthMode != "proxy" {
			return nil, &ExistingReleaseIncompatibleError{
				Namespace: namespace, Release: cfg.releaseName(),
				Reason: fmt.Sprintf("auth.mode=%q is incompatible with Cloud proxy authentication", current.AuthMode),
			}
		}
		return &PreparedProvision{
			upgrade: prepared, enableCloudFeatures: cfg.EnableCloudFeatures,
			disableSelfUpgrade: cfg.DisableSelfUpgrade,
		}, nil
	}

	prepared, err := hc.PrepareFreshInstall(ctx, &helm.InstallRequest{
		ReleaseName: cfg.releaseName(),
		Namespace:   namespace,
		ChartName:   chartName,
		Version:     cfg.ChartVersion,
		Repository:  chartRepo,
		// ProvisionPrepared creates the namespace first because the token
		// Secret must exist before Helm creates the Radar Deployment. Leaving
		// Helm's duplicate CreateNamespace mutation disabled keeps the prepared
		// permission plan identical to the real install.
		CreateNamespace: false,
		Values:          values,
	}, MinimumCloudChartVersion)
	if err != nil {
		return nil, err
	}
	return &PreparedProvision{install: prepared, disableSelfUpgrade: cfg.DisableSelfUpgrade}, nil
}

// ProvisionPrepared validates final Hub values, creates the token Secret once,
// and applies the chart bytes pinned by Prepare. On any Helm failure it deletes
// the Secret only if its UID and resourceVersion still identify the exact
// object created by this attempt.
func ProvisionPrepared(ctx context.Context, kc kubernetes.Interface, prepared *PreparedProvision, cfg ProvisionConfig) error {
	if kc == nil || prepared == nil || (prepared.install == nil && prepared.upgrade == nil) {
		return fmt.Errorf("provision prepared install: nil kubernetes client or plan")
	}
	preMutationError := func(err error, tokenSecretMayExist bool) error {
		if prepared.Mode() != ProvisionAdopt {
			return err
		}
		return &ProvisionPreMutationError{Err: err, TokenSecretMayExist: tokenSecretMayExist}
	}
	if err := validateProvisionConfig(cfg); err != nil {
		return preMutationError(err, false)
	}
	if cfg.namespace() != prepared.Namespace() || cfg.releaseName() != prepared.ReleaseName() {
		return preMutationError(fmt.Errorf("provision target %q/%q does not match prepared target %q/%q",
			cfg.namespace(), cfg.releaseName(), prepared.Namespace(), prepared.ReleaseName()), false)
	}
	if cfg.ChartVersion != "" && cfg.ChartVersion != "latest" && cfg.ChartVersion != prepared.ChartVersion() {
		return preMutationError(fmt.Errorf("provision chart version %q does not match prepared version %q", cfg.ChartVersion, prepared.ChartVersion()), false)
	}
	if err := CheckTokenSecretAvailable(ctx, kc, prepared.Namespace()); err != nil {
		return preMutationError(err, false)
	}
	values := prepared.values(cfg.CloudURL, cfg.ClusterID)
	if prepared.Mode() == ProvisionAdopt {
		if err := prepared.upgrade.Validate(ctx, values); err != nil {
			return preMutationError(err, false)
		}
	} else {
		if err := prepared.install.Validate(ctx, values); err != nil {
			return err
		}
		if err := ensureNamespace(ctx, kc, prepared.Namespace()); err != nil {
			return fmt.Errorf("ensure namespace %q: %w", prepared.Namespace(), err)
		}
	}
	created, err := createTokenSecret(ctx, kc, prepared.Namespace(), cfg.Token)
	if err != nil {
		mayExist := true
		var exists *TokenSecretExistsError
		var status apierrors.APIStatus
		if errors.As(err, &exists) || (errors.As(err, &status) && status.Status().Code >= 400 && status.Status().Code < 500) {
			mayExist = false
		}
		return preMutationError(fmt.Errorf("create token Secret: %w", err), mayExist)
	}
	var provisionErr error
	if prepared.Mode() == ProvisionAdopt {
		_, provisionErr = prepared.upgrade.Upgrade(ctx, values)
	} else {
		_, provisionErr = prepared.install.Install(ctx, values)
	}
	if provisionErr != nil {
		// The Helm mutation is an intentional non-cancelable critical section.
		// Cleanup must likewise survive the signal that may have arrived while it
		// ran; otherwise an unchanged live credential is left behind solely because
		// the caller context was canceled.
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), secretCleanupLimit)
		defer cleanupCancel()
		if prepared.Mode() == ProvisionAdopt {
			rolledBack, verifyErr := prepared.upgrade.VerifyRolledBack(cleanupCtx)
			if verifyErr != nil || !rolledBack {
				if verifyErr != nil {
					provisionErr = errors.Join(provisionErr, fmt.Errorf("verify atomic rollback: %w", verifyErr))
				}
				return &AdoptionUpgradeError{Err: provisionErr, TokenSecretPreserved: true}
			}
			cleanupErr := deleteTokenSecretIfUnchanged(cleanupCtx, kc, *created)
			if cleanupErr != nil {
				return &AdoptionUpgradeError{
					Err:                  errors.Join(provisionErr, fmt.Errorf("clean up token Secret after verified rollback: %w", cleanupErr)),
					TokenSecretPreserved: true, RollbackVerified: true,
				}
			}
			return &AdoptionUpgradeError{Err: provisionErr, RollbackVerified: true}
		}
		cleanupErr := deleteTokenSecretIfUnchanged(cleanupCtx, kc, *created)
		if cleanupErr != nil {
			return errors.Join(provisionErr, fmt.Errorf("clean up token Secret after failed install: %w", cleanupErr))
		}
		return provisionErr
	}
	return nil
}

func validateProvisionConfig(cfg ProvisionConfig) error {
	if strings.TrimSpace(cfg.Token) == "" || strings.TrimSpace(cfg.CloudURL) == "" || strings.TrimSpace(cfg.ClusterID) == "" {
		return fmt.Errorf("provision: token, cloud URL, and cluster id are required")
	}
	if err := cloud.ValidateWebSocketURL(cfg.CloudURL); err != nil {
		return fmt.Errorf("provision: %w", err)
	}
	return nil
}

// cloudInstallValues mirrors the wizard's fresh-install --set flags
// (installCommand.ts): cloud.* + the rbac.* surface a cloud install needs.
func cloudInstallValues(cloudURL, clusterID string, disableSelfUpgrade bool) map[string]any {
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
			"selfUpgrade": !disableSelfUpgrade,
		},
	}
}

// cloudAdoptionValues preserves an existing release's values and overlays only
// the Cloud connection plus explicitly consented capability changes. image.tag
// is cleared inside helm.PreparedUpgrade so the selected chart's AppVersion is
// always the resulting Radar version.
func cloudAdoptionValues(cloudURL, clusterID string, enableCloudFeatures, disableSelfUpgrade bool) map[string]any {
	rbac := map[string]any{"selfUpgrade": !disableSelfUpgrade}
	if enableCloudFeatures {
		rbac["helm"] = true
		rbac["secrets"] = true
		rbac["podExec"] = true
		rbac["portForward"] = true
		rbac["metrics"] = true
	}
	return map[string]any{
		"cloud": map[string]any{
			"enabled": true, "url": cloudURL,
			"clusterName": clusterID, "existingSecret": CloudTokenSecretName,
		},
		"rbac": rbac,
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

// CheckTokenSecretAvailable is the pre-mint, read-only gate for the fixed Cloud
// credential Secret name.
func CheckTokenSecretAvailable(ctx context.Context, kc kubernetes.Interface, ns string) error {
	if kc == nil {
		return errors.New("inspect token Secret: nil kubernetes client")
	}
	existing, err := kc.CoreV1().Secrets(ns).Get(ctx, CloudTokenSecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect token Secret %q in namespace %q: %w", CloudTokenSecretName, ns, err)
	}
	return tokenSecretExistsError(existing)
}

type tokenSecretIdentity struct {
	Namespace       string
	Name            string
	UID             types.UID
	ResourceVersion string
}

func createTokenSecret(ctx context.Context, kc kubernetes.Interface, ns, token string) (*tokenSecretIdentity, error) {
	attemptID := string(uuid.NewUUID())
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: CloudTokenSecretName, Namespace: ns,
			Annotations: map[string]string{secretAttemptKey: attemptID},
		},
		Type:       corev1.SecretTypeOpaque,
		StringData: map[string]string{cloudTokenSecretKey: token},
	}
	created, err := kc.CoreV1().Secrets(ns).Create(ctx, secret, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		existing, getErr := kc.CoreV1().Secrets(ns).Get(ctx, CloudTokenSecretName, metav1.GetOptions{})
		if getErr != nil {
			return nil, errors.Join(err, fmt.Errorf("inspect existing token Secret: %w", getErr))
		}
		return nil, tokenSecretExistsError(existing)
	}
	if err != nil {
		return nil, err
	}
	if created.UID == "" || created.ResourceVersion == "" {
		return nil, fmt.Errorf("Kubernetes created Secret %q but omitted UID/resourceVersion; refusing an install whose failure could not safely clean up that credential", CloudTokenSecretName)
	}
	return &tokenSecretIdentity{
		Namespace: ns, Name: created.Name, UID: created.UID, ResourceVersion: created.ResourceVersion,
	}, nil
}

func tokenSecretExistsError(secret *corev1.Secret) *TokenSecretExistsError {
	return &TokenSecretExistsError{
		Namespace: secret.Namespace, Name: secret.Name,
		UID: secret.UID, ResourceVersion: secret.ResourceVersion,
	}
}

func deleteTokenSecretIfUnchanged(ctx context.Context, kc kubernetes.Interface, created tokenSecretIdentity) error {
	current, err := kc.CoreV1().Secrets(created.Namespace).Get(ctx, created.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.UID != created.UID || current.ResourceVersion != created.ResourceVersion {
		return fmt.Errorf("Secret %q changed after this install created it (created UID/resourceVersion %s/%s, current %s/%s); refusing to delete it",
			created.Name, created.UID, created.ResourceVersion, current.UID, current.ResourceVersion)
	}
	uid := created.UID
	resourceVersion := created.ResourceVersion
	return kc.CoreV1().Secrets(created.Namespace).Delete(ctx, created.Name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &resourceVersion},
	})
}
