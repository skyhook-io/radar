package issues

import (
	"strings"

	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// classifyInput is the minimal signal the classifier reads. It mirrors the
// fields an Issue already carries, so wiring is a field read (no new data).
type classifyInput struct {
	Source               Source
	APIGroup             string // the resource's API group (Issue.Group)
	Kind                 string
	Reason               string
	Message              string
	LastTerminatedReason string
}

// Classify maps a detection signal to a user-facing issue category. Pure and
// deterministic — same inputs always yield the same category, so the
// category-in-issue-id contract stays stable (no oscillation). Grounded in the
// exact reason vocabulary emitted by the detector layer in internal/k8s and the
// CRD-condition source in internal/issues.
//
// Coverage is intentionally partial: signals without a clean mapping (and
// categories whose detectors don't exist yet — network policy, broad DNS)
// fall to unknown rather than being force-fit.
func Classify(in classifyInput) issuesapi.Category {
	switch in.Source {
	case SourceScheduling:
		switch in.Reason {
		case "Unschedulable":
			return issuesapi.CategoryUnschedulable
		case "QuotaExceeded", "LimitRangeViolation":
			return issuesapi.CategoryQuotaExceeded
		case "PodSecurityViolation":
			// Pod Security admission (built-in PSA) is NOT a webhook — don't
			// mislabel it as such.
			return issuesapi.CategoryPodSecurityViolation
		case "WebhookDenied":
			return issuesapi.CategoryAdmissionWebhookBlocking
		case "RBACForbidden":
			return issuesapi.CategoryRBACForbidden
		case "IPExhaustion", "SandboxCreationFailed", "PostBindStartupStall":
			// scheduled but stuck creating the sandbox — a startup-stage stall
			return issuesapi.CategoryContainerWaiting
		case "VolumeMultiAttach", "VolumeAttach", "VolumeMount":
			return issuesapi.CategoryVolumeMountFailed
		}
		return issuesapi.CategoryUnknown

	case SourceMissingRef:
		// Ingress backend refs are their own category; webhook backends map to
		// the control-plane "backend down"; everything else is a dangling
		// config/resource reference.
		switch in.Reason {
		case "Missing backend Service", "Missing backend Service port":
			return issuesapi.CategoryIngressBackendMissing
		case "Missing Gateway backend Service", "Missing Gateway backend Service port", "Missing Gateway ReferenceGrant":
			return issuesapi.CategoryGatewayRouteInvalid
		case "Missing webhook backend Service":
			return issuesapi.CategoryWebhookBackendDown
		case "Missing StorageClass":
			// the dangling ref is a StorageClass, but the user-facing effect is
			// a PVC that can't provision — surface it under storage.
			return issuesapi.CategoryPVCPending
		}
		// Missing PVC/ConfigMap/Secret/ServiceAccount/imagePullSecret (Pod),
		// Missing scaleTargetRef (HPA), Missing headless Service (StatefulSet),
		// Missing TLS Secret (Ingress), Missing roleRef target (RoleBinding).
		return issuesapi.CategoryMissingConfigRef

	case SourceCondition:
		// Generic CRD .status.conditions[]=False fallback. Discriminate the
		// well-known controller families by API group.
		g := strings.ToLower(in.APIGroup)
		switch {
		case strings.Contains(g, "cert-manager.io"):
			// Only a Certificate is "certificate not ready". Issuer/ClusterIssuer/
			// Order/Challenge are different objects — a not-ready Issuer is a
			// control-plane condition, not a certificate problem.
			if in.Kind == "Certificate" {
				return issuesapi.CategoryCertificateNotReady
			}
			return issuesapi.CategoryOperatorConditionFail
		case strings.Contains(g, "argoproj.io"):
			switch in.Kind {
			case "Application":
				return classifyGitOpsReason(in.Reason, issuesapi.CategoryGitOpsSyncFailed)
			case "Rollout":
				// Progressive-delivery workload, not a sync operation.
				return issuesapi.CategoryRolloutStalled
			}
			// AppProject/ApplicationSet/etc. are control-plane CRDs, not a sync.
			return issuesapi.CategoryOperatorConditionFail
		case g == "gateway.networking.k8s.io":
			switch in.Kind {
			case "GatewayClass", "Gateway":
				return issuesapi.CategoryGatewayNotReady
			case "HTTPRoute", "GRPCRoute", "TCPRoute", "TLSRoute":
				return issuesapi.CategoryGatewayRouteInvalid
			}
			return issuesapi.CategoryOperatorConditionFail
		case g == "apiregistration.k8s.io" && in.Kind == "APIService":
			return issuesapi.CategoryAPIServiceUnavailable
		case g == "external-secrets.io":
			return issuesapi.CategorySecretSyncFailed
		case g == "keda.sh":
			return issuesapi.CategoryHPALimitedOrFailed
		case strings.Contains(g, "karpenter"):
			return issuesapi.CategoryNodeProvisioningFail
		case strings.Contains(g, "crossplane.io"):
			return issuesapi.CategoryCrossplaneReconcile
		case g == "source.toolkit.fluxcd.io" || g == "image.toolkit.fluxcd.io" || g == "notification.toolkit.fluxcd.io":
			// Source objects (GitRepository/OCIRepository/HelmRepository/…) fail
			// at the fetch/render stage, so render_failed is the right default.
			// Reasons here arrive as condTypeReason display strings ("Ready:
			// GitOperationFailed"), which deliberately don't match the bare-token
			// cases in classifyGitOpsReason — and shouldn't: sources have no
			// apply/install/upgrade phase, so falling through to render_failed is
			// correct. Don't "fix" this by stripping the prefix; that would
			// mislabel a source's fetch failure as an operation failure.
			return classifyGitOpsReason(in.Reason, issuesapi.CategoryGitOpsRenderFailed)
		default:
			return issuesapi.CategoryOperatorConditionFail
		}

	case SourceProblem:
		return classifyProblem(in)
	}
	return issuesapi.CategoryUnknown
}

// classifyProblem handles the broad source=problem channel (radar's per-kind
// detection). Split out to keep Classify readable.
func classifyProblem(in classifyInput) issuesapi.Category {
	if in.Reason == "Terminating stuck" || in.Reason == "Namespace terminating stuck" {
		return issuesapi.CategoryTerminationStuck
	}
	switch in.Reason {
	case "CoreDNS NXDOMAIN override", "CoreDNS service DNS rewrite":
		return issuesapi.CategoryDNSFailure
	case "Missing referenced Service":
		return issuesapi.CategoryMissingConfigRef
	case "Service port mismatch":
		return issuesapi.CategoryMissingConfigRef
	}
	if isForbiddenMessage(in.Message) && !isBatchFailureProblem(in.Kind, in.Reason) {
		return issuesapi.CategoryRBACForbidden
	}
	switch in.Kind {
	case "Pod":
		if in.Reason == "OOMKilled" {
			return issuesapi.CategoryOOMKilled
		}
		switch in.Reason {
		case "ImagePullBackOff", "ErrImagePull", "InvalidImageName", "ImageInspectError":
			return issuesapi.CategoryImagePullFailed
		case "CrashLoopBackOff":
			if in.LastTerminatedReason == "OOMKilled" {
				return issuesapi.CategoryOOMKilled
			}
			return issuesapi.CategoryCrashLoop
		case "HighRestartCount":
			return issuesapi.CategoryHighRestart
		case "LivenessProbeFailed", "LivenessProbeInvalid":
			return issuesapi.CategoryLivenessProbeFail
		case "ReadinessProbeFailed", "ReadinessProbeInvalid":
			return issuesapi.CategoryReadinessFailed
		case "InitContainerStalled":
			return issuesapi.CategoryInitContainerFailed
		case "CreateContainerConfigError", "CreateContainerError", "RunContainerError", "Pending", "ContainerCreating":
			return issuesapi.CategoryContainerWaiting
		case "Error", "Failed":
			if in.LastTerminatedReason == "OOMKilled" {
				return issuesapi.CategoryOOMKilled
			}
			// a terminated/failed pod that isn't image-pull/OOM/scheduling —
			// closest runtime bucket is a crash.
			return issuesapi.CategoryCrashLoop
		}
		if in.LastTerminatedReason == "OOMKilled" {
			return issuesapi.CategoryOOMKilled
		}
		return issuesapi.CategoryUnknown

	case "Service", "Ingress":
		if in.Reason == "LoadBalancer pending" {
			return issuesapi.CategoryLoadBalancerPending
		}
		if in.Kind == "Ingress" {
			return issuesapi.CategoryUnknown
		}
		// "Selector matches no pods" / "0/N selected pods ready" /
		// "Unresolved named targetPort" all mean: no healthy endpoints.
		return issuesapi.CategoryServiceNoEndpoints

	case "Deployment", "StatefulSet", "DaemonSet":
		if in.Reason == "Rollout stuck" || in.Reason == "ReplicaFailure" {
			return issuesapi.CategoryRolloutStalled
		}
		// Stable reason literal emitted by sharedRWOVolumeConflicts —
		// a multi-replica Deployment mounting a ReadWriteOnce volume.
		if in.Reason == "ReadWriteOnce volume shared across replicas" {
			return issuesapi.CategoryVolumeAccessModeConflict
		}
		// "{avail}/{desired} available" / "{ready}/{desired} ready" /
		// "{n} unavailable" — workload under its desired healthy count. The
		// pod-level root (crashloop/image/etc.) groups under this once owner
		// grouping lands.
		return issuesapi.CategoryWorkloadDegraded

	case "HorizontalPodAutoscaler":
		return issuesapi.CategoryHPALimitedOrFailed

	case "Node":
		switch in.Reason {
		case "NotReady", "MemoryPressure", "DiskPressure", "PIDPressure":
			return issuesapi.CategoryNodeNotReady
		}
		// "Cordoned" is an intentional admin action, not a failure → unknown.
		return issuesapi.CategoryUnknown

	case "PersistentVolumeClaim":
		switch in.Reason {
		case "Pending":
			return issuesapi.CategoryPVCPending
		case "Lost":
			// bound volume gone — a storage failure, not unknown.
			return issuesapi.CategoryPVCLost
		case "ControllerResizeError", "NodeResizeError", "ModifyVolumeError":
			return issuesapi.CategoryPVCResizeFailed
		}
		return issuesapi.CategoryUnknown

	case "PersistentVolume":
		if in.Reason == "Failed" {
			return issuesapi.CategoryPVFailed
		}
		return issuesapi.CategoryUnknown

	case "PodDisruptionBudget":
		if in.Reason == "Voluntary evictions blocked" {
			return issuesapi.CategoryPDBBlocksEvictions
		}
		return issuesapi.CategoryUnknown

	case "Job":
		// DetectProblems only emits Job problems for genuine failures: a
		// JobFailed condition (reason e.g. BackoffLimitExceeded /
		// DeadlineExceeded, or the "Failed" fallback) or a stuck-active job
		// ("Running for … with no completions"). All map to the batch
		// workload-failure category rather than being discarded.
		return issuesapi.CategoryJobFailed

	case "CronJob":
		// "stale" (no recent run) / "never-scheduled" — the CronJob is not
		// producing the Jobs it's meant to.
		switch in.Reason {
		case "stale", "never-scheduled":
			return issuesapi.CategoryCronJobFailed
		}
		return issuesapi.CategoryUnknown

	case "Workflow", "CronWorkflow":
		if strings.Contains(strings.ToLower(in.APIGroup), "argoproj.io") && isForbiddenMessage(in.Message) {
			return issuesapi.CategoryRBACForbidden
		}
		return issuesapi.CategoryUnknown

	case "Application":
		// ArgoCD Application health/sync failure from DetectGitOpsProblems.
		// Gate on group so a same-named CRD from another controller can't be
		// force-fit into the GitOps bucket.
		if strings.Contains(strings.ToLower(in.APIGroup), "argoproj.io") {
			return classifyGitOpsReason(in.Reason, issuesapi.CategoryGitOpsSyncFailed)
		}
		return issuesapi.CategoryUnknown

	case "Kustomization", "HelmRelease":
		// Flux reconciler failure from DetectGitOpsProblems.
		g := strings.ToLower(in.APIGroup)
		if g == "kustomize.toolkit.fluxcd.io" || g == "helm.toolkit.fluxcd.io" {
			return classifyGitOpsReason(in.Reason, issuesapi.CategoryGitOpsSyncFailed)
		}
		if in.Kind == "HelmRelease" && g == NativeHelmGroup {
			return issuesapi.CategoryHelmReleaseFailed
		}
		return issuesapi.CategoryUnknown

	case "Cluster", "KubeadmControlPlane":
		// Cluster API control plane (cluster.x-k8s.io / controlplane.
		// cluster.x-k8s.io). Gate on the group so a same-named CRD from
		// another controller can't be force-fit.
		if strings.Contains(strings.ToLower(in.APIGroup), "cluster.x-k8s.io") {
			return issuesapi.CategoryControlPlaneNotReady
		}
		return issuesapi.CategoryUnknown

	case "Machine", "MachineDeployment", "MachineHealthCheck":
		// Cluster API machine layer — node-backing infra, distinct from the
		// control plane it forms.
		if strings.Contains(strings.ToLower(in.APIGroup), "cluster.x-k8s.io") {
			return issuesapi.CategoryMachineNotReady
		}
		return issuesapi.CategoryUnknown
	}

	return issuesapi.CategoryUnknown
}

func isForbiddenMessage(message string) bool {
	m := strings.ToLower(message)
	return strings.Contains(m, " is forbidden: user ") && strings.Contains(m, " cannot ")
}

func isBatchFailureProblem(kind, reason string) bool {
	if kind == "Job" {
		return true
	}
	return kind == "CronJob" && (reason == "stale" || reason == "never-scheduled")
}

// classifyGitOpsReason maps a GitOps detector/condition reason to a specific
// GitOps failure category. Shared by the Argo + Flux arms of both Classify
// (conditions) and classifyProblem (DetectGitOpsProblems) so the two paths
// can't drift. The reason vocabulary spans Argo health/sync/spec/operation
// states and Flux source-vs-apply reasons (the case labels below are the
// authoritative list). Unrecognized reasons return the caller's fallback so a
// novel controller reason still lands in a GitOps bucket rather than "unknown".
func classifyGitOpsReason(reason string, fallback issuesapi.Category) issuesapi.Category {
	switch reason {
	case "HealthDegraded", "HealthMissing", "HealthCheckFailed":
		return issuesapi.CategoryGitOpsHealthDegraded
	case "ComparisonError":
		return issuesapi.CategoryGitOpsRenderFailed
	case "InvalidSpecError":
		return issuesapi.CategoryGitOpsSpecInvalid
	case "OperationFailed", "SyncError", "StuckDriftLoop":
		return issuesapi.CategoryGitOpsOperationFailed
	case "OutOfSync":
		return issuesapi.CategoryGitOpsOutOfSync
	// Flux: couldn't fetch/build the source or render manifests.
	case "BuildFailed", "ArtifactFailed", "ArtifactFetchFailed", "SourceNotReady", "GitOperationFailed", "ChartNotReady", "ChartPullFailed", "ChartPullError":
		return issuesapi.CategoryGitOpsRenderFailed
	// Flux: source rendered but applying/installing failed.
	case "InstallFailed", "UpgradeFailed", "TestFailed", "ReconciliationFailed":
		return issuesapi.CategoryGitOpsOperationFailed
	default:
		return fallback
	}
}
