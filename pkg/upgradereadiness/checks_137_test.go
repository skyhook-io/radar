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
	}{
		{
			name: "effective kubelet gate",
			configure: func(input *Input) {
				input.NodeRuntimeEvidence[0].FeatureGates["SidecarContainers"] = true
			},
			wantStatus: CheckBlocked,
			wantPath:   "kubeletconfig.featureGates.SidecarContainers",
		},
		{
			name: "static pod API opt-out",
			configure: func(input *Input) {
				input.NodeRuntimeEvidence[0].FeatureGates["PreventStaticPodAPIReferences"] = false
			},
			wantStatus: CheckBlocked,
			wantImpact: "dependent static Pod cannot start",
		},
		{
			name: "control plane mirror pod",
			configure: func(input *Input) {
				input.Pods = []*corev1.Pod{{
					ObjectMeta: metav1.ObjectMeta{Name: "kube-apiserver-node-a", Namespace: "kube-system", Annotations: map[string]string{corev1.MirrorPodAnnotationKey: "mirror"}},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "kube-apiserver", Args: []string{"--feature-gates=APIServerTracing=false"}}}},
				}}
			},
			wantStatus: CheckBlocked,
			wantPath:   "spec.containers[0].args[--feature-gates].APIServerTracing",
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
		})
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
			if tc.wantStatus == CheckPassed && len(check.Findings) != 0 {
				t.Fatalf("locked default produced findings: %+v", check.Findings)
			}
			if tc.wantStatus == CheckBlocked && (len(check.Findings) != 1 || !strings.Contains(check.Findings[0].Impact, "locks this kube-apiserver feature gate to false")) {
				t.Fatalf("non-default finding = %+v", check.Findings)
			}
		})
	}
}

func TestRemovedControlPlaneConfigurationScope137(t *testing.T) {
	input := completeInput()
	input.Namespaces = []string{"apps"}
	featureGates := checkByID(t, scan137(t, input), "removed-feature-gates")
	if featureGates.Status != CheckPassed || !strings.Contains(featureGates.Caveat, "kube-system") {
		t.Fatalf("scoped feature-gate evidence = %+v, want kube-system caveat with readable node evidence", featureGates)
	}
	componentFlags := checkByID(t, scan137(t, input), "removed-component-flags")
	if componentFlags.Status != CheckUnknown || !strings.Contains(componentFlags.Summary, "kube-system") {
		t.Fatalf("scoped component-flag evidence = %+v, want unknown", componentFlags)
	}
}

func TestKubeletEventRecordQPS137(t *testing.T) {
	input := completeInput()
	input.NodeRuntimeEvidence[0].EventRecordQPS = 0
	check := checkByID(t, scan137(t, input), "kubelet-event-qps-change")
	if check.Status != CheckWarning || len(check.Findings) != 1 || !strings.Contains(check.Findings[0].Remediation, "50") {
		t.Fatalf("zero eventRecordQPS = %+v, want behavior-change warning", check)
	}

	input = completeInput()
	input.NodeRuntimeEvidence[0].EventRecordQPSAvailable = false
	check = checkByID(t, scan137(t, input), "kubelet-event-qps-change")
	if check.Status != CheckUnknown {
		t.Fatalf("missing eventRecordQPS = %+v, want unknown", check)
	}
}

func TestRemovedComponentFlag137(t *testing.T) {
	input := completeInput()
	input.Pods = []*corev1.Pod{{
		ObjectMeta: metav1.ObjectMeta{Name: "kube-controller-manager-node-a", Namespace: "kube-system", Annotations: map[string]string{corev1.MirrorPodAnnotationKey: "mirror"}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "kube-controller-manager", Args: []string{"--concurrent-service-syncs=10"}}}},
	}}
	check := checkByID(t, scan137(t, input), "removed-component-flags")
	if check.Status != CheckBlocked || len(check.Findings) != 1 {
		t.Fatalf("removed component flag = %+v, want blocker", check)
	}
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
	if check.Status != CheckBlocked || len(check.Findings) != 1 || check.Findings[0].Resource.Kind != "PodGroup" {
		t.Fatalf("stored alpha scheduling object = %+v, want blocker", check)
	}

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
}

func TestKubeadmV1Beta3Config137(t *testing.T) {
	input := completeInput()
	input.ConfigMaps = []*corev1.ConfigMap{{
		ObjectMeta: metav1.ObjectMeta{Name: "kubeadm-config", Namespace: "kube-system"},
		Data:       map[string]string{"ClusterConfiguration": "apiVersion: kubeadm.k8s.io/v1beta3\nkind: ClusterConfiguration\n"},
	}}
	check := checkByID(t, scan137(t, input), "kubeadm-config-v1beta3")
	if check.Status != CheckBlocked || len(check.Findings) != 1 {
		t.Fatalf("kubeadm v1beta3 = %+v, want blocker", check)
	}

	input.ConfigMaps[0].Data["ClusterConfiguration"] = "apiVersion: kubeadm.k8s.io/v1beta4\nkind: ClusterConfiguration\n"
	check = checkByID(t, scan137(t, input), "kubeadm-config-v1beta3")
	if check.Status != CheckPassed {
		t.Fatalf("kubeadm v1beta4 = %+v, want pass", check)
	}

	input.ConfigMaps[0].Data["ClusterConfiguration"] = "apiVersion: kubeadm.k8s.io/v1beta4\nkind: ClusterConfiguration\nfeatureGates:\n  NodeLocalCRISocket: true\n"
	check = checkByID(t, scan137(t, input), "kubeadm-config-v1beta3")
	if check.Status != CheckBlocked || len(check.Findings) != 1 || !strings.Contains(check.Findings[0].Title, "NodeLocalCRISocket") {
		t.Fatalf("removed kubeadm feature gate = %+v, want blocker", check)
	}

	input.ConfigMaps[0].Data["ClusterConfiguration"] = "[not yaml"
	check = checkByID(t, scan137(t, input), "kubeadm-config-v1beta3")
	if check.Status != CheckUnknown {
		t.Fatalf("unparseable kubeadm config = %+v, want unknown", check)
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
		name       string
		args       []string
		wantStatus CheckStatus
		wantRef    string
	}{
		{name: "iptables explicit", args: []string{"--proxy-mode=iptables"}, wantStatus: CheckPassed},
		{name: "ipvs deprecated", args: []string{"--proxy-mode=ipvs"}, wantStatus: CheckReview, wantRef: "5495-deprecate-ipvs-mode-in-kube-proxy"},
		{name: "linux default changing", wantStatus: CheckReview, wantRef: "5343-nftables-to-default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := completeInput()
			input.DaemonSets = []*appsv1.DaemonSet{kubeProxyDaemonSet(tc.args...)}
			check := checkByID(t, scan137(t, input), "kube-proxy-mode-transition")
			if check.Status != tc.wantStatus {
				t.Fatalf("check = %+v, want %s", check, tc.wantStatus)
			}
			if tc.wantRef != "" && (len(check.Findings) != 1 || len(check.Findings[0].References) != 1 || !strings.Contains(check.Findings[0].References[0].URL, tc.wantRef)) {
				t.Fatalf("finding references = %+v, want only %s", check.Findings, tc.wantRef)
			}
		})
	}
}

func TestKubeProxyModeTransitionOnlyAppliesWhenCrossing137(t *testing.T) {
	input := completeInput()
	input.DaemonSets = []*appsv1.DaemonSet{kubeProxyDaemonSet("--proxy-mode=ipvs")}
	for _, tc := range []struct {
		current string
		target  string
		want    bool
	}{
		{current: "1.36", target: "1.37", want: true},
		{current: "1.36", target: "1.38", want: true},
		{current: "1.37", target: "1.38", want: false},
		{current: "1.38", target: "1.39", want: false},
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

	if value, found := commandFlag(nil, []string{"--proxy-mode", "--config=/etc/kube-proxy.yaml"}, "--proxy-mode"); found || value != "" {
		t.Fatalf("flag followed by another flag = (%q, %v), want absent value", value, found)
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
	if check.Status != CheckWarning || len(check.Findings) != 1 {
		t.Fatalf("removed metric rule = %+v, want warning", check)
	}

	groups := input.PrometheusRules[0].Object["spec"].(map[string]any)["groups"].([]any)
	rule := groups[0].(map[string]any)["rules"].([]any)[0].(map[string]any)
	rule["expr"] = "sum(resourceclaim_controller_resource_claims)"
	check = checkByID(t, scan137(t, input), "removed-control-plane-metrics")
	if check.Status != CheckWarning || len(check.Findings) != 1 || check.Findings[0].Evidence.Detail != "resourceclaim_controller_resource_claims" || !strings.Contains(check.Findings[0].Remediation, "dynamic_resource_allocation_resource_claims") {
		t.Fatalf("renamed DRA metric = %+v, want exact replacement", check)
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
		{name: "different labels conflict", input: selinuxSharedVolumeInput(t, nil, nil, "s0:c1,c2", "s0:c3,c4"), wantStatus: CheckWarning, wantTitle: "conflicting SELinux labels"},
		{name: "different policies conflict", input: selinuxSharedVolumeInput(t, &mountOption, &recursive, "s0:c1,c2", "s0:c1,c2"), wantStatus: CheckWarning, wantTitle: "conflicting SELinux change policies"},
		{name: "unlabeled pod falls back to recursive policy", input: selinuxSharedVolumeInput(t, nil, nil, "s0:c1,c2", ""), wantStatus: CheckWarning, wantTitle: "falls back to recursive SELinux relabeling"},
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
		})
	}
}

func TestSELinuxMountTransitionEvidenceBoundaries137(t *testing.T) {
	input := completeInput()
	input.NodeRuntimeEvidence[0].SELinuxMismatchErrors = 2
	check := checkByID(t, scan137(t, input), "selinux-mount-transition")
	if check.Status != CheckWarning || len(check.Findings) != 1 || check.Findings[0].Resource.Kind != "Node" {
		t.Fatalf("positive kubelet metric = %+v, want node warning", check)
	}

	input = completeInput()
	input.Events = []*corev1.Event{{Reason: "SELinuxLabelConflict", Message: "volume already mounted with a different context", InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "apps", Name: "api"}}}
	check = checkByID(t, scan137(t, input), "selinux-mount-transition")
	if check.Status != CheckWarning || len(check.Findings) != 1 || check.Findings[0].Evidence.Source != "event" {
		t.Fatalf("positive conflict event = %+v, want warning", check)
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
