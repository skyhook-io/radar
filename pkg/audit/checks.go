package audit

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/timeutil"
)

// RunChecks runs all best-practice checks against the provided resources
// and returns aggregated results.
func RunChecks(input *CheckInput) *ScanResults {
	if input == nil {
		return &ScanResults{Summary: ScanSummary{Categories: map[string]CategorySummary{}}}
	}

	var findings []Finding
	var missingInputs []string
	tr := newEvalTracker()

	// Build indexes needed by cross-resource checks
	podsBySelector := indexPodsByLabels(input.Pods)
	hpaTargets := indexHPATargets(input)
	pdbSelectors := collectPDBSelectors(input.PodDisruptionBudgets)
	servicesByName := indexServicesByName(input.Services)

	// --- Security checks (container-level, attributed to owning workload) ---
	findings = append(findings, checkWorkloadPodSpecs(tr, input)...)
	if input.ServiceAccounts == nil {
		missingInputs = append(missingInputs, "serviceaccounts")
	}
	// Nil SUBJECT inventories matter too: zero evaluated subjects from an
	// unlisted kind must read as incomplete, not clean.
	if input.Deployments == nil {
		missingInputs = append(missingInputs, "deployments")
	}
	if input.StatefulSets == nil {
		missingInputs = append(missingInputs, "statefulsets")
	}
	if input.DaemonSets == nil {
		missingInputs = append(missingInputs, "daemonsets")
	}
	// Jobs/CronJobs are ConfigMap/Secret reference sources too
	// (configReferencePodSpecs): unlisted means orphan detection can't see
	// refs from them, so their absence must read as incomplete, not clean.
	if input.Jobs == nil {
		missingInputs = append(missingInputs, "jobs")
	}
	if input.CronJobs == nil {
		missingInputs = append(missingInputs, "cronjobs")
	}
	if input.LimitRanges == nil {
		missingInputs = append(missingInputs, "limitranges")
	}

	// --- Reliability checks ---
	// singleReplica's eligibility filter (HPA-managed deployments are out of
	// scope) needs the HPA inventory to be authoritative — nil means
	// unlisted, and an HPA-managed 1-replica deployment would otherwise be a
	// false positive counted from incomplete prerequisites.
	if input.HorizontalPodAutoscalers != nil {
		findings = append(findings, checkSingleReplica(tr, input.Deployments, hpaTargets)...)
	} else {
		missingInputs = append(missingInputs, "horizontalpodautoscalers")
	}
	// Only check PDB coverage if we can actually list PDBs (nil = RBAC denied, not "none exist")
	if input.PodDisruptionBudgets != nil {
		findings = append(findings, checkMissingPDB(tr, input.Deployments, input.StatefulSets, pdbSelectors)...)
	} else {
		missingInputs = append(missingInputs, "poddisruptionbudgets")
	}
	findings = append(findings, checkMissingTopologySpread(tr, input.Deployments, input.StatefulSets)...)
	// HA-risk placement needs the pod inventory — nil pods would make every
	// deployment look risk-free (or falsely clustered), not "no pods".
	if input.Pods != nil {
		findings = append(findings, checkPodHARisk(tr, input.Pods, input.Deployments)...)
	} else {
		missingInputs = append(missingInputs, "pods")
	}

	// --- Efficiency checks are included in checkWorkloadPodSpecs ---
	// nil ConfigMaps = RBAC denied, not "none exist" — the ConfigMap-subject
	// checks must neither run nor count anything as evaluated.
	// Orphan detection audits ConfigMaps AND Secrets — a nil side means that
	// KIND is unlisted, not that the whole check is off. References come from
	// the whole workload inventory (configReferencePodSpecs — pods AND
	// deployments/statefulsets/daemonsets/jobs) plus Ingress TLS refs; missing
	// reference inventory surfaces via missingInputs above, so the check runs
	// on whatever is visible.
	if input.ConfigMaps != nil || input.Secrets != nil {
		findings = append(findings, checkOrphanConfigMapsSecrets(tr, input)...)
	}
	if input.ConfigMaps != nil {
		findings = append(findings, checkSecretInConfigMap(tr, input.ConfigMaps)...)
	} else {
		missingInputs = append(missingInputs, "configmaps")
	}
	if input.Secrets == nil {
		missingInputs = append(missingInputs, "secrets")
	}

	// --- Cross-resource checks --- each needs BOTH sides of its relationship
	// listed; a nil side would flag every subject as dangling.
	if input.Services != nil && input.Pods != nil {
		findings = append(findings, checkServiceNoMatchingPods(tr, input.Services, podsBySelector)...)
	}
	if input.Ingresses != nil && input.Services != nil {
		findings = append(findings, checkIngressNoMatchingService(tr, input.Ingresses, servicesByName)...)
	}
	if input.Services == nil {
		missingInputs = append(missingInputs, "services")
	}
	if input.Ingresses == nil {
		missingInputs = append(missingInputs, "ingresses")
	}
	findings = append(findings, checkTraefikDanglingRefs(tr, input)...)

	// --- Utilization checks (optional, only when metrics provided) ---
	if input.PodMetrics != nil {
		findings = append(findings, checkResourceUtilization(tr, input.PodMetrics)...)
	} else {
		missingInputs = append(missingInputs, "podmetrics")
	}

	// --- Deprecated API checks ---
	findings = append(findings, checkDeprecatedAPIs(tr, input.ServedAPIs, input.ClusterVersion)...)

	// --- Lifecycle: stuck terminating resources ---
	// Catches the "zombie awaiting finalizer cleanup" pattern across every
	// typed kind we already scan. Pairs with the GitOps view's per-app
	// Terminating chip + insight; the audit surface broadens coverage to
	// non-GitOps resources (stuck Pods on failed nodes, Deployments
	// blocked by webhook finalizers, etc.).
	findings = append(findings, checkStuckTerminating(tr, input)...)

	// --- Crossplane: MRs/XRs/Claims stuck Ready=False or Synced=False ---
	// Same severity ramp as stuckTerminating (5min warning, 30min danger) so
	// operators see the same "long enough to flag" semantics across surfaces.
	findings = append(findings, checkCrossplaneStuck(tr, input)...)

	return buildResults(findings, tr, missingInputs)
}

// ============================================================================
// Evaluation tracking
// ============================================================================

// evalTracker accumulates how many distinct subjects each check evaluated,
// per namespace. Subjects are counted at the same (resource, checkID) grain
// the finding merge uses — per-container checks record once per workload —
// so buildResults can derive passed = evaluated - failed without unit
// mismatch. Distinctness comes from call discipline: every check iterates
// each of its subjects exactly once and records exactly once per subject
// that passed the check's own eligibility filters.
type evalTracker struct {
	counts map[string]map[string]int // checkID → namespace → subjects evaluated
}

func newEvalTracker() *evalTracker {
	return &evalTracker{counts: make(map[string]map[string]int)}
}

// record counts one evaluated subject for checkID. Cluster-scoped subjects
// use namespace "".
func (t *evalTracker) record(checkID, namespace string) {
	byNS := t.counts[checkID]
	if byNS == nil {
		byNS = make(map[string]int)
		t.counts[checkID] = byNS
	}
	byNS[namespace]++
}

// recordAll counts one evaluated subject for every checkID in ids — used by
// check families that evaluate several checkIDs against the same subject.
func (t *evalTracker) recordAll(ids []string, namespace string) {
	for _, id := range ids {
		t.record(id, namespace)
	}
}

// ============================================================================
// Pod spec checks (security, reliability, efficiency)
// Applied to workload pod templates; falls back to bare pods.
// ============================================================================

func checkWorkloadPodSpecs(tr *evalTracker, input *CheckInput) []Finding {
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

	lrAuthoritative := input.LimitRanges != nil
	saCovers := func(ns string) bool {
		if input.ServiceAccounts == nil {
			return false
		}
		return input.ServiceAccountsNamespace == "" || input.ServiceAccountsNamespace == ns
	}
	for _, w := range specs {
		findings = append(findings, checkPodSpecSecurity(tr, w.kind, w.namespace, w.name, w.spec, saByKey, saCovers(w.namespace))...)
		findings = append(findings, checkPodSpecReliability(tr, w.kind, w.namespace, w.name, w.spec)...)
		findings = append(findings, checkPodSpecEfficiency(tr, w.kind, w.namespace, w.name, w.spec, limitsByNs[w.namespace], lrAuthoritative)...)
		findings = append(findings, checkPodSpecVolumes(tr, w.kind, w.namespace, w.name, w.spec)...)
	}
	return findings
}

// Every checkID a pod-spec family member can emit. Each family function
// records its whole list once per workload spec — container-level findings
// merge to the workload, so the workload is the subject unit here.
var (
	podSpecSecurityCheckIDs = []string{
		"hostNetwork", "hostPID", "hostIPC", "automountServiceAccountToken",
		"runAsRoot", "privileged", "privilegeEscalation", "readOnlyRootFs",
		"dangerousCapabilities", "insecureCapabilities",
	}
	podSpecReliabilityCheckIDs = []string{
		"readinessProbeMissing", "livenessProbeMissing", "imageTagLatest", "pullPolicyNotAlways",
	}
	podSpecEfficiencyCheckIDs = []string{
		"cpuRequestMissing", "memoryRequestMissing", "cpuLimitMissing", "memoryLimitMissing",
	}
	podSpecVolumeCheckIDs = []string{"dockerSocketMount", "sensitiveHostPath"}
)

func withoutID(ids []string, drop string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != drop {
			out = append(out, id)
		}
	}
	return out
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

func checkPodSpecSecurity(tr *evalTracker, kind, namespace, name string, spec corev1.PodSpec, saByKey map[string]*corev1.ServiceAccount, saAuthoritative bool) []Finding {
	// The automount check needs the SA inventory only when the POD leaves
	// the field unset (SA-level opt-outs then decide). A pod-level explicit
	// value is evaluable regardless — skipping those too would drop real
	// detections.
	autoCheckable := saAuthoritative || spec.AutomountServiceAccountToken != nil
	ids := podSpecSecurityCheckIDs
	if !autoCheckable {
		ids = withoutID(ids, "automountServiceAccountToken")
	}
	tr.recordAll(ids, namespace)
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
	if autoCheckable && tokenAutoMounted(namespace, spec, saByKey) {
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
	saName := podServiceAccountName(spec)
	if sa, ok := saByKey[namespace+"/"+saName]; ok &&
		sa.AutomountServiceAccountToken != nil && !*sa.AutomountServiceAccountToken {
		return false
	}
	return true
}

func podServiceAccountName(spec corev1.PodSpec) string {
	if spec.ServiceAccountName != "" {
		return spec.ServiceAccountName
	}
	return "default"
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

func checkPodSpecVolumes(tr *evalTracker, kind, namespace, name string, spec corev1.PodSpec) []Finding {
	tr.recordAll(podSpecVolumeCheckIDs, namespace)
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

var sensitiveKeyPatterns = []string{
	"password", "passwd", "api_key", "apikey", "api-key",
	"private_key", "privatekey", "private-key",
	"access_key", "accesskey", "access-key",
	"secret_key", "secretkey", "secret-key",
}

var valueGatedSensitiveKeyPatterns = []string{
	"secret", "token", "auth", "authorization", "credential", "credentials",
}

const configMapSensitiveValueMinLength = 24

func checkSecretInConfigMap(tr *evalTracker, configMaps []*corev1.ConfigMap) []Finding {
	if len(configMaps) == 0 {
		return nil
	}

	var findings []Finding
	for _, cm := range configMaps {
		tr.record("secretInConfigMap", cm.Namespace)
		for key, value := range cm.Data {
			if !configMapEntryLooksSensitive(key, value) {
				continue
			}
			findings = append(findings, Finding{
				Kind: "ConfigMap", Namespace: cm.Namespace, Name: cm.Name,
				CheckID:  "secretInConfigMap",
				Category: CategorySecurity, Severity: SeverityWarning,
				Message: fmt.Sprintf("ConfigMap key %q may contain sensitive data — use a Secret instead", key),
			})
		}
	}
	return findings
}

func configMapEntryLooksSensitive(key, value string) bool {
	keyLower := strings.ToLower(key)
	if configMapKeyLooksSecretReference(keyLower) {
		return false
	}
	if configMapKeyIsAlwaysSensitive(keyLower) {
		return true
	}
	for _, pattern := range sensitiveKeyPatterns {
		if strings.Contains(keyLower, pattern) {
			return true
		}
	}
	for _, pattern := range valueGatedSensitiveKeyPatterns {
		if strings.Contains(keyLower, pattern) {
			return configMapValueLooksSensitive(value)
		}
	}
	return false
}

func configMapKeyLooksSecretReference(keyLower string) bool {
	normalized := strings.NewReplacer("-", "_", ".", "_").Replace(keyLower)
	return strings.HasSuffix(normalized, "secret_name") ||
		strings.HasSuffix(normalized, "secret_names") ||
		strings.HasSuffix(normalized, "secret_ref") ||
		strings.HasSuffix(normalized, "secret_refs") ||
		strings.HasSuffix(normalized, "secret_reference") ||
		strings.HasSuffix(normalized, "secret_references")
}

func configMapKeyIsAlwaysSensitive(keyLower string) bool {
	compact := strings.NewReplacer("_", "", "-", "", ".", "").Replace(keyLower)
	if compact == "secret" || compact == "clientsecret" || strings.HasSuffix(compact, "clientsecret") {
		return true
	}
	return false
}

func configMapValueLooksSensitive(value string) bool {
	v := strings.TrimSpace(value)
	if v == "" {
		return false
	}
	switch strings.ToLower(v) {
	case "true", "false", "yes", "no", "on", "off", "enabled", "disabled", "none", "null", "nil",
		"basic", "bearer", "oauth", "oauth2", "oidc", "anonymous":
		return false
	}
	if strings.HasPrefix(strings.ToLower(v), "bearer ") {
		v = strings.TrimSpace(v[len("bearer "):])
	}
	if strings.HasPrefix(strings.ToLower(v), "basic ") {
		v = strings.TrimSpace(v[len("basic "):])
	}
	if urlContainsUserInfo(v) {
		return true
	}
	if strings.HasPrefix(v, "$(") || strings.HasPrefix(v, "${") || strings.HasPrefix(v, "/") || startsWithURLScheme(v) {
		return false
	}
	if strings.Contains(v, "-----BEGIN ") {
		return true
	}
	parts := strings.Split(v, ".")
	if len(parts) == 3 && strings.HasPrefix(parts[0], "eyJ") {
		return true
	}
	if len(v) < 16 || strings.ContainsAny(v, " \t\r\n") {
		return false
	}

	classes := 0
	if strings.IndexFunc(v, func(r rune) bool { return r >= 'a' && r <= 'z' }) >= 0 {
		classes++
	}
	if strings.IndexFunc(v, func(r rune) bool { return r >= 'A' && r <= 'Z' }) >= 0 {
		classes++
	}
	if strings.IndexFunc(v, func(r rune) bool { return r >= '0' && r <= '9' }) >= 0 {
		classes++
	}
	if strings.IndexFunc(v, func(r rune) bool {
		return (r >= '!' && r <= '/') || (r >= ':' && r <= '@') || (r >= '[' && r <= '`') || (r >= '{' && r <= '~')
	}) >= 0 {
		classes++
	}
	return len(v) >= configMapSensitiveValueMinLength && classes >= 2
}

func startsWithURLScheme(value string) bool {
	scheme, _, ok := strings.Cut(value, "://")
	if !ok || scheme == "" {
		return false
	}
	for _, r := range scheme {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '+' || r == '-' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func urlContainsUserInfo(value string) bool {
	u, err := url.Parse(value)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User == nil {
		return false
	}
	if _, ok := u.User.Password(); ok {
		return true
	}
	return u.User.Username() != ""
}

// ============================================================================
// Reliability checks
// ============================================================================

func checkPodSpecReliability(tr *evalTracker, kind, namespace, name string, spec corev1.PodSpec) []Finding {
	tr.recordAll(podSpecReliabilityCheckIDs, namespace)
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

func checkSingleReplica(tr *evalTracker, deployments []*appsv1.Deployment, hpaTargets map[string]bool) []Finding {
	var findings []Finding
	for _, d := range deployments {
		// HPA-managed deployments are out of scope, not passing — the HPA
		// owns the replica count, so counting them would inflate "passed".
		if hpaTargets[hpaKey("Deployment", d.Namespace, d.Name)] {
			continue
		}
		tr.record("singleReplica", d.Namespace)
		replicas := int32(1)
		if d.Spec.Replicas != nil {
			replicas = *d.Spec.Replicas
		}
		if replicas <= 1 {
			findings = append(findings, Finding{
				Kind: "Deployment", Namespace: d.Namespace, Name: d.Name,
				CheckID: "singleReplica", Category: CategoryReliability, Severity: SeverityWarning,
				Message: "Deployment has only 1 replica",
			})
		}
	}
	return findings
}

func checkMissingPDB(tr *evalTracker, deployments []*appsv1.Deployment, statefulSets []*appsv1.StatefulSet, pdbSelectors []namespacedSelector) []Finding {
	var findings []Finding

	check := func(kind, namespace, name string, replicas *int32, matchLabels map[string]string) {
		r := int32(1)
		if replicas != nil {
			r = *replicas
		}
		if r <= 1 {
			return // PDB only matters for multi-replica workloads
		}
		tr.record("missingPDB", namespace)
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

func checkPodSpecEfficiency(tr *evalTracker, kind, namespace, name string, spec corev1.PodSpec, lrs []*corev1.LimitRange, lrAuthoritative bool) []Finding {
	if !lrAuthoritative {
		// LimitRange defaults suppress these findings; without the inventory
		// every emission would be a potential false positive. Skip the whole
		// family rather than report on data Radar couldn't see.
		return nil
	}
	tr.recordAll(podSpecEfficiencyCheckIDs, namespace)
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

func checkServiceNoMatchingPods(tr *evalTracker, services []*corev1.Service, podsBySelector map[string][]*corev1.Pod) []Finding {
	var findings []Finding
	for _, svc := range services {
		if len(svc.Spec.Selector) == 0 {
			continue // headless or external-name services
		}
		if svc.Spec.Type == corev1.ServiceTypeExternalName {
			continue
		}
		tr.record("serviceNoMatchingPods", svc.Namespace)
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

func checkIngressNoMatchingService(tr *evalTracker, ingresses []*networkingv1.Ingress, servicesByName map[string]bool) []Finding {
	var findings []Finding
	for _, ing := range ingresses {
		eligible := false
		for _, rule := range ing.Spec.Rules {
			if rule.HTTP == nil {
				continue
			}
			for _, path := range rule.HTTP.Paths {
				if path.Backend.Service == nil {
					continue
				}
				eligible = true
				svcKey := ing.Namespace + "/" + path.Backend.Service.Name
				if !servicesByName[svcKey] {
					findings = append(findings, Finding{
						Kind: "Ingress", Namespace: ing.Namespace, Name: ing.Name,
						CheckID: "ingressNoMatchingService", Category: CategoryReliability, Severity: SeverityWarning,
						Message: fmt.Sprintf("Ingress references non-existent Service %q", path.Backend.Service.Name),
					})
				}
			}
		}
		if eligible {
			tr.record("ingressNoMatchingService", ing.Namespace)
		}
	}
	return findings
}

// ============================================================================
// Topology spread + HA checks
// ============================================================================

func checkMissingTopologySpread(tr *evalTracker, deployments []*appsv1.Deployment, statefulSets []*appsv1.StatefulSet) []Finding {
	var findings []Finding

	check := func(kind, namespace, name string, replicas *int32, spec corev1.PodSpec) {
		r := int32(1)
		if replicas != nil {
			r = *replicas
		}
		if r <= 1 {
			return
		}
		tr.record("missingTopologySpread", namespace)
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

func checkPodHARisk(tr *evalTracker, pods []*corev1.Pod, deployments []*appsv1.Deployment) []Finding {
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
		tr.record("podHARisk", d.Namespace)
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

func checkOrphanConfigMapsSecrets(tr *evalTracker, input *CheckInput) []Finding {
	if len(input.ConfigMaps) == 0 && len(input.Secrets) == 0 {
		return nil
	}

	// Build set of referenced ConfigMap and Secret names (namespace/name)
	referencedCMs := make(map[string]bool)
	referencedSecrets := make(map[string]bool)
	saByKey := indexServiceAccounts(input.ServiceAccounts)

	for _, refSpec := range input.configReferencePodSpecs() {
		collectPodSpecRefs(refSpec.namespace, refSpec.spec, saByKey, referencedCMs, referencedSecrets)
	}
	for _, ref := range input.ConfigObjectRefs {
		switch ref.Kind {
		case "ConfigMap":
			addRef(referencedCMs, ref.Namespace, ref.Name)
		case "Secret":
			addRef(referencedSecrets, ref.Namespace, ref.Name)
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
		// Skip well-known system ConfigMaps
		if cm.Name == "kube-root-ca.crt" {
			continue
		}
		if isKnownPlatformConfigMap(cm) {
			continue
		}
		if hasControllerOwnerReference(cm.OwnerReferences) {
			continue
		}
		// In scope — count as evaluated before the referenced (pass) check.
		tr.record("orphanConfigMapSecret", cm.Namespace)
		if referencedCMs[cm.Namespace+"/"+cm.Name] {
			continue
		}
		findings = append(findings, Finding{
			Kind: "ConfigMap", Namespace: cm.Namespace, Name: cm.Name,
			CheckID: "orphanConfigMapSecret", Category: CategoryEfficiency, Severity: SeverityWarning,
			Message: fmt.Sprintf("ConfigMap %q is not referenced by any workload or supported controller config", cm.Name),
		})
	}

	// Check Secrets
	for _, sec := range input.Secrets {
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
		if isKnownPlatformSecret(sec) {
			continue
		}
		if hasControllerOwnerReference(sec.OwnerReferences) {
			continue
		}
		tr.record("orphanConfigMapSecret", sec.Namespace)
		if referencedSecrets[sec.Namespace+"/"+sec.Name] {
			continue
		}
		findings = append(findings, Finding{
			Kind: "Secret", Namespace: sec.Namespace, Name: sec.Name,
			CheckID: "orphanConfigMapSecret", Category: CategoryEfficiency, Severity: SeverityWarning,
			Message: fmt.Sprintf("Secret %q is not referenced by any workload, Ingress, or supported controller config", sec.Name),
		})
	}

	return findings
}

func isKnownPlatformConfigMap(cm *corev1.ConfigMap) bool {
	if cm == nil {
		return false
	}

	if isDynamicLoaderConfigMap(cm) {
		return true
	}

	// Leader-election locks are controller state and often have no owner ref.
	if strings.TrimSpace(labelsValue(cm.Annotations, "control-plane.alpha.kubernetes.io/leader")) != "" {
		return true
	}

	switch cm.Namespace + "/" + cm.Name {
	case "kube-system/extension-apiserver-authentication",
		"kube-system/kube-apiserver-legacy-service-account-token-tracking",
		"kube-system/aws-auth",
		"kube-system/amazon-vpc-cni":
		return true
	}

	if cm.Name == "cluster-autoscaler-status" && (cm.Namespace == "kube-system" || cm.Namespace == "cluster-autoscaler") {
		return true
	}

	if cm.Namespace == "kube-system" {
		switch cm.Name {
		case "cluster-kubestore",
			"clustermetrics",
			"gke-common-webhook-heartbeat",
			"gke-common-webhook-lock",
			"ingress-uid",
			"kube-dns-autoscaler",
			"konnectivity-agent-autoscaler-config",
			"kubedns-config-images",
			"pdcsi-metrics-collector-config-map":
			return true
		}
	}
	if cm.Namespace == "gmp-system" {
		switch cm.Name {
		case "config-images", "scheduled-jobs":
			return true
		}
	}

	if cm.Namespace == "argocd" || labelsValue(cm.Labels, "app.kubernetes.io/part-of") == "argocd" {
		switch cm.Name {
		case "argocd-cm",
			"argocd-cmd-params-cm",
			"argocd-dex-cm",
			"argocd-gpg-keys-cm",
			"argocd-notifications-cm",
			"argocd-rbac-cm",
			"argocd-ssh-known-hosts-cm",
			"argocd-tls-certs-cm":
			return true
		}
	}

	if labelsValue(cm.Labels, "app.kubernetes.io/name") == "ingress-nginx" {
		return true
	}

	if cm.Namespace == "argo-rollouts" {
		switch cm.Name {
		case "argo-rollouts-config", "argo-rollouts-notification-configmap":
			return true
		}
	}

	if labelsValue(cm.Labels, "app.kubernetes.io/part-of") == "kyverno" {
		switch cm.Name {
		case "kyverno", "kyverno-metrics":
			return true
		}
	}

	if cm.Name == "argo-workflows-workflow-controller-configmap" && labelsValue(cm.Labels, "app.kubernetes.io/part-of") == "argo-workflows" {
		return true
	}

	if cm.Name == "cnpg-controller-manager-config" && labelsValue(cm.Labels, "app.kubernetes.io/name") == "cloudnative-pg" {
		return true
	}

	return false
}

func isKnownPlatformSecret(sec *corev1.Secret) bool {
	if sec == nil {
		return false
	}

	if labelsValue(sec.Labels, "sealedsecrets.bitnami.com/sealed-secrets-key") != "" {
		return true
	}

	if labelsValue(sec.Labels, "argocd.argoproj.io/secret-type") != "" {
		return true
	}
	if labelsValue(sec.Labels, "app.kubernetes.io/part-of") == "argocd" {
		switch sec.Name {
		case "argocd-secret", "argocd-notifications-secret":
			return true
		}
	}

	if labelsValue(sec.Labels, "app.kubernetes.io/managed-by") == "cert-manager-webhook" && strings.Contains(sec.Name, "webhook-ca") {
		return true
	}
	if sec.Annotations != nil && sec.Annotations["cert-manager.io/allow-direct-injection"] == "true" && strings.Contains(sec.Name, "webhook-ca") {
		return true
	}
	if labelsValue(sec.Labels, "app.kubernetes.io/managed-by") == "cert-manager" && strings.Contains(sec.Name, "account-key") {
		return true
	}

	if sec.Namespace == "gmp-public" && sec.Name == "alertmanager" {
		return true
	}

	if sec.Namespace == "gmp-system" && sec.Name == "webhook-tls" {
		if metadataValueEqual(sec.Labels, sec.Annotations, "addonmanager.kubernetes.io/mode", "Reconcile") {
			return true
		}
		if hasMetadataKeyPrefix(sec.Labels, sec.Annotations, "components.gke.io/") {
			return true
		}
	}

	if sec.Namespace == "crossplane-system" && sec.Name == "crossplane-root-ca" {
		return true
	}

	return false
}

func isDynamicLoaderConfigMap(cm *corev1.ConfigMap) bool {
	// Sidecar loaders watch these label conventions instead of object refs.
	for _, key := range []string{
		"grafana_dashboard",
		"grafana_datasource",
		"prometheus_rule",
		"fluentd_config",
	} {
		if metadataValueEnabled(labelsValue(cm.Labels, key)) {
			return true
		}
	}

	return metadataValueTruthy(labelsValue(cm.Labels, "k8sgpt.ai/dynamically-loaded"))
}

func labelsValue(labels map[string]string, key string) string {
	if labels == nil {
		return ""
	}
	return labels[key]
}

func metadataValueEnabled(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	switch strings.ToLower(value) {
	case "false", "0", "no", "off":
		return false
	default:
		return true
	}
}

func metadataValueTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1", "yes", "on":
		return true
	default:
		return false
	}
}

func metadataValueEqual(labels, annotations map[string]string, key, want string) bool {
	return strings.EqualFold(labelsValue(labels, key), want) || strings.EqualFold(labelsValue(annotations, key), want)
}

func hasMetadataKeyPrefix(labels, annotations map[string]string, prefix string) bool {
	for key := range labels {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	for key := range annotations {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

type configReferencePodSpec struct {
	namespace string
	spec      corev1.PodSpec
}

func (input *CheckInput) configReferencePodSpecs() []configReferencePodSpec {
	var specs []configReferencePodSpec
	for _, pod := range input.Pods {
		if podIsTerminal(pod) {
			continue
		}
		specs = append(specs, configReferencePodSpec{namespace: pod.Namespace, spec: pod.Spec})
	}
	for _, d := range input.Deployments {
		specs = append(specs, configReferencePodSpec{namespace: d.Namespace, spec: d.Spec.Template.Spec})
	}
	for _, ss := range input.StatefulSets {
		specs = append(specs, configReferencePodSpec{namespace: ss.Namespace, spec: ss.Spec.Template.Spec})
	}
	for _, ds := range input.DaemonSets {
		specs = append(specs, configReferencePodSpec{namespace: ds.Namespace, spec: ds.Spec.Template.Spec})
	}
	for _, job := range input.Jobs {
		if jobIsTerminal(job) {
			continue
		}
		specs = append(specs, configReferencePodSpec{namespace: job.Namespace, spec: job.Spec.Template.Spec})
	}
	for _, cj := range input.CronJobs {
		specs = append(specs, configReferencePodSpec{namespace: cj.Namespace, spec: cj.Spec.JobTemplate.Spec.Template.Spec})
	}
	return specs
}

func jobIsTerminal(job *batchv1.Job) bool {
	for _, cond := range job.Status.Conditions {
		if cond.Status != corev1.ConditionTrue {
			continue
		}
		if cond.Type == batchv1.JobComplete || cond.Type == batchv1.JobFailed {
			return true
		}
	}
	return false
}

func podIsTerminal(pod *corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed
}

func collectPodSpecRefs(ns string, spec corev1.PodSpec, saByKey map[string]*corev1.ServiceAccount, cms, secrets map[string]bool) {
	for _, c := range spec.InitContainers {
		collectContainerRefs(ns, c, cms, secrets)
	}
	for _, c := range spec.Containers {
		collectContainerRefs(ns, c, cms, secrets)
	}
	for _, c := range spec.EphemeralContainers {
		collectEnvRefs(ns, c.Env, c.EnvFrom, cms, secrets)
	}
	for _, v := range spec.Volumes {
		collectVolumeRefs(ns, v, cms, secrets)
	}
	for _, ips := range spec.ImagePullSecrets {
		addRef(secrets, ns, ips.Name)
	}
	// ServiceAccount admission defaults imagePullSecrets only when the PodSpec leaves them empty.
	if len(spec.ImagePullSecrets) == 0 {
		collectServiceAccountImagePullSecrets(ns, spec, saByKey, secrets)
	}
}

func collectContainerRefs(ns string, c corev1.Container, cms, secrets map[string]bool) {
	collectEnvRefs(ns, c.Env, c.EnvFrom, cms, secrets)
}

func collectEnvRefs(ns string, envs []corev1.EnvVar, envFroms []corev1.EnvFromSource, cms, secrets map[string]bool) {
	for _, env := range envs {
		if env.ValueFrom != nil {
			if env.ValueFrom.ConfigMapKeyRef != nil {
				addRef(cms, ns, env.ValueFrom.ConfigMapKeyRef.Name)
			}
			if env.ValueFrom.SecretKeyRef != nil {
				addRef(secrets, ns, env.ValueFrom.SecretKeyRef.Name)
			}
		}
	}
	for _, envFrom := range envFroms {
		if envFrom.ConfigMapRef != nil {
			addRef(cms, ns, envFrom.ConfigMapRef.Name)
		}
		if envFrom.SecretRef != nil {
			addRef(secrets, ns, envFrom.SecretRef.Name)
		}
	}
}

func collectServiceAccountImagePullSecrets(ns string, spec corev1.PodSpec, saByKey map[string]*corev1.ServiceAccount, secrets map[string]bool) {
	if len(saByKey) == 0 {
		return
	}
	sa, ok := saByKey[ns+"/"+podServiceAccountName(spec)]
	if !ok {
		return
	}
	for _, ips := range sa.ImagePullSecrets {
		addRef(secrets, ns, ips.Name)
	}
}

func collectVolumeRefs(ns string, v corev1.Volume, cms, secrets map[string]bool) {
	if v.ConfigMap != nil {
		addRef(cms, ns, v.ConfigMap.Name)
	}
	if v.Secret != nil {
		addRef(secrets, ns, v.Secret.SecretName)
	}
	if v.Projected != nil {
		for _, src := range v.Projected.Sources {
			if src.ConfigMap != nil {
				addRef(cms, ns, src.ConfigMap.Name)
			}
			if src.Secret != nil {
				addRef(secrets, ns, src.Secret.Name)
			}
		}
	}
	if v.CSI != nil && v.CSI.NodePublishSecretRef != nil {
		addRef(secrets, ns, v.CSI.NodePublishSecretRef.Name)
	}
	if v.FlexVolume != nil && v.FlexVolume.SecretRef != nil {
		addRef(secrets, ns, v.FlexVolume.SecretRef.Name)
	}
	if v.AzureFile != nil {
		addRef(secrets, ns, v.AzureFile.SecretName)
	}
	if v.CephFS != nil && v.CephFS.SecretRef != nil {
		addRef(secrets, ns, v.CephFS.SecretRef.Name)
	}
	if v.RBD != nil && v.RBD.SecretRef != nil {
		addRef(secrets, ns, v.RBD.SecretRef.Name)
	}
	if v.Cinder != nil && v.Cinder.SecretRef != nil {
		addRef(secrets, ns, v.Cinder.SecretRef.Name)
	}
	if v.ScaleIO != nil && v.ScaleIO.SecretRef != nil {
		addRef(secrets, ns, v.ScaleIO.SecretRef.Name)
	}
	if v.ISCSI != nil && v.ISCSI.SecretRef != nil {
		addRef(secrets, ns, v.ISCSI.SecretRef.Name)
	}
	if v.StorageOS != nil && v.StorageOS.SecretRef != nil {
		addRef(secrets, ns, v.StorageOS.SecretRef.Name)
	}
}

func addRef(refs map[string]bool, namespace, name string) {
	if namespace == "" || name == "" {
		return
	}
	refs[namespace+"/"+name] = true
}

func hasControllerOwnerReference(refs []metav1.OwnerReference) bool {
	for _, ref := range refs {
		if ref.Controller != nil && *ref.Controller {
			return true
		}
	}
	return false
}

// ============================================================================
// Resource utilization checks
// ============================================================================

func checkResourceUtilization(tr *evalTracker, metrics []PodMetricsInput) []Finding {
	if len(metrics) == 0 {
		return nil
	}

	var findings []Finding
	for _, m := range metrics {
		// Pods with no requests at all can't be measured against anything —
		// they're out of scope, not passing.
		if m.CPURequest > 0 || m.MemoryRequest > 0 {
			tr.record("resourceUtilization", m.Namespace)
		}
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

func checkDeprecatedAPIs(tr *evalTracker, servedAPIs []string, clusterVersion string) []Finding {
	if len(servedAPIs) == 0 || clusterVersion == "" {
		return nil
	}

	deprecations := DeprecationsByGroupVersion()
	var findings []Finding

	for _, gv := range servedAPIs {
		tr.record("deprecatedAPIVersion", "")
		entries, ok := deprecations[gv]
		if !ok {
			continue
		}
		for _, entry := range entries {
			msg := fmt.Sprintf("API %s is deprecated (since %s, removed in %s) — use %s",
				gv, entry.DeprecatedIn, entry.RemovedIn, entry.Replacement)
			if entry.Kind != "" {
				msg = fmt.Sprintf("API %s %s is deprecated (since %s, removed in %s) — use %s",
					gv, entry.Kind, entry.DeprecatedIn, entry.RemovedIn, entry.Replacement)
			}
			// One evaluated subject per served group/version, so all of a
			// gv's deprecated kinds share one finding key (standard merge
			// joins the messages) — keeps failed <= evaluated by
			// construction instead of relying on the clamp.
			findings = append(findings, Finding{
				Kind:     "APIVersion",
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

func buildResults(findings []Finding, tr *evalTracker, missingInputs []string) *ScanResults {
	categories := map[string]CategorySummary{}
	// Initialize all categories
	for _, cat := range []string{CategorySecurity, CategoryReliability, CategoryEfficiency} {
		categories[cat] = CategorySummary{}
	}

	// Populate Group from the built-in (Kind→Group) table. Check emission
	// sites leave Group="" so the per-check code stays terse — single
	// point of truth here instead of every Finding{} literal.
	for i := range findings {
		if findings[i].Group == "" {
			findings[i].Group = resourceid.GroupForBuiltinKind(findings[i].Kind)
		}
	}

	// Merge findings: same (resource, checkID) get combined into one finding
	// with messages joined, so multi-container workloads show all affected containers.
	type checkKey struct{ resource, checkID string }
	mergeIndex := make(map[checkKey]int) // key → index in dedupFindings
	var dedupFindings []Finding

	for _, f := range findings {
		key := checkKey{ResourceKey(f.Group, f.Kind, f.Namespace, f.Name), f.CheckID}
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

	checkCounts, totalPassing := deriveCheckCounts(tr.counts, dedupFindings, categories)

	// Include full registry so settings dialog can show all checks (including disabled ones)
	checks := make(map[string]CheckMeta, len(CheckRegistry))
	for id, meta := range CheckRegistry {
		checks[id] = meta
	}

	return &ScanResults{
		Summary: ScanSummary{
			Passing:    totalPassing,
			Warning:    totalWarning,
			Danger:     totalDanger,
			Categories: categories,
		},
		Findings:             dedupFindings,
		Groups:               GroupByResource(dedupFindings),
		Checks:               checks,
		CheckCounts:          checkCounts,
		EvaluatedByNamespace: tr.counts,
		MissingInputs:        missingInputs,
	}
}

// deriveCheckCounts turns per-namespace evaluation tallies plus MERGED
// findings into per-check evaluated/passed counts, accumulating each check's
// passed into its registry category's Passing (categories is mutated). Both
// sides count distinct subjects, so passed = evaluated - failed; the zero
// clamp absorbs the one legitimate mismatch — a deprecated group/version
// serving several deprecated kinds yields one evaluated subject but one
// merged finding per kind.
func deriveCheckCounts(evalByNS map[string]map[string]int, mergedFindings []Finding, categories map[string]CategorySummary) (map[string]CheckCount, int) {
	failedByCheck := make(map[string]int)
	for _, f := range mergedFindings {
		failedByCheck[f.CheckID]++
	}

	checkCounts := make(map[string]CheckCount, len(evalByNS))
	totalPassing := 0
	for id, byNS := range evalByNS {
		evaluated := 0
		for _, n := range byNS {
			evaluated += n
		}
		if evaluated == 0 {
			continue
		}
		passed := evaluated - failedByCheck[id]
		if passed < 0 {
			passed = 0
		}
		checkCounts[id] = CheckCount{Evaluated: evaluated, Passed: passed}
		totalPassing += passed
		if meta, ok := CheckRegistry[id]; ok {
			cs := categories[meta.Category]
			cs.Passing += passed
			categories[meta.Category] = cs
		}
	}
	return checkCounts, totalPassing
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

// stuckTerminatingThresholdWarning is when "Terminating" stops looking
// like normal cleanup and starts looking like a stuck finalizer.
// Most controllers complete cleanup within a couple of minutes.
//
// keep in sync: pkg/gitops/insights/insights.go::detectPendingDeletion
// uses the same 5min/30min boundaries to ramp Issue severity. If you
// retune one, retune the other so the cluster Audit and the per-resource
// GitOps Issue agree on what counts as "stuck".
const (
	stuckTerminatingThresholdWarning = 5 * time.Minute
	stuckTerminatingThresholdDanger  = 30 * time.Minute
)

// checkStuckTerminating finds resources stuck in the Terminating state
// past the warning/danger thresholds. Scans every typed kind in the
// CheckInput and applies the same age-based severity ramp the insights
// detector uses, so an operator looking at the cluster Audit and the
// GitOps detail page sees the same severity for the same resource.
//
// Why this lives at the audit layer in addition to per-resource
// insights: an operator may not know which resources are stuck.
// Audit surfaces the *list* up-front; a per-resource insight only
// helps once they've drilled into a specific app. The two surfaces
// are complementary, not redundant.
func checkStuckTerminating(tr *evalTracker, input *CheckInput) []Finding {
	if input == nil {
		return nil
	}
	var findings []Finding
	now := time.Now()
	emit := func(kind string, obj metav1.Object) {
		tr.record("stuckTerminating", obj.GetNamespace())
		dt := obj.GetDeletionTimestamp()
		if dt == nil || dt.IsZero() {
			return
		}
		age := now.Sub(dt.Time)
		if age < stuckTerminatingThresholdWarning {
			return
		}
		severity := SeverityWarning
		if age >= stuckTerminatingThresholdDanger {
			severity = SeverityDanger
		}
		// Naming the finalizers is the most actionable hint we can
		// surface — the user otherwise has to drill into YAML to find
		// what's blocking cleanup. Some controllers add multiple keys
		// (Argo's `resources-finalizer.argocd.argoproj.io` plus the
		// legacy cascade); listing them all costs a few extra bytes
		// and is genuinely useful.
		finalizers := obj.GetFinalizers()
		var note string
		if len(finalizers) > 0 {
			note = " — finalizers: " + strings.Join(finalizers, ", ")
		}
		findings = append(findings, Finding{
			Kind:      kind,
			Namespace: obj.GetNamespace(),
			Name:      obj.GetName(),
			CheckID:   "stuckTerminating",
			Category:  CategoryReliability,
			Severity:  severity,
			Message:   fmt.Sprintf("Has been pending deletion for %s%s", timeutil.FormatAgeShort(age), note),
		})
	}
	// Scan every typed slice we have. Adding a new type to CheckInput
	// later means adding one line here — trade-off accepted for
	// explicitness over reflection.
	for _, p := range input.Pods {
		emit("Pod", p)
	}
	for _, d := range input.Deployments {
		emit("Deployment", d)
	}
	for _, s := range input.StatefulSets {
		emit("StatefulSet", s)
	}
	for _, d := range input.DaemonSets {
		emit("DaemonSet", d)
	}
	for _, s := range input.Services {
		emit("Service", s)
	}
	for _, i := range input.Ingresses {
		emit("Ingress", i)
	}
	for _, h := range input.HorizontalPodAutoscalers {
		emit("HorizontalPodAutoscaler", h)
	}
	for _, p := range input.PodDisruptionBudgets {
		emit("PodDisruptionBudget", p)
	}
	for _, c := range input.ConfigMaps {
		emit("ConfigMap", c)
	}
	for _, s := range input.Secrets {
		emit("Secret", s)
	}
	for _, sa := range input.ServiceAccounts {
		emit("ServiceAccount", sa)
	}
	return findings
}

// checkCrossplaneStuck finds Crossplane Managed Resources, Composites, and
// Claims with Ready=False or Synced=False past the same 5-minute/30-minute
// thresholds used by checkStuckTerminating. Reusing the thresholds keeps the
// audit page consistent across stuck-resource categories so operators don't
// have to relearn what "long enough to flag" means for each kind.
//
// The check inspects status.conditions on each unstructured object directly
// — Crossplane condition semantics are stable across every provider (Ready,
// Synced) so we don't need per-provider knowledge.
func checkCrossplaneStuck(tr *evalTracker, input *CheckInput) []Finding {
	if input == nil {
		return nil
	}
	var findings []Finding
	now := time.Now()

	emit := func(category string, u *unstructured.Unstructured) {
		// Skip terminating resources — they're already flagged by checkStuckTerminating
		// with the right severity ramp. Reporting both creates noise.
		if !u.GetDeletionTimestamp().IsZero() {
			return
		}
		// Don't flag paused resources — the operator intentionally stopped
		// reconciliation; lighting a "stuck" finding is misleading.
		if u.GetAnnotations()["crossplane.io/paused"] == "true" {
			return
		}
		tr.record("crossplaneStuck", u.GetNamespace())
		cond, ok := findFalseCrossplaneCondition(u)
		if !ok {
			return
		}
		age := now.Sub(cond.transitionTime)
		if age < stuckTerminatingThresholdWarning {
			return
		}
		severity := SeverityWarning
		if age >= stuckTerminatingThresholdDanger {
			severity = SeverityDanger
		}
		// Crossplane conditions almost always include a reason+message — surface
		// both, since the message often contains the upstream cloud-API error
		// verbatim (the actionable thing) and the reason classifies it.
		extra := ""
		if cond.reason != "" {
			extra = " (" + cond.reason + ")"
		}
		if cond.message != "" {
			// Keep messages bounded — some providers return multi-line errors.
			msg := strings.SplitN(cond.message, "\n", 2)[0]
			extra += ": " + msg
		}
		findings = append(findings, Finding{
			Kind:      u.GetKind(),
			Namespace: u.GetNamespace(),
			Name:      u.GetName(),
			CheckID:   "crossplaneStuck",
			Category:  category,
			Severity:  severity,
			Message:   fmt.Sprintf("%s=False for %s%s", cond.condType, timeutil.FormatAgeShort(age), extra),
		})
	}
	for _, mr := range input.ManagedResources {
		if mr != nil {
			emit(CategoryReliability, mr)
		}
	}
	for _, xr := range input.CompositeResources {
		if xr != nil {
			emit(CategoryReliability, xr)
		}
	}
	return findings
}

// crossplaneFalseCondition holds the fields we need from a False Ready/Synced
// condition. Local to the audit package so it doesn't pollute the public API.
type crossplaneFalseCondition struct {
	condType       string
	reason         string
	message        string
	transitionTime time.Time
}

// findFalseCrossplaneCondition returns the most-actionable False condition
// for a Crossplane resource — Synced=False first (configuration error,
// fixable), then Ready=False (provider can't converge, may resolve). Returns
// false if neither is False or the transition time is missing.
func findFalseCrossplaneCondition(u *unstructured.Unstructured) (crossplaneFalseCondition, bool) {
	conds, found, err := unstructured.NestedSlice(u.Object, "status", "conditions")
	if err != nil || !found {
		return crossplaneFalseCondition{}, false
	}
	// Synced gets priority: it usually indicates the provider rejected
	// the spec (bad ProviderConfig, malformed forProvider, missing perms).
	// Ready=False is downstream — fixing Synced often resolves Ready.
	priority := []string{"Synced", "Ready"}
	for _, want := range priority {
		for _, raw := range conds {
			c, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			t, _ := c["type"].(string)
			s, _ := c["status"].(string)
			if t != want || s != "False" {
				continue
			}
			reason, _ := c["reason"].(string)
			message, _ := c["message"].(string)
			tt, _ := c["lastTransitionTime"].(string)
			var transitionTime time.Time
			if tt != "" {
				if parsed, err := time.Parse(time.RFC3339, tt); err == nil {
					transitionTime = parsed
				}
			}
			if transitionTime.IsZero() {
				// Without a transition time we can't measure age — skip this
				// condition and let the outer loop fall through to the next
				// priority tier (Synced first, then Ready). Crossplane always
				// sets it on its own conditions; missing means non-standard
				// producer.
				continue
			}
			return crossplaneFalseCondition{
				condType:       t,
				reason:         reason,
				message:        message,
				transitionTime: transitionTime,
			}, true
		}
	}
	return crossplaneFalseCondition{}, false
}
