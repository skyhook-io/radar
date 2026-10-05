package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sort"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/policyreports"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// readablePolicyReportFamilies reports which families this identity may read at
// the scope implied by namespace. Empty means none, which is a denial.
//
// The table and the walk live in internal/k8s so the AI and MCP surfaces
// authorize against the same one.
func (s *Server) readablePolicyReportFamilies(r *http.Request, namespace string) map[string]bool {
	return k8s.ReadablePolicyReportFamilies(namespace, func(group, resource, ns string) bool {
		return s.canRead(r, group, resource, ns, "list")
	})
}

// canReadPolicyReports reports whether this identity may read policy results in
// the namespace. Permission on EITHER family is enough: the findings are merged
// from whichever families are served, so being able to read the one that
// actually carries data is what matters.
func (s *Server) canReadPolicyReports(r *http.Request, namespace string) bool {
	return len(s.readablePolicyReportFamilies(r, namespace)) > 0
}

// PolicyResourceFinding is one rule outcome for one resource, as rendered.
type PolicyResourceFinding struct {
	Policy   string `json:"policy"`
	Rule     string `json:"rule,omitempty"`
	Result   string `json:"result"`
	Severity string `json:"severity,omitempty"`
	Category string `json:"category,omitempty"`
	Message  string `json:"message,omitempty"`
	// Engine is the normalized producer ("kyverno", "trivy", …). A single
	// report stream carries several raw source strings for one engine, so the
	// raw value is deliberately not what the UI groups on.
	Engine string `json:"engine,omitempty"`
}

// PolicyResourceCounts is the pass/fail breakdown for the resource.
type PolicyResourceCounts struct {
	Pass  int `json:"pass"`
	Fail  int `json:"fail"`
	Warn  int `json:"warn"`
	Error int `json:"error"`
	Skip  int `json:"skip"`
}

// PolicyResourceResponse deliberately reports coverage alongside the findings.
//
// An empty finding list means at least four different things — nothing is
// violated, the engine is not installed, the caller may not read the reports,
// or the index has not warmed up — and a section that renders all four as blank
// space tells the operator "you are compliant" in three cases where we do not
// know that. Status/ReasonCode/DeniedGroups exist so the UI can say which.
type PolicyResourceResponse struct {
	// Evaluated is false whenever the answer is "we could not check", so a
	// consumer that reads nothing else still cannot mistake it for "clean".
	Evaluated  bool   `json:"evaluated"`
	Status     string `json:"status"`
	ReasonCode string `json:"reasonCode,omitempty"`
	// DeniedGroups are report families RADAR's own probe could not read, so the
	// index never carried them for anyone. Distinct from WithheldByFamily, which
	// is what THIS caller may not see. Present even on a `ready` status: the
	// index is real but incomplete.
	DeniedGroups []string `json:"deniedGroups,omitempty"`
	// LiveUpdates is false when the index is frozen at its initial contents.
	LiveUpdates bool                    `json:"liveUpdates"`
	Counts      PolicyResourceCounts    `json:"counts"`
	Findings    []PolicyResourceFinding `json:"findings"`
	// WithheldByFamily is how many findings were dropped because they came only
	// from a report family this caller cannot read. Counts exclude them too:
	// a total the caller is not entitled to see is not a total we can show.
	WithheldByFamily int `json:"withheldByFamily,omitempty"`
}

// handlePolicyResource returns the policy findings for a single resource.
// GET /api/policy/resource/{kind}/{namespace}/{name}
//
// Mirrors handleAuditResource's shape and kind resolution so the two
// per-resource drill-downs cannot drift on how a URL plural becomes a kind.
func (s *Server) handlePolicyResource(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}

	kind := chi.URLParam(r, "kind")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	// Deliberately not parseNamespacesForUser: that folds in the header's view
	// filter, a display preference, which would break this section for any
	// resource reached by deep link from outside the current filter. A denial is
	// a 403 rather than an empty list, which would read as "nothing is violated".
	families := s.readablePolicyReportFamilies(r, namespace)
	if len(families) == 0 {
		s.writeError(w, http.StatusForbidden, "no access to policy reports in this namespace")
		return
	}

	status := k8s.GetPolicyReportStatus()
	resp := PolicyResourceResponse{
		Status:       string(status.Status),
		ReasonCode:   status.ReasonCode,
		DeniedGroups: status.DeniedGroups,
		LiveUpdates:  status.LiveUpdates,
		Findings:     []PolicyResourceFinding{},
	}

	index := k8s.GetPolicyReportIndex()
	if index == nil || status.Status != k8s.KyvernoStatusReady {
		// Not evaluated. Everything above already says why; the empty finding
		// list is not an assertion of compliance.
		s.writeJSON(w, resp)
		return
	}
	resp.Evaluated = true

	goKind := kind
	if mapped := apiResourceToKind(kind); mapped != kind {
		goKind = mapped
	}
	// Reports key their subject by the resource's own API group. Built-in kinds
	// carry theirs ("apps" for Deployment), so a bare "" lookup would miss them
	// exactly the way the audit drill-down once did.
	group := resourceid.GroupForBuiltinKind(goKind)
	outcomes := index.SourcedFindingsFor(group, goKind, namespace, name)
	if len(outcomes) == 0 && group != "" {
		outcomes = index.SourcedFindingsFor("", goKind, namespace, name)
	}

	for _, o := range outcomes {
		// Same provenance filter as the per-policy view. Being authorized on one
		// family says nothing about the other, and the index merges both — so
		// without this the two views disagree about what this identity may see.
		if !k8s.PolicyOutcomeReadable(o, families) {
			resp.WithheldByFamily++
			continue
		}
		f := o.Finding
		resp.Counts.count(f.Result)
		// Passing checks are counted but not listed: a workload matched by
		// dozens of policies would bury its two failures in a wall of green.
		// The count still proves the check ran.
		if isPolicyPassResult(f.Result) {
			continue
		}
		resp.Findings = append(resp.Findings, PolicyResourceFinding{
			Policy:   f.Policy,
			Rule:     f.Rule,
			Result:   f.Result,
			Severity: f.Severity,
			Category: f.Category,
			Message:  f.Message,
			Engine:   string(f.Engine()),
		})
	}

	// Stable order so the section does not reshuffle between polls: worst
	// result first, then policy name.
	sort.SliceStable(resp.Findings, func(i, j int) bool {
		ri, rj := policyResultRank(resp.Findings[i].Result), policyResultRank(resp.Findings[j].Result)
		if ri != rj {
			return ri < rj
		}
		return resp.Findings[i].Policy < resp.Findings[j].Policy
	})

	s.writeJSON(w, resp)
}

// maxPolicyCoverageSubjects bounds the per-rule subject list.
//
// Deliberately high. The UI folds the list at ten and expands in place, which is
// the house pattern for related resources, and that pattern is only honest if
// the client actually holds everything it offers to show. This is the backstop
// for a policy matching a whole large cluster, not the display limit — and when
// it does bite, the response says so rather than quietly shortening the list.
// Counts are always exact regardless.
const maxPolicyCoverageSubjects = 200

// maxPolicyCoverageSubjectsHard is the ceiling `?limit=` may raise the list to.
//
// The default exists so an ordinary drawer open stays small; it must not be a
// wall. When the reader asks to see the rest, the list is re-fetched at a
// higher bound rather than pointing them at a view that cannot filter by
// policy. This ceiling is what stops a policy matching a 50k-object cluster
// from serializing all of it into one drawer.
const maxPolicyCoverageSubjectsHard = 5000

// coverageLimit reads ?limit=, clamped. An unparseable or absent value keeps
// the default rather than failing the request — the list is a display concern
// and the counts beside it are exact either way.
func coverageLimit(raw string) int {
	if raw == "" {
		return maxPolicyCoverageSubjects
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return maxPolicyCoverageSubjects
	}
	if n > maxPolicyCoverageSubjectsHard {
		return maxPolicyCoverageSubjectsHard
	}
	return n
}

// PolicyCoverageSubject is one resource this policy recorded an outcome for.
type PolicyCoverageSubject struct {
	Group     string `json:"group,omitempty"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Result    string `json:"result"`
	Message   string `json:"message,omitempty"`
	// Autogen marks a subject matched through a rule Kyverno synthesised from
	// an authored Pod rule. The UI folds these into the parent rule; the flag
	// exists so it can still say the match came via a controller.
	Autogen bool `json:"autogen,omitempty"`
}

// PolicyCoverageRule groups a policy's outcomes under one authored rule.
// Rule is empty for the modern CEL families, which have no named rules.
type PolicyCoverageRule struct {
	Rule      string                  `json:"rule"`
	Counts    PolicyResourceCounts    `json:"counts"`
	Subjects  []PolicyCoverageSubject `json:"subjects"`
	Total     int                     `json:"total"`
	Truncated bool                    `json:"truncated,omitempty"`
	// HiddenByFilter is how many of Total the namespace view filter held back.
	// Counts still describe every outcome, so the two must be read together.
	HiddenByFilter int `json:"hiddenByFilter,omitempty"`
	// HiddenNotable is the non-passing part of HiddenByFilter. Subjects are
	// listed only when they are non-passing, so without this split the client
	// cannot tell a row the server cap removed from one the view filter held
	// back — and blames the cap, offering to load subjects that no higher limit
	// will ever return.
	HiddenNotable int `json:"hiddenNotable,omitempty"`
}

// PolicyCoverageResponse answers "what did this policy decide about the
// cluster". It carries the same partial-knowledge contract as
// PolicyResourceResponse, plus one the per-resource view never needed: a
// resource may be missing because the caller cannot read its namespace, so an
// absent subject is not evidence of absence.
type PolicyCoverageResponse struct {
	Evaluated    bool     `json:"evaluated"`
	Status       string   `json:"status"`
	ReasonCode   string   `json:"reasonCode,omitempty"`
	DeniedGroups []string `json:"deniedGroups,omitempty"`
	LiveUpdates  bool     `json:"liveUpdates"`
	// Policy is the report-side name that matched, so the UI can show which
	// shape resolved (a namespaced policy reports as "namespace/name").
	Policy string               `json:"policy"`
	Counts PolicyResourceCounts `json:"counts"`
	// ScopeNamespaces counts namespaces represented in the returned subjects;
	// WithheldNamespaces counts those the caller may not read. The headline
	// states scope before numbers, so both are needed to write it.
	ScopeNamespaces    int  `json:"scopeNamespaces"`
	WithheldNamespaces int  `json:"withheldNamespaces"`
	ClusterScoped      bool `json:"clusterScoped,omitempty"`
	// WithheldClusterScoped is separate from WithheldNamespaces: a cluster-scoped
	// subject has no namespace to name.
	WithheldClusterScoped bool `json:"withheldClusterScoped,omitempty"`
	// UnreadableFamilies are report families the CALLER cannot read, distinct from
	// DeniedGroups, which are the ones radar's own probe could not read. Their
	// results are dropped rather than merged, so naming the families is what
	// turns a smaller number into an explained one.
	UnreadableFamilies []string `json:"unreadableFamilies,omitempty"`
	// Examined is how many outcomes back these counts, so the numbers read as a
	// bounded subset rather than an implied cluster total.
	Examined int `json:"examined"`
	// HiddenByFilter is how many subjects the namespace view filter held back
	// across all rules. Counts are unaffected by the filter.
	HiddenByFilter int `json:"hiddenByFilter,omitempty"`
	// WithheldByFamily is how many outcomes were dropped because they came only
	// from a report family this caller cannot read. Unlike HiddenByFilter these
	// are excluded from the counts too — they are not the caller's to see.
	WithheldByFamily int `json:"withheldByFamily,omitempty"`
	// Engines are the normalized engines that wrote these results. More than
	// one means the policy name is ambiguous across producers and the view
	// must not claim it all belongs to Kyverno.
	Engines []string `json:"engines,omitempty"`
	// Distinct resources, and how many of those FAILED a rule — not merely failed
	// to pass, which also covers warnings, skips and rules that reached no
	// verdict. Not derivable on the client: outcomes overcount a multi-rule
	// policy and the capped subject lists undercount a large one.
	Subjects        int                  `json:"subjects"`
	SubjectsFailing int                  `json:"subjectsFailing"`
	Rules           []PolicyCoverageRule `json:"rules"`
}

// handlePolicyCoverage returns every resource a policy recorded an outcome for.
// GET /api/policy/policies/{policy}   (optional ?namespace= for namespaced policies)
//
// The inverse of handlePolicyResource. Kyverno writes a bare name for
// cluster-scoped policies and "namespace/name" for namespaced ones, so a lookup
// on the object's metadata.name alone silently returns nothing for every
// namespaced Policy — both shapes are tried.
func (s *Server) handlePolicyCoverage(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}

	policy := chi.URLParam(r, "policy")
	if policy == "" {
		s.writeError(w, http.StatusBadRequest, "policy name is required")
		return
	}
	namespace := r.URL.Query().Get("namespace")
	limit := coverageLimit(r.URL.Query().Get("limit"))

	status := k8s.GetPolicyReportStatus()
	resp := PolicyCoverageResponse{
		Status:       string(status.Status),
		ReasonCode:   status.ReasonCode,
		DeniedGroups: status.DeniedGroups,
		LiveUpdates:  status.LiveUpdates,
		Policy:       policy,
		Rules:        []PolicyCoverageRule{},
	}

	index := k8s.GetPolicyReportIndex()
	if index == nil || status.Status != k8s.KyvernoStatusReady {
		s.writeJSON(w, resp)
		return
	}

	// Qualified first when a namespace is known. Kyverno permits a ClusterPolicy
	// and a namespaced Policy to share a name, and resolving the bare name first
	// would show the cluster-scoped policy's coverage on the namespaced one's page
	// — wrong data, with nothing on screen to reveal the substitution.
	//
	// And when the policy HAS a namespace, the qualified key is the only correct
	// one — falling back to the bare name would hand a namespaced Policy with no
	// reports yet whatever a same-named ClusterPolicy recorded, with nothing on
	// screen to say so. An empty view is the honest answer there.
	var outcomes []policyreports.PolicyOutcome
	if namespace != "" {
		qualified := namespace + "/" + policy
		outcomes = index.OutcomesForPolicy(qualified)
		if len(outcomes) > 0 {
			resp.Policy = qualified
		}
	} else {
		outcomes = index.OutcomesForPolicy(policy)
	}

	// Authorization is decided per subject scope and memoized: a policy spanning
	// 40 namespaces would otherwise run 40 SubjectAccessReviews per request.
	//
	// Per SUBJECT, not per policy. A ClusterPolicy has no namespace of its own,
	// and asking the question once against "" gates the whole view on
	// cluster-scoped permission — which withholds every finding from a
	// namespace-restricted caller, including the ones in their own namespace
	// that they are entitled to see.
	allowed := map[string]map[string]bool{}
	familiesIn := func(ns string) map[string]bool {
		if v, ok := allowed[ns]; ok {
			return v
		}
		v := s.readablePolicyReportFamilies(r, ns)
		allowed[ns] = v
		return v
	}

	resp.Evaluated = true

	// The header's namespace filter is a DISPLAY preference, not an authorization
	// one — hence the RBAC note above. It still has to apply here, because a
	// cluster-wide policy list is otherwise dominated by kube-system and the
	// engine's own namespace: on a stock cluster roughly three quarters of policy
	// reports describe infrastructure the user neither owns nor can fix, and the
	// per-rule cap is exhausted before their own workloads appear.
	//
	// Counts stay cluster-true so the headline never shrinks to match the filter;
	// only the listed subjects are narrowed, and HiddenByFilter says how many were
	// held back.
	viewFilter := map[string]bool{}
	for _, ns := range s.parseNamespacesForUser(r) {
		viewFilter[ns] = true
	}

	byRule := map[string]*PolicyCoverageRule{}
	ruleOrder := []string{}
	namespaces := map[string]bool{}
	withheld := map[string]bool{}
	engines := map[string]bool{}

	unreadable := map[string]bool{}
	seenSubjects := map[string]bool{}
	failingSubjects := map[string]bool{}
	for _, o := range outcomes {
		families := familiesIn(o.Subject.Namespace)
		// No family readable at this subject's scope at all — a permission
		// answer about the namespace, which is what the scope note reports.
		if len(families) == 0 {
			// A cluster-scoped subject has no namespace, so counting it as one
			// would make the UI report "1 namespace is not shown" about something
			// that never had a namespace.
			if o.Subject.Namespace == "" {
				resp.WithheldClusterScoped = true
			} else {
				withheld[o.Subject.Namespace] = true
			}
			continue
		}
		// Provenance filter. An outcome seen in both families is readable by a
		// caller authorized on either; one carrying no group at all predates the
		// provenance tracking and is withheld rather than guessed at.
		if !k8s.PolicyOutcomeReadable(o, families) {
			resp.WithheldByFamily++
			for _, g := range o.Groups {
				if !families[g] {
					unreadable[g] = true
				}
			}
			continue
		}
		if o.Subject.Namespace == "" {
			resp.ClusterScoped = true
		} else {
			namespaces[o.Subject.Namespace] = true
		}
		engines[string(o.Finding.Engine())] = true

		base, autogen := policyreports.BaseRule(o.Finding.Rule)
		bucket, ok := byRule[base]
		if !ok {
			bucket = &PolicyCoverageRule{Rule: base, Subjects: []PolicyCoverageSubject{}}
			byRule[base] = bucket
			ruleOrder = append(ruleOrder, base)
		}
		bucket.Counts.count(o.Finding.Result)
		bucket.Total++
		// Counted before the view filter is applied, deliberately. The filter is a
		// display preference; letting it shrink the totals would make the headline
		// silently describe the user's current filter while reading like a
		// statement about the cluster.
		resp.Counts.count(o.Finding.Result)

		// Distinct resources, tracked alongside the outcome counts because the two
		// answer different questions and one resource can appear under several
		// rules.
		subjectKey := o.Subject.Group + "/" + o.Subject.Kind + "/" + o.Subject.Namespace + "/" + o.Subject.Name
		seenSubjects[subjectKey] = true
		// Strictly `fail`. Not-passing is a wider set — warn, skip, and the
		// engine error that means no verdict was reached at all — and the
		// sentence this feeds says the next update to these will be REJECTED.
		// A rule that could not evaluate rejects nothing on the strength of it.
		if normalizePolicyResult(o.Finding.Result) == "fail" {
			failingSubjects[subjectKey] = true
		}

		// Cluster-scoped subjects have no namespace to filter on and are always
		// relevant, so they are never hidden by a namespace view filter.
		if len(viewFilter) > 0 && o.Subject.Namespace != "" && !viewFilter[o.Subject.Namespace] {
			bucket.HiddenByFilter++
			if !isPolicyPassResult(o.Finding.Result) {
				bucket.HiddenNotable++
			}
			resp.HiddenByFilter++
			continue
		}
		bucket.Subjects = append(bucket.Subjects, PolicyCoverageSubject{
			Group:     o.Subject.Group,
			Kind:      o.Subject.Kind,
			Namespace: o.Subject.Namespace,
			Name:      o.Subject.Name,
			Result:    o.Finding.Result,
			Message:   o.Finding.Message,
			Autogen:   autogen,
		})
	}

	resp.Examined = resp.Counts.Pass + resp.Counts.Fail + resp.Counts.Warn + resp.Counts.Error + resp.Counts.Skip
	resp.Subjects = len(seenSubjects)
	resp.SubjectsFailing = len(failingSubjects)
	resp.ScopeNamespaces = len(namespaces)
	resp.WithheldNamespaces = len(withheld)
	for e := range engines {
		resp.Engines = append(resp.Engines, e)
	}
	sort.Strings(resp.Engines)

	sort.Strings(ruleOrder)
	// Reported per caller, from what was actually withheld — not from what
	// radar's own probe could reach. A family the caller cannot read but which
	// carried nothing they asked for is not worth mentioning.
	for _, f := range k8s.PolicyReportFamilies {
		if unreadable[f.Group] {
			resp.UnreadableFamilies = append(resp.UnreadableFamilies, f.Group)
		}
	}

	for _, name := range ruleOrder {
		bucket := byRule[name]
		// Worst first so a truncated list never hides a failure behind passes.
		sort.SliceStable(bucket.Subjects, func(i, j int) bool {
			ri, rj := policyResultRank(bucket.Subjects[i].Result), policyResultRank(bucket.Subjects[j].Result)
			if ri != rj {
				return ri < rj
			}
			if bucket.Subjects[i].Namespace != bucket.Subjects[j].Namespace {
				return bucket.Subjects[i].Namespace < bucket.Subjects[j].Namespace
			}
			return bucket.Subjects[i].Name < bucket.Subjects[j].Name
		})
		if len(bucket.Subjects) > limit {
			bucket.Subjects = bucket.Subjects[:limit]
			bucket.Truncated = true
		}
		resp.Rules = append(resp.Rules, *bucket)
	}

	s.writeJSON(w, resp)
}

func (c *PolicyResourceCounts) count(result string) {
	switch normalizePolicyResult(result) {
	case "pass":
		c.Pass++
	case "fail":
		c.Fail++
	case "warn":
		c.Warn++
	case "error":
		c.Error++
	case "skip":
		c.Skip++
	}
}

// normalizePolicyResult lowercases the wgpolicy/openreports result vocabulary.
// Producers are inconsistent about case across report families.
func normalizePolicyResult(result string) string {
	switch result {
	case "pass", "Pass", "PASS":
		return "pass"
	case "fail", "Fail", "FAIL":
		return "fail"
	case "warn", "Warn", "WARN":
		return "warn"
	case "error", "Error", "ERROR":
		return "error"
	case "skip", "Skip", "SKIP":
		return "skip"
	}
	return result
}

func isPolicyPassResult(result string) bool {
	return normalizePolicyResult(result) == "pass"
}

func policyResultRank(result string) int {
	switch normalizePolicyResult(result) {
	case "error":
		return 0
	case "fail":
		return 1
	case "warn":
		return 2
	case "skip":
		return 3
	}
	return 4
}

// PolicyQueuedResponse is the background work Kyverno currently has in flight
// for one policy.
//
// Computed server-side for the same reason every other reverse lookup here is:
// the answer's scope is not the subject's. Kyverno records UpdateRequests in its
// own namespace whatever namespace the policy lives in, so a client asking the
// generic resource list would inherit the caller's namespace view filter — a
// browsing preference — and silently answer for the wrong scope.
type PolicyQueuedResponse struct {
	// Requests matching this policy. Zero is a real answer here: Kyverno deletes
	// these seconds after the work completes.
	Requests int            `json:"requests"`
	ByState  map[string]int `json:"byState,omitempty"`
	// OldestPending separates healthy churn from a stopped controller — two
	// Pending three seconds old and two Pending forty minutes old count the same.
	OldestPending string `json:"oldestPending,omitempty"`
	// Messages are the distinct status messages, which carry the only diagnosis
	// these objects have.
	Messages []string `json:"messages,omitempty"`
}

// listDynamicSynced reads a kind after making sure the cache can actually answer
// for the scope being read.
//
// Not a gate before the read: the informer for a namespace is started BY the
// read, so refusing to read until one exists is a deadlock — the first request
// for a namespace would fail forever. ListBlocking starts it and waits, so an
// empty result afterwards is an absence the cache established rather than one it
// simply had not looked for.
//
// Falls back to the plain read when the machinery is missing, leaving the
// caller's not-installed path to answer.
func listDynamicSynced(ctx context.Context, cache *k8s.ResourceCache, kind, group, namespace string) ([]*unstructured.Unstructured, error) {
	return listDynamicSyncedWithin(ctx, cache, kind, group, namespace, nil)
}

// listDynamicSyncedWithin is listDynamicSynced bounded by budget (nil: wait
// dynamicSyncWait for the sync).
func listDynamicSyncedWithin(ctx context.Context, cache *k8s.ResourceCache, kind, group, namespace string, budget *syncBudget) ([]*unstructured.Unstructured, error) {
	discovery := k8s.GetResourceDiscovery()
	dynamicCache := k8s.GetDynamicResourceCache()
	if discovery == nil || dynamicCache == nil {
		return cache.ListDynamicWithGroup(ctx, kind, namespace, group)
	}
	gvr, found := discovery.GetGVRWithGroup(kind, group)
	if !found {
		return cache.ListDynamicWithGroup(ctx, kind, namespace, group)
	}
	items, err := budget.listBlocking(dynamicCache, gvr, namespace)
	if err != nil {
		return nil, err
	}
	// ListBlocking discards what WaitForCacheSync returned, so an informer that
	// did not finish in time falls through to an empty indexer and no error —
	// the false absence again, one layer down. Confirm afterwards: the read has
	// already started the informer, so asking now cannot deadlock the way a gate
	// before the read did.
	if !dynamicCache.IsNamespaceSynced(gvr, namespace) {
		return nil, errDynamicNotSynced
	}
	return items, nil
}

// errDynamicNotSynced means the cache could not answer for the scope in time.
// Distinct from an absent CRD: nothing was established either way.
var errDynamicNotSynced = errors.New("resource cache not synced")

// Long enough for a cold informer on a healthy apiserver, short enough that a
// drawer section does not hang on one that will not sync.
const dynamicSyncWait = 3 * time.Second

// policyRequestName is the value Kyverno records in `spec.policy` for this
// policy. Exactly one form matches, never both: a namespaced Policy is recorded
// as `namespace/name` and a cluster-scoped one bare, and Kyverno permits the two
// to share a name — so accepting the bare form as a fallback for a namespaced
// policy hands it a ClusterPolicy's backlog.
func policyRequestName(policy, namespace string) string {
	if namespace != "" {
		return namespace + "/" + policy
	}
	return policy
}

// handlePolicyQueued returns the queued background work for one policy.
// GET /api/policy/policies/{policy}/queued?namespace=X
//
// Cluster-wide by design, gated on the caller's ability to list the kind —
// scope follows permission, never the namespace switcher.
func (s *Server) handlePolicyQueued(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	policy := chi.URLParam(r, "policy")
	if policy == "" {
		s.writeError(w, http.StatusBadRequest, "policy name is required")
		return
	}
	if !s.canRead(r, "kyverno.io", "updaterequests", "", "list") {
		s.writeError(w, http.StatusForbidden, "no access to update requests")
		return
	}

	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}
	items, err := listDynamicSynced(r.Context(), cache, "UpdateRequest", "kyverno.io", "")
	switch {
	case err == nil:
	case errors.Is(err, k8s.ErrUnknownDynamicKind):
		// No background controller on this cluster. Genuinely nothing queued.
		s.writeJSON(w, PolicyQueuedResponse{})
		return
	case errors.Is(err, errDynamicNotSynced):
		s.writeError(w, http.StatusServiceUnavailable, "queued work is still loading")
		return
	default:
		// Anything else — Radar's own SA denied the kind, discovery not ready,
		// transient — is a failure to look, and reporting it as an empty queue
		// is the "absence from a failed lookup" this surface exists to avoid.
		log.Printf("[policy] Failed to list UpdateRequests for %s: %v", sanitizeForLog(policy), err)
		s.writeError(w, http.StatusServiceUnavailable, "could not read queued work")
		return
	}

	want := policyRequestName(policy, r.URL.Query().Get("namespace"))

	resp := PolicyQueuedResponse{ByState: map[string]int{}}
	seen := map[string]bool{}
	for _, u := range items {
		if u == nil {
			continue
		}
		if got, _, _ := unstructured.NestedString(u.Object, "spec", "policy"); got != want {
			continue
		}
		resp.Requests++
		state, _, _ := unstructured.NestedString(u.Object, "status", "state")
		if state == "" {
			state = "Unknown"
		}
		resp.ByState[state]++
		if msg, _, _ := unstructured.NestedString(u.Object, "status", "message"); msg != "" && !seen[msg] {
			seen[msg] = true
			resp.Messages = append(resp.Messages, msg)
		}
		if state == "Pending" {
			if ts := u.GetCreationTimestamp(); !ts.IsZero() {
				iso := ts.UTC().Format(time.RFC3339)
				if resp.OldestPending == "" || iso < resp.OldestPending {
					resp.OldestPending = iso
				}
			}
		}
	}
	sort.Strings(resp.Messages)
	s.writeJSON(w, resp)
}
