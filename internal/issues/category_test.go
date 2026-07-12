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
		{"rbac forbidden", classifyInput{Source: SourceScheduling, Kind: "Deployment", Reason: "RBACForbidden"}, issuesapi.CategoryRBACForbidden},
		{"ip exhaustion is startup stall", classifyInput{Source: SourceScheduling, Kind: "Pod", Reason: "IPExhaustion"}, issuesapi.CategoryContainerWaiting},
		{"sandbox failed is startup stall", classifyInput{Source: SourceScheduling, Kind: "Pod", Reason: "SandboxCreationFailed"}, issuesapi.CategoryContainerWaiting},
		{"eventless post-bind startup stall", classifyInput{Source: SourceScheduling, Kind: "Pod", Reason: "PostBindStartupStall"}, issuesapi.CategoryContainerWaiting},
		{"volume multiattach", classifyInput{Source: SourceScheduling, Kind: "Pod", Reason: "VolumeMultiAttach"}, issuesapi.CategoryVolumeMountFailed},
		{"volume mount", classifyInput{Source: SourceScheduling, Kind: "Pod", Reason: "VolumeMount"}, issuesapi.CategoryVolumeMountFailed},
		{"terminating pod", classifyInput{Source: SourceProblem, Kind: "Pod", Reason: "Terminating stuck"}, issuesapi.CategoryTerminationStuck},
		{"terminating namespace", classifyInput{Source: SourceProblem, Kind: "Namespace", Reason: "Namespace terminating stuck"}, issuesapi.CategoryTerminationStuck},
		{"coredns nxdomain override", classifyInput{Source: SourceProblem, Kind: "ConfigMap", Reason: "CoreDNS NXDOMAIN override"}, issuesapi.CategoryDNSFailure},
		{"coredns service rewrite", classifyInput{Source: SourceProblem, Kind: "ConfigMap", Reason: "CoreDNS service DNS rewrite"}, issuesapi.CategoryDNSFailure},
		{"env missing service", classifyInput{Source: SourceProblem, Kind: "Deployment", Reason: "Missing referenced Service"}, issuesapi.CategoryMissingConfigRef},
		{"env service port mismatch", classifyInput{Source: SourceProblem, Kind: "Deployment", Reason: "Service port mismatch"}, issuesapi.CategoryMissingConfigRef},

		// problem / Pod
		{"image pull backoff", classifyInput{Source: SourceProblem, Kind: "Pod", Reason: "ImagePullBackOff"}, issuesapi.CategoryImagePullFailed},
		{"err image pull", classifyInput{Source: SourceProblem, Kind: "Pod", Reason: "ErrImagePull"}, issuesapi.CategoryImagePullFailed},
		{"crashloop", classifyInput{Source: SourceProblem, Kind: "Pod", Reason: "CrashLoopBackOff"}, issuesapi.CategoryCrashLoop},
		{"oom by reason", classifyInput{Source: SourceProblem, Kind: "Pod", Reason: "OOMKilled"}, issuesapi.CategoryOOMKilled},
		{"oom by last-terminated, crashloop reason", classifyInput{Source: SourceProblem, Kind: "Pod", Reason: "CrashLoopBackOff", LastTerminatedReason: "OOMKilled"}, issuesapi.CategoryOOMKilled},
		{"config error waiting", classifyInput{Source: SourceProblem, Kind: "Pod", Reason: "CreateContainerConfigError"}, issuesapi.CategoryContainerWaiting},
		{"pending pod (non-scheduling)", classifyInput{Source: SourceProblem, Kind: "Pod", Reason: "Pending"}, issuesapi.CategoryContainerWaiting},
		{"errored pod", classifyInput{Source: SourceProblem, Kind: "Pod", Reason: "Error"}, issuesapi.CategoryCrashLoop},
		{"pod status rbac forbidden message", classifyInput{Source: SourceProblem, Kind: "Pod", Reason: "Completed", Message: `workflowtaskresults.argoproj.io is forbidden: User "system:serviceaccount:radar-batch-visual:default" cannot create resource "workflowtaskresults" in API group "argoproj.io" in the namespace "radar-batch-visual"`}, issuesapi.CategoryRBACForbidden},
		{"high restart thrash", classifyInput{Source: SourceProblem, Kind: "Pod", Reason: "HighRestartCount"}, issuesapi.CategoryHighRestart},
		{"liveness probe failed", classifyInput{Source: SourceProblem, Kind: "Pod", Reason: "LivenessProbeFailed"}, issuesapi.CategoryLivenessProbeFail},
		{"liveness probe beats stale oom", classifyInput{Source: SourceProblem, Kind: "Pod", Reason: "LivenessProbeFailed", LastTerminatedReason: "OOMKilled"}, issuesapi.CategoryLivenessProbeFail},
		{"liveness probe invalid", classifyInput{Source: SourceProblem, Kind: "Pod", Reason: "LivenessProbeInvalid"}, issuesapi.CategoryLivenessProbeFail},
		{"readiness probe failed", classifyInput{Source: SourceProblem, Kind: "Pod", Reason: "ReadinessProbeFailed"}, issuesapi.CategoryReadinessFailed},
		{"readiness probe beats stale oom", classifyInput{Source: SourceProblem, Kind: "Pod", Reason: "ReadinessProbeFailed", LastTerminatedReason: "OOMKilled"}, issuesapi.CategoryReadinessFailed},
		{"readiness probe invalid", classifyInput{Source: SourceProblem, Kind: "Pod", Reason: "ReadinessProbeInvalid"}, issuesapi.CategoryReadinessFailed},
		{"init container stalled", classifyInput{Source: SourceProblem, Kind: "Pod", Reason: "InitContainerStalled"}, issuesapi.CategoryInitContainerFailed},

		// problem / GitOps reconcilers (DetectGitOpsProblems → SourceProblem)
		{"argo app degraded", classifyInput{Source: SourceProblem, Kind: "Application", APIGroup: "argoproj.io", Reason: "HealthDegraded"}, issuesapi.CategoryGitOpsHealthDegraded},
		{"argo app missing", classifyInput{Source: SourceProblem, Kind: "Application", APIGroup: "argoproj.io", Reason: "HealthMissing"}, issuesapi.CategoryGitOpsHealthDegraded},
		{"argo app outofsync", classifyInput{Source: SourceProblem, Kind: "Application", APIGroup: "argoproj.io", Reason: "OutOfSync"}, issuesapi.CategoryGitOpsOutOfSync},
		{"argo operation failed", classifyInput{Source: SourceProblem, Kind: "Application", APIGroup: "argoproj.io", Reason: "OperationFailed"}, issuesapi.CategoryGitOpsOperationFailed},
		{"argo comparison error is render", classifyInput{Source: SourceProblem, Kind: "Application", APIGroup: "argoproj.io", Reason: "ComparisonError"}, issuesapi.CategoryGitOpsRenderFailed},
		{"argo invalid spec", classifyInput{Source: SourceProblem, Kind: "Application", APIGroup: "argoproj.io", Reason: "InvalidSpecError"}, issuesapi.CategoryGitOpsSpecInvalid},
		{"argo unknown reason falls back to sync_failed", classifyInput{Source: SourceProblem, Kind: "Application", APIGroup: "argoproj.io", Reason: "SomethingNovel"}, issuesapi.CategoryGitOpsSyncFailed},
		{"flux kustomization problem", classifyInput{Source: SourceProblem, Kind: "Kustomization", APIGroup: "kustomize.toolkit.fluxcd.io", Reason: "ReconciliationFailed"}, issuesapi.CategoryGitOpsOperationFailed},
		{"flux helmrelease problem", classifyInput{Source: SourceProblem, Kind: "HelmRelease", APIGroup: "helm.toolkit.fluxcd.io", Reason: "InstallFailed"}, issuesapi.CategoryGitOpsOperationFailed},
		{"native helm release failed", classifyInput{Source: SourceProblem, Kind: "HelmRelease", APIGroup: "helm.sh", Reason: "HelmReleaseFailed"}, issuesapi.CategoryHelmReleaseFailed},
		{"native helm upgrade failed", classifyInput{Source: SourceProblem, Kind: "HelmRelease", APIGroup: "helm.sh", Reason: "HelmUpgradeFailed"}, issuesapi.CategoryHelmReleaseFailed},
		{"native helm pending", classifyInput{Source: SourceProblem, Kind: "HelmRelease", APIGroup: "helm.sh", Reason: "HelmReleasePending"}, issuesapi.CategoryHelmReleaseFailed},
		{"flux artifact failed is render", classifyInput{Source: SourceProblem, Kind: "Kustomization", APIGroup: "kustomize.toolkit.fluxcd.io", Reason: "ArtifactFailed"}, issuesapi.CategoryGitOpsRenderFailed},
		{"flux chart not ready is render", classifyInput{Source: SourceProblem, Kind: "HelmRelease", APIGroup: "helm.toolkit.fluxcd.io", Reason: "ChartNotReady"}, issuesapi.CategoryGitOpsRenderFailed},
		{"flux-looking kustomization group is not gitops", classifyInput{Source: SourceProblem, Kind: "Kustomization", APIGroup: "custom-fluxcd.io", Reason: "ReconciliationFailed"}, issuesapi.CategoryUnknown},
		{"non-argo Application kind is not gitops", classifyInput{Source: SourceProblem, Kind: "Application", APIGroup: "other.example.com", Reason: "whatever"}, issuesapi.CategoryUnknown},

		// argo Rollout is progressive delivery, NOT a sync failure
		{"argo rollout condition is rollout_stalled", classifyInput{Source: SourceCondition, Kind: "Rollout", APIGroup: "argoproj.io", Reason: "Ready: ProgressDeadlineExceeded"}, issuesapi.CategoryRolloutStalled},

		// problem / workloads
		{"deploy degraded", classifyInput{Source: SourceProblem, Kind: "Deployment", Reason: "3/5 available"}, issuesapi.CategoryWorkloadDegraded},
		{"deploy rollout stuck", classifyInput{Source: SourceProblem, Kind: "Deployment", Reason: "Rollout stuck"}, issuesapi.CategoryRolloutStalled},
		{"deploy replica failure", classifyInput{Source: SourceProblem, Kind: "Deployment", Reason: "ReplicaFailure"}, issuesapi.CategoryRolloutStalled},
		{"statefulset degraded", classifyInput{Source: SourceProblem, Kind: "StatefulSet", Reason: "2/3 ready"}, issuesapi.CategoryWorkloadDegraded},
		{"daemonset degraded", classifyInput{Source: SourceProblem, Kind: "DaemonSet", Reason: "1 unavailable"}, issuesapi.CategoryWorkloadDegraded},

		// problem / service
		{"service no pods", classifyInput{Source: SourceProblem, Kind: "Service", Reason: "Selector matches no pods"}, issuesapi.CategoryServiceNoEndpoints},
		{"service 0 ready", classifyInput{Source: SourceProblem, Kind: "Service", Reason: "0/3 selected pods ready"}, issuesapi.CategoryServiceNoEndpoints},
		{"service load balancer pending", classifyInput{Source: SourceProblem, Kind: "Service", Reason: "LoadBalancer pending"}, issuesapi.CategoryLoadBalancerPending},
		{"ingress load balancer pending", classifyInput{Source: SourceProblem, Kind: "Ingress", Reason: "LoadBalancer pending"}, issuesapi.CategoryLoadBalancerPending},

		// problem / hpa, node, pvc
		{"hpa maxed", classifyInput{Source: SourceProblem, Kind: "HorizontalPodAutoscaler", Reason: "maxed"}, issuesapi.CategoryHPALimitedOrFailed},
		{"node notready", classifyInput{Source: SourceProblem, Kind: "Node", Reason: "NotReady"}, issuesapi.CategoryNodeNotReady},
		{"node mempressure", classifyInput{Source: SourceProblem, Kind: "Node", Reason: "MemoryPressure"}, issuesapi.CategoryNodeNotReady},
		{"node cordoned is intentional", classifyInput{Source: SourceProblem, Kind: "Node", Reason: "Cordoned"}, issuesapi.CategoryUnknown},
		{"pvc pending", classifyInput{Source: SourceProblem, Kind: "PersistentVolumeClaim", Reason: "Pending"}, issuesapi.CategoryPVCPending},
		{"pvc lost is storage", classifyInput{Source: SourceProblem, Kind: "PersistentVolumeClaim", Reason: "Lost"}, issuesapi.CategoryPVCLost},
		{"pvc resize failed", classifyInput{Source: SourceProblem, Kind: "PersistentVolumeClaim", Reason: "ControllerResizeError"}, issuesapi.CategoryPVCResizeFailed},
		{"pv failed", classifyInput{Source: SourceProblem, Kind: "PersistentVolume", Reason: "Failed"}, issuesapi.CategoryPVFailed},
		{"pdb blocks evictions", classifyInput{Source: SourceProblem, Kind: "PodDisruptionBudget", Reason: "Voluntary evictions blocked"}, issuesapi.CategoryPDBBlocksEvictions},

		// problem / batch (Job/CronJob)
		{"job failed condition", classifyInput{Source: SourceProblem, Kind: "Job", Reason: "BackoffLimitExceeded"}, issuesapi.CategoryJobFailed},
		{"job failure reason beats embedded forbidden message", classifyInput{Source: SourceProblem, Kind: "Job", Reason: "BackoffLimitExceeded", Message: `workflowtaskresults.argoproj.io is forbidden: User "system:serviceaccount:ci:default" cannot create resource "workflowtaskresults"`}, issuesapi.CategoryJobFailed},
		{"job failed fallback", classifyInput{Source: SourceProblem, Kind: "Job", Reason: "Failed"}, issuesapi.CategoryJobFailed},
		{"job stuck active", classifyInput{Source: SourceProblem, Kind: "Job", Reason: "Running for 3h with no completions"}, issuesapi.CategoryJobFailed},
		{"cronjob stale", classifyInput{Source: SourceProblem, Kind: "CronJob", Reason: "stale"}, issuesapi.CategoryCronJobFailed},
		{"cronjob stale reason beats embedded forbidden message", classifyInput{Source: SourceProblem, Kind: "CronJob", Reason: "stale", Message: `jobs.batch is forbidden: User "system:serviceaccount:ci:default" cannot create resource "jobs"`}, issuesapi.CategoryCronJobFailed},
		{"cronjob never scheduled", classifyInput{Source: SourceProblem, Kind: "CronJob", Reason: "never-scheduled"}, issuesapi.CategoryCronJobFailed},
		{"argo workflow rbac forbidden", classifyInput{Source: SourceProblem, Kind: "Workflow", APIGroup: "argoproj.io", Reason: "Completed", Message: `workflowtaskresults.argoproj.io is forbidden: User "system:serviceaccount:radar-batch-visual:default" cannot create resource "workflowtaskresults" in API group "argoproj.io" in the namespace "radar-batch-visual"`}, issuesapi.CategoryRBACForbidden},
		{"argo workflow completed without forbidden stays unknown", classifyInput{Source: SourceProblem, Kind: "Workflow", APIGroup: "argoproj.io", Reason: "Completed", Message: "child 'example' failed"}, issuesapi.CategoryUnknown},

		// missing_ref
		{"missing configmap", classifyInput{Source: SourceMissingRef, Kind: "Pod", Reason: "Missing ConfigMap"}, issuesapi.CategoryMissingConfigRef},
		{"missing secret", classifyInput{Source: SourceMissingRef, Kind: "Pod", Reason: "Missing Secret"}, issuesapi.CategoryMissingConfigRef},
		{"missing pvc", classifyInput{Source: SourceMissingRef, Kind: "Pod", Reason: "Missing PVC"}, issuesapi.CategoryMissingConfigRef},
		{"missing sa", classifyInput{Source: SourceMissingRef, Kind: "Pod", Reason: "Missing ServiceAccount"}, issuesapi.CategoryMissingConfigRef},
		{"ingress backend missing", classifyInput{Source: SourceMissingRef, Kind: "Ingress", APIGroup: "networking.k8s.io", Reason: "Missing backend Service"}, issuesapi.CategoryIngressBackendMissing},
		{"gateway backend missing", classifyInput{Source: SourceMissingRef, Kind: "HTTPRoute", APIGroup: "gateway.networking.k8s.io", Reason: "Missing Gateway backend Service"}, issuesapi.CategoryGatewayRouteInvalid},
		{"gateway reference grant missing", classifyInput{Source: SourceMissingRef, Kind: "HTTPRoute", APIGroup: "gateway.networking.k8s.io", Reason: "Missing Gateway ReferenceGrant"}, issuesapi.CategoryGatewayRouteInvalid},
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
		{"gateway not programmed", classifyInput{Source: SourceCondition, Kind: "Gateway", APIGroup: "gateway.networking.k8s.io", Reason: "Programmed: AddressNotAssigned"}, issuesapi.CategoryGatewayNotReady},
		{"gateway route unresolved refs", classifyInput{Source: SourceCondition, Kind: "HTTPRoute", APIGroup: "gateway.networking.k8s.io", Reason: "ResolvedRefs: BackendNotFound"}, issuesapi.CategoryGatewayRouteInvalid},
		{"aggregated api unavailable", classifyInput{Source: SourceCondition, Kind: "APIService", APIGroup: "apiregistration.k8s.io", Reason: "Available: MissingEndpoints"}, issuesapi.CategoryAPIServiceUnavailable},
		{"external secret sync failed", classifyInput{Source: SourceCondition, Kind: "ExternalSecret", APIGroup: "external-secrets.io", Reason: "Ready: SecretSyncedError"}, issuesapi.CategorySecretSyncFailed},
		{"keda scaler failed", classifyInput{Source: SourceCondition, Kind: "ScaledObject", APIGroup: "keda.sh", Reason: "Ready: ScalerNotActive"}, issuesapi.CategoryHPALimitedOrFailed},
		{"karpenter nodeclaim failed", classifyInput{Source: SourceCondition, Kind: "NodeClaim", APIGroup: "karpenter.sh", Reason: "Ready: LaunchFailed"}, issuesapi.CategoryNodeProvisioningFail},
		{"crossplane package failed", classifyInput{Source: SourceCondition, Kind: "Provider", APIGroup: "pkg.crossplane.io", Reason: "Healthy: UnhealthyPackageRevision"}, issuesapi.CategoryCrossplaneReconcile},
		{"generic operator condition", classifyInput{Source: SourceCondition, Kind: "Foo", APIGroup: "example.com", Reason: "Ready=False"}, issuesapi.CategoryOperatorConditionFail},
		{"flux source repo is render_failed", classifyInput{Source: SourceCondition, Kind: "GitRepository", APIGroup: "source.toolkit.fluxcd.io", Reason: "Ready: GitOperationFailed"}, issuesapi.CategoryGitOpsRenderFailed},
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
