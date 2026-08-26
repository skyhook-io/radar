package upgradereadiness

import (
	"bytes"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	storageephemeral "k8s.io/component-helpers/storage/ephemeral"
)

var removedFeatureGates137 = map[string]bool{
	"APIServerTracing":                      true,
	"AnyVolumeDataSource":                   true,
	"BtreeWatchCache":                       true,
	"ConsistentListFromCache":               true,
	"GangScheduling":                        true,
	"OrderedNamespaceDeletion":              true,
	"PreventStaticPodAPIReferences":         true,
	"RelaxedDNSSearchValidation":            true,
	"ResilientWatchCacheInitialization":     true,
	"RetryGenerateName":                     true,
	"SidecarContainers":                     true,
	"StreamingCollectionEncodingToJSON":     true,
	"StreamingCollectionEncodingToProtobuf": true,
	"WorkloadAwarePreemption":               true,
}

var lockedAPIServerFeatureGates137 = map[string]bool{
	"DeclarativeValidationTakeover": false,
}

var removedKubeadmFeatureGates137 = map[string]bool{
	"NodeLocalCRISocket": true,
	"PublicKeysECDSA":    true,
}

func scanRemovedFeatureGates(input *Input) Check {
	check := Check{ID: "removed-feature-gates", Category: "Component configuration", Title: "Feature gates removed or locked in Kubernetes 1.37", Status: CheckPassed, Summary: "No incompatible feature-gate settings were found in readable component configuration.", Scope: "Effective kubelet configuration and readable control-plane mirror Pods", AppliesFrom: "1.37", References: append([]Reference(nil), changelog137References...)}
	evidenceByNode := make(map[string]NodeRuntimeEvidence, len(input.NodeRuntimeEvidence))
	for _, evidence := range input.NodeRuntimeEvidence {
		evidenceByNode[evidence.NodeName] = evidence
	}
	missingNodes := 0
	if input.Nodes == nil {
		check.Status = CheckUnknown
		check.Summary = "Node evidence was unavailable; Radar could not inspect effective kubelet feature gates."
	} else {
		for _, node := range input.Nodes {
			if node == nil {
				continue
			}
			check.Inspected++
			evidence, ok := evidenceByNode[node.Name]
			if !ok || !evidence.ConfigAvailable {
				missingNodes++
				continue
			}
			for name, enabled := range evidence.FeatureGates {
				if !removedFeatureGates137[name] {
					continue
				}
				impact := "Kubelet 1.37 no longer recognizes this feature gate and can reject the configuration during startup."
				if name == "PreventStaticPodAPIReferences" && !enabled {
					impact = "Kubelet 1.37 removes this gate and the opt-out that allowed static Pods to reference API objects; the node configuration will be rejected and any dependent static Pod cannot start."
				}
				check.Findings = append(check.Findings, Finding{RuleID: check.ID, Title: name + " is removed", Level: LevelBlocker, Resource: &ResourceRef{Kind: "Node", Name: node.Name}, Evidence: Evidence{Source: "kubelet configz", Path: "kubeletconfig.featureGates." + name, Detail: fmt.Sprintf("%t", enabled)}, AppliesFrom: check.AppliesFrom, Impact: impact, Remediation: "Remove " + name + " from the kubelet feature-gates configuration before upgrading this node.", References: append([]Reference(nil), removedFeatureGateReferences137[name]...)})
			}
		}
	}
	if !kubeSystemCovered(input, "pods") {
		check.Caveat = appendCaveat(check.Caveat, "kube-system is outside the readable Pod scope, so control-plane feature gates could not be inspected.")
	} else if input.Pods == nil {
		check.Caveat = appendCaveat(check.Caveat, "Pods were unavailable, so control-plane feature gates could not be inspected.")
	} else {
		for _, pod := range input.Pods {
			if pod == nil || pod.Annotations[corev1.MirrorPodAnnotationKey] == "" {
				continue
			}
			for containerIndex, container := range pod.Spec.Containers {
				if !isControlPlaneContainer(container.Name) {
					continue
				}
				for name, value := range parsedFeatureGates(container.Command, container.Args) {
					if removedFeatureGates137[name] {
						check.Findings = append(check.Findings, Finding{RuleID: check.ID, Title: name + " is removed", Level: LevelBlocker, Resource: &ResourceRef{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name}, Evidence: Evidence{Source: "live", Path: fmt.Sprintf("spec.containers[%d].args[--feature-gates].%s", containerIndex, name), Detail: value}, AppliesFrom: check.AppliesFrom, Impact: "This Kubernetes 1.37 control-plane component no longer recognizes the configured feature gate and can fail during startup.", Remediation: "Remove " + name + " from the component feature-gates argument before upgrading the control plane.", References: append([]Reference(nil), removedFeatureGateReferences137[name]...)})
						continue
					}
					defaultValue, locked := lockedAPIServerFeatureGates137[name]
					configuredValue, err := strconv.ParseBool(value)
					if container.Name != "kube-apiserver" || !locked || (err == nil && configuredValue == defaultValue) {
						continue
					}
					check.Findings = append(check.Findings, Finding{RuleID: check.ID, Title: name + " must use its locked default", Level: LevelBlocker, Resource: &ResourceRef{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name}, Evidence: Evidence{Source: "live", Path: fmt.Sprintf("spec.containers[%d].args[--feature-gates].%s", containerIndex, name), Detail: value}, AppliesFrom: check.AppliesFrom, Impact: fmt.Sprintf("Kubernetes 1.37 locks this kube-apiserver feature gate to %t and rejects a different configured value during startup.", defaultValue), Remediation: fmt.Sprintf("Set %s=%t or remove the explicit setting before upgrading the control plane.", name, defaultValue), References: append([]Reference(nil), lockedFeatureGateReferences137[name]...)})
				}
			}
		}
	}
	if missingNodes > 0 {
		check.Caveat = appendCaveat(check.Caveat, fmt.Sprintf("Effective kubelet configuration was unavailable for %d %s.", missingNodes, plural(missingNodes, "node", "nodes")))
		if len(check.Findings) == 0 {
			check.Status = CheckUnknown
			check.Summary = "No removed feature gates were found in readable component configuration, but kubelet configuration coverage is incomplete."
		}
	}
	if len(check.Findings) > 0 {
		check.Summary = fmt.Sprintf("%d incompatible feature-gate %s must be fixed before upgrading.", len(check.Findings), plural(len(check.Findings), "setting", "settings"))
	}
	return check
}

func isControlPlaneContainer(name string) bool {
	switch name {
	case "kube-apiserver", "kube-controller-manager", "kube-scheduler":
		return true
	default:
		return false
	}
}

func parsedFeatureGates(command, args []string) map[string]string {
	values := append(append([]string{}, command...), args...)
	result := map[string]string{}
	for i := 0; i < len(values); i++ {
		value := ""
		switch {
		case strings.HasPrefix(values[i], "--feature-gates="):
			value = strings.TrimPrefix(values[i], "--feature-gates=")
		case values[i] == "--feature-gates" && i+1 < len(values):
			i++
			value = values[i]
		default:
			continue
		}
		for _, entry := range strings.Split(value, ",") {
			name, enabled, ok := strings.Cut(strings.TrimSpace(entry), "=")
			if ok && name != "" {
				result[name] = enabled
			}
		}
	}
	return result
}

func scanRemovedComponentFlags(input *Input) Check {
	check := Check{ID: "removed-component-flags", Category: "Component configuration", Title: "Component flags removed in Kubernetes 1.37", Status: CheckPassed, Summary: "No readable control-plane mirror Pod uses a component flag removed in Kubernetes 1.37.", Scope: "Readable control-plane mirror Pods", AppliesFrom: "1.37", References: append([]Reference(nil), componentFlagReferences137...)}
	if !kubeSystemCovered(input, "pods") {
		check.Status, check.Summary = CheckUnknown, "kube-system is outside the readable Pod scope, so control-plane component flags could not be inspected."
		return check
	}
	if input.Pods == nil {
		check.Status, check.Summary = CheckUnknown, "Pods were unavailable, so control-plane component flags could not be inspected."
		return check
	}
	foundControllerManager := false
	for _, pod := range input.Pods {
		if pod == nil || pod.Annotations[corev1.MirrorPodAnnotationKey] == "" {
			continue
		}
		for containerIndex, container := range pod.Spec.Containers {
			if container.Name != "kube-controller-manager" {
				continue
			}
			foundControllerManager = true
			check.Inspected++
			if value, found := commandFlagPresence(container.Command, container.Args, "--concurrent-service-syncs"); found {
				check.Findings = append(check.Findings, Finding{RuleID: check.ID, Title: "--concurrent-service-syncs is removed", Level: LevelBlocker, Resource: &ResourceRef{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name}, Evidence: Evidence{Source: "live", Path: fmt.Sprintf("spec.containers[%d].args[--concurrent-service-syncs]", containerIndex), Detail: value}, AppliesFrom: check.AppliesFrom, Impact: "kube-controller-manager 1.37 no longer recognizes this flag and can fail during startup.", Remediation: "Remove --concurrent-service-syncs from the kube-controller-manager arguments before upgrading the control plane.", References: append([]Reference(nil), check.References...)})
			}
		}
	}
	if !foundControllerManager {
		check.Status, check.Summary = CheckNotApplicable, "No kube-controller-manager mirror Pod was readable; the provider may manage the control plane."
	} else if len(check.Findings) > 0 {
		check.Summary = "The removed kube-controller-manager flag must be deleted before upgrading."
	}
	return check
}

func commandFlagPresence(command, args []string, name string) (string, bool) {
	values := append(append([]string{}, command...), args...)
	for i, value := range values {
		if strings.HasPrefix(value, name+"=") {
			return strings.TrimPrefix(value, name+"="), true
		}
		if value == name {
			if i+1 < len(values) && !strings.HasPrefix(values[i+1], "--") {
				return values[i+1], true
			}
			return "set", true
		}
	}
	return "", false
}

func scanKubeletEventQPS(input *Input) Check {
	check := Check{ID: "kubelet-event-qps-change", Category: "Node configuration", Title: "Kubelet event throttling behavior", Status: CheckPassed, Summary: "No readable kubelet config sets eventRecordQPS to zero.", Scope: "Effective kubelet configuration", AppliesFrom: "1.37", References: append([]Reference(nil), eventRecordQPSReferences137...)}
	if input.Nodes == nil || input.NodeRuntimeEvidence == nil {
		check.Status, check.Summary = CheckUnknown, "Effective kubelet configuration was unavailable."
		return check
	}
	evidenceByNode := make(map[string]NodeRuntimeEvidence, len(input.NodeRuntimeEvidence))
	for _, evidence := range input.NodeRuntimeEvidence {
		evidenceByNode[evidence.NodeName] = evidence
	}
	missing := 0
	for _, node := range input.Nodes {
		if node == nil {
			continue
		}
		check.Inspected++
		evidence, ok := evidenceByNode[node.Name]
		if !ok || !evidence.ConfigAvailable || !evidence.EventRecordQPSAvailable {
			missing++
			continue
		}
		if evidence.EventRecordQPS != 0 {
			continue
		}
		check.Findings = append(check.Findings, Finding{RuleID: check.ID, Title: "eventRecordQPS zero becomes unlimited", Level: LevelWarning, Resource: &ResourceRef{Kind: "Node", Name: node.Name}, Evidence: Evidence{Source: "kubelet configz", Path: "kubeletconfig.eventRecordQPS", Detail: "0"}, AppliesFrom: check.AppliesFrom, Impact: "Kubernetes 1.37 fixes zero to mean unlimited event recording instead of the previous effective default, which can sharply increase event traffic.", Remediation: "Set eventRecordQPS to 50 to preserve the pre-1.37 effective limit, or choose another deliberate limit after reviewing event volume.", References: append([]Reference(nil), check.References...)})
	}
	if missing > 0 {
		check.Caveat = fmt.Sprintf("eventRecordQPS was unavailable for %d %s; kubelets with debugging handlers disabled do not serve configz.", missing, plural(missing, "node", "nodes"))
		if len(check.Findings) == 0 {
			check.Status, check.Summary = CheckUnknown, "No eventRecordQPS behavior change was found in readable kubelet configuration, but coverage is incomplete."
		}
	}
	if len(check.Findings) > 0 {
		check.Summary = fmt.Sprintf("%d %s will change to unlimited event recording in Kubernetes 1.37.", len(check.Findings), plural(len(check.Findings), "node", "nodes"))
	}
	return check
}

func scanRemovedSchedulingAPIs(input *Input) Check {
	check := Check{ID: "removed-scheduling-apis", Category: "API compatibility", Title: "Scheduling APIs removed in Kubernetes 1.37", Status: CheckPassed, Summary: "No stored scheduling.k8s.io/v1alpha2 Workload or PodGroup objects were found.", Scope: "Stored Workload and PodGroup objects", AppliesFrom: "1.37", References: append([]Reference(nil), schedulingAPIReferences137...)}
	if !input.SchedulingV1Alpha2DiscoveryAvailable {
		check.Status, check.Summary = CheckUnknown, "API discovery was unavailable for scheduling.k8s.io/v1alpha2."
		return check
	}
	if !input.SchedulingV1Alpha2Installed {
		check.Status, check.Summary = CheckNotApplicable, "The scheduling.k8s.io/v1alpha2 API is not served by this cluster."
		return check
	}
	if input.SchedulingV1Alpha2Objects == nil {
		check.Status, check.Summary = CheckUnknown, "The scheduling.k8s.io/v1alpha2 API is served but its objects could not be inspected."
		return check
	}
	check.Inspected = len(input.SchedulingV1Alpha2Objects)
	for _, object := range input.SchedulingV1Alpha2Objects {
		if object == nil {
			continue
		}
		check.Findings = append(check.Findings, Finding{RuleID: check.ID, Title: "scheduling.k8s.io/v1alpha2 " + object.GetKind(), Level: LevelBlocker, Resource: &ResourceRef{Group: "scheduling.k8s.io", Kind: object.GetKind(), Namespace: object.GetNamespace(), Name: object.GetName()}, Evidence: Evidence{Source: "live", Path: "apiVersion", Detail: "scheduling.k8s.io/v1alpha2"}, AppliesFrom: check.AppliesFrom, Impact: "Kubernetes 1.37 no longer serves this alpha API and requires its stored objects to be removed before upgrade.", Remediation: "Migrate this object to scheduling.k8s.io/v1beta1 or delete it before upgrading.", References: append([]Reference(nil), check.References...)})
	}
	if len(input.SchedulingV1Alpha2UnavailableKinds) > 0 {
		check.Caveat = "Could not inspect: " + formatBoundedList(input.SchedulingV1Alpha2UnavailableKinds, ", ") + "."
		if len(check.Findings) == 0 {
			check.Status, check.Summary = CheckUnknown, "No removed scheduling objects were found in readable kinds, but API coverage is incomplete."
		}
	}
	if len(check.Findings) > 0 {
		check.Summary = fmt.Sprintf("%d stored scheduling %s must be migrated or removed before upgrading.", len(check.Findings), plural(len(check.Findings), "object", "objects"))
	}
	return check
}

func scanKubeadmConfig(input *Input) Check {
	check := Check{ID: "kubeadm-config-v1beta3", Category: "Cluster lifecycle", Title: "kubeadm configuration API", Status: CheckNotApplicable, Summary: "No kubeadm cluster configuration was found.", Scope: "kube-system/kubeadm-config ConfigMap", AppliesFrom: "1.37", References: append([]Reference(nil), changelog137References...)}
	if !kubeSystemCovered(input, "configmaps") {
		check.Status, check.Summary = CheckUnknown, "kube-system is outside the readable ConfigMap scope, so kubeadm configuration could not be inspected."
		return check
	}
	if input.ConfigMaps == nil {
		check.Status, check.Summary = CheckUnknown, "ConfigMaps were unavailable, so kubeadm configuration could not be inspected."
		return check
	}
	var configMap *corev1.ConfigMap
	for _, candidate := range input.ConfigMaps {
		if candidate != nil && candidate.Namespace == "kube-system" && candidate.Name == "kubeadm-config" {
			configMap = candidate
			break
		}
	}
	if configMap == nil {
		return check
	}
	check.Status = CheckPassed
	check.Summary = "The kubeadm cluster configuration does not use kubeadm.k8s.io/v1beta3."
	parsed := 0
	parseErrors := 0
	for key, value := range configMap.Data {
		if key != "ClusterConfiguration" {
			continue
		}
		decoder := k8syaml.NewYAMLOrJSONDecoder(strings.NewReader(value), 4096)
		for document := 0; ; document++ {
			var object map[string]any
			err := decoder.Decode(&object)
			if err == io.EOF {
				break
			}
			if err != nil {
				parseErrors++
				break
			}
			if len(object) == 0 {
				continue
			}
			parsed++
			apiVersion, _, _ := unstructured.NestedString(object, "apiVersion")
			if apiVersion == "kubeadm.k8s.io/v1beta3" {
				check.Findings = append(check.Findings, Finding{RuleID: check.ID, Title: "kubeadm v1beta3 configuration is removed", Level: LevelBlocker, Resource: &ResourceRef{Kind: "ConfigMap", Namespace: configMap.Namespace, Name: configMap.Name}, Evidence: Evidence{Source: "live", Path: fmt.Sprintf("data.%s.document[%d].apiVersion", key, document), Detail: apiVersion}, AppliesFrom: check.AppliesFrom, Impact: "kubeadm 1.37 no longer accepts the v1beta3 configuration API, which can block upgrade operations using this configuration.", Remediation: "Use a supported pre-1.37 kubeadm binary to run kubeadm config migrate and store v1beta4 configuration before upgrading.", References: append([]Reference(nil), kubeadmV1Beta3References137...)})
			}
			featureGates, _, _ := unstructured.NestedMap(object, "featureGates")
			for name, value := range featureGates {
				if !removedKubeadmFeatureGates137[name] {
					continue
				}
				check.Findings = append(check.Findings, Finding{RuleID: check.ID, Title: name + " kubeadm feature gate is removed", Level: LevelBlocker, Resource: &ResourceRef{Kind: "ConfigMap", Namespace: configMap.Namespace, Name: configMap.Name}, Evidence: Evidence{Source: "live", Path: fmt.Sprintf("data.%s.document[%d].featureGates.%s", key, document, name), Detail: fmt.Sprint(value)}, AppliesFrom: check.AppliesFrom, Impact: "kubeadm 1.37 no longer recognizes this feature gate and can reject the configuration.", Remediation: "Remove " + name + " from kubeadm featureGates before upgrading.", References: append([]Reference(nil), kubeadmFeatureGateReferences137[name]...)})
			}
		}
	}
	if parseErrors > 0 || parsed == 0 {
		check.Caveat = "The kubeadm ConfigMap contained configuration data that Radar could not fully parse."
		if len(check.Findings) == 0 {
			check.Status, check.Summary = CheckUnknown, "The kubeadm ConfigMap was present but its configuration API could not be determined."
		}
	}
	if len(check.Findings) > 0 {
		check.Summary = fmt.Sprintf("%d removed kubeadm configuration %s must be fixed before upgrading.", len(check.Findings), plural(len(check.Findings), "setting", "settings"))
	}
	return check
}

func kubeSystemCovered(input *Input, kind string) bool {
	if input.Namespaces != nil && !containsString(input.Namespaces, "kube-system") {
		return false
	}
	if namespaces := input.CacheScopedKinds[kind]; len(namespaces) > 0 && !containsString(namespaces, "kube-system") {
		return false
	}
	return true
}

func scanKubeProxyModeTransition(input *Input) Check {
	check := Check{ID: "kube-proxy-mode-transition", Category: "Networking", Title: "kube-proxy mode transition", Status: CheckPassed, Summary: "Readable kube-proxy configuration uses an explicit supported mode.", Scope: "kube-proxy DaemonSet and mounted configuration", AppliesFrom: "1.37", References: append([]Reference(nil), kubeProxyModeReferences...)}
	if !kubeSystemCovered(input, "daemonsets") {
		check.Status, check.Summary = CheckUnknown, "kube-system is outside the readable DaemonSet scope, so kube-proxy mode could not be inspected."
		return check
	}
	if input.DaemonSets == nil {
		check.Status, check.Summary = CheckUnknown, "DaemonSets were unavailable, so kube-proxy mode could not be inspected."
		return check
	}
	found := 0
	unknown := 0
	for _, daemonSet := range input.DaemonSets {
		if daemonSet == nil || !isKubeProxy(daemonSet) {
			continue
		}
		found++
		check.Inspected++
		container := kubeProxyContainer(daemonSet)
		if container == nil {
			unknown++
			check.Caveat = appendCaveat(check.Caveat, fmt.Sprintf("%s/%s has multiple containers but none named kube-proxy.", daemonSet.Namespace, daemonSet.Name))
			continue
		}
		mode, sourcePath, known, detail := kubeProxyMode(input, daemonSet, container)
		if !known {
			unknown++
			check.Caveat = appendCaveat(check.Caveat, detail)
			continue
		}
		osName := strings.ToLower(daemonSet.Spec.Template.Spec.NodeSelector[corev1.LabelOSStable])
		if daemonSet.Spec.Template.Spec.OS != nil {
			osName = strings.ToLower(string(daemonSet.Spec.Template.Spec.OS.Name))
		}
		mode = strings.ToLower(strings.TrimSpace(mode))
		if mode == "" && osName == "windows" {
			mode = "kernelspace"
		}
		switch mode {
		case "iptables", "nftables", "kernelspace":
			continue
		case "ipvs":
			check.Findings = append(check.Findings, kubeProxyModeFinding(check, daemonSet, sourcePath, mode, "IPVS mode is deprecated", "IPVS is on a staged path to default-off in Kubernetes 1.40 and removal in 1.43.", "Plan and validate migration to nftables or iptables before the IPVS disablement timeline.", ipvsDeprecationReferences))
		case "":
			check.Findings = append(check.Findings, kubeProxyModeFinding(check, daemonSet, sourcePath, "unspecified", "Linux proxy mode is not explicit", "Kubernetes 1.37 warns because the implicit Linux default will change from iptables to nftables in a future release.", "Set mode explicitly to iptables or nftables after validating the selected backend.", nftablesDefaultReferences))
		default:
			unknown++
			check.Caveat = appendCaveat(check.Caveat, fmt.Sprintf("%s/%s uses unrecognized kube-proxy mode %q.", daemonSet.Namespace, daemonSet.Name, mode))
		}
	}
	if found == 0 {
		check.Status, check.Summary = CheckNotApplicable, "No kube-proxy DaemonSet was found; the provider may manage service proxying outside the cluster."
		return check
	}
	if unknown > 0 && len(check.Findings) == 0 {
		check.Status, check.Summary = CheckUnknown, "kube-proxy was found, but its effective mode could not be determined."
	} else if len(check.Findings) > 0 {
		check.Summary = fmt.Sprintf("%d kube-proxy mode %s require migration review.", len(check.Findings), plural(len(check.Findings), "setting", "settings"))
	}
	return check
}

func kubeProxyContainer(daemonSet *appsv1.DaemonSet) *corev1.Container {
	for i := range daemonSet.Spec.Template.Spec.Containers {
		container := &daemonSet.Spec.Template.Spec.Containers[i]
		if container.Name == "kube-proxy" {
			return container
		}
	}
	if len(daemonSet.Spec.Template.Spec.Containers) == 1 {
		return &daemonSet.Spec.Template.Spec.Containers[0]
	}
	return nil
}

func kubeProxyMode(input *Input, daemonSet *appsv1.DaemonSet, container *corev1.Container) (string, string, bool, string) {
	if configPath, ok := commandFlag(container.Command, container.Args, "--config"); ok {
		raw, evidencePath, err := mountedConfigMapFile(input.ConfigMaps, daemonSet, container, configPath)
		if err != nil {
			return "", "", false, fmt.Sprintf("%s/%s: %v", daemonSet.Namespace, daemonSet.Name, err)
		}
		var object map[string]any
		if err := k8syaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096).Decode(&object); err != nil {
			return "", "", false, fmt.Sprintf("%s/%s: kube-proxy config could not be parsed: %v", daemonSet.Namespace, daemonSet.Name, err)
		}
		mode, found, err := unstructured.NestedString(object, "mode")
		if err != nil {
			return "", "", false, fmt.Sprintf("%s/%s: kube-proxy mode has an invalid type", daemonSet.Namespace, daemonSet.Name)
		}
		if !found {
			return "", evidencePath + ".mode", true, ""
		}
		return mode, evidencePath + ".mode", true, ""
	}
	if mode, ok := commandFlag(container.Command, container.Args, "--proxy-mode"); ok {
		return mode, "spec.template.spec.containers[kube-proxy].args[--proxy-mode]", true, ""
	}
	return "", "spec.template.spec.containers[kube-proxy].args[--proxy-mode]", true, ""
}

func commandFlag(command, args []string, name string) (string, bool) {
	values := append(append([]string{}, command...), args...)
	for i := 0; i < len(values); i++ {
		if strings.HasPrefix(values[i], name+"=") {
			return strings.TrimPrefix(values[i], name+"="), true
		}
		if values[i] == name && i+1 < len(values) && !strings.HasPrefix(values[i+1], "--") {
			return values[i+1], true
		}
	}
	return "", false
}

func mountedConfigMapFile(configMaps []*corev1.ConfigMap, daemonSet *appsv1.DaemonSet, container *corev1.Container, filePath string) ([]byte, string, error) {
	if configMaps == nil {
		return nil, "", fmt.Errorf("ConfigMaps are unavailable for --config=%s", filePath)
	}
	for _, mount := range container.VolumeMounts {
		mountPath := strings.TrimSuffix(path.Clean(mount.MountPath), "/")
		cleanFile := path.Clean(filePath)
		if cleanFile != mountPath && !strings.HasPrefix(cleanFile, mountPath+"/") {
			continue
		}
		var volume *corev1.Volume
		for i := range daemonSet.Spec.Template.Spec.Volumes {
			if daemonSet.Spec.Template.Spec.Volumes[i].Name == mount.Name {
				volume = &daemonSet.Spec.Template.Spec.Volumes[i]
				break
			}
		}
		if volume == nil || volume.ConfigMap == nil {
			continue
		}
		key := mount.SubPath
		if key == "" {
			key = strings.TrimPrefix(cleanFile, mountPath+"/")
		}
		for _, item := range volume.ConfigMap.Items {
			if path.Clean(item.Path) == key {
				key = item.Key
				break
			}
		}
		for _, configMap := range configMaps {
			if configMap == nil || configMap.Namespace != daemonSet.Namespace || configMap.Name != volume.ConfigMap.Name {
				continue
			}
			if value, ok := configMap.Data[key]; ok {
				return []byte(value), fmt.Sprintf("ConfigMap/%s/%s.data.%s", configMap.Namespace, configMap.Name, key), nil
			}
			if value, ok := configMap.BinaryData[key]; ok {
				return value, fmt.Sprintf("ConfigMap/%s/%s.binaryData.%s", configMap.Namespace, configMap.Name, key), nil
			}
			return nil, "", fmt.Errorf("ConfigMap %s/%s does not contain key %q", configMap.Namespace, configMap.Name, key)
		}
		return nil, "", fmt.Errorf("ConfigMap %s/%s was not readable", daemonSet.Namespace, volume.ConfigMap.Name)
	}
	return nil, "", fmt.Errorf("--config=%s is not backed by a mounted ConfigMap", filePath)
}

func kubeProxyModeFinding(check Check, daemonSet *appsv1.DaemonSet, evidencePath, detail, title, impact, remediation string, references []Reference) Finding {
	return Finding{RuleID: check.ID, Title: title, Level: LevelReview, Resource: &ResourceRef{Group: "apps", Kind: "DaemonSet", Namespace: daemonSet.Namespace, Name: daemonSet.Name}, Evidence: Evidence{Source: "live", Path: evidencePath, Detail: detail}, AppliesFrom: check.AppliesFrom, Impact: impact, Remediation: remediation, References: append([]Reference(nil), references...)}
}

func scanRemovedControlPlaneMetrics(input *Input) Check {
	check := Check{ID: "removed-control-plane-metrics", Category: "Observability", Title: "Control-plane metric changes in Kubernetes 1.37", Status: CheckPassed, Summary: "No inspected PrometheusRule references a control-plane metric hidden or renamed in Kubernetes 1.37.", Scope: "Prometheus Operator rule expressions", AppliesFrom: "1.37", References: append([]Reference(nil), changelog137References...)}
	if !input.PrometheusRulesDiscoveryAvailable {
		check.Status, check.Summary = CheckUnknown, "API discovery is unavailable; Radar could not determine whether PrometheusRule is installed."
		return check
	}
	if !input.PrometheusRulesInstalled {
		check.Status, check.Summary = CheckNotApplicable, "PrometheusRule is not installed in this cluster."
		return check
	}
	if input.PrometheusRules == nil {
		check.Status, check.Summary = CheckUnknown, "PrometheusRule is installed but unavailable to Radar."
		return check
	}
	removed := map[string]string{
		"apiserver_cache_list_total":                  "apiserver_storage_list_*",
		"apiserver_cache_list_fetched_objects_total":  "apiserver_storage_list_*",
		"apiserver_cache_list_returned_objects_total": "apiserver_storage_list_*",
		"resourceclaim_controller_creates_total":      "dynamic_resource_allocation_resourceclaim_creates_total",
		"scheduler_resourceclaim_creates_total":       "dynamic_resource_allocation_resourceclaim_creates_total",
		"resourceclaim_controller_resource_claims":    "dynamic_resource_allocation_resource_claims",
	}
	check.Inspected = len(input.PrometheusRules)
	for _, rule := range input.PrometheusRules {
		if rule == nil {
			continue
		}
		expressions := prometheusRuleExpressions(rule.Object)
		for oldName, replacement := range removed {
			if !containsMetric(expressions, oldName) {
				continue
			}
			check.Findings = append(check.Findings, Finding{RuleID: check.ID, Title: oldName + " changes in Kubernetes 1.37", Level: LevelWarning, Resource: &ResourceRef{Group: "monitoring.coreos.com", Kind: "PrometheusRule", Namespace: rule.GetNamespace(), Name: rule.GetName()}, Evidence: Evidence{Source: "live", Path: "spec.groups[].rules[].expr", Detail: oldName}, AppliesFrom: check.AppliesFrom, Impact: "This alert or recording rule will stop receiving data by default when Kubernetes 1.37 hides or renames the metric.", Remediation: "Rewrite the expression using " + replacement + " before upgrading.", References: append([]Reference(nil), controlPlaneMetricReferences137[oldName]...)})
		}
	}
	if input.Namespaces != nil {
		check.Caveat = appendCaveat(check.Caveat, scopedCoverageNote(input.Namespaces, "PrometheusRules"))
	}
	if len(input.PrometheusRuleUnavailableNamespaces) > 0 {
		check.Caveat = appendCaveat(check.Caveat, "PrometheusRules could not be read in: "+formatBoundedList(input.PrometheusRuleUnavailableNamespaces, ", ")+".")
	}
	if len(check.Findings) > 0 {
		check.Summary = fmt.Sprintf("%d %s reference a control-plane metric hidden or renamed in Kubernetes 1.37.", len(check.Findings), plural(len(check.Findings), "PrometheusRule", "PrometheusRules"))
	} else if check.Caveat != "" {
		check.Status, check.Summary = CheckUnknown, "No removed metrics were found in readable PrometheusRules, but rule coverage is incomplete."
	}
	return check
}

type selinuxVolumeUse struct {
	pod             *corev1.Pod
	policy          corev1.PodSELinuxChangePolicy
	label           string
	recursiveReason string
}

func scanSELinuxMountTransition(input *Input) Check {
	check := Check{
		ID:          "selinux-mount-transition",
		Category:    "Storage",
		Title:       "SELinux volume labeling transition",
		Status:      CheckPassed,
		Summary:     "No current workload conflict was found for the Kubernetes 1.37 SELinux mount behavior.",
		Scope:       "Linux Pods, persistent volumes, CSI drivers, kubelet metrics, and Events",
		AppliesFrom: "1.37",
		References:  append([]Reference(nil), selinuxMountReferences...),
	}

	missingRuntime, runtimeEvidenceAvailable := scanSELinuxRuntimeEvidence(input, &check)
	usesByPV, eligibleUses, incomplete := selinuxPersistentVolumeUses(input, &check)
	check.Inspected = eligibleUses
	for pvName, uses := range usesByPV {
		scanSELinuxSharedVolume(&check, pvName, uses)
	}

	if input.Events == nil {
		check.Caveat = appendCaveat(check.Caveat, "Kubernetes Events were unavailable, so observed SELinux volume conflicts could not be inspected.")
	} else {
		appendSELinuxEventFindings(input.Events, &check)
	}
	if !runtimeEvidenceAvailable {
		check.Caveat = appendCaveat(check.Caveat, "Node evidence was unavailable, so kubelet SELinux conflict metrics could not be inspected.")
	} else if missingRuntime > 0 {
		check.Caveat = appendCaveat(check.Caveat, fmt.Sprintf("Kubelet SELinux conflict metrics were unavailable for %d %s.", missingRuntime, plural(missingRuntime, "Linux node", "Linux nodes")))
	}

	if input.Pods == nil {
		check.Caveat = appendCaveat(check.Caveat, "Pods were unavailable, so SELinux volume usage could not be inspected.")
		if len(check.Findings) == 0 {
			check.Status = CheckUnknown
			check.Summary = "No SELinux volume conflict was found in readable evidence, but Pod coverage is incomplete."
		}
	} else if incomplete {
		check.Caveat = appendCaveat(check.Caveat, "Persistent-volume or CSI-driver evidence was incomplete for at least one Linux Pod volume.")
		if len(check.Findings) == 0 {
			check.Status = CheckUnknown
			check.Summary = "No SELinux volume conflict was found in readable evidence, but storage coverage is incomplete."
		}
	} else if eligibleUses == 0 && len(check.Findings) == 0 {
		check.Status = CheckNotApplicable
		check.Summary = "No Linux Pod uses a persistent volume newly affected by the Kubernetes 1.37 SELinux mount behavior."
	}
	if len(check.Findings) > 0 {
		check.Summary = fmt.Sprintf("%d SELinux volume-labeling compatibility %s require attention before upgrading.", len(check.Findings), plural(len(check.Findings), "signal", "signals"))
	}
	return check
}

func scanSELinuxRuntimeEvidence(input *Input, check *Check) (int, bool) {
	if input.Nodes == nil {
		return 0, false
	}
	evidenceByNode := make(map[string]NodeRuntimeEvidence, len(input.NodeRuntimeEvidence))
	for _, evidence := range input.NodeRuntimeEvidence {
		evidenceByNode[evidence.NodeName] = evidence
	}
	missing := 0
	for _, node := range input.Nodes {
		if node == nil || strings.EqualFold(node.Status.NodeInfo.OperatingSystem, "windows") {
			continue
		}
		evidence, ok := evidenceByNode[node.Name]
		if !ok || !evidence.MetricsAvailable {
			missing++
			continue
		}
		if evidence.SELinuxMismatchErrors <= 0 && evidence.SELinuxMismatchWarnings <= 0 {
			continue
		}
		level := LevelWarning
		title := "Kubelet observed potential SELinux volume conflicts"
		impact := "Kubelet has observed volume contexts that can conflict when Kubernetes 1.37 uses SELinux mount options by default."
		if evidence.SELinuxMismatchErrors > 0 {
			title = "Kubelet observed SELinux volume conflicts"
			impact = "Kubelet has rejected conflicting SELinux volume contexts since it started; affected Pods can remain in ContainerCreating if the conflict is still present."
		}
		check.Findings = append(check.Findings, Finding{
			RuleID: check.ID, Title: title, Level: level,
			Resource:    &ResourceRef{Kind: "Node", Name: node.Name},
			Evidence:    Evidence{Source: "kubelet metrics", Path: "volume_manager_selinux_volume_context_mismatch_*_total", Detail: fmt.Sprintf("warnings=%g errors=%g", evidence.SELinuxMismatchWarnings, evidence.SELinuxMismatchErrors)},
			AppliesFrom: check.AppliesFrom,
			Impact:      impact,
			Remediation: "Inspect the kubelet SELinuxLabelConflict and SELinuxChangePolicyConflict events, then align shared-volume SELinux labels and seLinuxChangePolicy values.",
			References:  append([]Reference(nil), check.References...),
		})
	}
	return missing, true
}

func selinuxPersistentVolumeUses(input *Input, check *Check) (map[string][]selinuxVolumeUse, int, bool) {
	usesByPV := map[string][]selinuxVolumeUse{}
	if input.Pods == nil {
		return usesByPV, 0, true
	}
	pvcByName := make(map[string]*corev1.PersistentVolumeClaim, len(input.PersistentVolumeClaims))
	for _, pvc := range input.PersistentVolumeClaims {
		if pvc != nil {
			pvcByName[pvc.Namespace+"/"+pvc.Name] = pvc
		}
	}
	pvByName := make(map[string]*corev1.PersistentVolume, len(input.PersistentVolumes))
	for _, pv := range input.PersistentVolumes {
		if pv != nil {
			pvByName[pv.Name] = pv
		}
	}
	driverByName := make(map[string]*storagev1.CSIDriver, len(input.CSIDrivers))
	for _, driver := range input.CSIDrivers {
		if driver != nil {
			driverByName[driver.Name] = driver
		}
	}
	incomplete := false
	eligibleUses := 0
	for _, pod := range input.Pods {
		if pod == nil || pod.DeletionTimestamp != nil || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed || podIsWindows(input.Nodes, pod) {
			continue
		}
		for volumeIndex, volume := range pod.Spec.Volumes {
			claimName := ""
			switch {
			case volume.PersistentVolumeClaim != nil:
				claimName = volume.PersistentVolumeClaim.ClaimName
			case volume.Ephemeral != nil:
				claimName = storageephemeral.VolumeClaimName(pod, &volume)
			default:
				continue
			}
			if input.PersistentVolumeClaims == nil || input.PersistentVolumes == nil {
				incomplete = true
				continue
			}
			pvc := pvcByName[pod.Namespace+"/"+claimName]
			if pvc == nil || pvc.Spec.VolumeName == "" {
				incomplete = true
				continue
			}
			if volume.Ephemeral != nil && storageephemeral.VolumeIsForPod(pod, pvc) != nil {
				incomplete = true
				continue
			}
			pv := pvByName[pvc.Spec.VolumeName]
			if pv == nil {
				incomplete = true
				continue
			}
			newlyEligible, known := selinuxMountNewlyApplies(pv, pvc, driverByName, input.CSIDrivers != nil)
			if !known {
				incomplete = true
				continue
			}
			if !newlyEligible {
				continue
			}
			use, relevant, consistent := podSELinuxVolumeUse(pod, volume.Name)
			if !relevant {
				continue
			}
			eligibleUses++
			if !consistent {
				check.Findings = append(check.Findings, Finding{
					RuleID: check.ID, Title: "Pod uses multiple SELinux labels on one volume", Level: LevelWarning,
					Resource:    &ResourceRef{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name},
					Evidence:    Evidence{Source: "live", Path: fmt.Sprintf("spec.volumes[%d]", volumeIndex), Detail: "persistentVolume=" + pv.Name},
					AppliesFrom: check.AppliesFrom,
					Impact:      "Kubernetes cannot mount one volume with multiple SELinux contexts; the Pod can remain in ContainerCreating.",
					Remediation: "Use one effective SELinux label for every container that mounts this volume, or explicitly set seLinuxChangePolicy: Recursive.",
					References:  append([]Reference(nil), check.References...),
				})
				continue
			}
			use.pod = pod
			usesByPV[pv.Name] = append(usesByPV[pv.Name], use)
		}
	}
	return usesByPV, eligibleUses, incomplete
}

func podIsWindows(nodes []*corev1.Node, pod *corev1.Pod) bool {
	if pod.Spec.OS != nil && pod.Spec.OS.Name == corev1.Windows {
		return true
	}
	if pod.Spec.NodeName == "" {
		return false
	}
	for _, node := range nodes {
		if node != nil && node.Name == pod.Spec.NodeName {
			return strings.EqualFold(node.Status.NodeInfo.OperatingSystem, "windows")
		}
	}
	return false
}

func selinuxMountNewlyApplies(pv *corev1.PersistentVolume, pvc *corev1.PersistentVolumeClaim, drivers map[string]*storagev1.CSIDriver, driverEvidenceAvailable bool) (bool, bool) {
	accessModes := pvc.Spec.AccessModes
	if len(accessModes) == 0 {
		accessModes = pv.Spec.AccessModes
	}
	newAccessMode := false
	for _, accessMode := range accessModes {
		if accessMode != corev1.ReadWriteOncePod {
			newAccessMode = true
			break
		}
	}
	if !newAccessMode {
		return false, true
	}
	if pv.Spec.FC != nil || pv.Spec.ISCSI != nil {
		return true, true
	}
	if pv.Spec.CSI == nil {
		return false, true
	}
	if !driverEvidenceAvailable {
		return false, false
	}
	driver := drivers[pv.Spec.CSI.Driver]
	return driver != nil && driver.Spec.SELinuxMount != nil && *driver.Spec.SELinuxMount, true
}

func podSELinuxVolumeUse(pod *corev1.Pod, volumeName string) (selinuxVolumeUse, bool, bool) {
	policy := corev1.SELinuxChangePolicyMountOption
	if pod.Spec.SecurityContext != nil && pod.Spec.SecurityContext.SELinuxChangePolicy != nil {
		policy = *pod.Spec.SecurityContext.SELinuxChangePolicy
	}
	labels := map[string]bool{}
	mounted := 0
	labeled := 0
	privileged := false
	visit := func(container corev1.Container) {
		if !containerMountsVolume(container.VolumeMounts, volumeName) && !containerMountsVolumeDevice(container.VolumeDevices, volumeName) {
			return
		}
		mounted++
		if container.SecurityContext != nil && container.SecurityContext.Privileged != nil && *container.SecurityContext.Privileged {
			privileged = true
			return
		}
		options := (*corev1.SELinuxOptions)(nil)
		if pod.Spec.SecurityContext != nil {
			options = pod.Spec.SecurityContext.SELinuxOptions
		}
		if container.SecurityContext != nil && container.SecurityContext.SELinuxOptions != nil {
			options = container.SecurityContext.SELinuxOptions
		}
		if options != nil && options.Level != "" {
			labels[selinuxLabel(options)] = true
			labeled++
		}
	}
	for _, container := range pod.Spec.InitContainers {
		visit(container)
	}
	for _, container := range pod.Spec.Containers {
		visit(container)
	}
	for _, container := range pod.Spec.EphemeralContainers {
		visit(corev1.Container{Name: container.Name, SecurityContext: container.SecurityContext, VolumeMounts: container.VolumeMounts, VolumeDevices: container.VolumeDevices})
	}
	if mounted == 0 {
		return selinuxVolumeUse{}, false, true
	}
	if privileged {
		return selinuxVolumeUse{policy: corev1.SELinuxChangePolicyRecursive, recursiveReason: "a privileged container mounts the volume"}, true, true
	}
	if len(labels) == 0 || labeled != mounted {
		return selinuxVolumeUse{policy: corev1.SELinuxChangePolicyRecursive, recursiveReason: "a mounting container has no explicit SELinux level"}, true, true
	}
	label, labelsCompatible := compatibleSELinuxLabel(labels)
	if !labelsCompatible {
		return selinuxVolumeUse{}, true, false
	}
	return selinuxVolumeUse{policy: policy, label: label}, true, true
}

func containerMountsVolume(mounts []corev1.VolumeMount, volumeName string) bool {
	for _, mount := range mounts {
		if mount.Name == volumeName {
			return true
		}
	}
	return false
}

func containerMountsVolumeDevice(devices []corev1.VolumeDevice, volumeName string) bool {
	for _, device := range devices {
		if device.Name == volumeName {
			return true
		}
	}
	return false
}

func selinuxLabel(options *corev1.SELinuxOptions) string {
	return strings.Join([]string{options.User, options.Role, options.Type, options.Level}, ":")
}

func compatibleSELinuxLabel(labels map[string]bool) (string, bool) {
	parts := [4]string{}
	for label := range labels {
		candidate := strings.SplitN(label, ":", 4)
		if len(candidate) != 4 {
			return "", false
		}
		for i := range parts {
			if parts[i] == "" {
				parts[i] = candidate[i]
				continue
			}
			if candidate[i] != "" && candidate[i] != parts[i] {
				return "", false
			}
		}
	}
	if parts[3] == "" {
		return "", false
	}
	return strings.Join(parts[:], ":"), true
}

func scanSELinuxSharedVolume(check *Check, pvName string, uses []selinuxVolumeUse) {
	if len(uses) < 2 {
		return
	}
	policies := map[corev1.PodSELinuxChangePolicy]bool{}
	labels := map[string]bool{}
	pods := make([]string, 0, len(uses))
	recursiveFallbacks := []string{}
	for _, use := range uses {
		policies[use.policy] = true
		if use.policy == corev1.SELinuxChangePolicyMountOption {
			labels[use.label] = true
		}
		podName := use.pod.Namespace + "/" + use.pod.Name
		pods = append(pods, podName)
		if use.recursiveReason != "" {
			recursiveFallbacks = append(recursiveFallbacks, podName+" ("+use.recursiveReason+")")
		}
	}
	if len(policies) > 1 {
		if len(recursiveFallbacks) > 0 {
			check.Findings = append(check.Findings, selinuxSharedVolumeFinding(check, pvName, "Shared volume falls back to recursive SELinux relabeling", "effectivePolicies=MountOption,Recursive recursiveFallbacks="+formatBoundedList(recursiveFallbacks, ", ")+" pods="+formatBoundedList(pods, ", "), "At least one Pod cannot use mount-time labeling while another Pod requests it; kubelet can reject one of the mounts.", "Set an explicit SELinux level on every unprivileged container mounting the volume, or set seLinuxChangePolicy: Recursive on every Pod sharing it; privileged sharers require Recursive."))
			return
		}
		check.Findings = append(check.Findings, selinuxSharedVolumeFinding(check, pvName, "Shared volume uses conflicting SELinux change policies", "policies=MountOption,Recursive pods="+formatBoundedList(pods, ", "), "Pods sharing this volume use both recursive relabeling and mount-time labeling; kubelet can reject one of the mounts.", "Set the same seLinuxChangePolicy on every Pod sharing this volume; use Recursive when privileged and unprivileged Pods must share it."))
		return
	}
	_, labelsCompatible := compatibleSELinuxLabel(labels)
	if policies[corev1.SELinuxChangePolicyMountOption] && !labelsCompatible {
		check.Findings = append(check.Findings, selinuxSharedVolumeFinding(check, pvName, "Shared volume uses conflicting SELinux labels", "labels="+fmt.Sprintf("%d", len(labels))+" pods="+formatBoundedList(pods, ", "), "Kubernetes can mount a volume with only one SELinux context; Pods requesting different labels can remain in ContainerCreating.", "Align the effective SELinux label across every Pod sharing this volume, or explicitly set seLinuxChangePolicy: Recursive."))
	}
}

func selinuxSharedVolumeFinding(check *Check, pvName, title, detail, impact, remediation string) Finding {
	return Finding{
		RuleID: check.ID, Title: title, Level: LevelWarning,
		Resource:    &ResourceRef{Kind: "PersistentVolume", Name: pvName},
		Evidence:    Evidence{Source: "live", Path: "spec.claimRef", Detail: detail},
		AppliesFrom: check.AppliesFrom,
		Impact:      impact,
		Remediation: remediation,
		References:  append([]Reference(nil), check.References...),
	}
}

func appendSELinuxEventFindings(events []*corev1.Event, check *Check) {
	seen := map[string]bool{}
	for _, event := range events {
		if event == nil || (event.Reason != "SELinuxLabelConflict" && event.Reason != "SELinuxChangePolicyConflict" && event.Reason != "MultipleSELinuxLabels") {
			continue
		}
		ref := event.InvolvedObject
		key := event.Reason + "\x00" + ref.APIVersion + "\x00" + ref.Kind + "\x00" + ref.Namespace + "\x00" + ref.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		group := ""
		if apiGroup, _, ok := strings.Cut(ref.APIVersion, "/"); ok {
			group = apiGroup
		}
		check.Findings = append(check.Findings, Finding{
			RuleID: check.ID, Title: event.Reason, Level: LevelWarning,
			Resource:    &ResourceRef{Group: group, Kind: ref.Kind, Namespace: ref.Namespace, Name: ref.Name},
			Evidence:    Evidence{Source: "event", Path: "reason", Detail: event.Message},
			AppliesFrom: check.AppliesFrom,
			Impact:      "Kubelet reported an active SELinux volume-labeling conflict that can prevent the workload from starting.",
			Remediation: "Align SELinux labels and seLinuxChangePolicy values for every Pod sharing the affected volume.",
			References:  append([]Reference(nil), check.References...),
		})
	}
}
