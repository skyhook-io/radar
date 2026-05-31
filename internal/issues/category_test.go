package issues

import (
	"testing"

	"github.com/skyhook-io/radar/pkg/issuesapi"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		in   classifyInput
		want issuesapi.Category
	}{
		// scheduling
		{"unschedulable", classifyInput{Source: SourceScheduling, Kind: "Pod", Reason: "Unschedulable"}, issuesapi.CategoryUnschedulable},
		{"quota", classifyInput{Source: SourceScheduling, Kind: "Deployment", Reason: "QuotaExceeded"}, issuesapi.CategoryQuotaExceeded},
		{"limitrange", classifyInput{Source: SourceScheduling, Kind: "Deployment", Reason: "LimitRangeViolation"}, issuesapi.CategoryQuotaExceeded},
		{"podsecurity", classifyInput{Source: SourceScheduling, Kind: "Deployment", Reason: "PodSecurityViolation"}, issuesapi.CategoryPodSecurityViolation},
		{"webhook denied", classifyInput{Source: SourceScheduling, Kind: "StatefulSet", Reason: "WebhookDenied"}, issuesapi.CategoryAdmissionWebhookBlocking},
		{"ip exhaustion is startup stall", classifyInput{Source: SourceScheduling, Kind: "Pod", Reason: "IPExhaustion"}, issuesapi.CategoryContainerWaiting},
		{"sandbox failed is startup stall", classifyInput{Source: SourceScheduling, Kind: "Pod", Reason: "SandboxCreationFailed"}, issuesapi.CategoryContainerWaiting},
		{"volume multiattach", classifyInput{Source: SourceScheduling, Kind: "Pod", Reason: "VolumeMultiAttach"}, issuesapi.CategoryVolumeMountFailed},
		{"volume mount", classifyInput{Source: SourceScheduling, Kind: "Pod", Reason: "VolumeMount"}, issuesapi.CategoryVolumeMountFailed},

		// problem / Pod
		{"image pull backoff", classifyInput{Source: SourceProblem, Kind: "Pod", Reason: "ImagePullBackOff"}, issuesapi.CategoryImagePullFailed},
		{"err image pull", classifyInput{Source: SourceProblem, Kind: "Pod", Reason: "ErrImagePull"}, issuesapi.CategoryImagePullFailed},
		{"crashloop", classifyInput{Source: SourceProblem, Kind: "Pod", Reason: "CrashLoopBackOff"}, issuesapi.CategoryCrashLoop},
		{"oom by reason", classifyInput{Source: SourceProblem, Kind: "Pod", Reason: "OOMKilled"}, issuesapi.CategoryOOMKilled},
		{"oom by last-terminated, crashloop reason", classifyInput{Source: SourceProblem, Kind: "Pod", Reason: "CrashLoopBackOff", LastTerminatedReason: "OOMKilled"}, issuesapi.CategoryOOMKilled},
		{"config error waiting", classifyInput{Source: SourceProblem, Kind: "Pod", Reason: "CreateContainerConfigError"}, issuesapi.CategoryContainerWaiting},
		{"pending pod (non-scheduling)", classifyInput{Source: SourceProblem, Kind: "Pod", Reason: "Pending"}, issuesapi.CategoryContainerWaiting},
		{"errored pod", classifyInput{Source: SourceProblem, Kind: "Pod", Reason: "Error"}, issuesapi.CategoryCrashLoop},
		{"high restart thrash", classifyInput{Source: SourceProblem, Kind: "Pod", Reason: "HighRestartCount"}, issuesapi.CategoryHighRestart},

		// problem / GitOps reconcilers (DetectGitOpsProblems → SourceProblem)
		{"argo app degraded", classifyInput{Source: SourceProblem, Kind: "Application", APIGroup: "argoproj.io", Reason: "HealthDegraded"}, issuesapi.CategoryGitOpsSyncFailed},
		{"argo app outofsync", classifyInput{Source: SourceProblem, Kind: "Application", APIGroup: "argoproj.io", Reason: "OutOfSync"}, issuesapi.CategoryGitOpsSyncFailed},
		{"flux kustomization problem", classifyInput{Source: SourceProblem, Kind: "Kustomization", APIGroup: "kustomize.toolkit.fluxcd.io", Reason: "ReconciliationFailed"}, issuesapi.CategoryGitOpsSyncFailed},
		{"flux helmrelease problem", classifyInput{Source: SourceProblem, Kind: "HelmRelease", APIGroup: "helm.toolkit.fluxcd.io", Reason: "InstallFailed"}, issuesapi.CategoryGitOpsSyncFailed},
		{"flux-looking kustomization group is not gitops", classifyInput{Source: SourceProblem, Kind: "Kustomization", APIGroup: "custom-fluxcd.io", Reason: "ReconciliationFailed"}, issuesapi.CategoryUnknown},
		{"non-argo Application kind is not gitops", classifyInput{Source: SourceProblem, Kind: "Application", APIGroup: "other.example.com", Reason: "whatever"}, issuesapi.CategoryUnknown},

		// argo Rollout is progressive delivery, NOT a sync failure
		{"argo rollout condition is rollout_stalled", classifyInput{Source: SourceCondition, Kind: "Rollout", APIGroup: "argoproj.io", Reason: "Ready: ProgressDeadlineExceeded"}, issuesapi.CategoryRolloutStalled},

		// problem / workloads
		{"deploy degraded", classifyInput{Source: SourceProblem, Kind: "Deployment", Reason: "3/5 available"}, issuesapi.CategoryWorkloadDegraded},
		{"deploy rollout stuck", classifyInput{Source: SourceProblem, Kind: "Deployment", Reason: "Rollout stuck"}, issuesapi.CategoryRolloutStalled},
		{"statefulset degraded", classifyInput{Source: SourceProblem, Kind: "StatefulSet", Reason: "2/3 ready"}, issuesapi.CategoryWorkloadDegraded},
		{"daemonset degraded", classifyInput{Source: SourceProblem, Kind: "DaemonSet", Reason: "1 unavailable"}, issuesapi.CategoryWorkloadDegraded},

		// problem / service
		{"service no pods", classifyInput{Source: SourceProblem, Kind: "Service", Reason: "Selector matches no pods"}, issuesapi.CategoryServiceNoEndpoints},
		{"service 0 ready", classifyInput{Source: SourceProblem, Kind: "Service", Reason: "0/3 selected pods ready"}, issuesapi.CategoryServiceNoEndpoints},

		// problem / hpa, node, pvc
		{"hpa maxed", classifyInput{Source: SourceProblem, Kind: "HorizontalPodAutoscaler", Reason: "maxed"}, issuesapi.CategoryHPALimitedOrFailed},
		{"node notready", classifyInput{Source: SourceProblem, Kind: "Node", Reason: "NotReady"}, issuesapi.CategoryNodeNotReady},
		{"node mempressure", classifyInput{Source: SourceProblem, Kind: "Node", Reason: "MemoryPressure"}, issuesapi.CategoryNodeNotReady},
		{"node cordoned is intentional", classifyInput{Source: SourceProblem, Kind: "Node", Reason: "Cordoned"}, issuesapi.CategoryUnknown},
		{"pvc pending", classifyInput{Source: SourceProblem, Kind: "PersistentVolumeClaim", Reason: "Pending"}, issuesapi.CategoryPVCPending},
		{"pvc lost is storage", classifyInput{Source: SourceProblem, Kind: "PersistentVolumeClaim", Reason: "Lost"}, issuesapi.CategoryPVCLost},
		{"pdb blocks evictions", classifyInput{Source: SourceProblem, Kind: "PodDisruptionBudget", Reason: "Voluntary evictions blocked"}, issuesapi.CategoryPDBBlocksEvictions},

		// problem / batch (Job/CronJob)
		{"job failed condition", classifyInput{Source: SourceProblem, Kind: "Job", Reason: "BackoffLimitExceeded"}, issuesapi.CategoryJobFailed},
		{"job failed fallback", classifyInput{Source: SourceProblem, Kind: "Job", Reason: "Failed"}, issuesapi.CategoryJobFailed},
		{"job stuck active", classifyInput{Source: SourceProblem, Kind: "Job", Reason: "Running for 3h with no completions"}, issuesapi.CategoryJobFailed},
		{"cronjob stale", classifyInput{Source: SourceProblem, Kind: "CronJob", Reason: "stale"}, issuesapi.CategoryCronJobFailed},
		{"cronjob never scheduled", classifyInput{Source: SourceProblem, Kind: "CronJob", Reason: "never-scheduled"}, issuesapi.CategoryCronJobFailed},

		// missing_ref
		{"missing configmap", classifyInput{Source: SourceMissingRef, Kind: "Pod", Reason: "Missing ConfigMap"}, issuesapi.CategoryMissingConfigRef},
		{"missing secret", classifyInput{Source: SourceMissingRef, Kind: "Pod", Reason: "Missing Secret"}, issuesapi.CategoryMissingConfigRef},
		{"missing pvc", classifyInput{Source: SourceMissingRef, Kind: "Pod", Reason: "Missing PVC"}, issuesapi.CategoryMissingConfigRef},
		{"missing sa", classifyInput{Source: SourceMissingRef, Kind: "Pod", Reason: "Missing ServiceAccount"}, issuesapi.CategoryMissingConfigRef},
		{"ingress backend missing", classifyInput{Source: SourceMissingRef, Kind: "Ingress", APIGroup: "networking.k8s.io", Reason: "Missing backend Service"}, issuesapi.CategoryIngressBackendMissing},
		{"ingress tls secret is config", classifyInput{Source: SourceMissingRef, Kind: "Ingress", APIGroup: "networking.k8s.io", Reason: "Missing TLS Secret"}, issuesapi.CategoryMissingConfigRef},
		{"webhook backend down", classifyInput{Source: SourceMissingRef, Kind: "ValidatingWebhookConfiguration", APIGroup: "admissionregistration.k8s.io", Reason: "Missing webhook backend Service"}, issuesapi.CategoryWebhookBackendDown},
		{"missing storageclass is pvc pending", classifyInput{Source: SourceMissingRef, Kind: "PersistentVolumeClaim", Reason: "Missing StorageClass"}, issuesapi.CategoryPVCPending},
		{"missing roleref is config", classifyInput{Source: SourceMissingRef, Kind: "RoleBinding", APIGroup: "rbac.authorization.k8s.io", Reason: "Missing roleRef target"}, issuesapi.CategoryMissingConfigRef},

		// condition (CRD fallback) — discriminated by API group
		{"argo sync failed", classifyInput{Source: SourceCondition, Kind: "Application", APIGroup: "argoproj.io", Reason: "Synced: ComparisonError"}, issuesapi.CategoryGitOpsSyncFailed},
		{"flux helmrelease condition fallback is not sync", classifyInput{Source: SourceCondition, Kind: "HelmRelease", APIGroup: "helm.toolkit.fluxcd.io", Reason: "Ready=False"}, issuesapi.CategoryOperatorConditionFail},
		{"flux-looking helmrelease group is not sync", classifyInput{Source: SourceCondition, Kind: "HelmRelease", APIGroup: "custom-fluxcd.io", Reason: "Ready=False"}, issuesapi.CategoryOperatorConditionFail},
		{"cert-manager not ready", classifyInput{Source: SourceCondition, Kind: "Certificate", APIGroup: "cert-manager.io", Reason: "Ready: DoesNotExist"}, issuesapi.CategoryCertificateNotReady},
		{"cert-manager Issuer is NOT certificate_not_ready", classifyInput{Source: SourceCondition, Kind: "ClusterIssuer", APIGroup: "cert-manager.io", Reason: "Ready=False"}, issuesapi.CategoryOperatorConditionFail},
		{"generic operator condition", classifyInput{Source: SourceCondition, Kind: "Foo", APIGroup: "example.com", Reason: "Ready=False"}, issuesapi.CategoryOperatorConditionFail},
		// Flux source CRDs are NOT sync failures — they fall to operator condition.
		{"flux source repo is not sync", classifyInput{Source: SourceCondition, Kind: "GitRepository", APIGroup: "source.toolkit.fluxcd.io", Reason: "Ready: GitOperationFailed"}, issuesapi.CategoryOperatorConditionFail},
		{"argo non-app CRD is not sync", classifyInput{Source: SourceCondition, Kind: "AppProject", APIGroup: "argoproj.io", Reason: "Ready=False"}, issuesapi.CategoryOperatorConditionFail},

		// CAPI: control-plane vs machine layer, gated on the CAPI group.
		{"capi cluster failed", classifyInput{Source: SourceProblem, Kind: "Cluster", APIGroup: "cluster.x-k8s.io", Reason: "Cluster in Failed phase"}, issuesapi.CategoryControlPlaneNotReady},
		{"capi control plane not ready", classifyInput{Source: SourceProblem, Kind: "KubeadmControlPlane", APIGroup: "controlplane.cluster.x-k8s.io", Reason: "Ready=False"}, issuesapi.CategoryControlPlaneNotReady},
		{"capi machine failed", classifyInput{Source: SourceProblem, Kind: "Machine", APIGroup: "cluster.x-k8s.io", Reason: "Machine in Failed phase"}, issuesapi.CategoryMachineNotReady},
		{"capi machinedeployment", classifyInput{Source: SourceProblem, Kind: "MachineDeployment", APIGroup: "cluster.x-k8s.io", Reason: "Ready=False"}, issuesapi.CategoryMachineNotReady},
		{"non-capi Cluster kind is not control plane", classifyInput{Source: SourceProblem, Kind: "Cluster", APIGroup: "postgresql.cnpg.io", Reason: "whatever"}, issuesapi.CategoryUnknown},
		// new pod waiting reasons (bad image tag / container create)
		{"invalid image name", classifyInput{Source: SourceProblem, Kind: "Pod", Reason: "InvalidImageName"}, issuesapi.CategoryImagePullFailed},
		{"run container error", classifyInput{Source: SourceProblem, Kind: "Pod", Reason: "RunContainerError"}, issuesapi.CategoryContainerWaiting},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.in); got != tc.want {
				t.Errorf("Classify(%+v) = %q, want %q", tc.in, got, tc.want)
			}
			// Every category Classify emits must have a group rollup — a category
			// wired into Classify but missing from categoryGroup rolls up to
			// issuesapi.GroupUnknown silently. Asserted here, at the (mandatory) Classify
			// case, because the shared rollup table's own test cannot see a category
			// that Classify emits but the table omits.
			if tc.want != issuesapi.CategoryUnknown && issuesapi.GroupOf(tc.want) == issuesapi.GroupUnknown {
				t.Errorf("category %q has no categoryGroup rollup (→ issuesapi.GroupUnknown)", tc.want)
			}
		})
	}
}
