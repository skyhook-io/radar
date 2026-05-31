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
	LastTerminatedReason string
}

// Classify maps a detection signal to a user-facing issue category. Pure and
// deterministic — same inputs always yield the same category, so the
// category-in-issue-id contract stays stable (no oscillation). Grounded in the
// exact reason vocabulary emitted by the detector layer in internal/k8s and the
// CRD-condition source in internal/issues.
//
// Coverage is intentionally partial: signals without a clean mapping (and
// categories whose detectors don't exist yet — probes, DNS, network policy,
// real RBAC-forbidden) fall to unknown rather than being force-fit.
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
		case "IPExhaustion", "SandboxCreationFailed":
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
				return issuesapi.CategoryGitOpsSyncFailed
			case "Rollout":
				// Progressive-delivery workload, not a sync operation.
				return issuesapi.CategoryRolloutStalled
			}
			// AppProject/ApplicationSet/etc. are control-plane CRDs, not a sync.
			return issuesapi.CategoryOperatorConditionFail
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
	switch in.Kind {
	case "Pod":
		// OOM can show as the current Reason or only in the last-terminated
		// reason of a now-restarting container; check both.
		if in.Reason == "OOMKilled" || in.LastTerminatedReason == "OOMKilled" {
			return issuesapi.CategoryOOMKilled
		}
		switch in.Reason {
		case "ImagePullBackOff", "ErrImagePull", "InvalidImageName", "ImageInspectError":
			return issuesapi.CategoryImagePullFailed
		case "CrashLoopBackOff":
			return issuesapi.CategoryCrashLoop
		case "HighRestartCount":
			return issuesapi.CategoryHighRestart
		case "CreateContainerConfigError", "CreateContainerError", "RunContainerError", "Pending", "ContainerCreating":
			return issuesapi.CategoryContainerWaiting
		case "Error", "Failed":
			// a terminated/failed pod that isn't image-pull/OOM/scheduling —
			// closest runtime bucket is a crash.
			return issuesapi.CategoryCrashLoop
		}
		return issuesapi.CategoryUnknown

	case "Service":
		// "Selector matches no pods" / "0/N selected pods ready" /
		// "Unresolved named targetPort" all mean: no healthy endpoints.
		return issuesapi.CategoryServiceNoEndpoints

	case "Deployment", "StatefulSet", "DaemonSet":
		if in.Reason == "Rollout stuck" {
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

	case "Application":
		// ArgoCD Application health/sync failure from DetectGitOpsProblems.
		// Gate on group so a same-named CRD from another controller can't be
		// force-fit into the GitOps bucket.
		if strings.Contains(strings.ToLower(in.APIGroup), "argoproj.io") {
			return issuesapi.CategoryGitOpsSyncFailed
		}
		return issuesapi.CategoryUnknown

	case "Kustomization", "HelmRelease":
		// Flux reconciler failure from DetectGitOpsProblems.
		g := strings.ToLower(in.APIGroup)
		if g == "kustomize.toolkit.fluxcd.io" || g == "helm.toolkit.fluxcd.io" {
			return issuesapi.CategoryGitOpsSyncFailed
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
