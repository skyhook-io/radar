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
	csitranslation "k8s.io/csi-translation-lib"
	"k8s.io/klog/v2"
)

var removedFeatureGates137 = map[string]bool{
	"AnonymousAuthConfigurableEndpoints":    true,
	"APIServerTracing":                      true,
	"AnyVolumeDataSource":                   true,
	"AuthorizeNodeWithSelectors":            true,
	"AuthorizeWithSelectors":                true,
	"BtreeWatchCache":                       true,
	"ConsistentListFromCache":               true,
	"GangScheduling":                        true,
	"JobBackoffLimitPerIndex":               true,
	"JobPodReplacementPolicy":               true,
	"JobSuccessPolicy":                      true,
	"LogarithmicScaleDown":                  true,
	"OrderedNamespaceDeletion":              true,
	"PodLifecycleSleepAction":               true,
	"PodLifecycleSleepActionAllowZero":      true,
	"PreventStaticPodAPIReferences":         true,
	"RelaxedDNSSearchValidation":            true,
	"ResilientWatchCacheInitialization":     true,
	"RetryGenerateName":                     true,
	"SchedulerQueueingHints":                true,
	"SidecarContainers":                     true,
	"StreamingCollectionEncodingToJSON":     true,
	"StreamingCollectionEncodingToProtobuf": true,
	"StructuredAuthenticationConfiguration": true,
	"WorkloadAwarePreemption":               true,
}

var lockedFeatureGates137 = map[string]bool{
	"DeclarativeValidationTakeover":           false,
	"DisableCPUQuotaWithExclusiveCPUs":        true,
	"DRAExtendedResource":                     true,
	"DRAPrioritizedList":                      true,
	"DRAResourceClaimDeviceStatus":            true,
	"HostnameOverride":                        true,
	"HPAConfigurableTolerance":                true,
	"InPlacePodVerticalScalingInitContainers": true,
	"NodeDeclaredFeatures":                    true,
	"PLEGOnDemandRelist":                      true,
	"PodReadyToStartContainersCondition":      true,
	"RelaxedServiceNameValidation":            true,
	"WatchCacheInitializationPostStartHook":   true,
}

var removedKubeadmFeatureGates137 = map[string]bool{
	"NodeLocalCRISocket": true,
	"PublicKeysECDSA":    true,
}

var removedKubeletCAdvisorFlags137 = []string{
	"--application-metrics-count-limit",
	"--boot-id-file",
	"--container-hints",
	"--containerd",
	"--containerd-namespace",
	"--enable-load-reader",
	"--event-storage-age-limit",
	"--event-storage-event-limit",
	"--global-housekeeping-interval",
	"--log-cadvisor-usage",
	"--machine-id-file",
	"--storage-driver-user",
	"--storage-driver-password",
	"--storage-driver-host",
	"--storage-driver-db",
	"--storage-driver-table",
	"--storage-driver-secure",
	"--storage-driver-buffer-duration",
}

type commandLineToken struct {
	value string
	field string
}

type commandFlagSetting struct {
	value string
	field string
}

func scanRemovedFeatureGates(input *Input) Check {
	check := Check{ID: "removed-feature-gates", Category: "Component configuration", Title: "Feature gates removed or locked in Kubernetes 1.37", Status: CheckPassed, Summary: "No incompatible feature-gate settings were found in readable component configuration.", Scope: "Effective kubelet configuration and readable control-plane mirror Pods", AppliesFrom: "1.37", References: append([]Reference(nil), changelog137References...)}
	controlPlaneManaged := managedControlPlane(input)
	managedNodeFinding := false
	controlPlaneEvidenceUnavailable := false
	evidenceByNode := make(map[string]NodeRuntimeEvidence, len(input.NodeRuntimeEvidence))
	for _, evidence := range input.NodeRuntimeEvidence {
		evidenceByNode[evidence.NodeName] = evidence
	}
	missingNodes := 0
	inspectedNodes := 0
	if input.Nodes == nil {
		check.Status = CheckUnknown
		check.Summary = "Node evidence was unavailable; Radar could not inspect effective kubelet feature gates."
		check.Caveat = appendCaveat(check.Caveat, "Node evidence was unavailable, so kubelet feature gates could not be inspected.")
	} else {
		for _, node := range input.Nodes {
			if node == nil {
				continue
			}
			check.Inspected++
			inspectedNodes++
			evidence, ok := evidenceByNode[node.Name]
			if !ok || !evidence.ConfigAvailable {
				missingNodes++
				continue
			}
			for name, enabled := range evidence.FeatureGates {
				if removedFeatureGates137[name] {
					impact := "Kubelet 1.37 no longer recognizes this feature gate and can reject the configuration during startup."
					if name == "PreventStaticPodAPIReferences" && !enabled {
						impact = "Kubelet 1.37 removes this gate and the opt-out that allowed static Pods to reference API objects; the node configuration will be rejected and any dependent static Pod cannot start."
					}
					remediation := "Remove " + name + " from the kubelet feature-gates configuration before upgrading this node."
					if controlPlaneManaged {
						managedNodeFinding = true
						remediation = "If this setting comes from node-pool or bootstrap configuration, remove " + name + ". Otherwise, upgrade or replace the node through the provider-supported path and verify the target node image no longer emits it."
					}
					check.Findings = append(check.Findings, Finding{RuleID: check.ID, Title: name + " is removed", Level: LevelBlocker, Resource: &ResourceRef{Kind: "Node", Name: node.Name}, Evidence: Evidence{Source: "kubelet configz", Path: "kubeletconfig.featureGates." + name, Detail: fmt.Sprintf("%t", enabled)}, AppliesFrom: check.AppliesFrom, Impact: impact, Remediation: remediation, References: append([]Reference(nil), removedFeatureGateReferences137[name]...)})
					continue
				}
				defaultValue, locked := lockedFeatureGates137[name]
				if !locked || enabled == defaultValue {
					continue
				}
				remediation := fmt.Sprintf("Set %s=%t or remove the explicit setting before upgrading this node.", name, defaultValue)
				if controlPlaneManaged {
					managedNodeFinding = true
					remediation = fmt.Sprintf("If this setting comes from node-pool or bootstrap configuration, set %s=%t or remove it. Otherwise, upgrade or replace the node through the provider-supported path and verify the target node image uses the locked default.", name, defaultValue)
				}
				check.Findings = append(check.Findings, Finding{RuleID: check.ID, Title: name + " must use its locked default", Level: LevelBlocker, Resource: &ResourceRef{Kind: "Node", Name: node.Name}, Evidence: Evidence{Source: "kubelet configz", Path: "kubeletconfig.featureGates." + name, Detail: fmt.Sprintf("%t", enabled)}, AppliesFrom: check.AppliesFrom, Impact: fmt.Sprintf("Kubernetes 1.37 locks this kubelet feature gate to %t and rejects a different configured value during startup.", defaultValue), Remediation: remediation, References: append([]Reference(nil), lockedFeatureGateReferences137[name]...)})
			}
		}
	}
	if managedNodeFinding {
		check.EvidenceNote = appendCaveat(check.EvidenceNote, "On managed Kubernetes, configz does not reveal whether kubelet feature gates come from operator-controlled node settings or a provider-owned node image.")
	}
	foundControlPlaneComponents := map[string]bool{}
	if !kubeSystemCovered(input, "pods") {
		if controlPlaneManaged {
			check.EvidenceNote = appendCaveat(check.EvidenceNote, "The provider manages the control plane, so component feature gates are not exposed to Radar.")
		} else {
			controlPlaneEvidenceUnavailable = true
			check.Caveat = appendCaveat(check.Caveat, "kube-system is outside the readable Pod scope, so control-plane feature gates could not be inspected.")
		}
	} else if input.Pods == nil {
		if controlPlaneManaged {
			check.EvidenceNote = appendCaveat(check.EvidenceNote, "The provider manages the control plane, so component feature gates are not exposed to Radar.")
		} else {
			controlPlaneEvidenceUnavailable = true
			check.Caveat = appendCaveat(check.Caveat, "Pods were unavailable, so control-plane feature gates could not be inspected.")
		}
	} else {
		for _, pod := range input.Pods {
			if pod == nil || pod.Annotations[corev1.MirrorPodAnnotationKey] == "" {
				continue
			}
			for containerIndex, container := range pod.Spec.Containers {
				if !isControlPlaneContainer(container.Name) {
					continue
				}
				check.Inspected++
				foundControlPlaneComponents[container.Name] = true
				for name, setting := range parsedFeatureGates(container.Command, container.Args) {
					if removedFeatureGates137[name] {
						check.Findings = append(check.Findings, Finding{RuleID: check.ID, Title: name + " is removed", Level: LevelBlocker, Resource: &ResourceRef{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name}, Evidence: Evidence{Source: "live", Path: fmt.Sprintf("spec.containers[%d].%s[--feature-gates].%s", containerIndex, setting.field, name), Detail: setting.value}, AppliesFrom: check.AppliesFrom, Impact: "This Kubernetes 1.37 control-plane component no longer recognizes the configured feature gate and can fail during startup.", Remediation: "Remove " + name + " from the component feature-gates argument before upgrading the control plane.", References: append([]Reference(nil), removedFeatureGateReferences137[name]...)})
						continue
					}
					defaultValue, locked := lockedFeatureGates137[name]
					configuredValue, err := strconv.ParseBool(setting.value)
					if !locked || (err == nil && configuredValue == defaultValue) {
						continue
					}
					check.Findings = append(check.Findings, Finding{RuleID: check.ID, Title: name + " must use its locked default", Level: LevelBlocker, Resource: &ResourceRef{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name}, Evidence: Evidence{Source: "live", Path: fmt.Sprintf("spec.containers[%d].%s[--feature-gates].%s", containerIndex, setting.field, name), Detail: setting.value}, AppliesFrom: check.AppliesFrom, Impact: fmt.Sprintf("Kubernetes 1.37 locks this %s feature gate to %t and rejects a different configured value during startup.", container.Name, defaultValue), Remediation: fmt.Sprintf("Set %s=%t or remove the explicit setting before upgrading the control plane.", name, defaultValue), References: append([]Reference(nil), lockedFeatureGateReferences137[name]...)})
				}
			}
		}
		if len(foundControlPlaneComponents) == 0 {
			if controlPlaneManaged {
				check.EvidenceNote = appendCaveat(check.EvidenceNote, "No control-plane mirror Pod was readable; the provider manages the control plane, so component feature gates are not exposed to Radar.")
			} else {
				controlPlaneEvidenceUnavailable = true
				check.Caveat = appendCaveat(check.Caveat, "No control-plane mirror Pod was readable, so self-managed control-plane feature gates could not be inspected.")
			}
		} else {
			missingComponents := []string{}
			for _, component := range []string{"kube-apiserver", "kube-controller-manager", "kube-scheduler"} {
				if !foundControlPlaneComponents[component] {
					missingComponents = append(missingComponents, component)
				}
			}
			if len(missingComponents) > 0 {
				check.Caveat = appendCaveat(check.Caveat, "Control-plane feature gates could not be inspected for: "+strings.Join(missingComponents, ", ")+".")
			}
		}
	}
	if missingNodes > 0 {
		check.Caveat = appendCaveat(check.Caveat, nodeRuntimeCoverageCaveat(input, fmt.Sprintf("Effective kubelet configuration was unavailable for %d %s.", missingNodes, plural(missingNodes, "node", "nodes"))))
		if len(check.Findings) == 0 {
			check.Status = CheckUnknown
			check.Summary = "No removed feature gates were found in readable component configuration, but kubelet configuration coverage is incomplete."
		}
	}
	if controlPlaneEvidenceUnavailable && len(check.Findings) == 0 && inspectedNodes > 0 {
		check.Status = CheckUnknown
		check.Summary = "No incompatible feature gates were found in readable kubelet configuration, but control-plane coverage is unavailable."
	}
	if len(check.Findings) == 0 && check.Inspected == 0 {
		check.Status = CheckUnknown
		if input.Nodes != nil {
			check.Summary = "No kubelet or control-plane feature-gate configuration was available to inspect."
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

func parsedFeatureGates(command, args []string) map[string]commandFlagSetting {
	tokens := commandLineTokens(command, args)
	result := map[string]commandFlagSetting{}
	for i := 0; i < len(tokens); i++ {
		value := ""
		field := tokens[i].field
		switch {
		case strings.HasPrefix(tokens[i].value, "--feature-gates="):
			value = strings.TrimPrefix(tokens[i].value, "--feature-gates=")
		case tokens[i].value == "--feature-gates" && i+1 < len(tokens) && !strings.HasPrefix(tokens[i+1].value, "--"):
			i++
			value = tokens[i].value
		default:
			continue
		}
		for _, entry := range strings.Split(value, ",") {
			name, enabled, ok := strings.Cut(strings.TrimSpace(entry), "=")
			if ok && name != "" {
				result[name] = commandFlagSetting{value: enabled, field: field}
			}
		}
	}
	return result
}

func commandLineTokens(command, args []string) []commandLineToken {
	tokens := make([]commandLineToken, 0, len(command)+len(args))
	for _, value := range command {
		tokens = append(tokens, commandLineToken{value: value, field: "command"})
	}
	for _, value := range args {
		tokens = append(tokens, commandLineToken{value: value, field: "args"})
	}
	return tokens
}

func scanRemovedComponentFlags(input *Input) Check {
	references := append([]Reference(nil), componentFlagReferences137...)
	references = append(references, podGroupAdmissionReferences137...)
	check := Check{ID: "removed-component-flags", Category: "Component configuration", Title: "Component options removed in Kubernetes 1.37", Status: CheckPassed, Summary: "No readable control-plane mirror Pod uses a removed component flag or admission plugin.", Scope: "Readable control-plane mirror Pods", AppliesFrom: "1.37", References: references}
	controlPlaneManaged := managedControlPlane(input)
	if !kubeSystemCovered(input, "pods") {
		if controlPlaneManaged {
			check.Status, check.Summary = CheckNotApplicable, "The provider manages the control plane, so component startup options are not exposed to Radar."
			return check
		}
		check.Status, check.Summary = CheckUnknown, "kube-system is outside the readable Pod scope, so control-plane component options could not be inspected."
		return check
	}
	if input.Pods == nil {
		if controlPlaneManaged {
			check.Status, check.Summary = CheckNotApplicable, "The provider manages the control plane, so component startup options are not exposed to Radar."
			return check
		}
		check.Status, check.Summary = CheckUnknown, "Pods were unavailable, so control-plane component options could not be inspected."
		return check
	}
	foundControllerManager := false
	foundAPIServer := false
	for _, pod := range input.Pods {
		if pod == nil || pod.Annotations[corev1.MirrorPodAnnotationKey] == "" {
			continue
		}
		for containerIndex, container := range pod.Spec.Containers {
			switch container.Name {
			case "kube-controller-manager":
				foundControllerManager = true
				check.Inspected++
				if value, field, found := commandFlagPresence(container.Command, container.Args, "--concurrent-service-syncs"); found {
					check.Findings = append(check.Findings, Finding{RuleID: check.ID, Title: "--concurrent-service-syncs is removed", Level: LevelBlocker, Resource: &ResourceRef{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name}, Evidence: Evidence{Source: "live", Path: fmt.Sprintf("spec.containers[%d].%s[--concurrent-service-syncs]", containerIndex, field), Detail: value}, AppliesFrom: check.AppliesFrom, Impact: "kube-controller-manager 1.37 no longer recognizes this flag and can fail during startup.", Remediation: "Remove --concurrent-service-syncs from the kube-controller-manager arguments before upgrading the control plane.", References: append([]Reference(nil), componentFlagReferences137...)})
				}
			case "kube-apiserver":
				foundAPIServer = true
				check.Inspected++
				for _, flagName := range []string{"--enable-admission-plugins", "--disable-admission-plugins"} {
					for _, setting := range commandFlagSettings(container.Command, container.Args, flagName) {
						for _, plugin := range strings.Split(setting.value, ",") {
							if strings.TrimSpace(plugin) != "PodGroupWorkloadExists" {
								continue
							}
							check.Findings = append(check.Findings, Finding{RuleID: check.ID, Title: "PodGroupWorkloadExists admission plugin is removed", Level: LevelBlocker, Resource: &ResourceRef{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name}, Evidence: Evidence{Source: "live", Path: fmt.Sprintf("spec.containers[%d].%s[%s]", containerIndex, setting.field, flagName), Detail: "PodGroupWorkloadExists"}, AppliesFrom: check.AppliesFrom, Impact: "Kube-apiserver 1.37 rejects the removed admission plugin name and fails during startup.", Remediation: "Remove PodGroupWorkloadExists from " + flagName + " before upgrading the control plane.", References: append([]Reference(nil), podGroupAdmissionReferences137...)})
						}
					}
				}
			}
		}
	}
	if !foundControllerManager && !foundAPIServer && controlPlaneManaged {
		check.Status, check.Summary = CheckNotApplicable, "The provider manages the control plane, so component startup options are not exposed to Radar."
		return check
	}
	missingComponents := []string{}
	if !foundAPIServer {
		missingComponents = append(missingComponents, "kube-apiserver")
	}
	if !foundControllerManager {
		missingComponents = append(missingComponents, "kube-controller-manager")
	}
	if len(missingComponents) > 0 {
		check.Caveat = "Component startup options could not be inspected for: " + strings.Join(missingComponents, ", ") + "."
		if len(check.Findings) == 0 {
			check.Status, check.Summary = CheckUnknown, "No removed component option was found, but self-managed control-plane coverage is incomplete."
		}
	}
	if len(check.Findings) > 0 {
		check.Summary = fmt.Sprintf("%d removed component %s must be deleted before upgrading.", len(check.Findings), plural(len(check.Findings), "option", "options"))
	}
	return check
}

func commandFlagPresence(command, args []string, name string) (string, string, bool) {
	tokens := commandLineTokens(command, args)
	for i, token := range tokens {
		if strings.HasPrefix(token.value, name+"=") {
			return strings.TrimPrefix(token.value, name+"="), token.field, true
		}
		if token.value == name {
			if i+1 < len(tokens) && !strings.HasPrefix(tokens[i+1].value, "--") {
				return tokens[i+1].value, token.field, true
			}
			return "set", token.field, true
		}
	}
	return "", "", false
}

func commandFlagSettings(command, args []string, name string) []commandFlagSetting {
	tokens := commandLineTokens(command, args)
	settings := []commandFlagSetting{}
	for i := 0; i < len(tokens); i++ {
		if strings.HasPrefix(tokens[i].value, name+"=") {
			settings = append(settings, commandFlagSetting{value: strings.TrimPrefix(tokens[i].value, name+"="), field: tokens[i].field})
			continue
		}
		if tokens[i].value == name && i+1 < len(tokens) && !strings.HasPrefix(tokens[i+1].value, "--") {
			settings = append(settings, commandFlagSetting{value: tokens[i+1].value, field: tokens[i].field})
			i++
		}
	}
	return settings
}

func scanRemovedKubeletCAdvisorOptions() Check {
	flags := strings.Join(removedKubeletCAdvisorFlags137, ", ")
	check := Check{ID: "removed-kubelet-cadvisor-options", Category: "Node configuration", Title: "Kubelet cAdvisor options removed in Kubernetes 1.37", Status: CheckReview, Summary: "Kubelet startup arguments and direct cAdvisor metric consumers require manual review.", Scope: "Kubelet process arguments and direct cAdvisor API consumers", AppliesFrom: "1.37", References: append([]Reference(nil), cAdvisorRemovalReferences137...)}
	check.Findings = []Finding{{RuleID: check.ID, Title: "Kubelet cAdvisor settings are not observable through the Kubernetes API", Level: LevelReview, Evidence: Evidence{Source: "Kubernetes 1.37 release notes", Path: "kubelet process arguments and cAdvisor metric consumers", Detail: "manual verification required"}, AppliesFrom: check.AppliesFrom, Impact: "Kubelet 1.37 fails to start if any of 18 removed cAdvisor flags is configured. Direct consumers also lose userDefinedMetrics, container_application_* metrics, and three removed /metrics/cadvisor series.", Remediation: "Inspect every node's kubelet service and bootstrap arguments and remove these flags before upgrading: " + flags + ". --housekeeping-interval remains supported. Audit direct /stats/summary consumers for userDefinedMetrics and /metrics/cadvisor consumers for container_application_*, container_cpu_load_average_10s, container_cpu_load_d_average_10s, and container_tasks_state.", References: append([]Reference(nil), check.References...)}}
	return check
}

func scanKubeletEventQPS(input *Input) Check {
	check := Check{ID: "kubelet-event-qps-change", Category: "Node configuration", Title: "Kubelet event throttling behavior", Status: CheckPassed, Summary: "No readable kubelet config sets eventRecordQPS to zero.", Scope: "Effective kubelet configuration", AppliesFrom: "1.37", References: append([]Reference(nil), eventRecordQPSReferences137...)}
	if input.Nodes == nil {
		check.Status, check.Summary = CheckUnknown, "Effective kubelet configuration was unavailable."
		return check
	}
	if input.NodeRuntimeEvidence == nil {
		check.Status, check.Summary = CheckUnknown, "Effective kubelet configuration was unavailable."
		check.Caveat = nodeRuntimeCoverageCaveat(input, "eventRecordQPS could not be inspected.")
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
		check.Findings = append(check.Findings, Finding{RuleID: check.ID, Title: "eventRecordQPS zero becomes unlimited", Level: LevelWarning, Resource: &ResourceRef{Kind: "Node", Name: node.Name}, Evidence: Evidence{Source: "kubelet configz", Path: "kubeletconfig.eventRecordQPS", Detail: "0"}, AppliesFrom: check.AppliesFrom, Impact: "Kubernetes 1.37 makes an explicit zero mean unlimited event recording. In Kubernetes 1.36, that zero reached client-go and used its fallback limit of 5 events per second, so event traffic can increase sharply.", Remediation: "Choose an explicit limit before upgrading. The Kubernetes upgrade note recommends 50, the normal kubelet default. Use 5 only when intentionally matching the old explicit-zero runtime limit, or choose another value after reviewing event volume.", References: append([]Reference(nil), check.References...)})
	}
	if missing > 0 {
		unavailable := fmt.Sprintf("eventRecordQPS was unavailable for %d %s.", missing, plural(missing, "node", "nodes"))
		if !input.NodeProxyForbidden {
			unavailable += " Kubelets with debugging handlers disabled do not serve configz."
		}
		check.Caveat = nodeRuntimeCoverageCaveat(input, unavailable)
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
		check.Findings = append(check.Findings, Finding{RuleID: check.ID, Title: "scheduling.k8s.io/v1alpha2 " + object.GetKind(), Level: LevelBlocker, Resource: &ResourceRef{Group: "scheduling.k8s.io", Kind: object.GetKind(), Namespace: object.GetNamespace(), Name: object.GetName()}, Evidence: Evidence{Source: "live", Path: "apiVersion", Detail: "scheduling.k8s.io/v1alpha2"}, AppliesFrom: check.AppliesFrom, Impact: "Kubernetes 1.37 no longer serves this alpha API and requires its stored objects to be removed before upgrade.", Remediation: "Delete this v1alpha2 object before upgrading. After the control plane reaches Kubernetes 1.37, recreate it with scheduling.k8s.io/v1beta1.", References: append([]Reference(nil), check.References...)})
	}
	if input.Namespaces != nil {
		check.Caveat = appendCaveat(check.Caveat, scopedCoverageNote(input.Namespaces, "Workload and PodGroup objects"))
	}
	if len(input.SchedulingV1Alpha2UnavailableKinds) > 0 {
		check.Caveat = appendCaveat(check.Caveat, "Could not inspect: "+formatBoundedList(input.SchedulingV1Alpha2UnavailableKinds, ", ")+".")
	}
	if check.Caveat != "" && len(check.Findings) == 0 {
		check.Status, check.Summary = CheckUnknown, "No removed scheduling objects were found in readable kinds, but API coverage is incomplete."
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
	check.Inspected = 1
	check.Status = CheckPassed
	check.Summary = "The kubeadm cluster configuration does not use kubeadm.k8s.io/v1beta3."
	check.EvidenceNote = "Only the stored kubeadm-config ConfigMap was inspected; kubeadm configuration files passed with --config on control-plane hosts are not visible to Radar."
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
				check.Findings = append(check.Findings, Finding{RuleID: check.ID, Title: "kubeadm v1beta3 configuration is removed", Level: LevelBlocker, Resource: &ResourceRef{Kind: "ConfigMap", Namespace: configMap.Namespace, Name: configMap.Name}, Evidence: Evidence{Source: "live", Path: fmt.Sprintf("data.%s.document[%d].apiVersion", key, document), Detail: apiVersion}, AppliesFrom: check.AppliesFrom, Impact: "kubeadm 1.37 no longer accepts the v1beta3 configuration API, which can block upgrade operations using this configuration.", Remediation: "Use a supported pre-1.37 kubeadm binary to migrate the source file with kubeadm config migrate, then upload the migrated ClusterConfiguration with kubeadm init phase upload-config kubeadm --config <file> before upgrading.", References: append([]Reference(nil), kubeadmV1Beta3References137...)})
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
		check.Caveat = appendCaveat(check.Caveat, "The kubeadm ConfigMap contained configuration data that Radar could not fully parse.")
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
	check := Check{ID: "kube-proxy-mode-transition", Category: "Networking", Title: "kube-proxy mode transition", Status: CheckPassed, Summary: "Readable kube-proxy configuration uses an explicit supported mode.", Scope: "kube-proxy DaemonSet or mirror Pod configuration", AppliesFrom: "1.37", References: append([]Reference(nil), kubeProxyModeReferences...)}
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
		resource := &ResourceRef{Group: "apps", Kind: "DaemonSet", Namespace: daemonSet.Namespace, Name: daemonSet.Name}
		if !appendKubeProxyModeResult(&check, resource, sourcePath, mode, osName, managedControlPlane(input)) {
			unknown++
		}
	}
	if found == 0 {
		if !kubeSystemCovered(input, "pods") {
			check.Status, check.Summary = CheckUnknown, "No kube-proxy DaemonSet was found, and kube-system is outside the readable Pod scope."
			return check
		}
		if input.Pods == nil {
			check.Status, check.Summary = CheckUnknown, "No kube-proxy DaemonSet was found, and Pods were unavailable for static kube-proxy inspection."
			return check
		}
		mirrorFound, mirrorUnknown := scanKubeProxyMirrorPods(input, &check)
		found += mirrorFound
		unknown += mirrorUnknown
	}
	if found == 0 {
		check.Status, check.Summary = CheckUnknown, "No kube-proxy DaemonSet or mirror Pod was found; a host process or provider dataplane may be in use."
		return check
	}
	if unknown > 0 && len(check.Findings) == 0 {
		check.Status, check.Summary = CheckUnknown, "kube-proxy was found, but its effective mode could not be determined."
	} else if len(check.Findings) > 0 {
		check.Summary = fmt.Sprintf("%d kube-proxy mode %s migration review.", len(check.Findings), plural(len(check.Findings), "setting requires", "settings require"))
	}
	return check
}

func scanKubeProxyMirrorPods(input *Input, check *Check) (int, int) {
	found := 0
	unknown := 0
	for _, pod := range input.Pods {
		if pod == nil || pod.Namespace != "kube-system" || pod.Annotations[corev1.MirrorPodAnnotationKey] == "" {
			continue
		}
		for i := range pod.Spec.Containers {
			container := &pod.Spec.Containers[i]
			if container.Name != "kube-proxy" {
				continue
			}
			found++
			check.Inspected++
			resource := &ResourceRef{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name}
			if configPath, _, ok := commandFlag(container.Command, container.Args, "--config"); ok {
				unknown++
				check.Caveat = appendCaveat(check.Caveat, fmt.Sprintf("%s/%s: static kube-proxy --config=%s is not readable through the Kubernetes API.", pod.Namespace, pod.Name, configPath))
				break
			}
			mode, field, configured := commandFlag(container.Command, container.Args, "--proxy-mode")
			evidencePath := fmt.Sprintf("spec.containers[%s].command/args[--proxy-mode]", container.Name)
			if configured {
				evidencePath = fmt.Sprintf("spec.containers[%s].%s[--proxy-mode]", container.Name, field)
			}
			osName := strings.ToLower(pod.Spec.NodeSelector[corev1.LabelOSStable])
			if pod.Spec.OS != nil {
				osName = strings.ToLower(string(pod.Spec.OS.Name))
			}
			if !appendKubeProxyModeResult(check, resource, evidencePath, mode, osName, managedControlPlane(input)) {
				unknown++
			}
			break
		}
	}
	return found, unknown
}

func appendKubeProxyModeResult(check *Check, resource *ResourceRef, evidencePath, mode, osName string, managed bool) bool {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" && osName == "windows" {
		mode = "kernelspace"
	}
	switch mode {
	case "iptables", "nftables", "kernelspace":
		return true
	case "ipvs":
		remediation := "Plan and validate migration to nftables or iptables before the IPVS disablement timeline."
		if managed {
			remediation = "Use the provider-supported add-on or cluster configuration path to plan and validate migration to nftables or iptables before the IPVS disablement timeline; do not edit a reconciled kube-proxy workload directly."
		}
		check.Findings = append(check.Findings, kubeProxyModeFinding(*check, resource, evidencePath, mode, "IPVS mode is deprecated", "IPVS is on a staged path to default-off in Kubernetes 1.40 and removal in 1.43.", remediation, ipvsDeprecationReferences))
		return true
	case "":
		remediation := "Set mode explicitly to iptables or nftables after validating the selected backend."
		if managed {
			remediation = "Use the provider-supported add-on or cluster configuration path to select and validate an explicit iptables or nftables mode; do not edit a reconciled kube-proxy workload directly."
		}
		check.Findings = append(check.Findings, kubeProxyModeFinding(*check, resource, evidencePath, "unspecified", "Linux proxy mode is not explicit", "Kubernetes 1.37 warns because the implicit Linux default changes from iptables to nftables in Kubernetes 1.40.", remediation, nftablesDefaultReferences))
		return true
	default:
		check.Caveat = appendCaveat(check.Caveat, fmt.Sprintf("%s %s/%s uses unrecognized kube-proxy mode %q.", resource.Kind, resource.Namespace, resource.Name, mode))
		return false
	}
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
	if configPath, _, ok := commandFlag(container.Command, container.Args, "--config"); ok {
		if !kubeSystemCovered(input, "configmaps") {
			return "", "", false, fmt.Sprintf("%s/%s: kube-system is outside the readable ConfigMap scope, so --config=%s could not be inspected", daemonSet.Namespace, daemonSet.Name, configPath)
		}
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
	if mode, field, ok := commandFlag(container.Command, container.Args, "--proxy-mode"); ok {
		return mode, fmt.Sprintf("spec.template.spec.containers[%s].%s[--proxy-mode]", container.Name, field), true, ""
	}
	return "", fmt.Sprintf("spec.template.spec.containers[%s].command/args[--proxy-mode]", container.Name), true, ""
}

func commandFlag(command, args []string, name string) (string, string, bool) {
	tokens := commandLineTokens(command, args)
	for i := 0; i < len(tokens); i++ {
		if strings.HasPrefix(tokens[i].value, name+"=") {
			return strings.TrimPrefix(tokens[i].value, name+"="), tokens[i].field, true
		}
		if tokens[i].value == name && i+1 < len(tokens) && !strings.HasPrefix(tokens[i+1].value, "--") {
			return tokens[i+1].value, tokens[i].field, true
		}
	}
	return "", "", false
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

func kubeProxyModeFinding(check Check, resource *ResourceRef, evidencePath, detail, title, impact, remediation string, references []Reference) Finding {
	return Finding{RuleID: check.ID, Title: title, Level: LevelReview, Resource: resource, Evidence: Evidence{Source: "live", Path: evidencePath, Detail: detail}, AppliesFrom: check.AppliesFrom, Impact: impact, Remediation: remediation, References: append([]Reference(nil), references...)}
}

func scanRemovedControlPlaneMetrics(input *Input) Check {
	check := Check{ID: "removed-control-plane-metrics", Category: "Observability", Title: "Kubernetes metric changes in Kubernetes 1.37", Status: CheckPassed, Summary: "No inspected PrometheusRule references a metric renamed or removed in Kubernetes 1.37.", Scope: "Prometheus Operator rule expressions", AppliesFrom: "1.37", References: append([]Reference(nil), changelog137References...)}
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
	type metricChange struct {
		impact      string
		remediation string
	}
	changes := map[string]metricChange{
		"resourceclaim_controller_creates_total":   {impact: "This alert or recording rule will stop receiving data when Kubernetes 1.37 renames the metric.", remediation: "Rewrite the expression using dynamic_resource_allocation_resourceclaim_creates_total before upgrading."},
		"scheduler_resourceclaim_creates_total":    {impact: "This alert or recording rule will stop receiving data when Kubernetes 1.37 renames the metric.", remediation: "Rewrite the expression using dynamic_resource_allocation_resourceclaim_creates_total before upgrading."},
		"resourceclaim_controller_resource_claims": {impact: "This alert or recording rule will stop receiving data when Kubernetes 1.37 renames the metric.", remediation: "Rewrite the expression using dynamic_resource_allocation_resource_claims before upgrading."},
		"container_cpu_load_average_10s":           {impact: "This alert or recording rule will stop receiving data because Kubernetes 1.37 removes the cAdvisor metric.", remediation: "Remove or redesign the expression before upgrading; Kubernetes 1.37 provides no replacement for this metric."},
		"container_cpu_load_d_average_10s":         {impact: "This alert or recording rule will stop receiving data because Kubernetes 1.37 removes the cAdvisor metric.", remediation: "Remove or redesign the expression before upgrading; Kubernetes 1.37 provides no replacement for this metric."},
		"container_tasks_state":                    {impact: "This alert or recording rule will stop receiving data because Kubernetes 1.37 removes the cAdvisor metric.", remediation: "Remove or redesign the expression before upgrading; Kubernetes 1.37 provides no replacement for this metric."},
	}
	check.Inspected = len(input.PrometheusRules)
	for _, rule := range input.PrometheusRules {
		if rule == nil {
			continue
		}
		expressions := prometheusRuleExpressions(rule.Object)
		for oldName, change := range changes {
			if !containsMetric(expressions, oldName) {
				continue
			}
			check.Findings = append(check.Findings, Finding{RuleID: check.ID, Title: oldName + " changes in Kubernetes 1.37", Level: LevelWarning, Resource: &ResourceRef{Group: "monitoring.coreos.com", Kind: "PrometheusRule", Namespace: rule.GetNamespace(), Name: rule.GetName()}, Evidence: Evidence{Source: "live", Path: "spec.groups[].rules[].expr", Detail: oldName}, AppliesFrom: check.AppliesFrom, Impact: change.impact, Remediation: change.remediation, References: append([]Reference(nil), metricReferences137[oldName]...)})
		}
		for _, metricName := range metricsWithPrefix(expressions, "container_application_") {
			check.Findings = append(check.Findings, Finding{RuleID: check.ID, Title: metricName + " is removed in Kubernetes 1.37", Level: LevelWarning, Resource: &ResourceRef{Group: "monitoring.coreos.com", Kind: "PrometheusRule", Namespace: rule.GetNamespace(), Name: rule.GetName()}, Evidence: Evidence{Source: "live", Path: "spec.groups[].rules[].expr", Detail: metricName}, AppliesFrom: check.AppliesFrom, Impact: "This alert or recording rule will stop receiving data because Kubernetes 1.37 removes custom cAdvisor application metrics.", Remediation: "Remove or redesign the expression before upgrading; Kubernetes 1.37 no longer exports container_application_* metrics.", References: append([]Reference(nil), metricReferences137["container_application_*"]...)})
		}
	}
	if input.Namespaces != nil {
		check.Caveat = appendCaveat(check.Caveat, scopedCoverageNote(input.Namespaces, "PrometheusRules"))
	}
	if len(input.PrometheusRuleUnavailableNamespaces) > 0 {
		check.Caveat = appendCaveat(check.Caveat, "PrometheusRules could not be read in: "+formatBoundedList(input.PrometheusRuleUnavailableNamespaces, ", ")+".")
	}
	if len(check.Findings) > 0 {
		check.Summary = fmt.Sprintf("%d PrometheusRule metric %s must be updated for Kubernetes 1.37.", len(check.Findings), plural(len(check.Findings), "reference", "references"))
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

const selinuxMountOptOutRemediation = "For Kubernetes 1.37 only, setting the SELinuxMount feature gate to false on every kubelet defers this transition; the gate is expected to lock enabled in Kubernetes 1.38."

func scanSELinuxMountTransition(input *Input, targetAllowsOptOut bool) Check {
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
	if targetAllowsOptOut && allLinuxKubeletsDisableSELinuxMount(input) {
		check.Status = CheckNotApplicable
		check.Summary = "SELinuxMount is disabled on every readable Linux kubelet; this opt-out is valid for Kubernetes 1.37 only and is expected to disappear in 1.38."
		check.EvidenceNote = "Effective kubelet configz reported SELinuxMount=false on every readable Linux node."
		return check
	}

	missingRuntime, runtimeEvidenceAvailable := scanSELinuxRuntimeEvidence(input, &check, targetAllowsOptOut)
	runtimeFindingCount := len(check.Findings)
	usesByPV, eligibleUses, incomplete := selinuxPersistentVolumeUses(input, &check, targetAllowsOptOut)
	check.Inspected = eligibleUses
	for pvName, uses := range usesByPV {
		scanSELinuxSharedVolume(&check, pvName, uses, targetAllowsOptOut)
	}
	structuralConflictFound := len(check.Findings) > runtimeFindingCount
	if structuralConflictFound {
		check.Caveat = appendCaveat(check.Caveat, "Radar could not confirm whether the affected nodes enforce SELinux; structural findings do not apply where SELinux is disabled.")
	}

	if input.Events == nil {
		check.Caveat = appendCaveat(check.Caveat, "Kubernetes Events were unavailable, so observed SELinux volume conflicts could not be inspected.")
	} else {
		appendSELinuxEventFindings(input.Events, &check, targetAllowsOptOut)
	}
	if !runtimeEvidenceAvailable {
		check.Caveat = appendCaveat(check.Caveat, "Node evidence was unavailable, so kubelet SELinux conflict metrics could not be inspected.")
	} else if missingRuntime > 0 {
		check.Caveat = appendCaveat(check.Caveat, nodeRuntimeCoverageCaveat(input, fmt.Sprintf("Kubelet SELinux conflict metrics were unavailable for %d %s.", missingRuntime, plural(missingRuntime, "Linux node", "Linux nodes"))))
	}
	if input.Namespaces != nil {
		check.Caveat = appendCaveat(check.Caveat, scopedCoverageNote(input.Namespaces, "Pods, PersistentVolumeClaims and Events"))
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
	} else if input.Namespaces != nil && len(check.Findings) == 0 {
		check.Status = CheckUnknown
		check.Summary = "No SELinux volume conflict was found in the selected namespaces, but cluster-wide coverage is incomplete."
	} else if eligibleUses == 0 && len(check.Findings) == 0 {
		if len(input.CacheScopedKinds["pods"]) > 0 || len(input.CacheScopedKinds["persistentvolumeclaims"]) > 0 || len(input.CacheScopedKinds["events"]) > 0 {
			check.Status = CheckUnknown
			check.Summary = "No affected Linux Pod volume was found in readable cached evidence, but namespace coverage is incomplete."
		} else {
			check.Status = CheckNotApplicable
			check.Summary = "No Linux Pod uses a persistent volume newly affected by the Kubernetes 1.37 SELinux mount behavior."
		}
	}
	if len(check.Findings) > 0 {
		check.Summary = fmt.Sprintf("%d SELinux volume-labeling compatibility %s require attention before upgrading.", len(check.Findings), plural(len(check.Findings), "signal", "signals"))
	}
	return check
}

func allLinuxKubeletsDisableSELinuxMount(input *Input) bool {
	if input.Nodes == nil {
		return false
	}
	evidenceByNode := make(map[string]NodeRuntimeEvidence, len(input.NodeRuntimeEvidence))
	for _, evidence := range input.NodeRuntimeEvidence {
		evidenceByNode[evidence.NodeName] = evidence
	}
	foundLinuxNode := false
	for _, node := range input.Nodes {
		if node == nil || strings.EqualFold(node.Status.NodeInfo.OperatingSystem, "windows") {
			continue
		}
		foundLinuxNode = true
		evidence, ok := evidenceByNode[node.Name]
		if !ok || !evidence.ConfigAvailable {
			return false
		}
		enabled, configured := evidence.FeatureGates["SELinuxMount"]
		if !configured || enabled {
			return false
		}
	}
	return foundLinuxNode
}

func selinuxMountRemediation(action string, targetAllowsOptOut bool) string {
	if targetAllowsOptOut {
		return action + " " + selinuxMountOptOutRemediation
	}
	return action + " The SELinuxMount feature-gate opt-out is limited to a Kubernetes 1.37 target; resolve this conflict before upgrading to Kubernetes 1.38 or later."
}

func scanSELinuxRuntimeEvidence(input *Input, check *Check, targetAllowsOptOut bool) (int, bool) {
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
			Remediation: selinuxMountRemediation("Enable the selinux-warning-controller in kube-controller-manager and inspect selinux_warning_controller_selinux_volume_conflict to identify both Pods, then align shared-volume SELinux labels and seLinuxChangePolicy values.", targetAllowsOptOut),
			References:  append([]Reference(nil), check.References...),
		})
	}
	return missing, true
}

func selinuxPersistentVolumeUses(input *Input, check *Check, targetAllowsOptOut bool) (map[string][]selinuxVolumeUse, int, bool) {
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
					RuleID: check.ID, Title: "Pod uses multiple SELinux labels on one volume", Level: LevelReview,
					Resource:    &ResourceRef{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name},
					Evidence:    Evidence{Source: "live", Path: fmt.Sprintf("spec.volumes[%d]", volumeIndex), Detail: "persistentVolume=" + pv.Name},
					AppliesFrom: check.AppliesFrom,
					Impact:      "If the node enforces SELinux, Kubernetes cannot mount one volume with multiple SELinux contexts and the Pod can remain in ContainerCreating.",
					Remediation: selinuxMountRemediation("Use one effective SELinux label for every container that mounts this volume, or explicitly set seLinuxChangePolicy: Recursive.", targetAllowsOptOut),
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
	effectivePV := pv
	translator := csitranslation.New()
	if translator.IsPVMigratable(pv) {
		translated, err := translator.TranslateInTreePVToCSI(klog.Background(), pv.DeepCopy())
		if err != nil {
			return false, false
		}
		effectivePV = translated
	}
	if effectivePV.Spec.CSI == nil {
		return false, true
	}
	if !driverEvidenceAvailable {
		return false, false
	}
	driver := drivers[effectivePV.Spec.CSI.Driver]
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

func scanSELinuxSharedVolume(check *Check, pvName string, uses []selinuxVolumeUse, targetAllowsOptOut bool) {
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
			check.Findings = append(check.Findings, selinuxSharedVolumeFinding(check, pvName, "Shared volume falls back to recursive SELinux relabeling", "effectivePolicies=MountOption,Recursive recursiveFallbacks="+formatBoundedList(recursiveFallbacks, ", ")+" pods="+formatBoundedList(pods, ", "), "If these Pods run on the same SELinux-enforcing node, one Pod cannot use mount-time labeling while another Pod requests it and kubelet can reject one of the mounts.", "Set an explicit SELinux level on every unprivileged container mounting the volume, or set seLinuxChangePolicy: Recursive on every Pod sharing it; privileged sharers require Recursive.", targetAllowsOptOut))
			return
		}
		check.Findings = append(check.Findings, selinuxSharedVolumeFinding(check, pvName, "Shared volume uses conflicting SELinux change policies", "policies=MountOption,Recursive pods="+formatBoundedList(pods, ", "), "If these Pods run on the same SELinux-enforcing node, their incompatible relabeling modes can cause kubelet to reject one of the mounts.", "Set the same seLinuxChangePolicy on every Pod sharing this volume; use Recursive when privileged and unprivileged Pods must share it.", targetAllowsOptOut))
		return
	}
	_, labelsCompatible := compatibleSELinuxLabel(labels)
	if policies[corev1.SELinuxChangePolicyMountOption] && !labelsCompatible {
		check.Findings = append(check.Findings, selinuxSharedVolumeFinding(check, pvName, "Shared volume uses conflicting SELinux labels", "labels="+fmt.Sprintf("%d", len(labels))+" pods="+formatBoundedList(pods, ", "), "If these Pods run on the same SELinux-enforcing node, the volume can be mounted with only one context and Pods requesting different labels can remain in ContainerCreating.", "Align the effective SELinux label across every Pod sharing this volume, or explicitly set seLinuxChangePolicy: Recursive.", targetAllowsOptOut))
	}
}

func selinuxSharedVolumeFinding(check *Check, pvName, title, detail, impact, remediation string, targetAllowsOptOut bool) Finding {
	return Finding{
		RuleID: check.ID, Title: title, Level: LevelReview,
		Resource:    &ResourceRef{Kind: "PersistentVolume", Name: pvName},
		Evidence:    Evidence{Source: "live", Path: "spec.claimRef", Detail: detail},
		AppliesFrom: check.AppliesFrom,
		Impact:      impact,
		Remediation: selinuxMountRemediation(remediation, targetAllowsOptOut),
		References:  append([]Reference(nil), check.References...),
	}
}

func appendSELinuxEventFindings(events []*corev1.Event, check *Check, targetAllowsOptOut bool) {
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
		impact := "The selinux-warning-controller reported a potential volume-labeling conflict that can prevent the workload from starting if the affected Pods land on the same node."
		remediation := "Inspect selinux_warning_controller_selinux_volume_conflict to identify both Pods, then align SELinux labels and seLinuxChangePolicy values for every Pod sharing the affected volume."
		if event.Reason == "MultipleSELinuxLabels" {
			impact = "The selinux-warning-controller reported that this Pod mounts one volume more than once with different SELinux labels; the conflict can prevent the workload from starting."
			remediation = "Inspect this Pod's pod- and container-level securityContext.seLinuxOptions values, then align the SELinux labels for every mount of the affected volume inside the Pod."
		}
		check.Findings = append(check.Findings, Finding{
			RuleID: check.ID, Title: event.Reason, Level: LevelWarning,
			Resource:    &ResourceRef{Group: group, Kind: ref.Kind, Namespace: ref.Namespace, Name: ref.Name},
			Evidence:    Evidence{Source: "event", Path: "reason", Detail: event.Message},
			AppliesFrom: check.AppliesFrom,
			Impact:      impact,
			Remediation: selinuxMountRemediation(remediation, targetAllowsOptOut),
			References:  append([]Reference(nil), check.References...),
		})
	}
}
