package upgradereadiness

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	utilversion "k8s.io/apimachinery/pkg/util/version"

	"github.com/skyhook-io/radar/pkg/conditions"
	"github.com/skyhook-io/radar/pkg/k8score"
)

func scanNodeCgroupCompatibility(input *Input) Check {
	check := Check{ID: "node-cgroup-v1", Category: "Node compatibility", Title: "Node cgroup compatibility", Status: CheckPassed, Summary: "All inspected nodes use cgroup v2.", Scope: "Kubelet cgroup version metric", AppliesFrom: "1.35", References: append([]Reference(nil), cgroupReferences...)}
	if len(input.Nodes) == 0 {
		check.Status, check.Summary = CheckUnknown, "Nodes were unavailable; Radar could not inspect cgroup compatibility."
		return check
	}
	nonNilNodes, linuxNodes := nodeOSCounts(input.Nodes)
	if nonNilNodes == 0 {
		check.Status, check.Summary = CheckUnknown, "Nodes were unavailable; Radar could not inspect cgroup compatibility."
		return check
	}
	if linuxNodes == 0 {
		check.Status, check.Summary = CheckNotApplicable, "Cgroup v1 compatibility does not apply to Windows nodes."
		return check
	}
	if input.NodeRuntimeEvidence == nil {
		check.Status, check.Summary = CheckUnknown, "Kubelet cgroup version metrics were unavailable."
		return check
	}
	metrics := make(map[string]NodeRuntimeEvidence, len(input.NodeRuntimeEvidence))
	for _, evidence := range input.NodeRuntimeEvidence {
		metrics[evidence.NodeName] = evidence
	}
	missing := 0
	for _, node := range input.Nodes {
		if node == nil || strings.EqualFold(node.Status.NodeInfo.OperatingSystem, "windows") {
			continue
		}
		check.Inspected++
		evidence, ok := metrics[node.Name]
		if !ok || !evidence.CgroupVersionAvailable {
			missing++
			continue
		}
		if evidence.CgroupVersion == 1 {
			check.Findings = append(check.Findings, Finding{RuleID: check.ID, Title: "Node still uses cgroup v1", Level: LevelBlocker, Resource: &ResourceRef{Kind: "Node", Name: node.Name}, Evidence: Evidence{Source: "kubelet metrics", Path: "kubelet_cgroup_version", Detail: "1"}, AppliesFrom: check.AppliesFrom, Impact: "Kubernetes 1.35 defaults the kubelet to fail on cgroup v1; continuing requires an explicit temporary override and remains a migration risk.", Remediation: "Migrate the node operating system and runtime to cgroup v2, then replace or restart the node and re-run this check.", References: append([]Reference(nil), check.References...)})
		} else if evidence.CgroupVersion != 2 {
			missing++
		}
	}
	if missing > 0 {
		check.Caveat = fmt.Sprintf("The cgroup metric was unavailable on %d %s.", missing, plural(missing, "node", "nodes"))
		if len(check.Findings) == 0 {
			check.Status, check.Summary = CheckUnknown, "No cgroup v1 nodes were observed, but node coverage is incomplete."
		}
	}
	if len(check.Findings) > 0 {
		check.Summary = fmt.Sprintf("%d %s still %s cgroup v1.", len(check.Findings), plural(len(check.Findings), "node", "nodes"), plural(len(check.Findings), "uses", "use"))
	}
	return check
}

func nodeOSCounts(nodes []*corev1.Node) (nonNil, linux int) {
	for _, node := range nodes {
		if node == nil {
			continue
		}
		nonNil++
		if !strings.EqualFold(node.Status.NodeInfo.OperatingSystem, "windows") {
			linux++
		}
	}
	return nonNil, linux
}

func scanContainerRuntimeSupport(input *Input, target *utilversion.Version) Check {
	check := Check{ID: "container-runtime-support", Category: "Node compatibility", Title: "Container runtime support", Status: CheckPassed, Summary: "All inspected node runtimes are supported by the target version.", Scope: "Kubelet CRI support metric and Node status", AppliesFrom: "1.36", References: append([]Reference(nil), runtimeReferences...)}
	if len(input.Nodes) == 0 {
		check.Status, check.Summary = CheckUnknown, "Nodes were unavailable; Radar could not inspect container runtime versions."
		return check
	}
	nonNilNodes, linuxNodes := nodeOSCounts(input.Nodes)
	if nonNilNodes == 0 {
		check.Status, check.Summary = CheckUnknown, "Nodes were unavailable; Radar could not inspect container runtime versions."
		return check
	}
	if linuxNodes == 0 {
		check.Status, check.Summary = CheckNotApplicable, "Container runtime support for this upgrade does not apply to Windows nodes."
		return check
	}
	metrics := make(map[string]NodeRuntimeEvidence, len(input.NodeRuntimeEvidence))
	for _, evidence := range input.NodeRuntimeEvidence {
		metrics[evidence.NodeName] = evidence
	}
	check.Inspected = 0
	unknown := 0
	metricAvailableFrom := utilversion.MustParseGeneric("1.35")
	for _, node := range input.Nodes {
		if node == nil {
			continue
		}
		if strings.EqualFold(node.Status.NodeInfo.OperatingSystem, "windows") {
			continue
		}
		check.Inspected++
		evidence, hasMetric := metrics[node.Name]
		if !hasMetric || !evidence.MetricsAvailable {
			unknown++
			continue
		}
		if !evidence.CRILosingSupportAvailable {
			kubeletVersion, err := parseMinor(node.Status.NodeInfo.KubeletVersion)
			if err != nil || kubeletVersion.LessThan(metricAvailableFrom) {
				unknown++
			}
			continue
		}
		losing, err := parseMinor(evidence.CRILosingSupportVersion)
		if err != nil {
			unknown++
			continue
		}
		blocked := target.AtLeast(losing)
		if blocked {
			check.Findings = append(check.Findings, Finding{RuleID: check.ID, Title: "Container runtime configuration loses support", Level: LevelBlocker, Resource: &ResourceRef{Kind: "Node", Name: node.Name}, Evidence: Evidence{Source: "kubelet metrics", Path: "kubelet_cri_losing_support", Detail: evidence.CRILosingSupportVersion}, AppliesFrom: minorString(losing), Impact: "The kubelet reports that this node's current CRI implementation loses support by the target Kubernetes version.", Remediation: "Upgrade or reconfigure the container runtime to a target-supported CRI implementation, replace the node, and re-run this check.", References: append([]Reference(nil), check.References...)})
			continue
		}
	}
	if unknown > 0 {
		check.Caveat = fmt.Sprintf("Runtime support could not be proven for %d %s.", unknown, plural(unknown, "node", "nodes"))
		if len(check.Findings) == 0 {
			check.Status, check.Summary = CheckUnknown, "No unsupported runtimes were identified, but runtime evidence is incomplete."
		}
	}
	if len(check.Findings) > 0 {
		check.Summary = fmt.Sprintf("%d %s reports an unsupported container runtime configuration.", len(check.Findings), plural(len(check.Findings), "node", "nodes"))
	}
	return check
}

func scanNodeDrainFeasibility(input *Input) Check {
	check := Check{ID: "node-drain-feasibility", Category: "Upgrade operations", Title: "Node drain feasibility", Status: CheckPassed, Summary: "No inspected workload requires exceptional drain flags or is blocked by a PodDisruptionBudget.", Scope: "Live pods and PodDisruptionBudget configuration and status", References: append([]Reference(nil), drainReferences...)}
	if input.Namespaces != nil {
		check.Caveat = scopedCoverageNote(input.Namespaces, "Pods and PodDisruptionBudgets")
		check.Summary = "No drain risk was found in the selected namespace scope."
	}
	if input.Pods == nil || input.PodDisruptionBudgets == nil {
		check.Status, check.Summary = CheckUnknown, "Pods or PodDisruptionBudgets were unavailable; drain feasibility could not be established."
		return check
	}
	active := make([]*corev1.Pod, 0, len(input.Pods))
	for _, pod := range input.Pods {
		if drainRelevantPod(pod) {
			active = append(active, pod)
		}
	}
	check.Inspected = len(active)
	type pdbSelection struct {
		pdb  *policyv1.PodDisruptionBudget
		pods []*corev1.Pod
	}
	selections := make([]pdbSelection, 0, len(input.PodDisruptionBudgets))
	stale := 0
	for _, pdb := range input.PodDisruptionBudgets {
		// A deleting PDB can briefly keep blocking eviction until removal, but
		// reporting it here would turn a transient teardown into standing debt.
		if pdb == nil || pdb.DeletionTimestamp != nil {
			continue
		}
		selector, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
		if err != nil {
			stale++
			continue
		}
		selectedPods := make([]*corev1.Pod, 0)
		healthy := 0
		for _, pod := range active {
			if pod.Namespace == pdb.Namespace && selector.Matches(labels.Set(pod.Labels)) {
				selectedPods = append(selectedPods, pod)
				if podIsReady(pod) {
					healthy++
				}
			}
		}
		if len(selectedPods) == 0 {
			continue
		}
		// Multi-PDB eviction rejection is selector-based, so even a PDB with
		// stale controller status must participate in overlap detection.
		selections = append(selections, pdbSelection{pdb: pdb, pods: selectedPods})
		state := k8score.PodDisruptionBudgetEvictionState(pdb)
		if state == k8score.PDBEvictionUnknown {
			stale++
			continue
		}
		alwaysAllow := pdb.Spec.UnhealthyPodEvictionPolicy != nil && *pdb.Spec.UnhealthyPodEvictionPolicy == "AlwaysAllow"
		if state != k8score.PDBEvictionBlocked && (pdb.Status.DisruptionsAllowed != 0 || (alwaysAllow && healthy == 0)) {
			continue
		}
		check.Findings = append(check.Findings, Finding{RuleID: check.ID, Title: "PodDisruptionBudget allows no evictions", Level: LevelBlocker, Resource: &ResourceRef{Group: "policy", Kind: "PodDisruptionBudget", Namespace: pdb.Namespace, Name: pdb.Name}, Evidence: Evidence{Source: "live", Path: "status.disruptionsAllowed", Detail: fmt.Sprintf("0 across %d active pod(s)", len(selectedPods))}, Impact: "A default eviction-based node drain is blocked while this authoritative PDB status allows zero disruptions.", Remediation: "Restore workload headroom, scale replicas, or adjust minAvailable/maxUnavailable so at least one disruption is allowed before draining nodes.", References: append([]Reference(nil), check.References...)})
	}
	sort.Slice(selections, func(i, j int) bool {
		if selections[i].pdb.Namespace != selections[j].pdb.Namespace {
			return selections[i].pdb.Namespace < selections[j].pdb.Namespace
		}
		return selections[i].pdb.Name < selections[j].pdb.Name
	})
	for i := 0; i < len(selections); i++ {
		selected := make(map[string]bool, len(selections[i].pods))
		for _, pod := range selections[i].pods {
			selected[pod.Namespace+"/"+pod.Name] = true
		}
		for j := i + 1; j < len(selections); j++ {
			if selections[i].pdb.Namespace != selections[j].pdb.Namespace {
				continue
			}
			overlap := make([]string, 0)
			for _, pod := range selections[j].pods {
				key := pod.Namespace + "/" + pod.Name
				if selected[key] {
					overlap = append(overlap, key)
				}
			}
			if len(overlap) == 0 {
				continue
			}
			sort.Strings(overlap)
			first, second := selections[i].pdb, selections[j].pdb
			check.Findings = append(check.Findings, Finding{
				RuleID:      check.ID,
				Title:       "Pod is selected by multiple PodDisruptionBudgets",
				Level:       LevelBlocker,
				Resource:    &ResourceRef{Group: "policy", Kind: "PodDisruptionBudget", Namespace: first.Namespace, Name: first.Name},
				Evidence:    Evidence{Source: "live", Path: "spec.selector", Detail: fmt.Sprintf("also overlaps %s/%s across %d active pod(s), including Pod %s", second.Namespace, second.Name, len(overlap), overlap[0])},
				Impact:      "The Kubernetes Eviction API rejects eviction of a pod selected by multiple PodDisruptionBudgets, so a default node drain cannot proceed.",
				Remediation: "Narrow or remove one selector so each pod is covered by only one PodDisruptionBudget, or finish the intentional budget transition before draining nodes.",
				References:  append([]Reference(nil), check.References...),
			})
		}
	}
	for _, pod := range active {
		owner := controllerOwner(pod.OwnerReferences)
		if owner == nil {
			check.Findings = append(check.Findings, drainPodFinding(check, pod, LevelWarning, "Bare pod requires forced drain", "metadata.ownerReferences", "no controller owner", "kubectl drain requires --force to remove this unmanaged pod; it will not be recreated automatically.", "Replace the bare pod with a controller-managed workload or explicitly plan and approve a forced drain."))
		}
		emptyDirs := make([]string, 0)
		for _, volume := range pod.Spec.Volumes {
			if volume.EmptyDir != nil {
				emptyDirs = append(emptyDirs, volume.Name)
			}
		}
		if len(emptyDirs) > 0 {
			sort.Strings(emptyDirs)
			check.Findings = append(check.Findings, drainPodFinding(check, pod, LevelReview, "Pod uses node-local emptyDir data", "spec.volumes[].emptyDir", strings.Join(emptyDirs, ", "), "kubectl drain requires --delete-emptydir-data and this local data will be lost.", "Confirm the data is disposable or move it to persistent storage before draining the node."))
		}
	}
	if stale > 0 {
		check.Caveat = appendCaveat(check.Caveat, fmt.Sprintf("%d PodDisruptionBudget %s stale or unreadable status.", stale, plural(stale, "has", "have")))
		if len(check.Findings) == 0 {
			check.Summary = "No drain blockers were found, but PDB evidence is incomplete."
		}
	}
	if len(check.Findings) > 0 {
		check.Summary = fmt.Sprintf("%d drain %s.", len(check.Findings), plural(len(check.Findings), "condition requires action or explicit operator review", "conditions require action or explicit operator review"))
	} else if check.Caveat != "" {
		check.Status = CheckUnknown
		if input.Namespaces != nil {
			check.Summary = "No drain risk was found in the selected namespace scope, but Pod and PodDisruptionBudget coverage is incomplete."
		} else {
			check.Summary = "No drain risk was found in readable evidence, but coverage is incomplete."
		}
	}
	return check
}

func scanAPIServiceReadiness(input *Input) Check {
	check := Check{ID: "aggregated-apiservice-readiness", Category: "API extensions", Title: "Aggregated APIService readiness", Status: CheckPassed, Summary: "All inspected delegated APIServices report Available.", Scope: "Delegated APIService availability conditions", References: append([]Reference(nil), apiServiceReferences...)}
	if input.APIServices == nil {
		check.Status, check.Summary = CheckUnknown, "APIService availability could not be inspected."
		return check
	}
	for _, apiService := range input.APIServices {
		if apiService == nil {
			continue
		}
		if _, delegated, _ := unstructured.NestedMap(apiService.Object, "spec", "service"); !delegated {
			continue
		}
		check.Inspected++
		state, found := conditions.Find(apiService, "Available")
		if !found || (state.Status != "True" && state.Status != "False" && state.Status != "Unknown") {
			check.Caveat = appendCaveat(check.Caveat, apiService.GetName()+" has no readable Available condition.")
			continue
		}
		if state.Status == "True" {
			continue
		}
		check.Findings = append(check.Findings, Finding{
			RuleID:      check.ID,
			Title:       "Aggregated APIService is unavailable",
			Level:       LevelWarning,
			Resource:    &ResourceRef{Group: "apiregistration.k8s.io", Kind: "APIService", Name: apiService.GetName()},
			Evidence:    Evidence{Source: "live", Path: "status.conditions[type=Available]", Detail: apiServiceConditionDetail(state)},
			Impact:      "The kube-apiserver aggregation layer cannot currently serve this delegated API, which can disrupt discovery and clients that depend on it during control-plane operations.",
			Remediation: "Restore the backing Service and endpoints, then resolve API aggregation or TLS errors until the APIService reports Available=True.",
			References:  append([]Reference(nil), check.References...),
		})
	}
	if check.Inspected == 0 {
		check.Status, check.Summary = CheckNotApplicable, "No delegated APIService is registered."
	} else {
		if check.Caveat != "" {
			check.Status = CheckUnknown
		}
		if len(check.Findings) > 0 {
			check.Summary = fmt.Sprintf("%d delegated %s unavailable.", len(check.Findings), plural(len(check.Findings), "APIService is", "APIServices are"))
		} else if check.Caveat != "" {
			check.Summary = "No unavailable delegated APIs were identified, but APIService evidence is incomplete."
		}
	}
	return check
}

func apiServiceConditionDetail(state conditions.State) string {
	detail := state.Status
	if state.Reason != "" {
		detail += " (" + state.Reason + ")"
	}
	message := strings.Join(strings.Fields(state.Message), " ")
	if message != "" {
		const maxRunes = 180
		runes := []rune(message)
		if len(runes) > maxRunes {
			message = string(runes[:maxRunes-1]) + "…"
		}
		detail += ": " + message
	}
	return detail
}

func podIsReady(pod *corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func drainRelevantPod(pod *corev1.Pod) bool {
	if pod == nil || pod.DeletionTimestamp != nil || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		return false
	}
	if pod.Annotations[corev1.MirrorPodAnnotationKey] != "" {
		return false
	}
	owner := controllerOwner(pod.OwnerReferences)
	return owner == nil || owner.Kind != "DaemonSet"
}

func drainPodFinding(check Check, pod *corev1.Pod, level Level, title, path, detail, impact, remediation string) Finding {
	return Finding{RuleID: check.ID, Title: title, Level: level, Resource: &ResourceRef{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name}, Evidence: Evidence{Source: "live", Path: path, Detail: detail}, Impact: impact, Remediation: remediation, References: append([]Reference(nil), check.References...)}
}

func scanAdmissionWebhookReadiness(input *Input) Check {
	check := Check{ID: "admission-webhook-readiness", Category: "Admission control", Title: "Admission webhook readiness", Status: CheckPassed, Summary: "Inspected admission webhooks have reachable Service backends and upgrade-safe matching behavior.", Scope: "Admission webhook configurations, Services, and EndpointSlices", References: append([]Reference(nil), admissionReferences...)}
	if input.AdmissionWebhookConfigurations == nil {
		check.Status, check.Summary = CheckUnknown, "Admission webhook configuration evidence was unavailable."
		return check
	}
	if len(input.AdmissionWebhookUnavailableKinds) > 0 {
		check.Caveat = appendCaveat(check.Caveat, "Admission webhook configurations could not be inspected for: "+strings.Join(input.AdmissionWebhookUnavailableKinds, ", ")+".")
	}
	check.Inspected = len(input.AdmissionWebhookConfigurations)
	backendEvidenceAvailable := input.WebhookServices != nil && input.EndpointSlices != nil
	backendEvidenceCaveatAdded := false
	services := map[string]bool{}
	for _, svc := range input.WebhookServices {
		if svc != nil {
			services[svc.Namespace+"/"+svc.Name] = true
		}
	}
	ready := readyEndpointServices(input.EndpointSlices)
	for _, config := range input.AdmissionWebhookConfigurations {
		if config == nil {
			continue
		}
		webhooks, found, err := unstructured.NestedSlice(config.Object, "webhooks")
		if err != nil {
			check.Caveat = appendCaveat(check.Caveat, config.GetName()+" webhooks were unreadable.")
			continue
		}
		if !found {
			continue
		}
		for i, raw := range webhooks {
			webhook, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			name, _ := webhook["name"].(string)
			failurePolicy := "Fail"
			if strings.EqualFold(stringValue(webhook, "failurePolicy"), "Ignore") {
				failurePolicy = "Ignore"
			}
			if strings.EqualFold(stringValue(webhook, "matchPolicy"), "Exact") {
				check.Findings = append(check.Findings, admissionFinding(check, config, i, LevelReview, "Exact API version matching", "matchPolicy", "Exact", "Exact matching can skip equivalent API versions during an upgrade.", "Use matchPolicy: Equivalent unless exact version matching is intentionally required.", admissionMatchPolicyReferences))
			}
			service, found, _ := unstructured.NestedMap(webhook, "clientConfig", "service")
			if found {
				ns, _ := service["namespace"].(string)
				svc, _ := service["name"].(string)
				key := ns + "/" + svc
				if !backendEvidenceAvailable {
					if !backendEvidenceCaveatAdded {
						check.Caveat = appendCaveat(check.Caveat, "Service or EndpointSlice evidence was unavailable; webhook backend readiness could not be verified.")
						backendEvidenceCaveatAdded = true
					}
				} else if !services[key] || !ready[key] {
					level := LevelBlocker
					title := "Fail-closed webhook backend unavailable"
					impact := "Admission requests that match this fail-closed webhook are rejected while its backend is unavailable."
					if failurePolicy == "Ignore" {
						level = LevelWarning
						title = "Fail-open webhook backend unavailable"
						impact = "Admission requests that match this webhook currently fail open, bypassing the intended policy while its backend is unavailable."
					}
					check.Findings = append(check.Findings, admissionFinding(check, config, i, level, title, "clientConfig.service", key, impact, "Restore the Service and a ready EndpointSlice, or intentionally change failurePolicy after evaluating the admission bypass risk.", admissionBackendReferences))
				}
			}
			if webhookURL, found, _ := unstructured.NestedString(webhook, "clientConfig", "url"); found && webhookURL != "" {
				check.Findings = append(check.Findings, admissionFinding(check, config, i, LevelReview, "URL webhook backend requires external verification", "clientConfig.url", webhookURL, "Radar cannot verify the availability of an admission webhook backend outside the cluster.", "Verify the URL backend is reachable and highly available throughout the control-plane upgrade, especially for fail-closed webhooks.", admissionBackendReferences))
			}
			if webhookInterceptsAuth(webhook) {
				check.Findings = append(check.Findings, admissionFinding(check, config, i, LevelReview, "Authentication review interception", "rules", name, "The webhook can intercept TokenReview or SubjectAccessReview requests used during authentication and authorization.", "Verify the webhook remains available and deliberately excludes authentication and authorization review APIs if interception is unnecessary.", admissionAuthReviewReferences))
			}
		}
	}
	if check.Caveat != "" {
		check.Status = CheckUnknown
		if len(check.Findings) == 0 {
			check.Summary = "No admission risks were identified, but some webhook evidence was unreadable."
		}
	}
	if len(check.Findings) > 0 {
		check.Summary = fmt.Sprintf("%d admission webhook %s.", len(check.Findings), plural(len(check.Findings), "condition requires action or review", "conditions require action or review"))
	}
	return check
}

func readyEndpointServices(slices []*discoveryv1.EndpointSlice) map[string]bool {
	out := map[string]bool{}
	for _, slice := range slices {
		if slice == nil {
			continue
		}
		service := slice.Labels[discoveryv1.LabelServiceName]
		if service == "" {
			continue
		}
		for _, endpoint := range slice.Endpoints {
			if endpoint.Conditions.Ready == nil || *endpoint.Conditions.Ready {
				out[slice.Namespace+"/"+service] = true
				break
			}
		}
	}
	return out
}

func admissionFinding(check Check, config *unstructured.Unstructured, index int, level Level, title, path, detail, impact, remediation string, references []Reference) Finding {
	return Finding{RuleID: check.ID, Title: title, Level: level, Resource: &ResourceRef{Group: "admissionregistration.k8s.io", Kind: config.GetKind(), Name: config.GetName()}, Evidence: Evidence{Source: "live", Path: fmt.Sprintf("webhooks[%d].%s", index, path), Detail: detail}, Impact: impact, Remediation: remediation, References: append([]Reference(nil), references...)}
}

func stringValue(object map[string]any, key string) string {
	value, _ := object[key].(string)
	return value
}

func webhookInterceptsAuth(webhook map[string]any) bool {
	rules, _, _ := unstructured.NestedSlice(webhook, "rules")
	for _, raw := range rules {
		rule, _ := raw.(map[string]any)
		groups := stringSlice(rule["apiGroups"])
		resources := stringSlice(rule["resources"])
		operations := stringSlice(rule["operations"])
		if len(operations) > 0 && !stringSliceContainsFold(operations, "*") && !stringSliceContainsFold(operations, "CREATE") {
			continue
		}
		authentication := containsString(groups, "*") || containsString(groups, "authentication.k8s.io")
		authorization := containsString(groups, "*") || containsString(groups, "authorization.k8s.io")
		allResources := containsString(resources, "*") || containsString(resources, "*/*")
		if (authentication && (allResources || containsString(resources, "tokenreviews"))) || (authorization && (allResources || containsString(resources, "subjectaccessreviews") || containsString(resources, "selfsubjectaccessreviews"))) {
			return true
		}
	}
	return false
}

func stringSliceContainsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}

func stringSlice(value any) []string {
	raw, _ := value.([]any)
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func scanCRDConversionWebhookReadiness(input *Input) Check {
	check := Check{ID: "crd-conversion-webhook-readiness", Category: "API extensions", Title: "CRD conversion webhook readiness", Status: CheckPassed, Summary: "Inspected CRD conversion webhooks have ready Service backends.", Scope: "CustomResourceDefinitions, Services, and EndpointSlices", References: append([]Reference(nil), crdConversionReferences...)}
	if input.CustomResourceDefinitions == nil {
		check.Status, check.Summary = CheckUnknown, "CRD conversion configuration evidence was unavailable."
		return check
	}
	backendEvidenceAvailable := input.WebhookServices != nil && input.EndpointSlices != nil
	backendEvidenceCaveatAdded := false
	services := map[string]bool{}
	for _, svc := range input.WebhookServices {
		if svc != nil {
			services[svc.Namespace+"/"+svc.Name] = true
		}
	}
	ready := readyEndpointServices(input.EndpointSlices)
	for _, crd := range input.CustomResourceDefinitions {
		if crd == nil {
			continue
		}
		strategy, _, _ := unstructured.NestedString(crd.Object, "spec", "conversion", "strategy")
		if strategy != "Webhook" {
			continue
		}
		check.Inspected++
		config, found, err := unstructured.NestedMap(crd.Object, "spec", "conversion", "webhook", "clientConfig")
		if err != nil || !found {
			check.Caveat = appendCaveat(check.Caveat, crd.GetName()+" conversion config was unreadable.")
			continue
		}
		if url, ok := config["url"].(string); ok && url != "" {
			check.Findings = append(check.Findings, crdFinding(check, crd, LevelReview, "spec.conversion.webhook.clientConfig.url", url, "URL-based conversion backends are outside cluster Service readiness evidence.", "Verify DNS, TLS, network reachability, and availability of the external conversion endpoint during the upgrade."))
			continue
		}
		service, found, _ := unstructured.NestedMap(config, "service")
		ns, _ := service["namespace"].(string)
		name, _ := service["name"].(string)
		key := ns + "/" + name
		if !found || ns == "" || name == "" {
			level := LevelBlocker
			if !crdConversionCanBeNeeded(crd) {
				level = LevelWarning
			}
			check.Findings = append(check.Findings, crdFinding(check, crd, level, "spec.conversion.webhook.clientConfig.service", key, "The API server may be unable to convert stored custom resources while this backend is unavailable.", "Restore the conversion Service and at least one ready EndpointSlice before upgrading."))
			continue
		}
		if !backendEvidenceAvailable {
			if !backendEvidenceCaveatAdded {
				check.Caveat = appendCaveat(check.Caveat, "Service or EndpointSlice evidence was unavailable; conversion backend readiness could not be verified.")
				backendEvidenceCaveatAdded = true
			}
			continue
		}
		if !services[key] || !ready[key] {
			level := LevelBlocker
			if !crdConversionCanBeNeeded(crd) {
				level = LevelWarning
			}
			check.Findings = append(check.Findings, crdFinding(check, crd, level, "spec.conversion.webhook.clientConfig.service", key, "The API server may be unable to convert stored custom resources while this backend is unavailable.", "Restore the conversion Service and at least one ready EndpointSlice before upgrading."))
		}
	}
	if check.Inspected == 0 {
		check.Status, check.Summary = CheckNotApplicable, "No CRD uses webhook conversion."
	} else {
		if check.Caveat != "" {
			check.Status = CheckUnknown
		}
		if len(check.Findings) > 0 {
			check.Summary = fmt.Sprintf("%d CRD conversion webhook %s.", len(check.Findings), plural(len(check.Findings), "condition requires action or review", "conditions require action or review"))
		} else if check.Caveat != "" {
			check.Summary = "No conversion backend failures were identified, but CRD evidence is incomplete."
		}
	}
	return check
}

func crdConversionCanBeNeeded(crd *unstructured.Unstructured) bool {
	versions, _, _ := unstructured.NestedSlice(crd.Object, "spec", "versions")
	storageName := ""
	for _, raw := range versions {
		version, _ := raw.(map[string]any)
		name, _ := version["name"].(string)
		storage, _ := version["storage"].(bool)
		served, _ := version["served"].(bool)
		if storage {
			storageName = name
		}
		if served && !storage {
			return true
		}
	}
	stored, _, _ := unstructured.NestedStringSlice(crd.Object, "status", "storedVersions")
	for _, version := range stored {
		if storageName != "" && version != storageName {
			return true
		}
	}
	return false
}

func crdFinding(check Check, crd *unstructured.Unstructured, level Level, path, detail, impact, remediation string) Finding {
	return Finding{RuleID: check.ID, Title: "CRD conversion webhook requires upgrade review", Level: level, Resource: &ResourceRef{Group: "apiextensions.k8s.io", Kind: "CustomResourceDefinition", Name: crd.GetName()}, Evidence: Evidence{Source: "live", Path: path, Detail: detail}, Impact: impact, Remediation: remediation, References: append([]Reference(nil), check.References...)}
}

func scanStrictIPCIDRValidation(input *Input) Check {
	check := Check{ID: "strict-ip-cidr-validation", Category: "API compatibility", Title: "Strict source IP and CIDR validation", Status: CheckPassed, Summary: "No non-canonical IP or CIDR values were found in readable source manifests.", Scope: "Helm and kubectl last-applied source manifests", AppliesFrom: "1.36", References: append([]Reference(nil), strictIPReferences...)}
	if input.Namespaces != nil {
		check.Caveat = scopedCoverageNote(input.Namespaces, "source manifests")
		check.Summary = "No invalid network value was found in source manifests in the selected namespace scope."
	}
	resources := append([]ManifestResource(nil), input.ManifestResources...)
	lastApplied, lastAppliedParseErrors := lastAppliedResources(input.SourceObjects)
	resources = append(resources, lastApplied...)
	appendSourceManifestCoverageCaveats(&check, input, lastAppliedParseErrors)
	if resources == nil {
		check.Status, check.Summary = CheckUnknown, "Source manifests were unavailable; strict IP and CIDR validation could not be evaluated."
		return check
	}
	for _, resource := range dedupeManifestResources(resources) {
		if resource.Object == nil {
			continue
		}
		check.Inspected++
		for _, candidate := range strictNetworkCandidates(resource.Object) {
			var errs field.ErrorList
			if candidate.cidr {
				errs = validation.IsValidCIDRForLegacyField(field.NewPath(candidate.path), candidate.value, true, nil)
			} else {
				errs = validation.IsValidIPForLegacyField(field.NewPath(candidate.path), candidate.value, true, nil)
			}
			if len(errs) == 0 {
				continue
			}
			check.Findings = append(check.Findings, Finding{RuleID: check.ID, Title: "Source manifest contains a value rejected by strict validation", Level: LevelReview, Resource: &ResourceRef{Group: groupForAPIVersion(resource.APIVersion), Kind: resource.Kind, Namespace: resource.Namespace, Name: resource.Name}, Evidence: Evidence{Source: resource.Source, Path: candidate.path, Detail: candidate.value}, AppliesFrom: check.AppliesFrom, Impact: "A future update that touches this field can be rejected once strict IP/CIDR validation applies; existing stored values may remain because validation ratchets.", Remediation: "Normalize this source value to canonical IP or CIDR syntax, then reconcile it before upgrading.", References: append([]Reference(nil), check.References...)})
		}
	}
	if check.Inspected == 0 {
		check.Status, check.Summary = CheckUnknown, "Readable full source objects were unavailable; API-version-only manifest evidence is insufficient for strict validation."
	} else if len(check.Findings) > 0 {
		check.Summary = fmt.Sprintf("%d non-canonical source network %s.", len(check.Findings), plural(len(check.Findings), "value requires review", "values require review"))
	} else if check.Caveat != "" {
		check.Status = CheckUnknown
		if input.Namespaces != nil {
			check.Summary = "No invalid network values were found in source manifests in the selected namespace scope, but source-manifest coverage is incomplete."
		} else {
			check.Summary = "No invalid network values were found, but source-manifest coverage is incomplete."
		}
	}
	return check
}

type networkCandidate struct {
	path, value string
	cidr        bool
}

func strictNetworkCandidates(object *unstructured.Unstructured) []networkCandidate {
	var out []networkCandidate
	addStrings := func(path []string, cidr bool) {
		values, found, _ := unstructured.NestedStringSlice(object.Object, path...)
		if found {
			for i, value := range values {
				out = append(out, networkCandidate{path: strings.Join(path, ".") + fmt.Sprintf("[%d]", i), value: value, cidr: cidr})
			}
		}
	}
	addString := func(path []string, cidr bool) {
		value, found, _ := unstructured.NestedString(object.Object, path...)
		if found && value != "" && value != corev1.ClusterIPNone {
			out = append(out, networkCandidate{path: strings.Join(path, "."), value: value, cidr: cidr})
		}
	}
	prefix := []string{}
	switch object.GetKind() {
	case "Deployment", "ReplicaSet", "StatefulSet", "DaemonSet", "Job":
		prefix = []string{"spec", "template", "spec"}
	case "CronJob":
		prefix = []string{"spec", "jobTemplate", "spec", "template", "spec"}
	}
	if len(prefix) > 0 {
		addStrings(append(append([]string{}, prefix...), "dnsConfig", "nameservers"), false)
		hostAliases, _, _ := unstructured.NestedSlice(object.Object, append(append([]string{}, prefix...), "hostAliases")...)
		for i, raw := range hostAliases {
			alias, _ := raw.(map[string]any)
			ip, _ := alias["ip"].(string)
			if ip != "" {
				out = append(out, networkCandidate{path: strings.Join(prefix, ".") + fmt.Sprintf(".hostAliases[%d].ip", i), value: ip})
			}
		}
	}
	switch object.GetKind() {
	case "Service":
		addString([]string{"spec", "clusterIP"}, false)
		clusterIPs, found, _ := unstructured.NestedStringSlice(object.Object, "spec", "clusterIPs")
		if found {
			for i, value := range clusterIPs {
				if value != corev1.ClusterIPNone {
					out = append(out, networkCandidate{path: fmt.Sprintf("spec.clusterIPs[%d]", i), value: value})
				}
			}
		}
		addStrings([]string{"spec", "externalIPs"}, false)
		addStrings([]string{"spec", "loadBalancerSourceRanges"}, true)
	case "NetworkPolicy":
		for _, direction := range []string{"ingress", "egress"} {
			peerField := map[string]string{"ingress": "from", "egress": "to"}[direction]
			rules, _, _ := unstructured.NestedSlice(object.Object, "spec", direction)
			for i, rawRule := range rules {
				rule, _ := rawRule.(map[string]any)
				peers, _ := rule[peerField].([]any)
				for j, rawPeer := range peers {
					peer, _ := rawPeer.(map[string]any)
					cidr, _, _ := unstructured.NestedString(peer, "ipBlock", "cidr")
					if cidr != "" {
						out = append(out, networkCandidate{path: fmt.Sprintf("spec.%s[%d].%s[%d].ipBlock.cidr", direction, i, peerField, j), value: cidr, cidr: true})
					}
					exceptions, _, _ := unstructured.NestedStringSlice(peer, "ipBlock", "except")
					for k, exception := range exceptions {
						out = append(out, networkCandidate{path: fmt.Sprintf("spec.%s[%d].%s[%d].ipBlock.except[%d]", direction, i, peerField, j, k), value: exception, cidr: true})
					}
				}
			}
		}
	case "Pod":
		addStrings([]string{"spec", "dnsConfig", "nameservers"}, false)
		hostAliases, _, _ := unstructured.NestedSlice(object.Object, "spec", "hostAliases")
		for i, raw := range hostAliases {
			alias, _ := raw.(map[string]any)
			ip, _ := alias["ip"].(string)
			if ip != "" {
				out = append(out, networkCandidate{path: fmt.Sprintf("spec.hostAliases[%d].ip", i), value: ip})
			}
		}
	case "Node":
		addString([]string{"spec", "podCIDR"}, true)
		addStrings([]string{"spec", "podCIDRs"}, true)
	case "Endpoints":
		subsets, _, _ := unstructured.NestedSlice(object.Object, "subsets")
		for i, raw := range subsets {
			subset, _ := raw.(map[string]any)
			for _, name := range []string{"addresses", "notReadyAddresses"} {
				addresses, _ := subset[name].([]any)
				for j, rawAddress := range addresses {
					address, _ := rawAddress.(map[string]any)
					ip, _ := address["ip"].(string)
					if ip != "" {
						out = append(out, networkCandidate{path: fmt.Sprintf("subsets[%d].%s[%d].ip", i, name, j), value: ip})
					}
				}
			}
		}
	case "EndpointSlice":
		endpoints, _, _ := unstructured.NestedSlice(object.Object, "endpoints")
		for i, raw := range endpoints {
			endpoint, _ := raw.(map[string]any)
			addresses, _ := endpoint["addresses"].([]any)
			for j, rawAddress := range addresses {
				address, _ := rawAddress.(string)
				if address != "" {
					out = append(out, networkCandidate{path: fmt.Sprintf("endpoints[%d].addresses[%d]", i, j), value: address})
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

func scanGKEExecProbeTimeout(input *Input) Check {
	check := Check{ID: "gke-exec-probe-timeout", Category: "Managed Kubernetes", Title: "GKE exec probe timeout exposure", Status: CheckPassed, Summary: "No GKE exec probes rely on the one-second default timeout.", Scope: "Workload probes and recent Kubernetes Events", AppliesFrom: "1.35", References: append([]Reference(nil), gkeExecProbeReferences...)}
	platform := strings.ToLower(strings.TrimSpace(input.Platform))
	if platform == "" || platform == "unknown" {
		check.Status, check.Summary = CheckUnknown, "Cluster platform detection was unavailable; Radar could not determine whether the GKE-specific change applies."
		return check
	}
	if !strings.Contains(platform, "gke") && !strings.Contains(platform, "google") {
		check.Status, check.Summary = CheckNotApplicable, "This cluster is not running on GKE."
		return check
	}
	workloadCoverageIncomplete := workloadsUnavailable(input)
	if workloadCoverageIncomplete {
		check.Caveat = appendCaveat(check.Caveat, "One or more workload kinds were unavailable, so additional exec probes may exist.")
	}
	if input.Namespaces != nil {
		check.Caveat = appendCaveat(check.Caveat, scopedCoverageNote(input.Namespaces, "workloads and recent Events"))
		check.Summary = "No risky GKE exec probe was found in the selected namespace scope."
	}
	timedOut := map[string]bool{}
	if input.Events != nil {
		for _, event := range input.Events {
			if event != nil && execProbeTimeoutEvent.MatchString(event.Message) {
				markTimedOutWorkload(timedOut, input, event.InvolvedObject.Namespace, event.InvolvedObject.Kind, event.InvolvedObject.Name)
			}
		}
	}
	configured := 0
	for _, subject := range workloadSubjects(input, newWorkloadIndex(input)) {
		containerGroups := []struct {
			path       string
			containers []corev1.Container
		}{
			{path: "initContainers", containers: subject.spec.InitContainers},
			{path: "containers", containers: subject.spec.Containers},
		}
		for _, group := range containerGroups {
			for _, container := range group.containers {
				for name, probe := range map[string]*corev1.Probe{"livenessProbe": container.LivenessProbe, "readinessProbe": container.ReadinessProbe, "startupProbe": container.StartupProbe} {
					if probe == nil || probe.Exec == nil {
						continue
					}
					configured++
					check.Inspected++
					if probe.TimeoutSeconds > 1 {
						continue
					}
					level := LevelReview
					title := "Exec probe relies on default one-second timeout"
					impact := "This exec probe relies on the one-second default timeout and may begin failing after the GKE 1.35 behavior change."
					if timedOut[workloadTimeoutKey(subject.resource.Kind, subject.resource.Namespace, subject.resource.Name)] {
						level = LevelBlocker
						title = "Exec probe already timing out"
						impact = "Recent events show this exec probe timing out; the GKE behavior change is already observable."
					}
					check.Findings = append(check.Findings, workloadFinding(subject, check, title, level, subject.pathPrefix+"."+group.path+"["+container.Name+"]."+name+".timeoutSeconds", impact, "Set an explicit timeoutSeconds greater than 1 after measuring the command's normal duration, then verify probe behavior."))
				}
			}
		}
	}
	if input.Events == nil && len(check.Findings) > 0 {
		check.Caveat = appendCaveat(check.Caveat, "Recent Events were unavailable, so Radar could not distinguish observed timeouts from static exposure.")
	} else if len(check.Findings) > 0 && len(input.CacheScopedKinds["events"]) > 0 {
		check.Caveat = appendCaveat(check.Caveat, cacheScopedCoverageNote(input, []string{"events"}))
	}
	if len(check.Findings) == 0 && workloadCoverageIncomplete {
		check.Status = CheckUnknown
		check.Summary = "No risky GKE exec probes were found in readable workloads, but workload coverage is incomplete."
	} else if configured > 0 && len(check.Findings) == 0 {
		check.Status = CheckUnknown
		check.Summary = "Exec probe timeouts are configured, but static configuration and short-lived Events cannot prove their commands finish within the timeout."
		check.Caveat = appendCaveat(check.Caveat, "Verify probe duration in staging or longer-lived logs before upgrading.")
	} else if len(check.Findings) == 0 && input.Namespaces != nil {
		check.Status = CheckUnknown
		check.Summary = "No risky GKE exec probe was found in the selected namespace scope, but workload coverage is incomplete."
	}
	if len(check.Findings) > 0 {
		check.Summary = fmt.Sprintf("%d GKE exec %s rely on a one-second timeout.", len(check.Findings), plural(len(check.Findings), "probe", "probes"))
	}
	return check
}

var execProbeTimeoutEvent = regexp.MustCompile(`(?i)^(?:Liveness|Readiness|Startup) probe (?:failed|errored).*: command timed out|^\s*probe errored and resulted in .* state: command timed out`)

func workloadTimeoutKey(kind, namespace, name string) string {
	return kind + "\x00" + namespace + "\x00" + name
}

func markTimedOutWorkload(out map[string]bool, input *Input, namespace, kind, podName string) {
	if kind != "" && kind != "Pod" {
		return
	}
	out[workloadTimeoutKey("Pod", namespace, podName)] = true
	for _, pod := range input.Pods {
		if pod == nil || pod.Namespace != namespace || pod.Name != podName {
			continue
		}
		owner := controllerOwner(pod.OwnerReferences)
		if owner == nil {
			return
		}
		out[workloadTimeoutKey(owner.Kind, namespace, owner.Name)] = true
		if owner.Kind == "ReplicaSet" {
			for _, rs := range input.ReplicaSets {
				if rs != nil && rs.Namespace == namespace && rs.Name == owner.Name {
					if top := controllerOwner(rs.OwnerReferences); top != nil {
						out[workloadTimeoutKey(top.Kind, namespace, top.Name)] = true
					}
					break
				}
			}
		}
		if owner.Kind == "Job" {
			for _, job := range input.Jobs {
				if job != nil && job.Namespace == namespace && job.Name == owner.Name {
					if top := controllerOwner(job.OwnerReferences); top != nil {
						out[workloadTimeoutKey(top.Kind, namespace, top.Name)] = true
					}
					break
				}
			}
		}
		return
	}
}
