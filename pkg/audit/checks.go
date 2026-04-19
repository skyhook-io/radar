package audit

import (
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// RunChecks runs all best-practice checks against the provided resources
// and returns aggregated results.
func RunChecks(input *CheckInput) *ScanResults {
	if input == nil {
		return &ScanResults{Summary: ScanSummary{Categories: map[string]CategorySummary{}}}
	}

	var findings []Finding

	// Build indexes needed by cross-resource checks
	podsBySelector := indexPodsByLabels(input.Pods)
	hpaTargets := indexHPATargets(input)
	pdbSelectors := collectPDBSelectors(input.PodDisruptionBudgets)
	servicesByName := indexServicesByName(input.Services)

	// --- Security checks (container-level, attributed to owning workload) ---
	findings = append(findings, checkWorkloadPodSpecs(input)...)

	// --- Reliability checks ---
	findings = append(findings, checkSingleReplica(input.Deployments, hpaTargets)...)
	// Only check PDB coverage if we can actually list PDBs (nil = RBAC denied, not "none exist")
	if input.PodDisruptionBudgets != nil {
		findings = append(findings, checkMissingPDB(input.Deployments, input.StatefulSets, pdbSelectors)...)
	}
	findings = append(findings, checkMissingTopologySpread(input.Deployments, input.StatefulSets)...)
	findings = append(findings, checkPodHARisk(input.Pods, input.Deployments)...)

	// --- Efficiency checks are included in checkWorkloadPodSpecs ---
	findings = append(findings, checkOrphanConfigMapsSecrets(input)...)
	findings = append(findings, checkSecretInConfigMap(input.ConfigMaps)...)

	// --- Cross-resource checks ---
	findings = append(findings, checkServiceNoMatchingPods(input.Services, podsBySelector)...)
	findings = append(findings, checkIngressNoMatchingService(input.Ingresses, servicesByName)...)

	// --- Utilization checks (optional, only when metrics provided) ---
	findings = append(findings, checkResourceUtilization(input.PodMetrics)...)

	// --- Deprecated API checks ---
	findings = append(findings, checkDeprecatedAPIs(input.ServedAPIs, input.ClusterVersion)...)

	return buildResults(findings)
}

// ============================================================================
// Pod spec checks (security, reliability, efficiency)
// Applied to workload pod templates; falls back to bare pods.
// ============================================================================

func checkWorkloadPodSpecs(input *CheckInput) []Finding {
	var findings []Finding

	// Collect pod specs from workloads (attributed to the workload, not individual pods)
	type workloadPodSpec struct {
		kind, namespace, name string
		spec                  corev1.PodSpec
	}
	var specs []workloadPodSpec

	for _, d := range input.Deployments {
		specs = append(specs, workloadPodSpec{"Deployment", d.Namespace, d.Name, d.Spec.Template.Spec})
	}
	for _, ss := range input.StatefulSets {
		specs = append(specs, workloadPodSpec{"StatefulSet", ss.Namespace, ss.Name, ss.Spec.Template.Spec})
	}
	for _, ds := range input.DaemonSets {
		specs = append(specs, workloadPodSpec{"DaemonSet", ds.Namespace, ds.Name, ds.Spec.Template.Spec})
	}

	// Bare pods (no ownerReferences) get checked directly
	for _, p := range input.Pods {
		if len(p.OwnerReferences) == 0 {
			specs = append(specs, workloadPodSpec{"Pod", p.Namespace, p.Name, p.Spec})
		}
	}

	// Index ServiceAccounts and LimitRanges by namespace for inheritance lookups.
	saByKey := indexServiceAccounts(input.ServiceAccounts)
	limitsByNs := indexLimitRangesByNamespace(input.LimitRanges)

	for _, w := range specs {
		findings = append(findings, checkPodSpecSecurity(w.kind, w.namespace, w.name, w.spec, saByKey)...)
		findings = append(findings, checkPodSpecReliability(w.kind, w.namespace, w.name, w.spec)...)
		findings = append(findings, checkPodSpecEfficiency(w.kind, w.namespace, w.name, w.spec, limitsByNs[w.namespace])...)
		findings = append(findings, checkPodSpecVolumes(w.kind, w.namespace, w.name, w.spec)...)
	}
	return findings
}

// indexServiceAccounts returns a map keyed by "namespace/name".
func indexServiceAccounts(sas []*corev1.ServiceAccount) map[string]*corev1.ServiceAccount {
	if len(sas) == 0 {
		return nil
	}
	m := make(map[string]*corev1.ServiceAccount, len(sas))
	for _, sa := range sas {
		m[sa.Namespace+"/"+sa.Name] = sa
	}
	return m
}

// indexLimitRangesByNamespace groups LimitRanges by namespace.
func indexLimitRangesByNamespace(lrs []*corev1.LimitRange) map[string][]*corev1.LimitRange {
	if len(lrs) == 0 {
		return nil
	}
	m := make(map[string][]*corev1.LimitRange)
	for _, lr := range lrs {
		m[lr.Namespace] = append(m[lr.Namespace], lr)
	}
	return m
}

// containerDefaultsFromLimitRanges reports which container resource types
// (cpu/memory requests/limits) would be filled in by admission based on the
// namespace's LimitRange defaults. Only LimitRange items with Type=Container
// contribute to container defaults.
type containerDefaults struct {
	cpuRequest, memoryRequest bool
	cpuLimit, memoryLimit     bool
}

func containerDefaultsFromLimitRanges(lrs []*corev1.LimitRange) containerDefaults {
	var d containerDefaults
	for _, lr := range lrs {
		for _, item := range lr.Spec.Limits {
			if item.Type != corev1.LimitTypeContainer {
				continue
			}
			// DefaultRequest covers requests; Default covers limits.
			if _, ok := item.DefaultRequest[corev1.ResourceCPU]; ok {
				d.cpuRequest = true
			}
			if _, ok := item.DefaultRequest[corev1.ResourceMemory]; ok {
				d.memoryRequest = true
			}
			if _, ok := item.Default[corev1.ResourceCPU]; ok {
				d.cpuLimit = true
			}
			if _, ok := item.Default[corev1.ResourceMemory]; ok {
				d.memoryLimit = true
			}
		}
	}
	return d
}

// ============================================================================
// Security checks
// ============================================================================

func checkPodSpecSecurity(kind, namespace, name string, spec corev1.PodSpec, saByKey map[string]*corev1.ServiceAccount) []Finding {
	var findings []Finding
	f := func(checkID, severity, msg string) {
		findings = append(findings, Finding{
			Kind: kind, Namespace: namespace, Name: name,
			CheckID: checkID, Category: CategorySecurity, Severity: severity, Message: msg,
		})
	}

	// Pod-level checks
	if spec.HostNetwork {
		f("hostNetwork", SeverityWarning, "Pod uses host network")
	}
	if spec.HostPID {
		f("hostPID", SeverityDanger, "Pod uses host PID namespace")
	}
	if spec.HostIPC {
		f("hostIPC", SeverityDanger, "Pod uses host IPC namespace")
	}

	// automountServiceAccountToken: honors both pod-level and SA-level settings.
	// Pod-level takes precedence. If neither is explicitly false, the token is
	// auto-mounted. Only flag when the effective value is true (or unset, which
	// defaults to true per K8s).
	if tokenAutoMounted(namespace, spec, saByKey) {
		f("automountServiceAccountToken", SeverityWarning, "Service account token is auto-mounted")
	}

	// Container-level checks (iterate init and regular separately to avoid
	// mutating the InitContainers backing array via append).
	// Pod-level SecurityContext is passed so container checks can honor
	// fields like runAsNonRoot/runAsUser that inherit from the pod.
	for i := range spec.InitContainers {
		checkContainerSecurity(f, &spec.InitContainers[i], spec.SecurityContext)
	}
	for i := range spec.Containers {
		checkContainerSecurity(f, &spec.Containers[i], spec.SecurityContext)
	}

	return findings
}

// tokenAutoMounted reports whether a service account token would be mounted
// into the pod per K8s effective-value rules. Pod-level
// automountServiceAccountToken overrides the ServiceAccount-level setting;
// when neither is set, the default is true (token is mounted).
func tokenAutoMounted(namespace string, spec corev1.PodSpec, saByKey map[string]*corev1.ServiceAccount) bool {
	if spec.AutomountServiceAccountToken != nil {
		return *spec.AutomountServiceAccountToken
	}
	saName := spec.ServiceAccountName
	if saName == "" {
		saName = "default"
	}
	if sa, ok := saByKey[namespace+"/"+saName]; ok &&
		sa.AutomountServiceAccountToken != nil && !*sa.AutomountServiceAccountToken {
		return false
	}
	return true
}

// effectivelyNonRoot reports whether a container is guaranteed not to run as
// root, merging the pod-level PodSecurityContext with the container-level
// override. Each field (runAsNonRoot, runAsUser) independently inherits from
// the pod and is overridden by any non-nil container-level value. After the
// merge, the container is non-root if runAsNonRoot is true OR runAsUser is
// set to a non-zero UID.
func effectivelyNonRoot(sc *corev1.SecurityContext, podSC *corev1.PodSecurityContext) bool {
	var runAsNonRoot *bool
	var runAsUser *int64
	if podSC != nil {
		runAsNonRoot = podSC.RunAsNonRoot
		runAsUser = podSC.RunAsUser
	}
	if sc != nil {
		if sc.RunAsNonRoot != nil {
			runAsNonRoot = sc.RunAsNonRoot
		}
		if sc.RunAsUser != nil {
			runAsUser = sc.RunAsUser
		}
	}
	if runAsNonRoot != nil && *runAsNonRoot {
		return true
	}
	if runAsUser != nil && *runAsUser != 0 {
		return true
	}
	return false
}

func checkContainerSecurity(f func(string, string, string), c *corev1.Container, podSC *corev1.PodSecurityContext) {
	sc := c.SecurityContext

	if !effectivelyNonRoot(sc, podSC) {
		f("runAsRoot", SeverityWarning, fmt.Sprintf("Container %q may run as root (runAsNonRoot not set)", c.Name))
	}

	if sc != nil && sc.Privileged != nil && *sc.Privileged {
		f("privileged", SeverityDanger, fmt.Sprintf("Container %q runs in privileged mode", c.Name))
	}

	if sc == nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		f("privilegeEscalation", SeverityDanger, fmt.Sprintf("Container %q allows privilege escalation", c.Name))
	}

	if sc == nil || sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
		f("readOnlyRootFs", SeverityWarning, fmt.Sprintf("Container %q does not use a read-only root filesystem", c.Name))
	}

	if sc != nil && sc.Capabilities != nil {
		for _, cap := range sc.Capabilities.Add {
			capStr := strings.ToUpper(string(cap))
			switch capStr {
			case "SYS_ADMIN", "NET_ADMIN", "ALL":
				f("dangerousCapabilities", SeverityDanger, fmt.Sprintf("Container %q adds dangerous capability %s", c.Name, cap))
			case "NET_RAW", "SYS_PTRACE", "MKNOD", "DAC_OVERRIDE":
				f("insecureCapabilities", SeverityWarning, fmt.Sprintf("Container %q adds insecure capability %s", c.Name, cap))
			}
		}
	}
}

// ============================================================================
// Volume security checks
// ============================================================================

// sensitiveHostPaths lists host paths that should not be mounted into containers.
var sensitiveHostPaths = []string{"/etc", "/proc", "/sys", "/var/run", "/var/log", "/root"}

func checkPodSpecVolumes(kind, namespace, name string, spec corev1.PodSpec) []Finding {
	var findings []Finding
	f := func(checkID, severity, msg string) {
		findings = append(findings, Finding{
			Kind: kind, Namespace: namespace, Name: name,
			CheckID: checkID, Category: CategorySecurity, Severity: severity, Message: msg,
		})
	}

	for _, v := range spec.Volumes {
		if v.HostPath == nil {
			continue
		}
		p := v.HostPath.Path

		// Container runtime socket — critical attack vector
		if strings.Contains(p, "docker.sock") || strings.Contains(p, "containerd.sock") || strings.Contains(p, "crio.sock") {
			f("dockerSocketMount", SeverityDanger, fmt.Sprintf("Volume %q mounts container runtime socket %s", v.Name, p))
			continue
		}

		// Root filesystem mount
		if p == "/" {
			f("sensitiveHostPath", SeverityDanger, fmt.Sprintf("Volume %q mounts the entire host root filesystem", v.Name))
			continue
		}

		// Sensitive host paths
		for _, prefix := range sensitiveHostPaths {
			if p == prefix || strings.HasPrefix(p, prefix+"/") {
				f("sensitiveHostPath", SeverityWarning, fmt.Sprintf("Volume %q mounts sensitive host path %s", v.Name, p))
				break
			}
		}
	}

	return findings
}

// ============================================================================
// Secret detection in ConfigMaps
// ============================================================================

// sensitiveKeyPatterns matches ConfigMap keys that likely contain secrets.
var sensitiveKeyPatterns = []string{
	"password", "passwd", "secret", "api_key", "apikey", "api-key",
	"token", "private_key", "privatekey", "private-key",
	"credential", "credentials", "auth", "authorization",
	"access_key", "accesskey", "access-key",
	"secret_key", "secretkey", "secret-key",
}

func checkSecretInConfigMap(configMaps []*corev1.ConfigMap) []Finding {
	if len(configMaps) == 0 {
		return nil
	}

	var findings []Finding
	for _, cm := range configMaps {
		for key := range cm.Data {
			keyLower := strings.ToLower(key)
			for _, pattern := range sensitiveKeyPatterns {
				if strings.Contains(keyLower, pattern) {
					findings = append(findings, Finding{
						Kind: "ConfigMap", Namespace: cm.Namespace, Name: cm.Name,
						CheckID:  "secretInConfigMap",
						Category: CategorySecurity, Severity: SeverityWarning,
						Message:  fmt.Sprintf("ConfigMap key %q may contain sensitive data — use a Secret instead", key),
					})
					break // one finding per key is enough
				}
			}
		}
	}
	return findings
}

// ============================================================================
// Reliability checks
// ============================================================================

func checkPodSpecReliability(kind, namespace, name string, spec corev1.PodSpec) []Finding {
	var findings []Finding
	f := func(checkID, severity, msg string) {
		findings = append(findings, Finding{
			Kind: kind, Namespace: namespace, Name: name,
			CheckID: checkID, Category: CategoryReliability, Severity: severity, Message: msg,
		})
	}

	for _, c := range spec.Containers {
		if c.ReadinessProbe == nil {
			f("readinessProbeMissing", SeverityWarning, fmt.Sprintf("Container %q has no readiness probe", c.Name))
		}
		if c.LivenessProbe == nil {
			f("livenessProbeMissing", SeverityWarning, fmt.Sprintf("Container %q has no liveness probe", c.Name))
		}

		tag := imageTag(c.Image)
		if tag == "latest" || tag == "" {
			f("imageTagLatest", SeverityDanger, fmt.Sprintf("Container %q uses image tag %q", c.Name, tagDisplay(tag)))
		}

		if (tag == "latest" || tag == "") && c.ImagePullPolicy != corev1.PullAlways {
			f("pullPolicyNotAlways", SeverityWarning, fmt.Sprintf("Container %q with mutable tag should use imagePullPolicy=Always", c.Name))
		}
	}

	return findings
}

func checkSingleReplica(deployments []*appsv1.Deployment, hpaTargets map[string]bool) []Finding {
	var findings []Finding
	for _, d := range deployments {
		replicas := int32(1)
		if d.Spec.Replicas != nil {
			replicas = *d.Spec.Replicas
		}
		if replicas <= 1 && !hpaTargets[hpaKey("Deployment", d.Namespace, d.Name)] {
			findings = append(findings, Finding{
				Kind: "Deployment", Namespace: d.Namespace, Name: d.Name,
				CheckID: "singleReplica", Category: CategoryReliability, Severity: SeverityWarning,
				Message: "Deployment has only 1 replica",
			})
		}
	}
	return findings
}

func checkMissingPDB(deployments []*appsv1.Deployment, statefulSets []*appsv1.StatefulSet, pdbSelectors []namespacedSelector) []Finding {
	var findings []Finding

	check := func(kind, namespace, name string, replicas *int32, matchLabels map[string]string) {
		r := int32(1)
		if replicas != nil {
			r = *replicas
		}
		if r <= 1 {
			return // PDB only matters for multi-replica workloads
		}
		podLabels := labels.Set(matchLabels)
		for _, ns := range pdbSelectors {
			if ns.namespace == namespace && ns.selector.Matches(podLabels) {
				return // covered by a PDB in the same namespace
			}
		}
		findings = append(findings, Finding{
			Kind: kind, Namespace: namespace, Name: name,
			CheckID: "missingPDB", Category: CategoryReliability, Severity: SeverityWarning,
			Message: fmt.Sprintf("%s has %d replicas but no PodDisruptionBudget", kind, r),
		})
	}

	for _, d := range deployments {
		check("Deployment", d.Namespace, d.Name, d.Spec.Replicas, d.Spec.Selector.MatchLabels)
	}
	for _, ss := range statefulSets {
		check("StatefulSet", ss.Namespace, ss.Name, ss.Spec.Replicas, ss.Spec.Selector.MatchLabels)
	}
	return findings
}

// ============================================================================
// Efficiency checks
// ============================================================================

func checkPodSpecEfficiency(kind, namespace, name string, spec corev1.PodSpec, lrs []*corev1.LimitRange) []Finding {
	var findings []Finding
	f := func(checkID, severity, msg string) {
		findings = append(findings, Finding{
			Kind: kind, Namespace: namespace, Name: name,
			CheckID: checkID, Category: CategoryEfficiency, Severity: severity, Message: msg,
		})
	}

	// LimitRange defaults in the namespace are applied by admission — skip
	// flagging missing values that would be filled in automatically.
	defaults := containerDefaultsFromLimitRanges(lrs)

	for _, c := range spec.Containers {
		res := c.Resources
		if (res.Requests.Cpu() == nil || res.Requests.Cpu().IsZero()) && !defaults.cpuRequest {
			f("cpuRequestMissing", SeverityWarning, fmt.Sprintf("Container %q has no CPU request", c.Name))
		}
		if (res.Requests.Memory() == nil || res.Requests.Memory().IsZero()) && !defaults.memoryRequest {
			f("memoryRequestMissing", SeverityWarning, fmt.Sprintf("Container %q has no memory request", c.Name))
		}
		if (res.Limits.Cpu() == nil || res.Limits.Cpu().IsZero()) && !defaults.cpuLimit {
			f("cpuLimitMissing", SeverityWarning, fmt.Sprintf("Container %q has no CPU limit", c.Name))
		}
		if (res.Limits.Memory() == nil || res.Limits.Memory().IsZero()) && !defaults.memoryLimit {
			f("memoryLimitMissing", SeverityWarning, fmt.Sprintf("Container %q has no memory limit", c.Name))
		}
	}

	return findings
}

// ============================================================================
// Cross-resource checks (Radar-native)
// ============================================================================

func checkServiceNoMatchingPods(services []*corev1.Service, podsBySelector map[string][]*corev1.Pod) []Finding {
	var findings []Finding
	for _, svc := range services {
		if len(svc.Spec.Selector) == 0 {
			continue // headless or external-name services
		}
		if svc.Spec.Type == corev1.ServiceTypeExternalName {
			continue
		}
		sel := labels.SelectorFromSet(labels.Set(svc.Spec.Selector))
		found := false
		for _, pod := range podsBySelector[svc.Namespace] {
			if sel.Matches(labels.Set(pod.Labels)) {
				found = true
				break
			}
		}
		if !found {
			findings = append(findings, Finding{
				Kind: "Service", Namespace: svc.Namespace, Name: svc.Name,
				CheckID: "serviceNoMatchingPods", Category: CategoryReliability, Severity: SeverityWarning,
				Message: "Service selector matches no pods",
			})
		}
	}
	return findings
}

func checkIngressNoMatchingService(ingresses []*networkingv1.Ingress, servicesByName map[string]bool) []Finding {
	var findings []Finding
	for _, ing := range ingresses {
		for _, rule := range ing.Spec.Rules {
			if rule.HTTP == nil {
				continue
			}
			for _, path := range rule.HTTP.Paths {
				if path.Backend.Service == nil {
					continue
				}
				svcKey := ing.Namespace + "/" + path.Backend.Service.Name
				if !servicesByName[svcKey] {
					findings = append(findings, Finding{
						Kind: "Ingress", Namespace: ing.Namespace, Name: ing.Name,
						CheckID:  "ingressNoMatchingService", Category: CategoryReliability, Severity: SeverityWarning,
						Message:  fmt.Sprintf("Ingress references non-existent Service %q", path.Backend.Service.Name),
					})
				}
			}
		}
	}
	return findings
}

// ============================================================================
// Topology spread + HA checks
// ============================================================================

func checkMissingTopologySpread(deployments []*appsv1.Deployment, statefulSets []*appsv1.StatefulSet) []Finding {
	var findings []Finding

	check := func(kind, namespace, name string, replicas *int32, spec corev1.PodSpec) {
		r := int32(1)
		if replicas != nil {
			r = *replicas
		}
		if r <= 1 {
			return
		}
		if len(spec.TopologySpreadConstraints) > 0 {
			return
		}
		findings = append(findings, Finding{
			Kind: kind, Namespace: namespace, Name: name,
			CheckID: "missingTopologySpread", Category: CategoryReliability, Severity: SeverityWarning,
			Message: fmt.Sprintf("%s has %d replicas but no topology spread constraints", kind, r),
		})
	}

	for _, d := range deployments {
		check("Deployment", d.Namespace, d.Name, d.Spec.Replicas, d.Spec.Template.Spec)
	}
	for _, ss := range statefulSets {
		check("StatefulSet", ss.Namespace, ss.Name, ss.Spec.Replicas, ss.Spec.Template.Spec)
	}
	return findings
}

func checkPodHARisk(pods []*corev1.Pod, deployments []*appsv1.Deployment) []Finding {
	var findings []Finding
	for _, d := range deployments {
		replicas := int32(1)
		if d.Spec.Replicas != nil {
			replicas = *d.Spec.Replicas
		}
		if replicas <= 1 || d.Spec.Selector == nil {
			continue
		}
		sel, err := metav1.LabelSelectorAsSelector(d.Spec.Selector)
		if err != nil {
			continue
		}
		nodeSet := make(map[string]bool)
		matchCount := 0
		for _, pod := range pods {
			if pod.Namespace != d.Namespace {
				continue
			}
			if !sel.Matches(labels.Set(pod.Labels)) {
				continue
			}
			// Skip pods not running (pending pods don't have a node yet)
			if pod.Spec.NodeName == "" {
				continue
			}
			nodeSet[pod.Spec.NodeName] = true
			matchCount++
		}
		if matchCount > 1 && len(nodeSet) == 1 {
			var nodeName string
			for n := range nodeSet {
				nodeName = n
			}
			findings = append(findings, Finding{
				Kind: "Deployment", Namespace: d.Namespace, Name: d.Name,
				CheckID: "podHARisk", Category: CategoryReliability, Severity: SeverityWarning,
				Message: fmt.Sprintf("All %d running pods are on node %s", matchCount, nodeName),
			})
		}
	}
	return findings
}

// ============================================================================
// Orphan resource checks
// ============================================================================

func checkOrphanConfigMapsSecrets(input *CheckInput) []Finding {
	if len(input.ConfigMaps) == 0 && len(input.Secrets) == 0 {
		return nil
	}

	// Build set of referenced ConfigMap and Secret names (namespace/name)
	referencedCMs := make(map[string]bool)
	referencedSecrets := make(map[string]bool)

	for _, pod := range input.Pods {
		ns := pod.Namespace
		spec := pod.Spec
		// Check all containers (init + regular)
		for _, c := range spec.InitContainers {
			collectContainerRefs(ns, c, referencedCMs, referencedSecrets)
		}
		for _, c := range spec.Containers {
			collectContainerRefs(ns, c, referencedCMs, referencedSecrets)
		}
		// Volume references
		for _, v := range spec.Volumes {
			if v.ConfigMap != nil {
				referencedCMs[ns+"/"+v.ConfigMap.Name] = true
			}
			if v.Secret != nil {
				referencedSecrets[ns+"/"+v.Secret.SecretName] = true
			}
			if v.Projected != nil {
				for _, src := range v.Projected.Sources {
					if src.ConfigMap != nil {
						referencedCMs[ns+"/"+src.ConfigMap.Name] = true
					}
					if src.Secret != nil {
						referencedSecrets[ns+"/"+src.Secret.Name] = true
					}
				}
			}
		}
		// ImagePullSecrets
		for _, ips := range spec.ImagePullSecrets {
			referencedSecrets[ns+"/"+ips.Name] = true
		}
		// ServiceAccount token secrets (referenced implicitly)
		if spec.ServiceAccountName != "" {
			// SA token secrets are named like "<sa-name>-token-xxxxx" — we can't match exactly,
			// but we skip SA-related secrets via the type filter below
		}
	}

	// Ingress TLS secrets
	for _, ing := range input.Ingresses {
		for _, tls := range ing.Spec.TLS {
			if tls.SecretName != "" {
				referencedSecrets[ing.Namespace+"/"+tls.SecretName] = true
			}
		}
	}

	var findings []Finding

	// Check ConfigMaps
	for _, cm := range input.ConfigMaps {
		key := cm.Namespace + "/" + cm.Name
		if referencedCMs[key] {
			continue
		}
		// Skip well-known system ConfigMaps
		if cm.Name == "kube-root-ca.crt" {
			continue
		}
		findings = append(findings, Finding{
			Kind: "ConfigMap", Namespace: cm.Namespace, Name: cm.Name,
			CheckID: "orphanConfigMapSecret", Category: CategoryEfficiency, Severity: SeverityWarning,
			Message: fmt.Sprintf("ConfigMap %q is not referenced by any pod", cm.Name),
		})
	}

	// Check Secrets
	for _, sec := range input.Secrets {
		key := sec.Namespace + "/" + sec.Name
		if referencedSecrets[key] {
			continue
		}
		// Skip service account tokens and Helm release secrets
		if sec.Type == corev1.SecretTypeServiceAccountToken {
			continue
		}
		if sec.Type == "helm.sh/release.v1" {
			continue
		}
		// Skip TLS secrets used by cert-manager (they may be referenced by Ingress annotations, not spec)
		if sec.Labels != nil && sec.Labels["cert-manager.io/certificate-name"] != "" {
			continue
		}
		findings = append(findings, Finding{
			Kind: "Secret", Namespace: sec.Namespace, Name: sec.Name,
			CheckID: "orphanConfigMapSecret", Category: CategoryEfficiency, Severity: SeverityWarning,
			Message: fmt.Sprintf("Secret %q is not referenced by any pod", sec.Name),
		})
	}

	return findings
}

func collectContainerRefs(ns string, c corev1.Container, cms, secrets map[string]bool) {
	for _, env := range c.Env {
		if env.ValueFrom != nil {
			if env.ValueFrom.ConfigMapKeyRef != nil {
				cms[ns+"/"+env.ValueFrom.ConfigMapKeyRef.Name] = true
			}
			if env.ValueFrom.SecretKeyRef != nil {
				secrets[ns+"/"+env.ValueFrom.SecretKeyRef.Name] = true
			}
		}
	}
	for _, envFrom := range c.EnvFrom {
		if envFrom.ConfigMapRef != nil {
			cms[ns+"/"+envFrom.ConfigMapRef.Name] = true
		}
		if envFrom.SecretRef != nil {
			secrets[ns+"/"+envFrom.SecretRef.Name] = true
		}
	}
}

// ============================================================================
// Resource utilization checks
// ============================================================================

func checkResourceUtilization(metrics []PodMetricsInput) []Finding {
	if len(metrics) == 0 {
		return nil
	}

	var findings []Finding
	for _, m := range metrics {
		// CPU utilization
		if m.CPURequest > 0 {
			cpuPct := float64(m.CPUUsage) / float64(m.CPURequest) * 100
			if cpuPct < 10 && m.CPUUsage > 0 {
				findings = append(findings, Finding{
					Kind: "Pod", Namespace: m.Namespace, Name: m.Name,
					CheckID: "resourceUtilization", Category: CategoryEfficiency, Severity: SeverityWarning,
					Message: fmt.Sprintf("CPU usage is %.0f%% of request (%dm used, %dm requested) — consider reducing request", cpuPct, m.CPUUsage, m.CPURequest),
				})
			} else if cpuPct > 90 {
				findings = append(findings, Finding{
					Kind: "Pod", Namespace: m.Namespace, Name: m.Name,
					CheckID: "resourceUtilization", Category: CategoryEfficiency, Severity: SeverityWarning,
					Message: fmt.Sprintf("CPU usage is %.0f%% of request (%dm used, %dm requested) — at risk of throttling", cpuPct, m.CPUUsage, m.CPURequest),
				})
			}
		}

		// Memory utilization
		if m.MemoryRequest > 0 {
			memPct := float64(m.MemoryUsage) / float64(m.MemoryRequest) * 100
			memUsageMi := m.MemoryUsage / (1024 * 1024)
			memReqMi := m.MemoryRequest / (1024 * 1024)
			if memPct < 10 && m.MemoryUsage > 0 {
				findings = append(findings, Finding{
					Kind: "Pod", Namespace: m.Namespace, Name: m.Name,
					CheckID: "resourceUtilization", Category: CategoryEfficiency, Severity: SeverityWarning,
					Message: fmt.Sprintf("Memory usage is %.0f%% of request (%dMi used, %dMi requested) — consider reducing request", memPct, memUsageMi, memReqMi),
				})
			} else if memPct > 90 {
				findings = append(findings, Finding{
					Kind: "Pod", Namespace: m.Namespace, Name: m.Name,
					CheckID: "resourceUtilization", Category: CategoryEfficiency, Severity: SeverityWarning,
					Message: fmt.Sprintf("Memory usage is %.0f%% of request (%dMi used, %dMi requested) — at risk of OOM kill", memPct, memUsageMi, memReqMi),
				})
			}
		}
	}
	return findings
}

// ============================================================================
// Deprecated API checks
// ============================================================================

func checkDeprecatedAPIs(servedAPIs []string, clusterVersion string) []Finding {
	if len(servedAPIs) == 0 || clusterVersion == "" {
		return nil
	}

	deprecations := DeprecationsByGroupVersion()
	var findings []Finding

	for _, gv := range servedAPIs {
		entries, ok := deprecations[gv]
		if !ok {
			continue
		}
		for _, entry := range entries {
			kindLabel := gv
			msg := fmt.Sprintf("API %s is deprecated (since %s, removed in %s) — use %s",
				gv, entry.DeprecatedIn, entry.RemovedIn, entry.Replacement)
			if entry.Kind != "" {
				kindLabel = entry.Kind
				msg = fmt.Sprintf("API %s %s is deprecated (since %s, removed in %s) — use %s",
					gv, entry.Kind, entry.DeprecatedIn, entry.RemovedIn, entry.Replacement)
			}
			findings = append(findings, Finding{
				Kind:     kindLabel,
				Name:     gv,
				CheckID:  "deprecatedAPIVersion",
				Category: CategoryReliability,
				Severity: SeverityDanger,
				Message:  msg,
			})
		}
	}

	return findings
}

// ============================================================================
// Index builders
// ============================================================================

// indexPodsByLabels groups pods by namespace for selector matching.
func indexPodsByLabels(pods []*corev1.Pod) map[string][]*corev1.Pod {
	m := make(map[string][]*corev1.Pod)
	for _, p := range pods {
		m[p.Namespace] = append(m[p.Namespace], p)
	}
	return m
}

// indexHPATargets returns a set of "Kind/namespace/name" for HPA targets.
func indexHPATargets(input *CheckInput) map[string]bool {
	m := make(map[string]bool)
	for _, hpa := range input.HorizontalPodAutoscalers {
		ref := hpa.Spec.ScaleTargetRef
		m[hpaKey(ref.Kind, hpa.Namespace, ref.Name)] = true
	}
	return m
}

func hpaKey(kind, namespace, name string) string {
	return kind + "/" + namespace + "/" + name
}

// namespacedSelector pairs a label selector with its namespace.
type namespacedSelector struct {
	namespace string
	selector  labels.Selector
}

// collectPDBSelectors returns label selectors from all PodDisruptionBudgets,
// keyed by namespace so we only match PDBs in the same namespace as the workload.
func collectPDBSelectors(pdbs []*policyv1.PodDisruptionBudget) []namespacedSelector {
	var sels []namespacedSelector
	for _, pdb := range pdbs {
		if pdb.Spec.Selector == nil {
			continue
		}
		sel, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
		if err != nil {
			continue
		}
		sels = append(sels, namespacedSelector{namespace: pdb.Namespace, selector: sel})
	}
	return sels
}

// indexServicesByName returns a set of "namespace/name" for all services.
func indexServicesByName(services []*corev1.Service) map[string]bool {
	m := make(map[string]bool)
	for _, svc := range services {
		m[svc.Namespace+"/"+svc.Name] = true
	}
	return m
}

// ============================================================================
// Result aggregation
// ============================================================================

func buildResults(findings []Finding) *ScanResults {
	categories := map[string]CategorySummary{}
	// Initialize all categories
	for _, cat := range []string{CategorySecurity, CategoryReliability, CategoryEfficiency} {
		categories[cat] = CategorySummary{}
	}

	// Merge findings: same (resource, checkID) get combined into one finding
	// with messages joined, so multi-container workloads show all affected containers.
	type checkKey struct{ resource, checkID string }
	mergeIndex := make(map[checkKey]int) // key → index in dedupFindings
	var dedupFindings []Finding

	for _, f := range findings {
		key := checkKey{ResourceKey(f.Kind, f.Namespace, f.Name), f.CheckID}
		if idx, exists := mergeIndex[key]; exists {
			dedupFindings[idx].Message += "; " + f.Message
			continue
		}
		mergeIndex[key] = len(dedupFindings)
		dedupFindings = append(dedupFindings, f)

		cs := categories[f.Category]
		switch f.Severity {
		case SeverityWarning:
			cs.Warning++
		case SeverityDanger:
			cs.Danger++
		}
		categories[f.Category] = cs
	}

	totalWarning, totalDanger := 0, 0
	for _, cs := range categories {
		totalWarning += cs.Warning
		totalDanger += cs.Danger
	}

	// Include full registry so settings dialog can show all checks (including disabled ones)
	checks := make(map[string]CheckMeta, len(CheckRegistry))
	for id, meta := range CheckRegistry {
		checks[id] = meta
	}

	return &ScanResults{
		Summary: ScanSummary{
			Warning:    totalWarning,
			Danger:     totalDanger,
			Categories: categories,
		},
		Findings: dedupFindings,
		Groups:   GroupByResource(dedupFindings),
		Checks:   checks,
	}
}

// ============================================================================
// Utilities
// ============================================================================

// imageTag extracts the tag from an image reference.
// Returns "" if no tag or digest is present.
func imageTag(image string) string {
	// Handle digest references (image@sha256:...)
	if strings.Contains(image, "@") {
		return strings.SplitN(image, "@", 2)[1]
	}
	// Handle tag references (image:tag)
	parts := strings.SplitN(image, ":", 2)
	if len(parts) == 2 && !strings.Contains(parts[1], "/") {
		return parts[1]
	}
	return ""
}

func tagDisplay(tag string) string {
	if tag == "" {
		return "<none>"
	}
	return tag
}
