package insights

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// kubectlLastAppliedAnnotation is the annotation kubectl (and Argo's
// client-side apply path) writes on every apply, holding a JSON dump of
// the desired state at the time of apply. We diff this against the live
// spec to produce a per-resource drift view without needing the Argo API
// server or a Git fetch.
//
// Limitations:
//   - server-side-apply doesn't write this annotation; SSA tracks intent
//     via metadata.managedFields instead. Future work: managedFields-based
//     drift for SSA-applied resources.
//   - Helm-installed resources (Flux HelmRelease, helm CLI) don't carry
//     this annotation either.
//   - Resources mutated by other controllers between apply and drift
//     check will show those mutations as added/changed entries — that's
//     the *correct* behavior for a "what's drifted" view, even if the
//     mutation is harmless (defaults, status fields).
const kubectlLastAppliedAnnotation = "kubectl.kubernetes.io/last-applied-configuration"

// driftEntryCap bounds the number of entries returned per resource. Sets
// the worst-case payload size and keeps the UI scannable. When trimmed,
// Drift.Truncated is set so the UI can suggest "see Argo for full diff".
const driftEntryCap = 50

// computeDriftFromLastApplied diffs the live spec against the desired spec
// captured in the last-applied-configuration annotation. Returns nil when
// drift can't be computed (no annotation, parse failure, no spec on either
// side) — callers should treat nil as "no diff available", not "no drift".
//
// ignorePointers is a list of RFC 6902 JSON pointer prefixes (e.g.,
// "/spec/replicas") that should be filtered from the result. Sourced from
// the Argo Application's `spec.ignoreDifferences` for the matching
// (group, kind) — same mechanism the Argo UI uses to suppress operator-
// declared "this field isn't managed by GitOps" rules.
func computeDriftFromLastApplied(live *unstructured.Unstructured, ignorePointers []string) *Drift {
	if live == nil {
		return nil
	}
	annotations := live.GetAnnotations()
	raw := annotations[kubectlLastAppliedAnnotation]
	if raw == "" {
		return nil
	}
	var desiredObj map[string]any
	if err := json.Unmarshal([]byte(raw), &desiredObj); err != nil {
		log.Printf("[gitops/drift] last-applied annotation unparseable for %s/%s/%s: %v", live.GetKind(), live.GetNamespace(), live.GetName(), err)
		return nil
	}
	// Apply K8s API-server-style defaults to the desired side before diffing.
	// Mirrors Argo's gitops-engine generateSchemeDefaultPatch: fields the
	// API server fills in automatically (progressDeadlineSeconds: 600,
	// imagePullPolicy: IfNotPresent, dnsPolicy: ClusterFirst, etc.) become
	// the same on both sides instead of showing as drift. Without this,
	// every Deployment applied with a partial manifest produces ~7-10 false
	// drift entries that operators have to mentally filter out.
	desiredObj = applySchemeDefaults(desiredObj)
	desiredSpec, _ := desiredObj["spec"].(map[string]any)
	liveSpec, _, _ := unstructured.NestedMap(live.Object, "spec")
	if desiredSpec == nil && liveSpec == nil {
		return nil
	}
	entries := diffValues("spec", desiredSpec, liveSpec, nil)
	// Strip server-assigned fields (clusterIP, nodeName, etc.) from the
	// diff output regardless of what the user set. These are *runtime*-
	// assigned values; the user can never predict them, and showing them
	// as drift just trains operators to ignore the chip.
	if kind, _ := live.Object["kind"].(string); kind != "" {
		if serverAssigned := kindServerAssignedPaths[kind]; len(serverAssigned) > 0 {
			entries = filterIgnoredPaths(entries, serverAssigned)
		}
	}
	if len(ignorePointers) > 0 {
		entries = filterIgnoredPaths(entries, ignorePointers)
	}
	if len(entries) == 0 {
		// last-applied parsed successfully but no field-level diff —
		// returning an empty Drift would be misleading (UI would show
		// "no drift" alongside the "OutOfSync" badge). Nil signals "we
		// looked but didn't find structural drift" and the UI can fall
		// back to the textual explainer.
		return nil
	}
	entries, truncated := sortAndCapDriftEntries(entries)
	return &Drift{
		Entries:   entries,
		Source:    DriftSourceLastApplied,
		Truncated: truncated,
	}
}

// applySchemeDefaults fills in K8s API server defaults on the desired side
// of the diff before comparison. Otherwise a Deployment applied with a
// partial manifest produces ~7-10 false drift entries — every API-server-
// added default (progressDeadlineSeconds:600, dnsPolicy:ClusterFirst,
// imagePullPolicy:IfNotPresent, etc.) shows up as "added" because the
// last-applied annotation only contains the user-written fields.
//
// We don't use k8s.io/client-go/kubernetes/scheme.Default() because client-
// go's scheme has the types but not the defaulter functions — those live
// in k8s.io/kubernetes/pkg/apis/*/install which isn't intended as a public
// dependency. Argo CD's gitops-engine takes that dependency directly
// (a k8s.io/kubernetes import in their go.mod); we hardcode the small set
// of defaults for the kinds operators most commonly hit drift noise on.
// The list is finite, public, and changes ~once per major K8s release.
//
// Returns the input unchanged when the kind isn't in the defaults table —
// CRDs and uncommon kinds keep the raw-diff behavior.
func applySchemeDefaults(obj map[string]any) map[string]any {
	kind, _ := obj["kind"].(string)
	defaulter, ok := kindDefaulters[kind]
	if !ok {
		return obj
	}
	spec, ok := obj["spec"].(map[string]any)
	if !ok {
		// A registered kind with a non-map spec is structurally malformed —
		// usually a corrupt last-applied annotation. Defaulter can't run,
		// and the diff degrades to the noisy unfiltered output. Log so the
		// "drift suddenly got noisy on Deployment X" question is greppable.
		if _, present := obj["spec"]; present {
			log.Printf("[gitops/drift] kind=%s has non-map spec in last-applied; defaulter skipped", kind)
		}
		return obj
	}
	defaulter(spec)
	return obj
}

// kindServerAssignedPaths lists fields the API server fills in at create
// time with values the user can't predict (cluster IPs, scheduler-assigned
// node, controller-set ports, etc.). These are stripped from drift output
// regardless of the user's manifest. RFC-6902 pointer format to match
// filterIgnoredPaths' input shape.
//
// Distinct from kindDefaulters: defaulters have *known* default values we
// can pre-fill; these have *runtime-assigned* values we can only ignore.
var kindServerAssignedPaths = map[string][]string{
	"Service": {
		// API server allocates from the cluster IP pool. Single-stack
		// clusters also auto-populate clusterIPs[] and ipFamilies[].
		"/spec/clusterIP",
		"/spec/clusterIPs",
		"/spec/ipFamilies",
		"/spec/ipFamilyPolicy",
		// Defaults to "Cluster" when omitted, but the field is technically
		// user-settable; we treat it as runtime-assigned because nobody
		// actually overrides it in practice.
		"/spec/internalTrafficPolicy",
	},
	"Pod": {
		// Scheduler assigns this at admission. Drift makes no sense.
		"/spec/nodeName",
	},
	"PersistentVolumeClaim": {
		// volumeName is filled in by the PV provisioner.
		"/spec/volumeName",
	},
}

// specDefaulter mutates a resource's spec sub-map to fill in fields the
// K8s API server defaults at admission. Convention: write only via
// setIfMissing — never overwrite a user-set value.
type specDefaulter func(spec map[string]any)

// kindDefaulters maps a Kubernetes Kind to its specDefaulter. The handlers
// compose: workload kinds (Deployment/StatefulSet/DaemonSet/Job) all delegate
// to the Pod template defaulter for spec.template.spec, which in turn
// delegates to the Container defaulter for each container.
//
// Keys are bare Kind strings. K8s built-in kinds only — adding a CRD that
// collides with a core kind (e.g. Knative `Service`, CAPI `Cluster`) requires
// disambiguating by group, not just adding here.
var kindDefaulters = map[string]specDefaulter{
	"Deployment":  applyDeploymentSpecDefaults,
	"StatefulSet": applyStatefulSetSpecDefaults,
	"DaemonSet":   applyDaemonSetSpecDefaults,
	"ReplicaSet":  applyReplicaSetSpecDefaults,
	"Job":         applyJobSpecDefaults,
	"CronJob":     applyCronJobSpecDefaults,
	"Pod":         applyPodSpecDefaults,
	"Service":     applyServiceSpecDefaults,
}

func applyDeploymentSpecDefaults(spec map[string]any) {
	setIfMissing(spec, "replicas", int64(1))
	setIfMissing(spec, "progressDeadlineSeconds", int64(600))
	setIfMissing(spec, "revisionHistoryLimit", int64(10))
	setIfMissing(spec, "strategy", map[string]any{
		"type": "RollingUpdate",
		"rollingUpdate": map[string]any{
			"maxSurge":       "25%",
			"maxUnavailable": "25%",
		},
	})
	applyPodTemplateDefaults(spec, "Always")
}

func applyStatefulSetSpecDefaults(spec map[string]any) {
	setIfMissing(spec, "replicas", int64(1))
	setIfMissing(spec, "revisionHistoryLimit", int64(10))
	setIfMissing(spec, "podManagementPolicy", "OrderedReady")
	setIfMissing(spec, "updateStrategy", map[string]any{"type": "RollingUpdate"})
	applyPodTemplateDefaults(spec, "Always")
}

func applyDaemonSetSpecDefaults(spec map[string]any) {
	setIfMissing(spec, "revisionHistoryLimit", int64(10))
	setIfMissing(spec, "updateStrategy", map[string]any{
		"type":          "RollingUpdate",
		"rollingUpdate": map[string]any{"maxUnavailable": int64(1)},
	})
	applyPodTemplateDefaults(spec, "Always")
}

func applyReplicaSetSpecDefaults(spec map[string]any) {
	setIfMissing(spec, "replicas", int64(1))
	applyPodTemplateDefaults(spec, "Always")
}

func applyJobSpecDefaults(spec map[string]any) {
	setIfMissing(spec, "backoffLimit", int64(6))
	setIfMissing(spec, "completionMode", "NonIndexed")
	// Job's Pod template can't use restartPolicy=Always (the K8s validator
	// rejects it). The actual API server default for omitted restartPolicy
	// on a Job is "Never". Pass it through so we don't fill in a value the
	// API server would reject and the user obviously didn't choose.
	applyPodTemplateDefaults(spec, "Never")
}

func applyCronJobSpecDefaults(spec map[string]any) {
	setIfMissing(spec, "concurrencyPolicy", "Allow")
	setIfMissing(spec, "failedJobsHistoryLimit", int64(1))
	setIfMissing(spec, "successfulJobsHistoryLimit", int64(3))
	setIfMissing(spec, "suspend", false)
	// CronJob's Pod template lives at spec.jobTemplate.spec.template, not
	// spec.template. Without this descent, every CronJob shows the full 7-10
	// pod-template defaults as drift — the entire commit's value
	// proposition would be broken for CronJobs specifically.
	if jt, ok := spec["jobTemplate"].(map[string]any); ok {
		if js, ok := jt["spec"].(map[string]any); ok {
			applyJobSpecDefaults(js)
		}
	}
}

// applyPodTemplateDefaults walks spec.template.spec (the Pod template
// inside a workload kind) and applies Pod-level + Container-level defaults.
// Container defaults run per-element of spec.containers and spec.initContainers.
// defaultRestartPolicy is parameterized so Job pod templates can use "Never"
// instead of the long-running-workload default of "Always".
func applyPodTemplateDefaults(spec map[string]any, defaultRestartPolicy string) {
	template, ok := spec["template"].(map[string]any)
	if !ok {
		return
	}
	podSpec, ok := template["spec"].(map[string]any)
	if !ok {
		return
	}
	applyPodSpecDefaultsWithRestart(podSpec, defaultRestartPolicy)
}

// applyPodSpecDefaults is the entry point for standalone Pods (no enclosing
// workload kind). Standalone Pods default to restartPolicy=Always.
func applyPodSpecDefaults(spec map[string]any) {
	applyPodSpecDefaultsWithRestart(spec, "Always")
}

func applyPodSpecDefaultsWithRestart(spec map[string]any, defaultRestartPolicy string) {
	setIfMissing(spec, "dnsPolicy", "ClusterFirst")
	setIfMissing(spec, "restartPolicy", defaultRestartPolicy)
	setIfMissing(spec, "schedulerName", "default-scheduler")
	setIfMissing(spec, "securityContext", map[string]any{})
	setIfMissing(spec, "terminationGracePeriodSeconds", int64(30))
	for _, key := range []string{"containers", "initContainers"} {
		if list, ok := spec[key].([]any); ok {
			for _, c := range list {
				if cm, ok := c.(map[string]any); ok {
					applyContainerDefaults(cm)
				}
			}
		}
	}
}

func applyContainerDefaults(c map[string]any) {
	image, _ := c["image"].(string)
	setIfMissing(c, "imagePullPolicy", defaultImagePullPolicy(image))
	setIfMissing(c, "terminationMessagePath", "/dev/termination-log")
	setIfMissing(c, "terminationMessagePolicy", "File")
	setIfMissing(c, "resources", map[string]any{})
	if ports, ok := c["ports"].([]any); ok {
		for _, p := range ports {
			if pm, ok := p.(map[string]any); ok {
				setIfMissing(pm, "protocol", "TCP")
			}
		}
	}
}

func defaultImagePullPolicy(image string) string {
	image = strings.TrimSpace(image)
	if image == "" {
		return "Always"
	}
	base := image
	if i := strings.IndexByte(base, '@'); i >= 0 {
		base = base[:i]
	}
	lastSlash := strings.LastIndexByte(base, '/')
	lastColon := strings.LastIndexByte(base, ':')
	if lastColon <= lastSlash {
		return "Always"
	}
	if base[lastColon+1:] == "latest" {
		return "Always"
	}
	return "IfNotPresent"
}

func applyServiceSpecDefaults(spec map[string]any) {
	setIfMissing(spec, "type", "ClusterIP")
	setIfMissing(spec, "sessionAffinity", "None")
	setIfMissing(spec, "ipFamilyPolicy", "SingleStack")
	if ports, ok := spec["ports"].([]any); ok {
		for _, p := range ports {
			if pm, ok := p.(map[string]any); ok {
				setIfMissing(pm, "protocol", "TCP")
				// targetPort defaults to port when omitted — but we can't
				// safely back-fill an integer here without the matching port
				// value. Skip; the controller's resulting field shows as drift
				// only if the user actually changed it.
			}
		}
	}
}

// setIfMissing writes a default value when the key is absent or maps to a
// nilish value (per isNilish). Matches the "API server fills in this default
// if the user didn't set it" semantics: an explicit empty value in the
// user's manifest still gets defaulted, which is what the API server does
// for most fields.
func setIfMissing(m map[string]any, key string, value any) {
	existing, present := m[key]
	if !present || isNilish(existing) {
		m[key] = value
	}
}

// filterIgnoredPaths strips drift entries whose path matches any of the
// supplied RFC 6902 JSON pointer prefixes. Argo's `spec.ignoreDifferences`
// uses pointers like "/spec/replicas" — we translate that to our dot-path
// shape ("spec.replicas") and match by exact-or-prefix so an ignore on
// "/spec/template" removes every entry under it.
func filterIgnoredPaths(entries []DriftEntry, pointers []string) []DriftEntry {
	if len(pointers) == 0 {
		return entries
	}
	prefixes := make([]string, 0, len(pointers))
	for _, p := range pointers {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		prefixes = append(prefixes, pointerToDotPath(p))
	}
	// Fresh allocation rather than `entries[:0]` aliasing — the caller may
	// still hold the original slice (e.g., for a pre-filter count or audit
	// log), and silently mutating it would be a latent corruption footgun.
	// driftEntryCap caps the size at 50, so the allocation cost is trivial.
	out := make([]DriftEntry, 0, len(entries))
	for _, e := range entries {
		if pathMatchesAnyPrefix(e.Path, prefixes) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// pointerToDotPath converts an RFC 6902 JSON pointer ("/spec/template/spec/
// containers/0/image") into the dot-path shape diffValues emits ("spec.template.
// spec.containers.[0].image"). Numeric segments are wrapped in brackets to match
// joinPath, so an ignoreDifferences rule that points into a specific array index
// still matches the corresponding drift entry.
func pointerToDotPath(pointer string) string {
	out := ""
	for seg := range strings.SplitSeq(strings.TrimPrefix(pointer, "/"), "/") {
		out = joinPath(out, decodeJSONPointerSegment(seg))
	}
	return out
}

// decodeJSONPointerSegment unescapes an RFC 6901 reference token: "~1" → "/"
// and "~0" → "~". Order matters (~1 before ~0) so an escaped tilde isn't
// re-interpreted. Without this, an ignoreDifferences.jsonPointers entry like
// "/metadata/annotations/example.com~1checksum" wouldn't match the dotted path
// carrying a literal "/", and the explicitly-ignored field is reported as drift.
func decodeJSONPointerSegment(seg string) string {
	seg = strings.ReplaceAll(seg, "~1", "/")
	return strings.ReplaceAll(seg, "~0", "~")
}

func pathMatchesAnyPrefix(path string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if path == prefix || strings.HasPrefix(path, prefix+".") || strings.HasPrefix(path, prefix+".[") {
			return true
		}
	}
	return false
}

// diffValues recursively walks desired vs live and emits entries where they
// differ. Maps and arrays are both descended (arrays element-by-element by
// index — see the array branch for the order-alignment assumption); scalars are
// compared by serialized equality. nil/absent
// values are normalized so {a: nil} and missing-a are treated as equal.
//
// out is passed by reference to avoid allocations per recursion level.
func diffValues(path string, desired, live any, out []DriftEntry) []DriftEntry {
	if isNilish(desired) && isNilish(live) {
		return out
	}
	if isNilish(desired) {
		return append(out, DriftEntry{Path: path, Op: DriftOpAdded, Live: jsonString(live)})
	}
	if isNilish(live) {
		return append(out, DriftEntry{Path: path, Op: DriftOpRemoved, Desired: jsonString(desired)})
	}
	dMap, dIsMap := desired.(map[string]any)
	lMap, lIsMap := live.(map[string]any)
	if dIsMap && lIsMap {
		// Recurse on union of keys so we catch added-on-live and removed-on-live.
		keys := make(map[string]struct{}, len(dMap)+len(lMap))
		for k := range dMap {
			keys[k] = struct{}{}
		}
		for k := range lMap {
			keys[k] = struct{}{}
		}
		for k := range keys {
			out = diffValues(joinPath(path, k), dMap[k], lMap[k], out)
		}
		return out
	}
	dArr, dIsArr := desired.([]any)
	lArr, lIsArr := live.([]any)
	if dIsArr && lIsArr {
		// Descend element-by-element by index rather than dumping the whole
		// array as one entry. A single field changing inside one list element
		// (e.g. spec.template.spec.containers.[0].resources.limits.memory) then
		// reads as that one path, not a wall of serialized JSON. Index
		// alignment is accurate for our inputs: K8s controllers preserve list
		// order between apply and observe, and the Argo API normalizes both
		// sides — neither reorders. A length change surfaces the extra/missing
		// elements as added/removed at their index.
		n := max(len(dArr), len(lArr))
		for i := range n {
			var dv, lv any
			if i < len(dArr) {
				dv = dArr[i]
			}
			if i < len(lArr) {
				lv = lArr[i]
			}
			out = diffValues(joinPath(path, strconv.Itoa(i)), dv, lv, out)
		}
		return out
	}
	// At least one side is a scalar, or the two sides have mismatched shapes
	// (map vs array vs scalar — a schema change). Compare by serialized form.
	desiredStr := jsonString(desired)
	liveStr := jsonString(live)
	if desiredStr == liveStr {
		return out
	}
	return append(out, DriftEntry{Path: path, Op: DriftOpChanged, Desired: desiredStr, Live: liveStr})
}

// joinPath produces dot-notation paths. If the segment looks like an array
// index, it's wrapped in brackets ("foo.[0].bar"); otherwise concatenated
// with a dot. diffValues descends into arrays by index, so this is how those
// element paths are formed.
func joinPath(prefix, segment string) string {
	if prefix == "" {
		return segment
	}
	if isAllDigits(segment) {
		return prefix + ".[" + segment + "]"
	}
	return prefix + "." + segment
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// isNilish treats nil, empty maps, empty arrays, and empty strings as
// equivalent. Without this, a field defaulted to {} or [] in one side
// would always show as drift even though semantically there's nothing
// there.
//
// Note for setIfMissing callers: the empty-string-as-nilish rule means
// `imagePullPolicy: ""` in a user's manifest will be overwritten by the
// defaulter to `IfNotPresent`. This matches kubectl's last-applied semantics
// (an explicit empty for a scalar K8s field round-trips through the API
// server's defaulting at admission), but a user trying to "explicitly clear"
// a field would lose that intent here. Defensible for diff output; surprising
// in isolation.
func isNilish(v any) bool {
	if v == nil {
		return true
	}
	switch val := v.(type) {
	case map[string]any:
		return len(val) == 0
	case []any:
		return len(val) == 0
	case string:
		return val == ""
	}
	return false
}

// jsonString returns a stable single-line JSON encoding of v, suitable for
// scalar comparison and UI display. Strings are intentionally quoted (so
// the UI can tell `"true"` from `true`) and maps are sorted by key
// (json.Marshal on map[string]any does this since Go 1.12).
func jsonString(v any) string {
	if v == nil {
		return "null"
	}
	if s, ok := v.(string); ok {
		// Quote strings via Marshal so embedded characters escape correctly.
		b, err := json.Marshal(s)
		if err != nil {
			return s
		}
		return string(b)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}
