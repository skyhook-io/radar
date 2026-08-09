package upgradereadiness

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilversion "k8s.io/apimachinery/pkg/util/version"

	bp "github.com/skyhook-io/radar/pkg/audit"
	"github.com/skyhook-io/radar/pkg/topology"
)

var (
	ErrInvalidCurrentVersion = errors.New("invalid current Kubernetes version")
	ErrInvalidTargetVersion  = errors.New("invalid target Kubernetes version")
	ErrNonForwardTarget      = errors.New("target Kubernetes version must be newer than the current version")
)

func Scan(input *Input, currentVersion, targetVersion string) (*ScanResults, error) {
	current, target, err := validateUpgradeVersions(currentVersion, targetVersion)
	if err != nil {
		return nil, err
	}
	if input == nil {
		input = &Input{}
	}

	result := &ScanResults{
		CurrentVersion:  minorString(current),
		TargetVersion:   minorString(target),
		ReviewedThrough: ReviewedThrough,
		Checks:          []Check{},
		Coverage: Coverage{
			Source: "live",
			State:  "complete",
		},
	}
	if input.Namespaces != nil {
		result.Coverage.State = "partial"
		result.Coverage.ScopedNamespaces = append([]string(nil), input.Namespaces...)
		sort.Strings(result.Coverage.ScopedNamespaces)
	}
	if len(input.CacheScopedKinds) > 0 {
		result.Coverage.State = "partial"
		result.Coverage.ScopedKinds = make(map[string][]string, len(input.CacheScopedKinds))
		for kind, namespaces := range input.CacheScopedKinds {
			copied := append([]string(nil), namespaces...)
			sort.Strings(copied)
			result.Coverage.ScopedKinds[kind] = copied
		}
	}
	result.Coverage.UnavailableKinds = unavailableKinds(input)
	if len(result.Coverage.UnavailableKinds) > 0 {
		result.Coverage.State = "partial"
	}

	index := newWorkloadIndex(input)
	result.Checks = append(result.Checks,
		scanUpgradePath(current, target),
		scanDeprecatedAPIRequests(input, target),
		scanManifestCompatibility(input, target),
		scanNodeHealth(input),
		scanKubeletSkew(input, target),
		scanKubeProxySkew(input, target),
	)

	if crossesRelease(current, target, "1.35") {
		result.Checks = append(result.Checks,
			scanNodeCgroupCompatibility(input),
			scanGKEExecProbeTimeout(input),
		)
	}

	if crossesRelease(current, target, "1.36") {
		result.Checks = append(result.Checks,
			scanContainerRuntimeSupport(input, target),
			scanGitRepo(input, index),
			scanFlexVolume(input, index),
			scanServiceExternalIPs(input),
			scanRenamedMetrics(input),
			scanStrictIPCIDRValidation(input),
		)
	}

	result.Checks = append(result.Checks,
		scanNodeDrainFeasibility(input),
		scanAdmissionWebhookReadiness(input),
		scanCRDConversionWebhookReadiness(input),
		scanAPIServiceReadiness(input),
	)

	for i := range result.Checks {
		applyCacheScope(&result.Checks[i], input)
		finalizeCheck(&result.Checks[i])
		check := &result.Checks[i]
		if check.Caveat != "" {
			result.Coverage.State = "partial"
		}
		if check.Status == CheckUnknown {
			result.Coverage.State = "partial"
		}
		result.Summary.Findings += len(check.Findings)
		switch check.Status {
		case CheckBlocked:
			result.Summary.Blocked++
		case CheckWarning:
			result.Summary.Warnings++
		case CheckReview:
			result.Summary.Reviews++
		case CheckPassed:
			result.Summary.Passed++
		case CheckUnknown:
			result.Summary.Unknown++
		case CheckNotApplicable:
			result.Summary.NotApplicable++
		}
	}

	reviewed := utilversion.MustParseGeneric(ReviewedThrough)
	switch {
	case result.Summary.Blocked > 0:
		result.Verdict = VerdictBlocked
	case result.Summary.Warnings > 0:
		result.Verdict = VerdictWarning
	case result.Summary.Reviews > 0:
		result.Verdict = VerdictReview
	case result.Summary.Unknown > 0 || target.GreaterThan(reviewed) || result.Coverage.State != "complete":
		result.Verdict = VerdictUnknown
	default:
		result.Verdict = VerdictNoKnownBlockers
	}

	return result, nil
}

var cacheKindsByCheck = map[string][]string{
	"gitrepo-volume-removed":         {"pods", "deployments", "replicasets", "statefulsets", "daemonsets", "jobs", "cronjobs"},
	"flexvolume-kubeadm-support":     {"pods", "deployments", "replicasets", "statefulsets", "daemonsets", "jobs", "cronjobs"},
	"service-externalips-deprecated": {"services"},
	"node-drain-feasibility":         {"pods", "poddisruptionbudgets"},
	"gke-exec-probe-timeout":         {"pods", "deployments", "replicasets", "statefulsets", "daemonsets", "jobs", "cronjobs"},
}

func applyCacheScope(check *Check, input *Input) {
	if len(input.CacheScopedKinds) == 0 {
		return
	}
	if check.ID == "kube-proxy-version-skew" {
		namespaces := input.CacheScopedKinds["daemonsets"]
		if len(namespaces) == 0 || containsString(namespaces, "kube-system") {
			return
		}
		check.Caveat = appendCaveat(check.Caveat, cacheScopedCoverageNote(input, []string{"daemonsets"}))
		if len(check.Findings) == 0 && (check.Status == CheckPassed || check.Status == CheckNotApplicable) {
			check.Status = CheckUnknown
			check.Summary = "kube-proxy could not be inspected because kube-system is outside the DaemonSet informer scope."
		}
		return
	}
	kinds := cacheKindsByCheck[check.ID]
	if len(kinds) == 0 || check.Status == CheckNotApplicable {
		return
	}
	note := cacheScopedCoverageNote(input, kinds)
	if note == "" {
		return
	}
	check.Caveat = appendCaveat(check.Caveat, note)
	if len(check.Findings) == 0 && check.Status == CheckPassed {
		check.Status = CheckUnknown
		check.Summary = "No issue was found in readable cached evidence, but per-kind namespace coverage is incomplete."
	}
}

func cacheScopedCoverageNote(input *Input, kinds []string) string {
	parts := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		namespaces := input.CacheScopedKinds[kind]
		if len(namespaces) == 0 {
			continue
		}
		namespaces = append([]string(nil), namespaces...)
		sort.Strings(namespaces)
		parts = append(parts, kind+" ("+formatBoundedList(namespaces, ", ")+")")
	}
	if len(parts) == 0 {
		return ""
	}
	return "Cached evidence is namespace-limited for: " + formatBoundedList(parts, "; ") + "."
}

func crossesRelease(current, target *utilversion.Version, release string) bool {
	v := utilversion.MustParseGeneric(release)
	return current.LessThan(v) && target.AtLeast(v)
}

func scanUpgradePath(current, target *utilversion.Version) Check {
	check := Check{
		ID:         "control-plane-upgrade-path",
		Category:   "Upgrade path",
		Title:      "Control plane version sequence",
		Status:     CheckPassed,
		Summary:    "The target is the next Kubernetes minor version.",
		Scope:      "Kubernetes control plane version policy",
		References: append([]Reference(nil), versionSkewReferences...),
	}
	if target.Major() == current.Major() && target.Minor() <= current.Minor()+1 {
		return check
	}
	if target.Major() != current.Major() {
		sequence := minorString(current) + " → " + minorString(target)
		check.Summary = "This target skips required Kubernetes version upgrades."
		check.Findings = []Finding{{
			RuleID:      check.ID,
			Title:       "Unsupported control plane version jump",
			Level:       LevelBlocker,
			Evidence:    Evidence{Source: "version policy", Path: "controlPlane", Detail: sequence},
			AppliesFrom: minorString(target),
			Impact:      "Kubernetes control planes must follow the supported one-minor upgrade sequence; a direct major-version jump is unsupported.",
			Remediation: fmt.Sprintf("Choose Kubernetes %d.%d as the next target, then re-run upgrade impact for each subsequent step.", current.Major(), current.Minor()+1),
			References:  append([]Reference(nil), versionSkewReferences...),
		}}
		return check
	}
	const maxPathEntries = 10
	steps := target.Minor() - current.Minor() + 1
	path := make([]string, 0, maxPathEntries)
	if steps <= maxPathEntries {
		for minor := current.Minor(); minor <= target.Minor(); minor++ {
			path = append(path, fmt.Sprintf("%d.%d", current.Major(), minor))
		}
	} else {
		for minor := current.Minor(); len(path) < maxPathEntries-2; minor++ {
			path = append(path, fmt.Sprintf("%d.%d", current.Major(), minor))
		}
		path = append(path, "…", minorString(target))
	}
	sequence := strings.Join(path, " → ")
	check.Summary = "This target skips required Kubernetes minor-version upgrades."
	check.Findings = []Finding{{
		RuleID:      check.ID,
		Title:       "Unsupported control plane version jump",
		Level:       LevelBlocker,
		Evidence:    Evidence{Source: "version policy", Path: "controlPlane", Detail: sequence},
		AppliesFrom: minorString(target),
		Impact:      "Kubernetes control planes must be upgraded one minor version at a time; skipping minors is unsupported.",
		Remediation: "Plan the control plane upgrades in sequence: " + sequence + ". Re-run upgrade impact for each step.",
		References:  append([]Reference(nil), versionSkewReferences...),
	}}
	return check
}

func finalizeCheck(check *Check) {
	if check.Findings == nil {
		check.Findings = []Finding{}
	}
	sort.Slice(check.Findings, func(i, j int) bool {
		return compareFindings(check.Findings[i], check.Findings[j]) < 0
	})
	for _, finding := range check.Findings {
		if finding.Level == LevelBlocker {
			check.Status = CheckBlocked
			return
		}
	}
	for _, finding := range check.Findings {
		if finding.Level == LevelWarning {
			check.Status = CheckWarning
			return
		}
	}
	if len(check.Findings) > 0 {
		check.Status = CheckReview
	}
}

func compareFindings(a, b Finding) int {
	return cmp.Or(
		cmp.Compare(findingLevelOrder(a.Level), findingLevelOrder(b.Level)),
		compareResourceRefs(a.Resource, b.Resource),
		cmp.Compare(a.Evidence.Source, b.Evidence.Source),
		cmp.Compare(a.Evidence.Path, b.Evidence.Path),
		cmp.Compare(a.Evidence.Detail, b.Evidence.Detail),
		cmp.Compare(a.Title, b.Title),
		cmp.Compare(a.AppliesFrom, b.AppliesFrom),
		compareResourceRefs(a.ManagedBy, b.ManagedBy),
		cmp.Compare(a.Impact, b.Impact),
		cmp.Compare(a.Remediation, b.Remediation),
	)
}

func compareResourceRefs(a, b *ResourceRef) int {
	if a == nil && b == nil {
		return 0
	}
	if a == nil {
		return -1
	}
	if b == nil {
		return 1
	}
	return cmp.Or(
		cmp.Compare(a.Group, b.Group),
		cmp.Compare(a.Kind, b.Kind),
		cmp.Compare(a.Namespace, b.Namespace),
		cmp.Compare(a.Name, b.Name),
	)
}

func findingLevelOrder(level Level) int {
	switch level {
	case LevelBlocker:
		return 0
	case LevelWarning:
		return 1
	default:
		return 2
	}
}

func scanDeprecatedAPIRequests(input *Input, target *utilversion.Version) Check {
	processScope := "since that process last restarted"
	if input.DeprecatedAPIMetricsWindow != "" {
		processScope = "over its " + input.DeprecatedAPIMetricsWindow + " process lifetime"
	}
	check := Check{
		ID:           "deprecated-api-requests",
		Category:     "API compatibility",
		Title:        "Removed API usage",
		Status:       CheckPassed,
		Summary:      "No removed API requests were observed on one API server process " + processScope + ".",
		Scope:        "Kubernetes API server usage metrics",
		EvidenceNote: "API usage metrics cover one API server process " + processScope + "; they are not cluster-wide history.",
		References:   append([]Reference(nil), manifestAPIReferences...),
	}
	if input.DeprecatedAPIRequests == nil {
		check.Status = CheckUnknown
		check.Summary = "API usage metrics are unavailable; Radar could not inspect deprecated API requests."
		check.Caveat = "Grant get on the API server /metrics non-resource URL to the current identity to enable this evidence. Radar Cloud's default role intentionally omits this broader read."
		check.EvidenceNote = ""
		check.References = append(check.References, cloudMetricsPermissionReference)
		return check
	}
	check.Inspected = len(input.DeprecatedAPIRequests)
	for _, request := range input.DeprecatedAPIRequests {
		removed, err := parseMinor(request.RemovedRelease)
		if err != nil || target.LessThan(removed) || request.Requests <= 0 {
			continue
		}
		gv := request.Version
		if request.Group != "" {
			gv = request.Group + "/" + request.Version
		}
		path := gv + "/" + request.Resource
		if request.Subresource != "" {
			path += "/" + request.Subresource
		}
		check.Findings = append(check.Findings, Finding{
			RuleID:      check.ID,
			Title:       "API removed in Kubernetes " + minorString(removed),
			Level:       LevelBlocker,
			Evidence:    Evidence{Source: "apiserver metrics", Path: path, Detail: "observed on one API server process " + processScope},
			AppliesFrom: minorString(removed),
			Impact:      "A client is still requesting an API that the target Kubernetes version no longer serves.",
			Remediation: "Identify the client from audit logs or user-agent metrics, then migrate it to the replacement API before upgrading.",
			References:  append([]Reference(nil), manifestAPIReferences...),
		})
	}
	if len(check.Findings) > 0 {
		check.Summary = fmt.Sprintf("%d removed API request %s observed on one API server process %s.", len(check.Findings), plural(len(check.Findings), "series", "series"), processScope)
	}
	return check
}

func scanManifestCompatibility(input *Input, target *utilversion.Version) Check {
	check := Check{
		ID:         "manifest-api-compatibility",
		Category:   "API compatibility",
		Title:      "Source manifest API versions",
		Status:     CheckPassed,
		Summary:    "No incompatible API versions were found in readable source manifests.",
		Scope:      "Helm release manifests and kubectl last-applied configuration",
		References: append([]Reference(nil), manifestAPIReferences...),
	}
	if input.Namespaces != nil {
		check.Caveat = scopedCoverageNote(input.Namespaces, "source manifests")
		check.Summary = "No incompatible API versions were found in readable source manifests in the selected namespace scope."
	}
	helmManifestsAvailable := input.ManifestResources != nil
	helmUnavailable := len(input.HelmUnavailableNamespaces) > 0
	lastApplied, lastAppliedParseErrors := lastAppliedResources(input.SourceObjects)
	resources := append([]ManifestResource(nil), input.ManifestResources...)
	resources = append(resources, lastApplied...)
	resources = dedupeManifestResources(resources)
	check.Inspected = len(resources)
	appendSourceManifestCoverageCaveats(&check, input, lastAppliedParseErrors)
	if len(resources) == 0 {
		check.Status = CheckUnknown
		switch {
		case helmUnavailable:
			check.Summary = "Stored Helm manifests were unavailable in some namespaces, and no other original manifests were available."
		case !helmManifestsAvailable:
			check.Summary = "Helm manifests were unavailable, and no kubectl last-applied manifests were available."
		default:
			check.Summary = "No original Helm or kubectl last-applied manifests were available to inspect."
		}
		return check
	}

	deprecations := bp.DeprecationsByGroupVersion()
	for _, resource := range resources {
		entries := deprecations[resource.APIVersion]
		for _, entry := range entries {
			if entry.Kind != "" && entry.Kind != resource.Kind {
				continue
			}
			deprecated, depErr := parseMinor(entry.DeprecatedIn)
			removed, remErr := parseMinor(entry.RemovedIn)
			level := LevelReview
			appliesFrom := ""
			impact := "This manifest uses an API deprecated by the target Kubernetes version."
			if remErr == nil && target.AtLeast(removed) {
				level = LevelBlocker
				appliesFrom = minorString(removed)
				impact = "A future Helm upgrade or GitOps reconciliation will fail because the target Kubernetes version no longer serves this API."
			} else if depErr != nil || target.LessThan(deprecated) {
				continue
			} else {
				appliesFrom = minorString(deprecated)
			}

			group := groupForAPIVersion(resource.APIVersion)
			ref := &ResourceRef{Group: group, Kind: resource.Kind, Namespace: resource.Namespace, Name: resource.Name}
			var managedBy *ResourceRef
			if resource.Source == "Helm" {
				managedBy = &ResourceRef{Kind: "HelmRelease", Namespace: resource.SourceNamespace, Name: resource.SourceName}
			}
			check.Findings = append(check.Findings, Finding{
				RuleID:      check.ID,
				Title:       resource.APIVersion + " " + resource.Kind,
				Level:       level,
				Resource:    ref,
				ManagedBy:   managedBy,
				Evidence:    Evidence{Source: resource.Source, Path: "apiVersion", Detail: resource.APIVersion},
				AppliesFrom: appliesFrom,
				Impact:      impact,
				Remediation: "Update the source manifest to " + entry.Replacement + " and apply it before upgrading.",
				References:  append([]Reference(nil), manifestAPIReferences...),
			})
		}
	}
	if len(check.Findings) > 0 {
		check.Summary = fmt.Sprintf("%d source %s %s an API affected by the target version.", len(check.Findings), plural(len(check.Findings), "manifest", "manifests"), plural(len(check.Findings), "uses", "use"))
	} else if check.Caveat != "" {
		check.Status = CheckUnknown
		if input.Namespaces != nil {
			check.Summary = fmt.Sprintf("No incompatible APIs were found in %d readable source %s in the selected namespace scope, but manifest coverage is incomplete.", len(resources), plural(len(resources), "manifest", "manifests"))
		} else {
			check.Summary = fmt.Sprintf("No incompatible APIs were found in %d readable source %s, but manifest coverage is incomplete.", len(resources), plural(len(resources), "manifest", "manifests"))
		}
	}
	return check
}

func scanNodeHealth(input *Input) Check {
	check := Check{
		ID:       "node-health",
		Category: "Cluster health",
		Title:    "Node readiness",
		Status:   CheckPassed,
		Summary:  "All inspected nodes are Ready.",
		Scope:    "Live Node Ready conditions",
	}
	if input.Nodes == nil || len(input.Nodes) == 0 {
		check.Status = CheckUnknown
		check.Summary = "Nodes are unavailable; Radar could not verify cluster health before the upgrade."
		return check
	}
	check.Inspected = len(input.Nodes)
	for _, node := range input.Nodes {
		if node == nil || nodeReady(node) {
			continue
		}
		ref := &ResourceRef{Kind: "Node", Name: node.Name}
		check.Findings = append(check.Findings, Finding{
			RuleID:      check.ID,
			Title:       "Node is not Ready",
			Level:       LevelWarning,
			Resource:    ref,
			Evidence:    Evidence{Source: "live", Path: "status.conditions[Ready]"},
			Impact:      "Starting an upgrade while a node is unhealthy reduces capacity and makes disruption harder to distinguish from an existing failure.",
			Remediation: "Restore the node to Ready or remove it from the cluster before starting the upgrade.",
		})
	}
	if len(check.Findings) > 0 {
		check.Summary = fmt.Sprintf("%d %s %s not Ready.", len(check.Findings), plural(len(check.Findings), "node", "nodes"), plural(len(check.Findings), "is", "are"))
	}
	return check
}

func scanKubeletSkew(input *Input, target *utilversion.Version) Check {
	check := Check{
		ID:         "kubelet-version-skew",
		Category:   "Component compatibility",
		Title:      "Kubelet version skew",
		Status:     CheckPassed,
		Summary:    "All inspected kubelets are supported by the target control plane.",
		Scope:      "Node status kubelet versions",
		References: append([]Reference(nil), versionSkewReferences...),
	}
	if input.Nodes == nil || len(input.Nodes) == 0 {
		check.Status = CheckUnknown
		check.Summary = "Nodes are unavailable; Radar could not validate kubelet version skew."
		return check
	}
	check.Inspected = len(input.Nodes)
	minimum := minimumComponentVersion(target)
	unparsed := 0
	for _, node := range input.Nodes {
		if node == nil {
			continue
		}
		version, err := parseMinor(node.Status.NodeInfo.KubeletVersion)
		if err != nil {
			unparsed++
			continue
		}
		if version.LessThan(minimum) || version.GreaterThan(target) {
			ref := &ResourceRef{Kind: "Node", Name: node.Name}
			remediation := fmt.Sprintf("Upgrade this node's kubelet to at least Kubernetes %s before upgrading the control plane.", minorString(minimum))
			if version.GreaterThan(target) {
				remediation = fmt.Sprintf("This kubelet is newer than the Kubernetes %s target. Choose a target that supports kubelet %s or replace the node with a target-compatible version.", minorString(target), minorString(version))
			}
			check.Findings = append(check.Findings, Finding{
				RuleID:      check.ID,
				Title:       "Unsupported kubelet " + minorString(version),
				Level:       LevelBlocker,
				Resource:    ref,
				Evidence:    Evidence{Source: "live", Path: "status.nodeInfo.kubeletVersion", Detail: node.Status.NodeInfo.KubeletVersion},
				AppliesFrom: minorString(target),
				Impact:      fmt.Sprintf("Kubernetes %s supports kubelets from %s through %s.", minorString(target), minorString(minimum), minorString(target)),
				Remediation: remediation,
				References:  append([]Reference(nil), versionSkewReferences...),
			})
		}
	}
	if len(check.Findings) > 0 {
		check.Summary = fmt.Sprintf("%d %s %s outside the supported skew for Kubernetes %s.", len(check.Findings), plural(len(check.Findings), "kubelet", "kubelets"), plural(len(check.Findings), "is", "are"), minorString(target))
	}
	if unparsed > 0 {
		check.Caveat = fmt.Sprintf("The kubelet version could not be parsed on %d %s.", unparsed, plural(unparsed, "node", "nodes"))
	}
	if len(check.Findings) == 0 && unparsed > 0 {
		check.Status = CheckUnknown
		check.Summary = fmt.Sprintf("Radar could not parse the kubelet version on %d %s.", unparsed, plural(unparsed, "node", "nodes"))
	}
	return check
}

func scanKubeProxySkew(input *Input, target *utilversion.Version) Check {
	check := Check{
		ID:         "kube-proxy-version-skew",
		Category:   "Component compatibility",
		Title:      "kube-proxy version skew",
		Status:     CheckPassed,
		Summary:    "The detected kube-proxy version is supported by the target control plane.",
		Scope:      "kube-proxy DaemonSet images",
		References: append([]Reference(nil), versionSkewReferences...),
	}
	if input.DaemonSets == nil {
		check.Status = CheckUnknown
		check.Summary = "DaemonSets are unavailable; Radar could not inspect kube-proxy."
		return check
	}
	if input.Namespaces != nil && !containsString(input.Namespaces, "kube-system") {
		check.Status = CheckUnknown
		check.Summary = "kube-proxy could not be inspected because kube-system is outside the current namespace scope."
		return check
	}
	var proxies []*appsv1.DaemonSet
	for _, daemonSet := range input.DaemonSets {
		if daemonSet != nil && isKubeProxy(daemonSet) {
			proxies = append(proxies, daemonSet)
		}
	}
	if len(proxies) == 0 {
		check.Status = CheckNotApplicable
		check.Summary = "kube-proxy is provider-managed, replaced, or not exposed as a DaemonSet."
		return check
	}
	check.Inspected = len(proxies)
	minimum := minimumComponentVersion(target)
	unparsed := 0
	for _, daemonSet := range proxies {
		version, image := kubeProxyVersion(daemonSet)
		if version == nil {
			unparsed++
			continue
		}
		if version.LessThan(minimum) || version.GreaterThan(target) {
			ref := &ResourceRef{Group: "apps", Kind: "DaemonSet", Namespace: daemonSet.Namespace, Name: daemonSet.Name}
			check.Findings = append(check.Findings, Finding{
				RuleID:      check.ID,
				Title:       "Unsupported kube-proxy " + minorString(version),
				Level:       LevelBlocker,
				Resource:    ref,
				Evidence:    Evidence{Source: "live", Path: "spec.template.spec.containers[].image", Detail: image},
				AppliesFrom: minorString(target),
				Impact:      fmt.Sprintf("Kubernetes %s supports kube-proxy versions from %s through %s.", minorString(target), minorString(minimum), minorString(target)),
				Remediation: "Upgrade kube-proxy to a supported version as part of the cluster upgrade sequence.",
				References:  append([]Reference(nil), versionSkewReferences...),
			})
		}
	}
	if len(check.Findings) > 0 {
		check.Summary = fmt.Sprintf("%d kube-proxy %s %s outside the supported skew.", len(check.Findings), plural(len(check.Findings), "installation", "installations"), plural(len(check.Findings), "is", "are"))
	}
	if unparsed > 0 {
		check.Caveat = "The kube-proxy image version could not be determined for one or more installations."
	}
	if len(check.Findings) == 0 && unparsed > 0 {
		check.Status = CheckUnknown
		check.Summary = "Radar found kube-proxy but could not determine its Kubernetes version from the image."
	}
	return check
}

func scanGitRepo(input *Input, index *workloadIndex) Check {
	check := Check{
		ID:          "gitrepo-volume-removed",
		Category:    "Workload configuration",
		Title:       "gitRepo volume driver disabled",
		Status:      CheckPassed,
		Summary:     "No live workload uses a gitRepo volume.",
		Scope:       "Pod specs across seven workload kinds",
		AppliesFrom: "1.36",
		References:  append([]Reference(nil), gitRepoReferences...),
	}
	if input.Namespaces != nil {
		check.Caveat = scopedCoverageNote(input.Namespaces, "live workloads")
		check.Summary = "No inspected workload in the selected namespace scope uses a gitRepo volume."
	}
	subjects := workloadSubjects(input, index)
	check.Inspected = len(subjects)
	for _, subject := range subjects {
		for i, volume := range subject.spec.Volumes {
			if volume.GitRepo == nil {
				continue
			}
			check.Findings = append(check.Findings, workloadFinding(subject, check, check.Title, LevelBlocker,
				fmt.Sprintf("%s.volumes[%d].gitRepo", subject.pathPrefix, i),
				"Pods that reference a gitRepo volume cannot start because Kubernetes 1.36 disables the driver by default with no option to turn it back on.",
				"Clone the repository into an emptyDir from an init container, then mount that emptyDir into the workload containers."))
		}
	}
	if len(check.Findings) > 0 {
		check.Summary = fmt.Sprintf("%d %s %s the disabled gitRepo volume driver.", len(check.Findings), plural(len(check.Findings), "workload", "workloads"), plural(len(check.Findings), "uses", "use"))
	}
	if workloadsUnavailable(input) {
		check.Caveat = appendCaveat(check.Caveat, "One or more workload kinds were unavailable, so additional gitRepo usage may exist.")
	}
	if len(check.Findings) == 0 && (workloadsUnavailable(input) || input.Namespaces != nil) {
		check.Status = CheckUnknown
		if input.Namespaces != nil {
			check.Summary = "No gitRepo usage was found in the selected namespace scope, but workload coverage is incomplete."
		} else {
			check.Summary = "Workload coverage is incomplete; gitRepo volume usage could not be ruled out."
		}
	}
	return check
}

func scanFlexVolume(input *Input, index *workloadIndex) Check {
	check := Check{
		ID:          "flexvolume-kubeadm-support",
		Category:    "Storage",
		Title:       "FlexVolume upgrade exposure",
		Status:      CheckPassed,
		Summary:     "No FlexVolume usage was found in inspected workloads or PersistentVolumes.",
		Scope:       "Pod specs and PersistentVolume sources",
		AppliesFrom: "1.36",
		References:  append([]Reference(nil), changelog136References...),
	}
	if managedControlPlane(input) {
		check.Status = CheckNotApplicable
		check.Summary = "The Kubernetes 1.36 FlexVolume change is specific to kubeadm-managed control planes."
		return check
	}
	if countNonNil(input.Nodes) == 0 {
		check.Status = CheckUnknown
		check.Summary = "Nodes are unavailable; Radar could not determine whether this kubeadm-specific change applies."
		return check
	}
	if input.Namespaces != nil {
		check.Caveat = scopedCoverageNote(input.Namespaces, "live workloads")
		check.Summary = "No FlexVolume usage was found in workloads in the selected namespace scope or inspected PersistentVolumes."
	}
	subjects := workloadSubjects(input, index)
	check.Inspected = len(subjects) + countNonNil(input.PersistentVolumes)
	for _, subject := range subjects {
		for i, volume := range subject.spec.Volumes {
			if volume.FlexVolume == nil {
				continue
			}
			check.Findings = append(check.Findings, workloadFinding(subject, check, "Workload uses a FlexVolume volume", LevelWarning,
				fmt.Sprintf("%s.volumes[%d].flexVolume", subject.pathPrefix, i),
				"Kubernetes 1.36 removes kubeadm's integrated FlexVolume setup; kubeadm-managed clusters need explicit controller-manager configuration.",
				"Migrate the volume to a CSI driver, or verify the required custom kube-controller-manager image, flag, and mount before upgrading."))
		}
	}
	for _, volume := range input.PersistentVolumes {
		if volume == nil || volume.Spec.FlexVolume == nil {
			continue
		}
		ref := &ResourceRef{Kind: "PersistentVolume", Name: volume.Name}
		check.Findings = append(check.Findings, Finding{
			RuleID:      check.ID,
			Title:       "PersistentVolume uses FlexVolume",
			Level:       LevelWarning,
			Resource:    ref,
			Evidence:    Evidence{Source: "live", Path: "spec.flexVolume"},
			AppliesFrom: check.AppliesFrom,
			Impact:      "Kubernetes 1.36 removes kubeadm's integrated FlexVolume setup; kubeadm-managed clusters need explicit controller-manager configuration.",
			Remediation: "Migrate this volume to CSI, or verify the kubeadm FlexVolume compatibility steps before upgrading.",
			References:  append([]Reference(nil), check.References...),
		})
	}
	if len(check.Findings) > 0 {
		check.Summary = fmt.Sprintf("%d FlexVolume %s %s attention for Kubernetes 1.36.", len(check.Findings), plural(len(check.Findings), "reference", "references"), plural(len(check.Findings), "needs", "need"))
	}
	if workloadsUnavailable(input) || input.PersistentVolumes == nil {
		check.Caveat = appendCaveat(check.Caveat, "Workload or PersistentVolume coverage is incomplete, so additional FlexVolume usage may exist.")
	}
	if len(check.Findings) == 0 && (workloadsUnavailable(input) || input.PersistentVolumes == nil || input.Namespaces != nil) {
		check.Status = CheckUnknown
		if input.Namespaces != nil {
			check.Summary = "No FlexVolume usage was found in workloads in the selected namespace scope or inspected PersistentVolumes, but workload coverage is incomplete."
		} else {
			check.Summary = "Workload or PersistentVolume coverage is incomplete; FlexVolume usage could not be ruled out."
		}
	}
	return check
}

func managedControlPlane(input *Input) bool {
	if strings.EqualFold(input.Platform, "gke-autopilot") {
		return true
	}
	for _, node := range input.Nodes {
		if node == nil {
			continue
		}
		labels := node.GetLabels()
		if _, ok := labels["cloud.google.com/gke-nodepool"]; ok {
			return true
		}
		if _, ok := labels["cloud.google.com/gke-os-distribution"]; ok {
			return true
		}
		if _, ok := labels["eks.amazonaws.com/nodegroup"]; ok {
			return true
		}
		if _, ok := labels["eks.amazonaws.com/capacityType"]; ok {
			return true
		}
		for label := range labels {
			if strings.HasPrefix(label, "kubernetes.azure.com/") {
				return true
			}
		}
	}
	return false
}

func scanServiceExternalIPs(input *Input) Check {
	check := Check{
		ID:          "service-externalips-deprecated",
		Category:    "Networking",
		Title:       "Service externalIPs deprecation",
		Status:      CheckPassed,
		Summary:     "No Service uses spec.externalIPs.",
		Scope:       "Live Services",
		AppliesFrom: "1.36",
		References:  append([]Reference(nil), externalIPReferences...),
	}
	if input.Namespaces != nil {
		check.Caveat = scopedCoverageNote(input.Namespaces, "Services")
		check.Summary = "No inspected Service in the selected namespace scope uses spec.externalIPs."
	}
	if input.Services == nil {
		check.Status = CheckUnknown
		check.Summary = "Services are unavailable; Radar could not inspect spec.externalIPs."
		return check
	}
	check.Inspected = countNonNil(input.Services)
	for _, service := range input.Services {
		if service == nil || len(service.Spec.ExternalIPs) == 0 {
			continue
		}
		ref := &ResourceRef{Kind: "Service", Namespace: service.Namespace, Name: service.Name}
		check.Findings = append(check.Findings, Finding{
			RuleID:      check.ID,
			Title:       check.Title,
			Level:       LevelReview,
			Resource:    ref,
			ManagedBy:   managedByRef(service, *ref),
			Evidence:    Evidence{Source: "live", Path: "spec.externalIPs", Detail: strings.Join(service.Spec.ExternalIPs, ", ")},
			AppliesFrom: check.AppliesFrom,
			Impact:      "Kubernetes 1.36 deprecates this insecure Service field and plans to disable its behavior in a future release.",
			Remediation: "Move this exposure to a LoadBalancer Service, Gateway API, or another implementation with explicit administrative control.",
			References:  append([]Reference(nil), externalIPReferences...),
		})
	}
	if len(check.Findings) > 0 {
		check.Summary = fmt.Sprintf("%d %s %s deprecated spec.externalIPs.", len(check.Findings), plural(len(check.Findings), "Service", "Services"), plural(len(check.Findings), "uses", "use"))
	} else if input.Namespaces != nil {
		check.Status = CheckUnknown
		check.Summary = "No deprecated spec.externalIPs usage was found in Services in the selected namespace scope, but Service coverage is incomplete."
	}
	return check
}

func scanRenamedMetrics(input *Input) Check {
	check := Check{
		ID:          "renamed-control-plane-metrics",
		Category:    "Observability",
		Title:       "Renamed Kubernetes metrics",
		Status:      CheckPassed,
		Summary:     "No inspected PrometheusRule references a metric renamed in Kubernetes 1.36.",
		Scope:       "Prometheus Operator rule expressions",
		AppliesFrom: "1.36",
		References:  append([]Reference(nil), changelog136References...),
	}
	if !input.PrometheusRulesDiscoveryAvailable {
		check.Status = CheckUnknown
		check.Summary = "API discovery is unavailable; Radar could not determine whether PrometheusRule is installed."
		return check
	}
	if !input.PrometheusRulesInstalled {
		check.Status = CheckNotApplicable
		check.Summary = "PrometheusRule is not installed in this cluster."
		return check
	}
	if input.Namespaces != nil {
		check.Caveat = scopedCoverageNote(input.Namespaces, "PrometheusRules")
		check.Summary = "No inspected PrometheusRule in the selected namespace scope references a metric renamed in Kubernetes 1.36."
	}
	if len(input.PrometheusRuleUnavailableNamespaces) > 0 {
		check.Caveat = appendCaveat(check.Caveat, "PrometheusRules could not be read in: "+formatBoundedList(input.PrometheusRuleUnavailableNamespaces, ", ")+".")
	}
	if input.PrometheusRules == nil {
		check.Status = CheckUnknown
		if check.Caveat != "" {
			check.Summary = "PrometheusRules are installed but unavailable in the current namespace scope."
		} else {
			check.Summary = "PrometheusRules are installed but unavailable to Radar."
		}
		return check
	}
	check.Inspected = len(input.PrometheusRules)
	renamed := map[string]string{
		"volume_operation_total_errors": "volume_operation_errors_total",
		"etcd_bookmark_counts":          "etcd_bookmark_total",
	}
	for _, rule := range input.PrometheusRules {
		if rule == nil {
			continue
		}
		expressions := prometheusRuleExpressions(rule.Object)
		for oldName, newName := range renamed {
			if !containsMetric(expressions, oldName) {
				continue
			}
			ref := &ResourceRef{Group: "monitoring.coreos.com", Kind: "PrometheusRule", Namespace: rule.GetNamespace(), Name: rule.GetName()}
			check.Findings = append(check.Findings, Finding{
				RuleID:      check.ID,
				Title:       oldName + " was renamed",
				Level:       LevelWarning,
				Resource:    ref,
				Evidence:    Evidence{Source: "live", Path: "spec.groups[].rules[].expr", Detail: oldName},
				AppliesFrom: check.AppliesFrom,
				Impact:      "This alert or recording rule will stop receiving data after the metric rename in Kubernetes 1.36.",
				Remediation: "Replace " + oldName + " with " + newName + " in the rule expression before upgrading.",
				References:  append([]Reference(nil), changelog136References...),
			})
		}
	}
	if len(check.Findings) > 0 {
		check.Summary = fmt.Sprintf("%d %s %s a metric renamed in Kubernetes 1.36.", len(check.Findings), plural(len(check.Findings), "PrometheusRule", "PrometheusRules"), plural(len(check.Findings), "references", "reference"))
	} else if check.Caveat != "" {
		check.Status = CheckUnknown
		if input.Namespaces != nil {
			check.Summary = "No renamed metrics were found in readable PrometheusRules in the selected namespace scope, but rule coverage is incomplete."
		} else {
			check.Summary = "No renamed metrics were found in readable PrometheusRules, but rule coverage is incomplete."
		}
	}
	return check
}

type podSpecSubject struct {
	obj        metav1.Object
	resource   ResourceRef
	spec       corev1.PodSpec
	pathPrefix string
}

func workloadSubjects(input *Input, index *workloadIndex) []podSpecSubject {
	var subjects []podSpecSubject
	for _, pod := range input.Pods {
		if pod != nil && !index.podCoveredByScannedWorkload(pod) {
			subjects = append(subjects, podSpecSubject{obj: pod, resource: ResourceRef{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name}, spec: pod.Spec, pathPrefix: "spec"})
		}
	}
	for _, deployment := range input.Deployments {
		if deployment != nil {
			subjects = append(subjects, podSpecSubject{obj: deployment, resource: ResourceRef{Group: "apps", Kind: "Deployment", Namespace: deployment.Namespace, Name: deployment.Name}, spec: deployment.Spec.Template.Spec, pathPrefix: "spec.template.spec"})
		}
	}
	for _, replicaSet := range input.ReplicaSets {
		if replicaSet != nil && !index.replicaSetCoveredByDeployment(replicaSet) {
			subjects = append(subjects, podSpecSubject{obj: replicaSet, resource: ResourceRef{Group: "apps", Kind: "ReplicaSet", Namespace: replicaSet.Namespace, Name: replicaSet.Name}, spec: replicaSet.Spec.Template.Spec, pathPrefix: "spec.template.spec"})
		}
	}
	for _, statefulSet := range input.StatefulSets {
		if statefulSet != nil {
			subjects = append(subjects, podSpecSubject{obj: statefulSet, resource: ResourceRef{Group: "apps", Kind: "StatefulSet", Namespace: statefulSet.Namespace, Name: statefulSet.Name}, spec: statefulSet.Spec.Template.Spec, pathPrefix: "spec.template.spec"})
		}
	}
	for _, daemonSet := range input.DaemonSets {
		if daemonSet != nil {
			subjects = append(subjects, podSpecSubject{obj: daemonSet, resource: ResourceRef{Group: "apps", Kind: "DaemonSet", Namespace: daemonSet.Namespace, Name: daemonSet.Name}, spec: daemonSet.Spec.Template.Spec, pathPrefix: "spec.template.spec"})
		}
	}
	for _, job := range input.Jobs {
		if job != nil && !index.jobCoveredByCronJob(job) {
			subjects = append(subjects, podSpecSubject{obj: job, resource: ResourceRef{Group: "batch", Kind: "Job", Namespace: job.Namespace, Name: job.Name}, spec: job.Spec.Template.Spec, pathPrefix: "spec.template.spec"})
		}
	}
	for _, cronJob := range input.CronJobs {
		if cronJob != nil {
			subjects = append(subjects, podSpecSubject{obj: cronJob, resource: ResourceRef{Group: "batch", Kind: "CronJob", Namespace: cronJob.Namespace, Name: cronJob.Name}, spec: cronJob.Spec.JobTemplate.Spec.Template.Spec, pathPrefix: "spec.jobTemplate.spec.template.spec"})
		}
	}
	return subjects
}

func workloadFinding(subject podSpecSubject, check Check, title string, level Level, path, impact, remediation string) Finding {
	resource := subject.resource
	return Finding{
		RuleID:      check.ID,
		Title:       title,
		Level:       level,
		Resource:    &resource,
		ManagedBy:   managedByRef(subject.obj, resource),
		Evidence:    Evidence{Source: "live", Path: path},
		AppliesFrom: check.AppliesFrom,
		Impact:      impact,
		Remediation: remediation,
		References:  append([]Reference(nil), check.References...),
	}
}

func managedByRef(obj metav1.Object, resource ResourceRef) *ResourceRef {
	refs := topology.SynthesizeManagedBy(obj, resource.Kind, resource.Namespace, resource.Name, nil, nil, nil)
	if len(refs) == 0 {
		return nil
	}
	ref := refs[0]
	return &ResourceRef{Group: ref.Group, Kind: ref.Kind, Namespace: ref.Namespace, Name: ref.Name}
}

func lastAppliedResources(objects []metav1.Object) ([]ManifestResource, int) {
	const annotation = "kubectl.kubernetes.io/last-applied-configuration"
	var resources []ManifestResource
	parseErrors := 0
	for _, object := range objects {
		if object == nil {
			continue
		}
		raw := object.GetAnnotations()[annotation]
		if raw == "" {
			continue
		}
		var manifest unstructured.Unstructured
		if json.Unmarshal([]byte(raw), &manifest.Object) != nil || manifest.GetAPIVersion() == "" || manifest.GetKind() == "" {
			parseErrors++
			continue
		}
		if manifest.GetName() == "" {
			manifest.SetName(object.GetName())
		}
		if manifest.GetNamespace() == "" {
			manifest.SetNamespace(object.GetNamespace())
		}
		resources = append(resources, ManifestResource{
			APIVersion: manifest.GetAPIVersion(),
			Kind:       manifest.GetKind(),
			Namespace:  manifest.GetNamespace(),
			Name:       manifest.GetName(),
			Source:     "kubectl last-applied",
			Object:     manifest.DeepCopy(),
		})
	}
	return resources, parseErrors
}

func dedupeManifestResources(resources []ManifestResource) []ManifestResource {
	seen := make(map[string]bool)
	result := make([]ManifestResource, 0, len(resources))
	for _, resource := range resources {
		key := strings.Join([]string{resource.APIVersion, resource.Kind, resource.Namespace, resource.Name}, "\x00")
		if resource.APIVersion == "" || resource.Kind == "" || resource.Name == "" || seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, resource)
	}
	return result
}

func nodeReady(node *corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func minimumComponentVersion(target *utilversion.Version) *utilversion.Version {
	minor := target.Minor()
	if minor > 3 {
		minor -= 3
	} else {
		minor = 0
	}
	return utilversion.MajorMinor(target.Major(), minor)
}

func isKubeProxy(daemonSet *appsv1.DaemonSet) bool {
	return daemonSet.Name == "kube-proxy" || daemonSet.Labels["k8s-app"] == "kube-proxy" || daemonSet.Labels["component"] == "kube-proxy"
}

var imageVersionPattern = regexp.MustCompile(`(?:^|[:@/])v?(\d+\.\d+)(?:\.\d+)?(?:[-+@]|$)`)

func kubeProxyVersion(daemonSet *appsv1.DaemonSet) (*utilversion.Version, string) {
	for _, container := range daemonSet.Spec.Template.Spec.Containers {
		if container.Name != "kube-proxy" && len(daemonSet.Spec.Template.Spec.Containers) > 1 {
			continue
		}
		match := imageVersionPattern.FindStringSubmatch(container.Image)
		if len(match) != 2 {
			return nil, container.Image
		}
		version, err := parseMinor(match[1])
		if err != nil {
			return nil, container.Image
		}
		return version, container.Image
	}
	return nil, ""
}

func prometheusRuleExpressions(object map[string]any) []string {
	groups, _, _ := nestedSlice(object, "spec", "groups")
	var expressions []string
	for _, group := range groups {
		groupMap, _ := group.(map[string]any)
		rules, _ := groupMap["rules"].([]any)
		for _, rule := range rules {
			ruleMap, _ := rule.(map[string]any)
			if expr, ok := ruleMap["expr"].(string); ok {
				expressions = append(expressions, expr)
			}
		}
	}
	return expressions
}

func nestedSlice(object map[string]any, fields ...string) ([]any, bool, error) {
	current := any(object)
	for _, field := range fields {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, false, nil
		}
		current, ok = m[field]
		if !ok {
			return nil, false, nil
		}
	}
	value, ok := current.([]any)
	return value, ok, nil
}

func containsMetric(expressions []string, metric string) bool {
	pattern := regexp.MustCompile(`(^|[^a-zA-Z0-9_:])` + regexp.QuoteMeta(metric) + `([^a-zA-Z0-9_:]|$)`)
	for _, expression := range expressions {
		if pattern.MatchString(expression) {
			return true
		}
	}
	return false
}

func workloadsUnavailable(input *Input) bool {
	return input.Pods == nil || input.Deployments == nil || input.ReplicaSets == nil || input.StatefulSets == nil || input.DaemonSets == nil || input.Jobs == nil || input.CronJobs == nil
}

func scopedCoverageNote(namespaces []string, subject string) string {
	names := append([]string(nil), namespaces...)
	sort.Strings(names)
	if len(names) == 0 {
		return ""
	}
	label, verb := "namespace", "was"
	if len(names) != 1 {
		label, verb = "namespaces", "were"
	}
	return fmt.Sprintf("Only %s %s %s inspected for %s.", label, formatBoundedList(names, ", "), verb, subject)
}

func formatBoundedList(values []string, separator string) string {
	values = append([]string(nil), values...)
	sort.Strings(values)
	unique := values[:0]
	for _, value := range values {
		if len(unique) == 0 || unique[len(unique)-1] != value {
			unique = append(unique, value)
		}
	}
	const limit = 5
	if len(unique) <= limit {
		return strings.Join(unique, separator)
	}
	return fmt.Sprintf("%s%sand %d more", strings.Join(unique[:limit], separator), separator, len(unique)-limit)
}

func appendCaveat(existing, addition string) string {
	if existing == "" {
		return addition
	}
	if addition == "" {
		return existing
	}
	return existing + " " + addition
}

func appendSourceManifestCoverageCaveats(check *Check, input *Input, lastAppliedParseErrors int) {
	if input.ManifestResources == nil {
		if input.SourceObjects == nil {
			check.Caveat = appendCaveat(check.Caveat, "Stored Helm release manifests and kubectl last-applied configuration were unavailable.")
		} else {
			check.Caveat = appendCaveat(check.Caveat, "Stored Helm release manifests were unavailable; only kubectl last-applied configuration was inspected.")
		}
	} else if len(input.HelmUnavailableNamespaces) > 0 {
		check.Caveat = appendCaveat(check.Caveat, "Stored Helm release manifests could not be read in: "+formatBoundedList(input.HelmUnavailableNamespaces, ", ")+".")
	}
	if input.HelmScopedNamespaces != nil {
		check.Caveat = appendCaveat(check.Caveat, scopedCoverageNote(input.HelmScopedNamespaces, "stored Helm release manifests"))
	}
	if input.ManifestParseErrors > 0 {
		check.Caveat = appendCaveat(check.Caveat, fmt.Sprintf("%d Helm manifest %s could not be parsed.", input.ManifestParseErrors, plural(input.ManifestParseErrors, "document", "documents")))
	}
	if lastAppliedParseErrors > 0 {
		check.Caveat = appendCaveat(check.Caveat, fmt.Sprintf("%d kubectl last-applied %s could not be parsed.", lastAppliedParseErrors, plural(lastAppliedParseErrors, "annotation", "annotations")))
	}
	if len(input.SourceObjectUnavailableKinds) > 0 {
		check.Caveat = appendCaveat(check.Caveat, "kubectl last-applied configuration could not be inspected for: "+formatBoundedList(input.SourceObjectUnavailableKinds, ", ")+".")
	}
}

func unavailableKinds(input *Input) []string {
	unavailable := append([]string(nil), input.AdmissionWebhookUnavailableKinds...)
	checks := []struct {
		name      string
		available bool
	}{
		{"pods", input.Pods != nil},
		{"deployments", input.Deployments != nil},
		{"replicasets", input.ReplicaSets != nil},
		{"statefulsets", input.StatefulSets != nil},
		{"daemonsets", input.DaemonSets != nil},
		{"jobs", input.Jobs != nil},
		{"cronjobs", input.CronJobs != nil},
		{"services", input.Services != nil},
		{"poddisruptionbudgets", input.PodDisruptionBudgets != nil},
		{"persistentvolumes", input.PersistentVolumes != nil},
		{"nodes", input.Nodes != nil},
		{"customresourcedefinitions", input.CustomResourceDefinitions != nil},
		{"apiservices", input.APIServices != nil},
	}
	for _, item := range checks {
		if !item.available {
			unavailable = append(unavailable, item.name)
		}
	}
	sort.Strings(unavailable)
	return unavailable
}

var targetMinorPattern = regexp.MustCompile(`^v?\d{1,4}\.\d{1,4}$`)

// ValidateTarget validates the requested version before evidence collection begins.
func ValidateTarget(currentVersion, targetVersion string) error {
	_, _, err := validateUpgradeVersions(currentVersion, targetVersion)
	return err
}

func validateUpgradeVersions(currentVersion, targetVersion string) (*utilversion.Version, *utilversion.Version, error) {
	current, err := parseMinor(currentVersion)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %q", ErrInvalidCurrentVersion, currentVersion)
	}
	if targetVersion == "" {
		targetVersion = current.AddMinor(1).String()
	}
	target, err := parseTargetMinor(targetVersion)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %q", ErrInvalidTargetVersion, targetVersion)
	}
	if !target.GreaterThan(current) {
		return nil, nil, ErrNonForwardTarget
	}
	return current, target, nil
}

func parseTargetMinor(raw string) (*utilversion.Version, error) {
	raw = strings.TrimSpace(raw)
	if !targetMinorPattern.MatchString(raw) {
		return nil, fmt.Errorf("invalid version")
	}
	return parseMinor(raw)
}

func parseMinor(raw string) (*utilversion.Version, error) {
	raw = strings.TrimSpace(strings.TrimPrefix(raw, "v"))
	v, err := utilversion.ParseGeneric(raw)
	if err != nil || v.Major() == 0 {
		return nil, fmt.Errorf("invalid version")
	}
	return utilversion.MajorMinor(v.Major(), v.Minor()), nil
}

func minorString(v *utilversion.Version) string {
	return fmt.Sprintf("%d.%d", v.Major(), v.Minor())
}

func groupForAPIVersion(apiVersion string) string {
	group, _, ok := strings.Cut(apiVersion, "/")
	if !ok {
		return ""
	}
	return group
}

var strictNetworkSourceGroups = map[string]string{
	"CronJob": "batch", "DaemonSet": "apps", "Deployment": "apps", "EndpointSlice": "discovery.k8s.io",
	"Endpoints": "", "Job": "batch", "NetworkPolicy": "networking.k8s.io", "Node": "",
	"Pod": "", "ReplicaSet": "apps", "Service": "", "StatefulSet": "apps",
}

// IsUpgradeSourceObjectCandidate reports whether a currently served resource
// can carry source evidence used by an upgrade check.
func IsUpgradeSourceObjectCandidate(kind, group string) bool {
	if allowedGroup, ok := strictNetworkSourceGroups[kind]; ok && allowedGroup == group {
		return true
	}
	for _, entry := range bp.DeprecationTable {
		deprecatedGroup := groupForAPIVersion(entry.GroupVersion)
		replacementGroup := groupForAPIVersion(entry.Replacement)
		if (entry.Kind == kind && (deprecatedGroup == group || replacementGroup == group)) || (entry.Kind == "" && deprecatedGroup == group) {
			return true
		}
	}
	return false
}

func plural(count int, singular, plural string) string {
	if count == 1 {
		return singular
	}
	return plural
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func countNonNil[T any](items []*T) int {
	count := 0
	for _, item := range items {
		if item != nil {
			count++
		}
	}
	return count
}

type workloadKey struct {
	group     string
	kind      string
	namespace string
	name      string
}

type workloadIndex struct {
	coveredPodOwners map[workloadKey]struct{}
	deployments      map[workloadKey]*appsv1.Deployment
	cronJobs         map[workloadKey]*batchv1.CronJob
}

func newWorkloadIndex(input *Input) *workloadIndex {
	index := &workloadIndex{
		coveredPodOwners: make(map[workloadKey]struct{}),
		deployments:      make(map[workloadKey]*appsv1.Deployment),
		cronJobs:         make(map[workloadKey]*batchv1.CronJob),
	}
	for _, deployment := range input.Deployments {
		if deployment == nil {
			continue
		}
		key := workloadKey{group: "apps", kind: "Deployment", namespace: deployment.Namespace, name: deployment.Name}
		index.deployments[key] = deployment
		index.coveredPodOwners[key] = struct{}{}
	}
	for _, cronJob := range input.CronJobs {
		if cronJob == nil {
			continue
		}
		key := workloadKey{group: "batch", kind: "CronJob", namespace: cronJob.Namespace, name: cronJob.Name}
		index.cronJobs[key] = cronJob
		index.coveredPodOwners[key] = struct{}{}
	}
	addWorkloads(index.coveredPodOwners, "apps", "ReplicaSet", input.ReplicaSets, func(item *appsv1.ReplicaSet) metav1.Object { return item })
	addWorkloads(index.coveredPodOwners, "apps", "StatefulSet", input.StatefulSets, func(item *appsv1.StatefulSet) metav1.Object { return item })
	addWorkloads(index.coveredPodOwners, "apps", "DaemonSet", input.DaemonSets, func(item *appsv1.DaemonSet) metav1.Object { return item })
	addWorkloads(index.coveredPodOwners, "batch", "Job", input.Jobs, func(item *batchv1.Job) metav1.Object { return item })
	return index
}

func addWorkloads[T any](dest map[workloadKey]struct{}, group, kind string, items []*T, object func(*T) metav1.Object) {
	for _, item := range items {
		if item == nil {
			continue
		}
		obj := object(item)
		dest[workloadKey{group: group, kind: kind, namespace: obj.GetNamespace(), name: obj.GetName()}] = struct{}{}
	}
}

func (index *workloadIndex) podCoveredByScannedWorkload(pod *corev1.Pod) bool {
	owner := controllerOwner(pod.OwnerReferences)
	if owner == nil {
		return false
	}
	key, ok := ownerKey(owner, pod.Namespace)
	if !ok {
		return false
	}
	_, covered := index.coveredPodOwners[key]
	return covered
}

func (index *workloadIndex) replicaSetCoveredByDeployment(replicaSet *appsv1.ReplicaSet) bool {
	owner := controllerOwner(replicaSet.OwnerReferences)
	if owner == nil {
		return false
	}
	key, ok := ownerKey(owner, replicaSet.Namespace)
	if !ok {
		return false
	}
	deployment, covered := index.deployments[key]
	return covered && reflect.DeepEqual(replicaSet.Spec.Template.Spec, deployment.Spec.Template.Spec)
}

func (index *workloadIndex) jobCoveredByCronJob(job *batchv1.Job) bool {
	owner := controllerOwner(job.OwnerReferences)
	if owner == nil {
		return false
	}
	key, ok := ownerKey(owner, job.Namespace)
	if !ok {
		return false
	}
	cronJob, covered := index.cronJobs[key]
	return covered && reflect.DeepEqual(job.Spec.Template.Spec, cronJob.Spec.JobTemplate.Spec.Template.Spec)
}

func ownerKey(owner *metav1.OwnerReference, namespace string) (workloadKey, bool) {
	groupVersion, err := schema.ParseGroupVersion(owner.APIVersion)
	if err != nil {
		return workloadKey{}, false
	}
	return workloadKey{group: groupVersion.Group, kind: owner.Kind, namespace: namespace, name: owner.Name}, true
}

func controllerOwner(refs []metav1.OwnerReference) *metav1.OwnerReference {
	for i := range refs {
		if refs[i].Controller != nil && *refs[i].Controller {
			return &refs[i]
		}
	}
	return nil
}
