package upgradereadiness

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func scan137(t *testing.T, input *Input) *ScanResults {
	t.Helper()
	result, err := Scan(input, "1.36", "1.37")
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestRemovedFeatureGates137(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configure  func(*Input)
		wantStatus CheckStatus
		wantPath   string
		wantImpact string
		wantRef    string
	}{
		{
			name: "effective kubelet gate",
			configure: func(input *Input) {
				input.NodeRuntimeEvidence[0].FeatureGates["SidecarContainers"] = true
			},
			wantStatus: CheckBlocked,
			wantPath:   "kubeletconfig.featureGates.SidecarContainers",
			wantRef:    "/pull/137755",
		},
		{
			name: "static pod API opt-out",
			configure: func(input *Input) {
				input.NodeRuntimeEvidence[0].FeatureGates["PreventStaticPodAPIReferences"] = false
			},
			wantStatus: CheckBlocked,
			wantImpact: "dependent static Pod cannot start",
			wantRef:    "/pull/140226",
		},
		{
			name: "control plane mirror pod split command and args",
			configure: func(input *Input) {
				input.Pods = []*corev1.Pod{{
					ObjectMeta: metav1.ObjectMeta{Name: "kube-apiserver-node-a", Namespace: "kube-system", Annotations: map[string]string{corev1.MirrorPodAnnotationKey: "mirror"}},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "kube-apiserver", Command: []string{"kube-apiserver", "--feature-gates"}, Args: []string{"APIServerTracing=false"}}}},
				}}
			},
			wantStatus: CheckBlocked,
			wantPath:   "spec.containers[0].command[--feature-gates].APIServerTracing",
			wantRef:    "/pull/138907",
		},
		{
			name: "gate present only in the lifecycle diff",
			configure: func(input *Input) {
				input.Pods = []*corev1.Pod{{
					ObjectMeta: metav1.ObjectMeta{Name: "kube-scheduler-node-a", Namespace: "kube-system", Annotations: map[string]string{corev1.MirrorPodAnnotationKey: "mirror"}},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "kube-scheduler", Args: []string{"--feature-gates=SchedulerQueueingHints=false"}}}},
				}}
			},
			wantStatus: CheckBlocked,
			wantPath:   "spec.containers[0].args[--feature-gates].SchedulerQueueingHints",
			wantRef:    "versioned_feature_list.yaml",
		},
		{
			name: "configz unavailable",
			configure: func(input *Input) {
				input.NodeRuntimeEvidence[0].ConfigAvailable = false
			},
			wantStatus: CheckUnknown,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := completeInput()
			tc.configure(input)
			check := checkByID(t, scan137(t, input), "removed-feature-gates")
			if check.Status != tc.wantStatus {
				t.Fatalf("check = %+v, want status %s", check, tc.wantStatus)
			}
			if tc.wantPath != "" && (len(check.Findings) != 1 || check.Findings[0].Evidence.Path != tc.wantPath) {
				t.Fatalf("findings = %+v, want path %q", check.Findings, tc.wantPath)
			}
			if tc.wantImpact != "" && (len(check.Findings) != 1 || !strings.Contains(check.Findings[0].Impact, tc.wantImpact)) {
				t.Fatalf("findings = %+v, want impact containing %q", check.Findings, tc.wantImpact)
			}
			if tc.wantRef != "" {
				requireFindingReference(t, check.Findings, tc.wantRef)
			}
		})
	}
}

func TestFeatureGateLifecycleCatalog137(t *testing.T) {
	removed := []string{
		"AnonymousAuthConfigurableEndpoints", "APIServerTracing", "AnyVolumeDataSource", "AuthorizeNodeWithSelectors", "AuthorizeWithSelectors",
		"BtreeWatchCache", "ConsistentListFromCache", "GangScheduling", "JobBackoffLimitPerIndex", "JobPodReplacementPolicy",
		"JobSuccessPolicy", "LogarithmicScaleDown", "OrderedNamespaceDeletion", "PodLifecycleSleepAction", "PodLifecycleSleepActionAllowZero",
		"PreventStaticPodAPIReferences", "RelaxedDNSSearchValidation", "ResilientWatchCacheInitialization", "RetryGenerateName", "SchedulerQueueingHints",
		"SidecarContainers", "StreamingCollectionEncodingToJSON", "StreamingCollectionEncodingToProtobuf", "StructuredAuthenticationConfiguration", "WorkloadAwarePreemption",
	}
	if len(removedFeatureGates137) != len(removed) {
		t.Fatalf("removed feature-gate count = %d, want %d from the 1.36→1.37 lifecycle diff", len(removedFeatureGates137), len(removed))
	}
	for _, name := range removed {
		if !removedFeatureGates137[name] {
			t.Errorf("removed feature-gate catalog is missing %s", name)
		}
	}

	locked := map[string]bool{
		"DeclarativeValidationTakeover": false, "DisableCPUQuotaWithExclusiveCPUs": true, "DRAExtendedResource": true,
		"DRAPrioritizedList": true, "DRAResourceClaimDeviceStatus": true, "HostnameOverride": true,
		"HPAConfigurableTolerance": true, "InPlacePodVerticalScalingInitContainers": true, "NodeDeclaredFeatures": true,
		"PLEGOnDemandRelist": true, "PodReadyToStartContainersCondition": true, "RelaxedServiceNameValidation": true,
		"WatchCacheInitializationPostStartHook": true,
	}
	if len(lockedFeatureGates137) != len(locked) {
		t.Fatalf("newly locked feature-gate count = %d, want %d from the 1.37 lifecycle", len(lockedFeatureGates137), len(locked))
	}
	for name, wantDefault := range locked {
		if gotDefault, ok := lockedFeatureGates137[name]; !ok || gotDefault != wantDefault {
			t.Errorf("locked feature gate %s = (%t, %t), want default %t", name, gotDefault, ok, wantDefault)
		}
	}
}

func TestLockedAPIServerFeatureGate137(t *testing.T) {
	for _, tc := range []struct {
		name       string
		value      string
		wantStatus CheckStatus
	}{
		{name: "locked default is accepted", value: "false", wantStatus: CheckPassed},
		{name: "non-default is blocked", value: "true", wantStatus: CheckBlocked},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := completeInput()
			input.Pods = []*corev1.Pod{{
				ObjectMeta: metav1.ObjectMeta{Name: "kube-apiserver-node-a", Namespace: "kube-system", Annotations: map[string]string{corev1.MirrorPodAnnotationKey: "mirror"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "kube-apiserver", Args: []string{"--feature-gates=DeclarativeValidationTakeover=" + tc.value}}}},
			}}
			check := checkByID(t, scan137(t, input), "removed-feature-gates")
			if check.Status != tc.wantStatus {
				t.Fatalf("check = %+v, want status %s", check, tc.wantStatus)
			}
			if check.Inspected != 2 {
				t.Fatalf("inspected = %d, want one node and one control-plane container", check.Inspected)
			}
			if tc.wantStatus == CheckPassed && len(check.Findings) != 0 {
				t.Fatalf("locked default produced findings: %+v", check.Findings)
			}
			if tc.wantStatus == CheckBlocked && (len(check.Findings) != 1 || !strings.Contains(check.Findings[0].Impact, "locks this kube-apiserver feature gate to false")) {
				t.Fatalf("non-default finding = %+v", check.Findings)
			}
			if tc.wantStatus == CheckBlocked {
				requireFindingReference(t, check.Findings, "/pull/139212")
			}
		})
	}
}

func TestLockedFeatureGateAcrossComponents137(t *testing.T) {
	for _, component := range []string{"kube-controller-manager", "kube-scheduler"} {
		t.Run(component, func(t *testing.T) {
			input := completeInput()
			input.Pods = []*corev1.Pod{{
				ObjectMeta: metav1.ObjectMeta{Name: component + "-node-a", Namespace: "kube-system", Annotations: map[string]string{corev1.MirrorPodAnnotationKey: "mirror"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: component, Args: []string{"--feature-gates=DeclarativeValidationTakeover=true"}}}},
			}}
			check := checkByID(t, scan137(t, input), "removed-feature-gates")
			if check.Status != CheckBlocked || len(check.Findings) != 1 || !strings.Contains(check.Findings[0].Impact, component) {
				t.Fatalf("%s locked gate = %+v, want component-specific blocker", component, check)
			}
		})
	}

	input := completeInput()
	input.NodeRuntimeEvidence[0].FeatureGates["DeclarativeValidationTakeover"] = true
	check := checkByID(t, scan137(t, input), "removed-feature-gates")
	if check.Status != CheckBlocked || len(check.Findings) != 1 || check.Findings[0].Resource.Kind != "Node" {
		t.Fatalf("kubelet locked gate = %+v, want node blocker", check)
	}

	input = completeInput()
	input.NodeRuntimeEvidence[0].FeatureGates["DisableCPUQuotaWithExclusiveCPUs"] = false
	check = checkByID(t, scan137(t, input), "removed-feature-gates")
	if check.Status != CheckBlocked || len(check.Findings) != 1 || !strings.Contains(check.Findings[0].Impact, "locks this kubelet feature gate to true") {
		t.Fatalf("newly locked kubelet gate = %+v, want blocker", check)
	}
	requireFindingReference(t, check.Findings, "versioned_feature_list.yaml")
}

func TestRemovedFeatureGatesReportsManagedControlPlaneGap137(t *testing.T) {
	input := completeInput()
	check := checkByID(t, scan137(t, input), "removed-feature-gates")
	if check.Status != CheckUnknown || !strings.Contains(check.Caveat, "self-managed control-plane") {
		t.Fatalf("unclassified control plane = %+v, want unknown without mirror Pod evidence", check)
	}

	input = completeInput()
	input.Nodes[0].Labels = map[string]string{"eks.amazonaws.com/nodegroup": "workers"}
	check = checkByID(t, scan137(t, input), "removed-feature-gates")
	if check.Status != CheckPassed || check.Caveat != "" || !strings.Contains(check.EvidenceNote, "provider manages the control plane") {
		t.Fatalf("managed control plane = %+v, want passed node evidence with a non-limiting provider note", check)
	}

	input = completeInput()
	input.Pods = []*corev1.Pod{{
		ObjectMeta: metav1.ObjectMeta{Name: "kube-apiserver-node-a", Namespace: "kube-system", Annotations: map[string]string{corev1.MirrorPodAnnotationKey: "mirror"}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "kube-apiserver"}}},
	}}
	check = checkByID(t, scan137(t, input), "removed-feature-gates")
	if check.Status != CheckPassed || !strings.Contains(check.Caveat, "kube-controller-manager, kube-scheduler") {
		t.Fatalf("partial control-plane evidence = %+v, want missing-component caveat", check)
	}

	input = completeInput()
	input.Nodes = nil
	input.Pods = []*corev1.Pod{{
		ObjectMeta: metav1.ObjectMeta{Name: "kube-apiserver-node-a", Namespace: "kube-system", Annotations: map[string]string{corev1.MirrorPodAnnotationKey: "mirror"}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "kube-apiserver", Args: []string{"--feature-gates=APIServerTracing=false"}}}},
	}}
	check = checkByID(t, scan137(t, input), "removed-feature-gates")
	if check.Status != CheckBlocked || !strings.Contains(check.Caveat, "Node evidence was unavailable") {
		t.Fatalf("control-plane blocker without node evidence = %+v, want blocker with kubelet coverage caveat", check)
	}
}

func TestRemovedFeatureGatesPreservesMissingNodeSummary137(t *testing.T) {
	input := completeInput()
	input.Nodes = nil
	input.Pods = nil
	check := checkByID(t, scan137(t, input), "removed-feature-gates")
	if check.Status != CheckUnknown || !strings.Contains(check.Summary, "Node evidence was unavailable") || strings.Contains(check.Summary, "readable kubelet configuration") {
		t.Fatalf("missing node and control-plane evidence = %+v, want the node evidence boundary in the summary", check)
	}

	input = completeInput()
	input.Nodes = []*corev1.Node{}
	input.Pods = []*corev1.Pod{}
	check = checkByID(t, scan137(t, input), "removed-feature-gates")
	if check.Status != CheckUnknown || check.Inspected != 0 || !strings.Contains(check.Summary, "No kubelet or control-plane") {
		t.Fatalf("empty node and control-plane evidence = %+v, want unknown without a clean claim", check)
	}
}

func TestRemovedControlPlaneConfigurationScope137(t *testing.T) {
	input := completeInput()
	input.Namespaces = []string{"apps"}
	featureGates := checkByID(t, scan137(t, input), "removed-feature-gates")
	if featureGates.Status != CheckUnknown || !strings.Contains(featureGates.Caveat, "kube-system") {
		t.Fatalf("scoped feature-gate evidence = %+v, want unknown with kube-system caveat", featureGates)
	}
	componentFlags := checkByID(t, scan137(t, input), "removed-component-flags")
	if componentFlags.Status != CheckUnknown || !strings.Contains(componentFlags.Summary, "kube-system") {
		t.Fatalf("scoped component-flag evidence = %+v, want unknown", componentFlags)
	}

	input = completeInput()
	input.Pods = nil
	featureGates = checkByID(t, scan137(t, input), "removed-feature-gates")
	if featureGates.Status != CheckUnknown || !strings.Contains(featureGates.Caveat, "Pods were unavailable") {
		t.Fatalf("unavailable Pod evidence = %+v, want unknown", featureGates)
	}
}

func TestManagedControlPlaneScopeDoesNotBecomeUnknown137(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*Input)
	}{
		{name: "kube-system outside Pod informer", configure: func(input *Input) {
			input.CacheScopedKinds = map[string][]string{"pods": {"team-a"}}
		}},
		{name: "Pods unavailable", configure: func(input *Input) {
			input.Pods = nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := completeInput()
			input.Nodes[0].Labels = map[string]string{"eks.amazonaws.com/nodegroup": "workers"}
			tc.configure(input)

			featureGates := checkByID(t, scan137(t, input), "removed-feature-gates")
			if featureGates.Status != CheckPassed || featureGates.Caveat != "" || !strings.Contains(featureGates.EvidenceNote, "provider manages the control plane") {
				t.Fatalf("managed feature-gate scope = %+v, want passed kubelet evidence with provider boundary", featureGates)
			}
			componentFlags := checkByID(t, scan137(t, input), "removed-component-flags")
			if componentFlags.Status != CheckNotApplicable || !strings.Contains(componentFlags.Summary, "provider manages the control plane") {
				t.Fatalf("managed component-option scope = %+v, want not applicable", componentFlags)
			}

			input.NodeRuntimeEvidence[0].FeatureGates["SidecarContainers"] = true
			featureGates = checkByID(t, scan137(t, input), "removed-feature-gates")
			if featureGates.Status != CheckBlocked || len(featureGates.Findings) != 1 || featureGates.Caveat != "" || !strings.Contains(featureGates.EvidenceNote, "provider manages the control plane") || !strings.Contains(featureGates.EvidenceNote, "does not reveal") || !strings.Contains(featureGates.Findings[0].Remediation, "provider-supported") {
				t.Fatalf("managed control plane with kubelet blocker = %+v, want blocker with provider boundary", featureGates)
			}
		})
	}
}

func TestKubeletEventRecordQPS137(t *testing.T) {
	input := completeInput()
	input.NodeRuntimeEvidence[0].EventRecordQPS = 0
	check := checkByID(t, scan137(t, input), "kubelet-event-qps-change")
	if check.Status != CheckWarning || len(check.Findings) != 1 || !strings.Contains(check.Findings[0].Impact, "client-go") || !strings.Contains(check.Findings[0].Impact, "5 events per second") || !strings.Contains(check.Findings[0].Remediation, "upgrade note recommends 50") || !strings.Contains(check.Findings[0].Remediation, "matching the old explicit-zero runtime limit") {
		t.Fatalf("zero eventRecordQPS = %+v, want behavior-change warning", check)
	}
	requireFindingReference(t, check.Findings, "/pull/117119")
	requireFindingReference(t, check.Findings, "/blob/v1.36.0/")

	input = completeInput()
	input.NodeRuntimeEvidence[0].EventRecordQPSAvailable = false
	check = checkByID(t, scan137(t, input), "kubelet-event-qps-change")
	if check.Status != CheckUnknown || !strings.Contains(check.Caveat, "debugging handlers disabled") {
		t.Fatalf("missing eventRecordQPS = %+v, want unknown", check)
	}
}

func TestKubeletEventQPSObservedProxyDenialDoesNotBlameDebuggingHandlers(t *testing.T) {
	input := completeInput()
	input.NodeProxyForbidden = true
	input.NodeRuntimeEvidence = []NodeRuntimeEvidence{{NodeName: "node-a", FeatureGates: map[string]bool{}}}

	check := checkByID(t, scan137(t, input), "kubelet-event-qps-change")
	if !strings.Contains(check.Caveat, "nodes/proxy request was forbidden") || strings.Contains(check.Caveat, "debugging handlers disabled") {
		t.Fatalf("observed proxy denial caveat = %q, want RBAC attribution without a configz explanation", check.Caveat)
	}
}

func TestRemovedComponentFlag137(t *testing.T) {
	input := completeInput()
	input.Pods = []*corev1.Pod{{
		ObjectMeta: metav1.ObjectMeta{Name: "kube-controller-manager-node-a", Namespace: "kube-system", Annotations: map[string]string{corev1.MirrorPodAnnotationKey: "mirror"}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "kube-controller-manager", Command: []string{"kube-controller-manager", "--concurrent-service-syncs=10"}}}},
	}}
	check := checkByID(t, scan137(t, input), "removed-component-flags")
	if check.Status != CheckBlocked || len(check.Findings) != 1 || !strings.Contains(check.Findings[0].Evidence.Path, ".command[") {
		t.Fatalf("removed component flag = %+v, want blocker", check)
	}
	requireFindingReference(t, check.Findings, "/pull/138002")

	input = completeInput()
	check = checkByID(t, scan137(t, input), "removed-component-flags")
	if check.Status != CheckUnknown || !strings.Contains(check.Summary, "self-managed") {
		t.Fatalf("unclassified control plane = %+v, want unknown", check)
	}

	input.Nodes[0].Labels = map[string]string{"cloud.google.com/gke-nodepool": "default"}
	check = checkByID(t, scan137(t, input), "removed-component-flags")
	if check.Status != CheckNotApplicable || !strings.Contains(check.Summary, "provider manages") {
		t.Fatalf("managed control plane = %+v, want not applicable", check)
	}
}

func TestRemovedPodGroupAdmissionPlugin137(t *testing.T) {
	for _, tc := range []struct {
		name      string
		container corev1.Container
		wantPath  string
	}{
		{name: "enabled in args", container: corev1.Container{Name: "kube-apiserver", Args: []string{"--enable-admission-plugins=NodeRestriction,PodGroupWorkloadExists"}}, wantPath: ".args[--enable-admission-plugins]"},
		{name: "disabled with split command value", container: corev1.Container{Name: "kube-apiserver", Command: []string{"kube-apiserver", "--disable-admission-plugins"}, Args: []string{"PodGroupWorkloadExists"}}, wantPath: ".command[--disable-admission-plugins]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := completeInput()
			input.Pods = []*corev1.Pod{{
				ObjectMeta: metav1.ObjectMeta{Name: "control-plane-node-a", Namespace: "kube-system", Annotations: map[string]string{corev1.MirrorPodAnnotationKey: "mirror"}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{
					tc.container,
					{Name: "kube-controller-manager"},
				}},
			}}
			check := checkByID(t, scan137(t, input), "removed-component-flags")
			if check.Status != CheckBlocked || len(check.Findings) != 1 || !strings.Contains(check.Findings[0].Evidence.Path, tc.wantPath) || !strings.Contains(check.Findings[0].Impact, "fails during startup") {
				t.Fatalf("removed admission plugin = %+v, want source-anchored blocker", check)
			}
			requireFindingReference(t, check.Findings, "/pull/139008")
		})
	}

	input := completeInput()
	input.Pods = []*corev1.Pod{{
		ObjectMeta: metav1.ObjectMeta{Name: "control-plane-node-a", Namespace: "kube-system", Annotations: map[string]string{corev1.MirrorPodAnnotationKey: "mirror"}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "kube-apiserver", Args: []string{"--enable-admission-plugins=NodeRestriction"}},
			{Name: "kube-controller-manager"},
		}},
	}}
	check := checkByID(t, scan137(t, input), "removed-component-flags")
	if check.Status != CheckPassed || check.Caveat != "" {
		t.Fatalf("clean complete component options = %+v, want passed", check)
	}
}

func TestRemovedKubeletCAdvisorOptions137(t *testing.T) {
	wantFlags := []string{
		"--application-metrics-count-limit", "--boot-id-file", "--container-hints", "--containerd", "--containerd-namespace",
		"--enable-load-reader", "--event-storage-age-limit", "--event-storage-event-limit", "--global-housekeeping-interval",
		"--log-cadvisor-usage", "--machine-id-file", "--storage-driver-user", "--storage-driver-password", "--storage-driver-host",
		"--storage-driver-db", "--storage-driver-table", "--storage-driver-secure", "--storage-driver-buffer-duration",
	}
	if strings.Join(removedKubeletCAdvisorFlags137, "\n") != strings.Join(wantFlags, "\n") {
		t.Fatalf("removed cAdvisor flags = %v, want exact upstream list %v", removedKubeletCAdvisorFlags137, wantFlags)
	}
	check := checkByID(t, scan137(t, completeInput()), "removed-kubelet-cadvisor-options")
	if check.Status != CheckReview || len(check.Findings) != 1 || !strings.Contains(check.Findings[0].Impact, "fails to start") || !strings.Contains(check.Findings[0].Remediation, "--housekeeping-interval remains supported") || !strings.Contains(check.Findings[0].Remediation, "container_application_*") {
		t.Fatalf("cAdvisor manual review = %+v, want bounded review with startup and metric impact", check)
	}
	requireFindingReference(t, check.Findings, "/pull/139870")
}

func TestRemovedSchedulingAPIs137(t *testing.T) {
	input := completeInput()
	input.SchedulingV1Alpha2Installed = true
	input.SchedulingV1Alpha2Objects = []*unstructured.Unstructured{{Object: map[string]any{
		"apiVersion": "scheduling.k8s.io/v1alpha2",
		"kind":       "PodGroup",
		"metadata":   map[string]any{"namespace": "batch", "name": "workers"},
	}}}
	check := checkByID(t, scan137(t, input), "removed-scheduling-apis")
	if check.Status != CheckBlocked || len(check.Findings) != 1 || check.Findings[0].Resource.Kind != "PodGroup" || !strings.Contains(check.Findings[0].Remediation, "After the control plane reaches Kubernetes 1.37") {
		t.Fatalf("stored alpha scheduling object = %+v, want blocker", check)
	}
	requireFindingReference(t, check.Findings, "/pull/140184")

	input = completeInput()
	input.SchedulingV1Alpha2DiscoveryAvailable = false
	check = checkByID(t, scan137(t, input), "removed-scheduling-apis")
	if check.Status != CheckUnknown {
		t.Fatalf("missing scheduling discovery = %+v, want unknown", check)
	}

	input = completeInput()
	check = checkByID(t, scan137(t, input), "removed-scheduling-apis")
	if check.Status != CheckNotApplicable {
		t.Fatalf("absent scheduling API = %+v, want not applicable", check)
	}

	input = completeInput()
	input.SchedulingV1Alpha2Installed = true
	input.Namespaces = []string{"apps"}
	check = checkByID(t, scan137(t, input), "removed-scheduling-apis")
	if check.Status != CheckUnknown || !strings.Contains(check.Caveat, "Only namespace apps was inspected") {
		t.Fatalf("namespace-scoped scheduling evidence = %+v, want unknown with scope caveat", check)
	}
}

func TestKubeadmV1Beta3Config137(t *testing.T) {
	input := completeInput()
	input.ConfigMaps = []*corev1.ConfigMap{{
		ObjectMeta: metav1.ObjectMeta{Name: "kubeadm-config", Namespace: "kube-system"},
		Data:       map[string]string{"ClusterConfiguration": "apiVersion: kubeadm.k8s.io/v1beta3\nkind: ClusterConfiguration\n"},
	}}
	check := checkByID(t, scan137(t, input), "kubeadm-config-v1beta3")
	if check.Status != CheckBlocked || check.Inspected != 1 || len(check.Findings) != 1 || !strings.Contains(check.Findings[0].Remediation, "config migrate") || !strings.Contains(check.Findings[0].Remediation, "upload-config kubeadm") {
		t.Fatalf("kubeadm v1beta3 = %+v, want blocker", check)
	}
	requireFindingReference(t, check.Findings, "/pull/136016")

	input.ConfigMaps[0].Data["ClusterConfiguration"] = "apiVersion: kubeadm.k8s.io/v1beta4\nkind: ClusterConfiguration\n"
	check = checkByID(t, scan137(t, input), "kubeadm-config-v1beta3")
	if check.Status != CheckPassed || check.Caveat != "" || !strings.Contains(check.EvidenceNote, "--config") || !strings.Contains(check.EvidenceNote, "not visible") {
		t.Fatalf("kubeadm v1beta4 = %+v, want pass with a non-limiting host-file evidence note", check)
	}

	input.ConfigMaps[0].Data["ClusterConfiguration"] = "apiVersion: kubeadm.k8s.io/v1beta4\nkind: ClusterConfiguration\nfeatureGates:\n  NodeLocalCRISocket: true\n"
	check = checkByID(t, scan137(t, input), "kubeadm-config-v1beta3")
	if check.Status != CheckBlocked || len(check.Findings) != 1 || !strings.Contains(check.Findings[0].Title, "NodeLocalCRISocket") {
		t.Fatalf("removed kubeadm feature gate = %+v, want blocker", check)
	}
	requireFindingReference(t, check.Findings, "/pull/138645")

	input.ConfigMaps[0].Data["ClusterConfiguration"] = "[not yaml"
	check = checkByID(t, scan137(t, input), "kubeadm-config-v1beta3")
	if check.Status != CheckUnknown || !strings.Contains(check.EvidenceNote, "--config") || !strings.Contains(check.Caveat, "could not fully parse") {
		t.Fatalf("unparseable kubeadm config = %+v, want unknown with both evidence boundaries", check)
	}
}

func kubeProxyDaemonSet(args ...string) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "kube-proxy", Namespace: "kube-system", Labels: map[string]string{"k8s-app": "kube-proxy"}},
		Spec:       appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "kube-proxy", Args: args}}}}},
	}
}

func TestKubeProxyModeTransition137(t *testing.T) {
	for _, tc := range []struct {
		name        string
		args        []string
		wantStatus  CheckStatus
		wantRef     string
		wantImpact  string
		wantSummary string
	}{
		{name: "iptables explicit", args: []string{"--proxy-mode=iptables"}, wantStatus: CheckPassed},
		{name: "ipvs deprecated", args: []string{"--proxy-mode=ipvs"}, wantStatus: CheckReview, wantRef: "5495-deprecate-ipvs-mode-in-kube-proxy"},
		{name: "linux default changing", wantStatus: CheckReview, wantRef: "5343-nftables-to-default", wantImpact: "Kubernetes 1.40", wantSummary: "1 kube-proxy mode setting requires migration review."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := completeInput()
			input.DaemonSets = []*appsv1.DaemonSet{kubeProxyDaemonSet(tc.args...)}
			check := checkByID(t, scan137(t, input), "kube-proxy-mode-transition")
			if check.Status != tc.wantStatus {
				t.Fatalf("check = %+v, want %s", check, tc.wantStatus)
			}
			if tc.wantRef != "" {
				requireFindingReference(t, check.Findings, tc.wantRef)
			}
			if tc.wantImpact != "" && (len(check.Findings) != 1 || !strings.Contains(check.Findings[0].Impact, tc.wantImpact)) {
				t.Fatalf("impact = %+v, want %q", check.Findings, tc.wantImpact)
			}
			if tc.wantSummary != "" && check.Summary != tc.wantSummary {
				t.Fatalf("summary = %q, want %q", check.Summary, tc.wantSummary)
			}
		})
	}
}

func TestKubeProxyCommandEvidencePath137(t *testing.T) {
	input := completeInput()
	daemonSet := kubeProxyDaemonSet()
	daemonSet.Spec.Template.Spec.Containers[0].Command = []string{"kube-proxy", "--proxy-mode=ipvs"}
	input.DaemonSets = []*appsv1.DaemonSet{daemonSet}
	check := checkByID(t, scan137(t, input), "kube-proxy-mode-transition")
	if check.Status != CheckReview || len(check.Findings) != 1 || !strings.Contains(check.Findings[0].Evidence.Path, ".command[") {
		t.Fatalf("command-based proxy mode = %+v, want command evidence path", check)
	}
}

func TestKubeProxySoleFallbackContainerUsesExactEvidencePath137(t *testing.T) {
	input := completeInput()
	daemonSet := kubeProxyDaemonSet()
	daemonSet.Spec.Template.Spec.Containers[0].Name = "network-proxy"
	daemonSet.Spec.Template.Spec.Containers[0].Args = []string{"--proxy-mode=ipvs"}
	input.DaemonSets = []*appsv1.DaemonSet{daemonSet}
	check := checkByID(t, scan137(t, input), "kube-proxy-mode-transition")
	if check.Status != CheckReview || len(check.Findings) != 1 || !strings.Contains(check.Findings[0].Evidence.Path, "containers[network-proxy]") {
		t.Fatalf("fallback container evidence = %+v, want its actual name in the path", check)
	}
}

func TestManagedKubeProxyUsesProviderRemediation137(t *testing.T) {
	input := completeInput()
	input.Nodes[0].Labels = map[string]string{"cloud.google.com/gke-nodepool": "default"}
	input.DaemonSets = []*appsv1.DaemonSet{kubeProxyDaemonSet()}
	check := checkByID(t, scan137(t, input), "kube-proxy-mode-transition")
	if check.Status != CheckReview || len(check.Findings) != 1 || !strings.Contains(check.Findings[0].Remediation, "provider-supported") || !strings.Contains(check.Findings[0].Remediation, "do not edit") {
		t.Fatalf("managed kube-proxy remediation = %+v, want provider-aware action", check)
	}
}

func TestKubeProxyModeTransitionPersistsUntilDefaultChange(t *testing.T) {
	input := completeInput()
	input.DaemonSets = []*appsv1.DaemonSet{kubeProxyDaemonSet("--proxy-mode=ipvs")}
	for _, tc := range []struct {
		current string
		target  string
		want    bool
	}{
		{current: "1.36", target: "1.37", want: true},
		{current: "1.36", target: "1.38", want: true},
		{current: "1.37", target: "1.38", want: true},
		{current: "1.38", target: "1.39", want: true},
		{current: "1.39", target: "1.40", want: true},
		{current: "1.40", target: "1.41", want: false},
	} {
		result, err := Scan(input, tc.current, tc.target)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, check := range result.Checks {
			if check.ID == "kube-proxy-mode-transition" {
				found = true
				break
			}
		}
		if found != tc.want {
			t.Fatalf("Scan(%s, %s) includes kube-proxy transition = %v, want %v", tc.current, tc.target, found, tc.want)
		}
	}
}

func TestKubeProxyModeTransitionInspectsMirrorPods137(t *testing.T) {
	input := completeInput()
	input.Pods = []*corev1.Pod{{
		ObjectMeta: metav1.ObjectMeta{Name: "kube-proxy-node-a", Namespace: "kube-system", Annotations: map[string]string{corev1.MirrorPodAnnotationKey: "mirror"}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "kube-proxy", Args: []string{"--proxy-mode=ipvs"}}}},
	}}
	check := checkByID(t, scan137(t, input), "kube-proxy-mode-transition")
	if check.Status != CheckReview || len(check.Findings) != 1 || check.Findings[0].Resource == nil || check.Findings[0].Resource.Kind != "Pod" || check.Findings[0].Evidence.Detail != "ipvs" {
		t.Fatalf("mirror Pod kube-proxy = %+v, want Pod-backed IPVS review", check)
	}

	input.Pods[0].Spec.Containers[0].Args = []string{"--config=/var/lib/kube-proxy/config.yaml"}
	check = checkByID(t, scan137(t, input), "kube-proxy-mode-transition")
	if check.Status != CheckUnknown || !strings.Contains(check.Caveat, "not readable through the Kubernetes API") {
		t.Fatalf("mirror Pod file config = %+v, want unknown", check)
	}
}

func TestKubeProxyModeTransitionWithoutObservableWorkloadIsUnknown137(t *testing.T) {
	check := checkByID(t, scan137(t, completeInput()), "kube-proxy-mode-transition")
	if check.Status != CheckUnknown || !strings.Contains(check.Summary, "DaemonSet or mirror Pod") {
		t.Fatalf("unobservable kube-proxy = %+v, want unknown", check)
	}
}

func TestKubeProxyWindowsPodOSDefaultsToKernelspace137(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*appsv1.DaemonSet)
	}{
		{name: "pod OS", configure: func(daemonSet *appsv1.DaemonSet) {
			daemonSet.Spec.Template.Spec.OS = &corev1.PodOS{Name: corev1.Windows}
		}},
		{name: "node selector", configure: func(daemonSet *appsv1.DaemonSet) {
			daemonSet.Spec.Template.Spec.NodeSelector = map[string]string{corev1.LabelOSStable: "windows"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := completeInput()
			daemonSet := kubeProxyDaemonSet()
			tc.configure(daemonSet)
			input.DaemonSets = []*appsv1.DaemonSet{daemonSet}
			check := checkByID(t, scan137(t, input), "kube-proxy-mode-transition")
			if check.Status != CheckPassed {
				t.Fatalf("Windows kube-proxy default = %+v, want passed", check)
			}
		})
	}
}

func TestKubeProxyConfigFileOverridesFlag137(t *testing.T) {
	input := completeInput()
	daemonSet := kubeProxyDaemonSet("--config=/var/lib/kube-proxy/config.conf", "--proxy-mode=iptables")
	daemonSet.Spec.Template.Spec.Volumes = []corev1.Volume{{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "kube-proxy"}}}}}
	daemonSet.Spec.Template.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{Name: "config", MountPath: "/var/lib/kube-proxy"}}
	input.DaemonSets = []*appsv1.DaemonSet{daemonSet}
	input.ConfigMaps = []*corev1.ConfigMap{{ObjectMeta: metav1.ObjectMeta{Name: "kube-proxy", Namespace: "kube-system"}, Data: map[string]string{"config.conf": "apiVersion: kubeproxy.config.k8s.io/v1alpha1\nmode: ipvs\n"}}}
	check := checkByID(t, scan137(t, input), "kube-proxy-mode-transition")
	if check.Status != CheckReview || len(check.Findings) != 1 || !strings.Contains(check.Findings[0].Evidence.Path, "ConfigMap/") {
		t.Fatalf("mounted kube-proxy config = %+v, want authoritative IPVS finding", check)
	}

	input.ConfigMaps = []*corev1.ConfigMap{}
	check = checkByID(t, scan137(t, input), "kube-proxy-mode-transition")
	if check.Status != CheckUnknown {
		t.Fatalf("missing mounted kube-proxy config = %+v, want unknown", check)
	}
}

func TestKubeSystemCacheScopesFollowActualEvidence137(t *testing.T) {
	input := completeInput()
	input.Pods = []*corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "kube-apiserver-node-a", Namespace: "kube-system", Annotations: map[string]string{corev1.MirrorPodAnnotationKey: "mirror"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "kube-apiserver"}}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "kube-controller-manager-node-a", Namespace: "kube-system", Annotations: map[string]string{corev1.MirrorPodAnnotationKey: "mirror"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "kube-controller-manager"}}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "kube-scheduler-node-a", Namespace: "kube-system", Annotations: map[string]string{corev1.MirrorPodAnnotationKey: "mirror"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "kube-scheduler"}}}},
	}
	input.ConfigMaps = []*corev1.ConfigMap{{ObjectMeta: metav1.ObjectMeta{Name: "kubeadm-config", Namespace: "kube-system"}, Data: map[string]string{"ClusterConfiguration": "apiVersion: kubeadm.k8s.io/v1beta4\nkind: ClusterConfiguration\n"}}}
	input.DaemonSets = []*appsv1.DaemonSet{kubeProxyDaemonSet("--proxy-mode=iptables")}
	input.CacheScopedKinds = map[string][]string{
		"pods":       {"kube-system"},
		"configmaps": {"team-a"},
		"daemonsets": {"kube-system"},
	}

	result := scan137(t, input)
	for _, id := range []string{"removed-feature-gates", "removed-component-flags"} {
		check := checkByID(t, result, id)
		if check.Status != CheckPassed || strings.Contains(check.Caveat, "Cached evidence is namespace-limited") {
			t.Fatalf("%s = %+v, kube-system Pod coverage should be sufficient", id, check)
		}
	}
	kubeadm := checkByID(t, result, "kubeadm-config-v1beta3")
	if kubeadm.Status != CheckUnknown || !strings.Contains(kubeadm.Summary, "kube-system is outside") {
		t.Fatalf("kubeadm scope = %+v, want unknown because ConfigMaps exclude kube-system", kubeadm)
	}
	proxy := checkByID(t, result, "kube-proxy-mode-transition")
	if proxy.Status != CheckPassed || proxy.Caveat != "" {
		t.Fatalf("flag-based kube-proxy mode = %+v, ConfigMap scope should be irrelevant", proxy)
	}

	input.CacheScopedKinds["pods"] = []string{"team-a"}
	input.CacheScopedKinds["daemonsets"] = []string{"team-a"}
	result = scan137(t, input)
	for _, id := range []string{"removed-feature-gates", "removed-component-flags", "kube-proxy-mode-transition"} {
		check := checkByID(t, result, id)
		if check.Status != CheckUnknown || !strings.Contains(check.Summary+" "+check.Caveat, "kube-system is outside") {
			t.Fatalf("%s = %+v, want unknown when its informer excludes kube-system", id, check)
		}
	}
	input.CacheScopedKinds["pods"] = []string{"kube-system"}
	input.CacheScopedKinds["daemonsets"] = []string{"kube-system"}

	configMap := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "kube-proxy", Namespace: "kube-system"}, Data: map[string]string{"config.conf": "mode: nftables\n"}}
	configuredProxy := kubeProxyDaemonSet("--config=/var/lib/kube-proxy/config.conf")
	configuredProxy.Spec.Template.Spec.Volumes = []corev1.Volume{{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "kube-proxy"}}}}}
	configuredProxy.Spec.Template.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{Name: "config", MountPath: "/var/lib/kube-proxy"}}
	input.DaemonSets = []*appsv1.DaemonSet{configuredProxy}
	input.ConfigMaps = append(input.ConfigMaps, configMap)
	result = scan137(t, input)
	proxy = checkByID(t, result, "kube-proxy-mode-transition")
	if proxy.Status != CheckUnknown || !strings.Contains(proxy.Caveat, "outside the readable ConfigMap scope") {
		t.Fatalf("file-based kube-proxy mode = %+v, want ConfigMap scope boundary", proxy)
	}

	input.CacheScopedKinds["configmaps"] = []string{"kube-system"}
	result = scan137(t, input)
	for _, id := range []string{"kubeadm-config-v1beta3", "kube-proxy-mode-transition"} {
		check := checkByID(t, result, id)
		if check.Status != CheckPassed || strings.Contains(check.Caveat, "Cached evidence is namespace-limited") {
			t.Fatalf("%s = %+v, kube-system ConfigMap coverage should be sufficient", id, check)
		}
	}
}

func TestKubeProxyConfigMapItemSubPath137(t *testing.T) {
	input := completeInput()
	daemonSet := kubeProxyDaemonSet("--config=/var/lib/kube-proxy/config.yaml")
	daemonSet.Spec.Template.Spec.Volumes = []corev1.Volume{{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
		LocalObjectReference: corev1.LocalObjectReference{Name: "kube-proxy"},
		Items:                []corev1.KeyToPath{{Key: "config.conf", Path: "projected.yaml"}},
	}}}}
	daemonSet.Spec.Template.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{Name: "config", MountPath: "/var/lib/kube-proxy/config.yaml", SubPath: "projected.yaml"}}
	input.DaemonSets = []*appsv1.DaemonSet{daemonSet}
	input.ConfigMaps = []*corev1.ConfigMap{{ObjectMeta: metav1.ObjectMeta{Name: "kube-proxy", Namespace: "kube-system"}, Data: map[string]string{"config.conf": "mode: nftables\n"}}}
	check := checkByID(t, scan137(t, input), "kube-proxy-mode-transition")
	if check.Status != CheckPassed {
		t.Fatalf("ConfigMap item path through subPath = %+v, want passed", check)
	}
}

func TestKubeProxyModeDiagnostics137(t *testing.T) {
	input := completeInput()
	daemonSet := kubeProxyDaemonSet()
	daemonSet.Spec.Template.Spec.Containers = []corev1.Container{{Name: "sidecar"}, {Name: "proxy"}}
	input.DaemonSets = []*appsv1.DaemonSet{daemonSet}
	check := checkByID(t, scan137(t, input), "kube-proxy-mode-transition")
	if check.Status != CheckUnknown || !strings.Contains(check.Caveat, "none named kube-proxy") {
		t.Fatalf("ambiguous kube-proxy container = %+v, want actionable caveat", check)
	}

	if value, field, found := commandFlag(nil, []string{"--proxy-mode", "--config=/etc/kube-proxy.yaml"}, "--proxy-mode"); found || value != "" || field != "" {
		t.Fatalf("flag followed by another flag = (%q, %q, %v), want absent value", value, field, found)
	}
	if gates := parsedFeatureGates(nil, []string{"--feature-gates", "--secure-port=6443"}); len(gates) != 0 {
		t.Fatalf("feature-gates followed by another flag = %v, want no parsed gates", gates)
	}
}

func TestRemovedControlPlaneMetrics137(t *testing.T) {
	input := completeInput()
	input.PrometheusRulesInstalled = true
	input.PrometheusRules = []*unstructured.Unstructured{{Object: map[string]any{
		"apiVersion": "monitoring.coreos.com/v1",
		"kind":       "PrometheusRule",
		"metadata":   map[string]any{"namespace": "monitoring", "name": "apiserver"},
		"spec": map[string]any{
			"groups": []any{map[string]any{
				"rules": []any{map[string]any{
					"alert": "CacheLists",
					"expr":  "rate(apiserver_cache_list_total[5m]) > 0",
				}},
			}},
		},
	}}}
	check := checkByID(t, scan137(t, input), "removed-control-plane-metrics")
	if check.Status != CheckPassed || len(check.Findings) != 0 {
		t.Fatalf("cache-list metric deprecation = %+v, want no 1.37 compatibility warning", check)
	}

	groups := input.PrometheusRules[0].Object["spec"].(map[string]any)["groups"].([]any)
	rule := groups[0].(map[string]any)["rules"].([]any)[0].(map[string]any)
	rule["expr"] = "sum(resourceclaim_controller_resource_claims)"
	check = checkByID(t, scan137(t, input), "removed-control-plane-metrics")
	if check.Status != CheckWarning || len(check.Findings) != 1 || check.Findings[0].Evidence.Detail != "resourceclaim_controller_resource_claims" || !strings.Contains(check.Findings[0].Remediation, "dynamic_resource_allocation_resource_claims") {
		t.Fatalf("renamed DRA metric = %+v, want exact replacement", check)
	}
	requireFindingReference(t, check.Findings, "/pull/138542")

	for _, metric := range []string{"container_cpu_load_average_10s", "container_cpu_load_d_average_10s", "container_tasks_state"} {
		rule["expr"] = "sum(" + metric + ")"
		check = checkByID(t, scan137(t, input), "removed-control-plane-metrics")
		if check.Status != CheckWarning || len(check.Findings) != 1 || check.Findings[0].Evidence.Detail != metric || !strings.Contains(check.Findings[0].Remediation, "no replacement") {
			t.Fatalf("removed cAdvisor metric %s = %+v, want warning without invented replacement", metric, check)
		}
		requireFindingReference(t, check.Findings, "/pull/139870")
	}

	rule["expr"] = "rate(container_application_http_requests_total[5m]) + container_application_queue_depth"
	check = checkByID(t, scan137(t, input), "removed-control-plane-metrics")
	if check.Status != CheckWarning || len(check.Findings) != 2 || check.Findings[0].Evidence.Detail != "container_application_http_requests_total" || check.Findings[1].Evidence.Detail != "container_application_queue_depth" {
		t.Fatalf("removed custom cAdvisor metric = %+v, want prefix match", check)
	}
	for _, finding := range check.Findings {
		requireFindingReference(t, []Finding{finding}, "/pull/139870")
	}
}

func requireFindingReference(t *testing.T, findings []Finding, urlSubstring string) {
	t.Helper()
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want one finding with reference containing %q", findings, urlSubstring)
	}
	for _, reference := range findings[0].References {
		if strings.Contains(reference.URL, urlSubstring) {
			return
		}
	}
	t.Fatalf("finding references = %+v, want URL containing %q", findings[0].References, urlSubstring)
}

func TestEvery137ConfigurationRemovalHasAnUpstreamReference(t *testing.T) {
	for name := range removedFeatureGates137 {
		if len(removedFeatureGateReferences137[name]) == 0 {
			t.Errorf("removed feature gate %s has no upstream reference", name)
		}
	}
	for name := range lockedFeatureGates137 {
		if len(lockedFeatureGateReferences137[name]) == 0 {
			t.Errorf("locked feature gate %s has no upstream reference", name)
		}
	}
	for name := range removedKubeadmFeatureGates137 {
		if len(kubeadmFeatureGateReferences137[name]) == 0 {
			t.Errorf("removed kubeadm feature gate %s has no upstream reference", name)
		}
	}
	if len(podGroupAdmissionReferences137) == 0 {
		t.Error("removed PodGroupWorkloadExists admission plugin has no upstream reference")
	}
}

func selinuxSharedVolumeInput(t *testing.T, firstPolicy, secondPolicy *corev1.PodSELinuxChangePolicy, firstLevel, secondLevel string) *Input {
	t.Helper()
	input := completeInput()
	seLinuxMount := true
	input.CSIDrivers = []*storagev1.CSIDriver{{ObjectMeta: metav1.ObjectMeta{Name: "csi.example.com"}, Spec: storagev1.CSIDriverSpec{SELinuxMount: &seLinuxMount}}}
	input.PersistentVolumes = []*corev1.PersistentVolume{{
		ObjectMeta: metav1.ObjectMeta{Name: "shared"},
		Spec: corev1.PersistentVolumeSpec{
			AccessModes:            []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			ClaimRef:               &corev1.ObjectReference{Namespace: "apps", Name: "shared"},
			PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: "csi.example.com", VolumeHandle: "shared"}},
		},
	}}
	input.PersistentVolumeClaims = []*corev1.PersistentVolumeClaim{{
		ObjectMeta: metav1.ObjectMeta{Name: "shared", Namespace: "apps"},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "shared", AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}},
	}}
	input.Pods = []*corev1.Pod{
		selinuxPod("one", firstPolicy, firstLevel),
		selinuxPod("two", secondPolicy, secondLevel),
	}
	return input
}

func selinuxPod(name string, policy *corev1.PodSELinuxChangePolicy, level string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "apps"},
		Spec: corev1.PodSpec{
			SecurityContext: &corev1.PodSecurityContext{SELinuxChangePolicy: policy, SELinuxOptions: &corev1.SELinuxOptions{Level: level}},
			Volumes:         []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "shared"}}}},
			Containers:      []corev1.Container{{Name: "app", Image: "example", VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data"}}}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func TestSELinuxMountTransition137(t *testing.T) {
	mountOption := corev1.SELinuxChangePolicyMountOption
	recursive := corev1.SELinuxChangePolicyRecursive
	compatibleLabels := selinuxSharedVolumeInput(t, nil, nil, "s0:c1,c2", "s0:c1,c2")
	compatibleLabels.Pods[1].Spec.SecurityContext.SELinuxOptions.User = "system_u"
	compatibleLabels.Pods[1].Spec.SecurityContext.SELinuxOptions.Role = "system_r"
	compatibleLabels.Pods[1].Spec.SecurityContext.SELinuxOptions.Type = "container_t"
	for _, tc := range []struct {
		name       string
		input      *Input
		wantStatus CheckStatus
		wantTitle  string
	}{
		{name: "different labels conflict", input: selinuxSharedVolumeInput(t, nil, nil, "s0:c1,c2", "s0:c3,c4"), wantStatus: CheckReview, wantTitle: "conflicting SELinux labels"},
		{name: "different policies conflict", input: selinuxSharedVolumeInput(t, &mountOption, &recursive, "s0:c1,c2", "s0:c1,c2"), wantStatus: CheckReview, wantTitle: "conflicting SELinux change policies"},
		{name: "unlabeled pod falls back to recursive policy", input: selinuxSharedVolumeInput(t, nil, nil, "s0:c1,c2", ""), wantStatus: CheckReview, wantTitle: "falls back to recursive SELinux relabeling"},
		{name: "recursive opt out", input: selinuxSharedVolumeInput(t, &recursive, &recursive, "s0:c1,c2", "s0:c3,c4"), wantStatus: CheckPassed},
		{name: "matching mount labels", input: selinuxSharedVolumeInput(t, nil, nil, "s0:c1,c2", "s0:c1,c2"), wantStatus: CheckPassed},
		{name: "defaultable label components", input: compatibleLabels, wantStatus: CheckPassed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			check := checkByID(t, scan137(t, tc.input), "selinux-mount-transition")
			if check.Status != tc.wantStatus {
				t.Fatalf("check = %+v, want %s", check, tc.wantStatus)
			}
			if tc.wantTitle != "" && (len(check.Findings) != 1 || !strings.Contains(check.Findings[0].Title, tc.wantTitle)) {
				t.Fatalf("findings = %+v, want title containing %q", check.Findings, tc.wantTitle)
			}
			if tc.wantStatus == CheckReview && (!strings.Contains(check.Caveat, "could not confirm") || !strings.Contains(check.Findings[0].Remediation, "Kubernetes 1.37 only")) {
				t.Fatalf("conditional SELinux finding = %+v, want applicability caveat and interim opt-out", check)
			}
			if tc.wantStatus == CheckReview && !strings.Contains(check.Findings[0].Impact, "same SELinux-enforcing node") {
				t.Fatalf("conditional SELinux impact = %q, want the same-node precondition", check.Findings[0].Impact)
			}
		})
	}
}

func TestSELinuxMountTransitionDetectsMultipleLabelsWithinOnePod137(t *testing.T) {
	input := selinuxSharedVolumeInput(t, nil, nil, "s0:c1,c2", "s0:c1,c2")
	input.Pods = input.Pods[:1]
	input.Pods[0].Spec.Containers = append(input.Pods[0].Spec.Containers, corev1.Container{
		Name: "sidecar", Image: "example", VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data"}},
		SecurityContext: &corev1.SecurityContext{SELinuxOptions: &corev1.SELinuxOptions{Level: "s0:c3,c4"}},
	})
	check := checkByID(t, scan137(t, input), "selinux-mount-transition")
	if check.Status != CheckReview || len(check.Findings) != 1 || check.Findings[0].Title != "Pod uses multiple SELinux labels on one volume" || strings.Contains(check.Findings[0].Title, "conflicting SELinux labels") {
		t.Fatalf("single-Pod multi-label volume = %+v, want structural in-Pod review", check)
	}
}

func TestSELinuxMountTransitionEvidenceBoundaries137(t *testing.T) {
	input := completeInput()
	input.NodeRuntimeEvidence[0].SELinuxMismatchErrors = 2
	check := checkByID(t, scan137(t, input), "selinux-mount-transition")
	if check.Status != CheckWarning || len(check.Findings) != 1 || check.Findings[0].Resource.Kind != "Node" {
		t.Fatalf("positive kubelet metric = %+v, want node warning", check)
	}
	if !strings.Contains(check.Findings[0].Remediation, "selinux_warning_controller_selinux_volume_conflict") || !strings.Contains(check.Findings[0].Remediation, "Kubernetes 1.37 only") {
		t.Fatalf("kubelet metric remediation = %q, want controller diagnostics and interim opt-out", check.Findings[0].Remediation)
	}

	input = completeInput()
	input.Events = []*corev1.Event{{Reason: "SELinuxLabelConflict", Message: "volume already mounted with a different context", InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "apps", Name: "api"}}}
	check = checkByID(t, scan137(t, input), "selinux-mount-transition")
	if check.Status != CheckWarning || len(check.Findings) != 1 || check.Findings[0].Evidence.Source != "event" {
		t.Fatalf("positive conflict event = %+v, want warning", check)
	}
	if !strings.Contains(check.Findings[0].Impact, "selinux-warning-controller") || !strings.Contains(check.Findings[0].Impact, "potential") {
		t.Fatalf("conflict event impact = %q, want accurate producer and semantics", check.Findings[0].Impact)
	}
	if !strings.Contains(check.Findings[0].Remediation, "selinux_warning_controller_selinux_volume_conflict") {
		t.Fatalf("conflict event remediation = %q, want the controller metric that identifies both Pods", check.Findings[0].Remediation)
	}

	input = completeInput()
	input.Events = []*corev1.Event{{Reason: "MultipleSELinuxLabels", Message: "Volume shared is mounted twice with different SELinux labels inside this pod", InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "apps", Name: "api"}}}
	check = checkByID(t, scan137(t, input), "selinux-mount-transition")
	if check.Status != CheckWarning || len(check.Findings) != 1 || !strings.Contains(check.Findings[0].Impact, "this Pod mounts one volume more than once") || strings.Contains(check.Findings[0].Impact, "same node") {
		t.Fatalf("single-Pod SELinux event = %+v, want unconditional in-Pod conflict", check)
	}
	if !strings.Contains(check.Findings[0].Remediation, "securityContext.seLinuxOptions") || strings.Contains(check.Findings[0].Remediation, "identify both Pods") {
		t.Fatalf("single-Pod SELinux remediation = %q, want this Pod's security contexts", check.Findings[0].Remediation)
	}

	input = selinuxSharedVolumeInput(t, nil, nil, "s0:c1,c2", "s0:c1,c2")
	input.CSIDrivers = nil
	check = checkByID(t, scan137(t, input), "selinux-mount-transition")
	if check.Status != CheckUnknown {
		t.Fatalf("missing CSI driver evidence = %+v, want unknown", check)
	}

	input = selinuxSharedVolumeInput(t, nil, nil, "s0:c1,c2", "s0:c3,c4")
	input.PersistentVolumes[0].Spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOncePod}
	input.PersistentVolumeClaims[0].Spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOncePod}
	check = checkByID(t, scan137(t, input), "selinux-mount-transition")
	if check.Status != CheckNotApplicable {
		t.Fatalf("already-covered ReadWriteOncePod volume = %+v, want not applicable", check)
	}

	input = completeInput()
	input.Nodes = nil
	check = checkByID(t, scan137(t, input), "selinux-mount-transition")
	if !strings.Contains(check.Caveat, "Node evidence was unavailable") || strings.Contains(check.Caveat, "1 Linux node") {
		t.Fatalf("missing node evidence = %+v, want no fabricated node count", check)
	}

	input = completeInput()
	input.Pods = nil
	check = checkByID(t, scan137(t, input), "selinux-mount-transition")
	if check.Status != CheckUnknown || !strings.Contains(check.Caveat, "Pods were unavailable") || strings.Contains(check.Caveat, "Persistent-volume or CSI-driver evidence") {
		t.Fatalf("missing Pod evidence = %+v, want Pod-specific incomplete coverage", check)
	}

	input = completeInput()
	input.Namespaces = []string{"apps"}
	check = checkByID(t, scan137(t, input), "selinux-mount-transition")
	if check.Status != CheckUnknown || !strings.Contains(check.Caveat, "Pods, PersistentVolumeClaims and Events") {
		t.Fatalf("namespace-scoped SELinux evidence = %+v, want unknown with scope caveat", check)
	}

	input = completeInput()
	input.CacheScopedKinds = map[string][]string{"pods": {"team-a"}, "persistentvolumeclaims": {"team-a"}, "events": {"team-a"}}
	check = checkByID(t, scan137(t, input), "selinux-mount-transition")
	if check.Status != CheckUnknown || !strings.Contains(check.Caveat, "Cached evidence is namespace-limited") {
		t.Fatalf("informer-scoped SELinux evidence = %+v, want unknown with cache-scope caveat", check)
	}
}

func TestSELinuxMountExplicitOptOut137(t *testing.T) {
	input := completeInput()
	input.NodeRuntimeEvidence[0].FeatureGates["SELinuxMount"] = false
	check := checkByID(t, scan137(t, input), "selinux-mount-transition")
	if check.Status != CheckNotApplicable || !strings.Contains(check.Summary, "every readable Linux kubelet") || !strings.Contains(check.Summary, "1.38") || !strings.Contains(check.EvidenceNote, "configz") || check.Caveat != "" {
		t.Fatalf("complete 1.37 opt-out = %+v, want not applicable with expiry", check)
	}

	input = selinuxSharedVolumeInput(t, nil, nil, "s0:c1,c2", "s0:c3,c4")
	input.NodeRuntimeEvidence[0].FeatureGates["SELinuxMount"] = false
	input.Nodes = append(input.Nodes, readyNode("node-b", "v1.35.7"))
	input.NodeRuntimeEvidence = append(input.NodeRuntimeEvidence, NodeRuntimeEvidence{NodeName: "node-b", ConfigAvailable: true, FeatureGates: map[string]bool{}})
	check = checkByID(t, scan137(t, input), "selinux-mount-transition")
	if check.Status != CheckReview {
		t.Fatalf("partial node opt-out = %+v, want structural review", check)
	}

	input = selinuxSharedVolumeInput(t, nil, nil, "s0:c1,c2", "s0:c3,c4")
	input.NodeRuntimeEvidence[0].FeatureGates["SELinuxMount"] = false
	result, err := Scan(input, "1.36", "1.38")
	if err != nil {
		t.Fatal(err)
	}
	check = checkByID(t, result, "selinux-mount-transition")
	if check.Status != CheckReview || len(check.Findings) != 1 || strings.Contains(check.Findings[0].Remediation, "For Kubernetes 1.37 only") || !strings.Contains(check.Findings[0].Remediation, "limited to a Kubernetes 1.37 target") {
		t.Fatalf("1.37-only opt-out with a 1.38 target = %+v, want structural review", check)
	}
}

func TestSELinuxMountTransitionTranslatesMigratedInTreeVolume137(t *testing.T) {
	input := selinuxSharedVolumeInput(t, nil, nil, "s0:c1,c2", "s0:c3,c4")
	input.CSIDrivers[0].Name = "ebs.csi.aws.com"
	input.PersistentVolumes[0].Spec.PersistentVolumeSource = corev1.PersistentVolumeSource{
		AWSElasticBlockStore: &corev1.AWSElasticBlockStoreVolumeSource{VolumeID: "aws://us-east-1a/vol-123"},
	}
	check := checkByID(t, scan137(t, input), "selinux-mount-transition")
	if check.Status != CheckReview || len(check.Findings) != 1 || !strings.Contains(check.Findings[0].Title, "conflicting SELinux labels") {
		t.Fatalf("migrated in-tree EBS volume = %+v, want CSI-translated conflict review", check)
	}
	if input.PersistentVolumes[0].Spec.AWSElasticBlockStore == nil || input.PersistentVolumes[0].Spec.CSI != nil {
		t.Fatalf("scan mutated source PV = %+v", input.PersistentVolumes[0].Spec.PersistentVolumeSource)
	}
}

func TestSELinuxMountTransitionIncludesGenericEphemeralPVC137(t *testing.T) {
	input := completeInput()
	seLinuxMount := true
	input.CSIDrivers = []*storagev1.CSIDriver{{ObjectMeta: metav1.ObjectMeta{Name: "csi.example.com"}, Spec: storagev1.CSIDriverSpec{SELinuxMount: &seLinuxMount}}}
	input.PersistentVolumes = []*corev1.PersistentVolume{{
		ObjectMeta: metav1.ObjectMeta{Name: "ephemeral"},
		Spec: corev1.PersistentVolumeSpec{
			AccessModes:            []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			ClaimRef:               &corev1.ObjectReference{Namespace: "apps", Name: "one-data"},
			PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: "csi.example.com", VolumeHandle: "ephemeral"}},
		},
	}}
	controller := true
	input.PersistentVolumeClaims = []*corev1.PersistentVolumeClaim{{ObjectMeta: metav1.ObjectMeta{Name: "one-data", Namespace: "apps", OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: "one", UID: "pod-one", Controller: &controller}}}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "ephemeral", AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}}}}
	input.Pods = []*corev1.Pod{{
		ObjectMeta: metav1.ObjectMeta{Name: "one", Namespace: "apps", UID: "pod-one"},
		Spec: corev1.PodSpec{
			SecurityContext: &corev1.PodSecurityContext{SELinuxOptions: &corev1.SELinuxOptions{Level: "s0:c1,c2"}},
			Volumes:         []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{Ephemeral: &corev1.EphemeralVolumeSource{VolumeClaimTemplate: &corev1.PersistentVolumeClaimTemplate{}}}}},
			Containers:      []corev1.Container{{Name: "app", Image: "example", VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data"}}}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}}
	check := checkByID(t, scan137(t, input), "selinux-mount-transition")
	if check.Status != CheckPassed || check.Inspected != 1 {
		t.Fatalf("generic ephemeral PVC = %+v, want one passed inspection", check)
	}

	input.PersistentVolumeClaims[0].OwnerReferences = nil
	check = checkByID(t, scan137(t, input), "selinux-mount-transition")
	if check.Status != CheckUnknown || check.Inspected != 0 {
		t.Fatalf("generic ephemeral PVC without Pod ownership = %+v, want incomplete evidence", check)
	}
}
