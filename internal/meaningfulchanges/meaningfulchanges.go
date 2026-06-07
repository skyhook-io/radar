package meaningfulchanges

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

const (
	DefaultSince      = time.Hour
	DefaultLimit      = 20
	DefaultFieldLimit = 10
	IssueChangesLimit      = 5
	ResourceLimit     = 3
	maxCandidateLimit = 100
)

const (
	ChangesReasonNoCriticalIssues                           = "no_critical_issues"
	ChangesReasonAllCriticalStartedAtCreation = "all_critical_issues_started_at_resource_creation"
)

var (
	configKinds = []string{"ConfigMap"}
	specKinds   = []string{
		"Deployment", "StatefulSet", "DaemonSet", "Service", "Ingress",
		"HorizontalPodAutoscaler", "Application", "Kustomization", "HelmRelease",
		"GitRepository", "OCIRepository", "HelmRepository",
	}
)

type Query struct {
	Namespaces []string
	Kinds      []string
	Name       string
	Since      time.Duration
	Limit      int
	FieldLimit int
}

func Recent(ctx context.Context, q Query) ([]issuesapi.RecentChange, bool, error) {
	store := timeline.GetStore()
	if store == nil {
		return nil, false, fmt.Errorf("timeline store not initialized")
	}
	q = normalizeQuery(q)

	if len(q.Kinds) > 0 || q.Name != "" {
		queryLimit := candidateLimit(q.Limit, q.Name != "")
		events, err := queryCandidates(ctx, store, q, q.Kinds, queryLimit)
		if err != nil {
			return nil, false, err
		}
		changes, capped, err := rankedChanges(events, q.Name, q.Limit, q.FieldLimit)
		return changes, capped || len(events) >= queryLimit, err
	}

	perQueryLimit := candidateLimit(q.Limit, false)
	configEvents, err := queryCandidates(ctx, store, q, configKinds, perQueryLimit)
	if err != nil {
		return nil, false, err
	}
	specEvents, err := queryCandidates(ctx, store, q, specKinds, perQueryLimit)
	if err != nil {
		return nil, false, err
	}
	changes, capped, err := rankedChanges(dedupeEvents(append(configEvents, specEvents...)), "", q.Limit, q.FieldLimit)
	return changes, capped || len(configEvents) >= perQueryLimit || len(specEvents) >= perQueryLimit, err
}

func RecentForResource(ctx context.Context, kind, namespace, name string, since time.Duration, limit, fieldLimit int) ([]issuesapi.RecentChange, error) {
	changes, _, err := Recent(ctx, Query{
		Namespaces: []string{namespace},
		Kinds:      []string{canonicalKind(kind)},
		Name:       name,
		Since:      since,
		Limit:      limit,
		FieldLimit: fieldLimit,
	})
	return changes, err
}

func RecentForWorkloadAndConfigMaps(ctx context.Context, obj any, kind, namespace, name string, since time.Duration, limit, fieldLimit int) ([]issuesapi.RecentChange, error) {
	var all []issuesapi.RecentChange
	if isWorkloadKind(kind) {
		changes, err := RecentForResource(ctx, kind, namespace, name, since, limit, fieldLimit)
		if err != nil {
			return nil, err
		}
		all = append(all, changes...)
	}
	for _, cm := range DirectConfigMapNames(obj) {
		changes, err := RecentForResource(ctx, "ConfigMap", namespace, cm, since, limit, fieldLimit)
		if err != nil {
			return nil, err
		}
		all = append(all, changes...)
	}
	RankAndCap(&all, limit)
	return all, nil
}

func ShouldAttachIssueChanges(issues []issuesapi.Issue) bool {
	return IssueChangesReason(issues) != ""
}

func IssueChangesQueryEligible(kindFilter, celFilter, severityFilter string) bool {
	if strings.TrimSpace(kindFilter) != "" || strings.TrimSpace(celFilter) != "" {
		return false
	}
	severityFilter = strings.TrimSpace(severityFilter)
	if severityFilter == "" {
		return true
	}
	for _, part := range strings.Split(severityFilter, ",") {
		if strings.ToLower(strings.TrimSpace(part)) == "critical" {
			return true
		}
	}
	return false
}

func IssueChangesReason(issues []issuesapi.Issue) string {
	criticalCount := 0
	for _, issue := range issues {
		if issue.Severity != issuesapi.SeverityCritical {
			continue
		}
		criticalCount++
		if issue.IssueTiming != "started_at_resource_creation" {
			return ""
		}
	}
	if criticalCount == 0 {
		return ChangesReasonNoCriticalIssues
	}
	return ChangesReasonAllCriticalStartedAtCreation
}

func ConfigMapKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "configmap", "configmaps", "cm":
		return true
	default:
		return false
	}
}

func WorkloadKind(kind string) bool { return isWorkloadKind(kind) }

func DirectConfigMapNames(obj any) []string {
	spec, ok := podSpecForObject(obj)
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	add := func(name string) {
		if name != "" {
			seen[name] = true
		}
	}
	for _, v := range spec.Volumes {
		if v.ConfigMap != nil {
			add(v.ConfigMap.Name)
		}
		if v.Projected != nil {
			for _, source := range v.Projected.Sources {
				if source.ConfigMap != nil {
					add(source.ConfigMap.Name)
				}
			}
		}
	}
	for _, c := range allContainers(spec) {
		for _, from := range c.EnvFrom {
			if from.ConfigMapRef != nil {
				add(from.ConfigMapRef.Name)
			}
		}
		for _, env := range c.Env {
			if env.ValueFrom != nil && env.ValueFrom.ConfigMapKeyRef != nil {
				add(env.ValueFrom.ConfigMapKeyRef.Name)
			}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func normalizeQuery(q Query) Query {
	if q.Since <= 0 {
		q.Since = DefaultSince
	}
	if q.Limit <= 0 {
		q.Limit = DefaultLimit
	}
	if q.Limit > 50 {
		q.Limit = 50
	}
	if q.FieldLimit <= 0 {
		q.FieldLimit = DefaultFieldLimit
	}
	if q.FieldLimit > 50 {
		q.FieldLimit = 50
	}
	for i, kind := range q.Kinds {
		q.Kinds[i] = canonicalKind(kind)
	}
	return q
}

func candidateLimit(finalLimit int, nameFiltered bool) int {
	limit := finalLimit * 4
	if limit < 50 {
		limit = 50
	}
	if nameFiltered && limit < 100 {
		limit = 100
	}
	if limit > maxCandidateLimit {
		limit = maxCandidateLimit
	}
	return limit
}

func queryCandidates(ctx context.Context, store timeline.EventStore, q Query, kinds []string, limit int) ([]timeline.TimelineEvent, error) {
	opts := timeline.QueryOptions{
		Namespaces: q.Namespaces,
		Kinds:      compactKinds(kinds),
		Since:      time.Now().Add(-q.Since),
		Sources:    []timeline.EventSource{timeline.SourceInformer},
		// Changes are root-cause evidence for the CURRENT cluster — the
		// persistent store retains other contexts' events across switches,
		// and serving those here hands agents phantom changes.
		ClusterContext:   k8s.ActiveClusterContext(),
		Limit:            limit,
		IncludeManaged:   false,
		IncludeK8sEvents: false,
	}
	return store.Query(ctx, opts)
}

func rankedChanges(events []timeline.TimelineEvent, name string, limit, fieldLimit int) ([]issuesapi.RecentChange, bool, error) {
	out := make([]issuesapi.RecentChange, 0, len(events))
	for _, e := range events {
		if name != "" && e.Name != name {
			continue
		}
		change := fromEvent(e, fieldLimit)
		if change.ChangeCategory == "" {
			continue
		}
		out = append(out, change)
	}
	capped := len(out) > limit
	RankAndCap(&out, limit)
	return out, capped, nil
}

func RankAndCap(changes *[]issuesapi.RecentChange, limit int) {
	if changes == nil {
		return
	}
	sort.SliceStable(*changes, func(i, j int) bool {
		a, b := (*changes)[i], (*changes)[j]
		if score(a) != score(b) {
			return score(a) > score(b)
		}
		at, _ := time.Parse(time.RFC3339, a.Timestamp)
		bt, _ := time.Parse(time.RFC3339, b.Timestamp)
		if !at.Equal(bt) {
			return at.After(bt)
		}
		return changeKey(a) < changeKey(b)
	})
	if limit > 0 && len(*changes) > limit {
		*changes = (*changes)[:limit]
	}
}

func fromEvent(e timeline.TimelineEvent, fieldLimit int) issuesapi.RecentChange {
	category, reason := classify(e)
	if category == "" {
		return issuesapi.RecentChange{}
	}
	change := issuesapi.RecentChange{
		Kind:           e.Kind,
		Namespace:      e.Namespace,
		Name:           e.Name,
		ChangeType:     string(e.EventType),
		Summary:        eventSummary(e),
		Timestamp:      e.Timestamp.Format(time.RFC3339),
		ChangeCategory: category,
		RankReason:     reason,
	}
	if e.Diff != nil {
		fields := e.Diff.Fields
		if fieldLimit > 0 && len(fields) > fieldLimit {
			fields = fields[:fieldLimit]
		}
		for _, f := range fields {
			change.Fields = append(change.Fields, issuesapi.ChangeField{
				Path:     f.Path,
				OldValue: f.OldValue,
				NewValue: f.NewValue,
			})
		}
	}
	return change
}

func classify(e timeline.TimelineEvent) (string, string) {
	if e.Source != timeline.SourceInformer {
		return "", ""
	}
	if e.EventType == timeline.EventTypeAdd || e.EventType == timeline.EventTypeDelete {
		if isConfigKind(e.Kind) || isSpecKind(e.Kind) {
			return "lifecycle", "resource create/delete for config or desired state"
		}
		return "", ""
	}
	if e.Diff == nil || len(e.Diff.Fields) == 0 {
		return "", ""
	}
	if hasSpecConfigField(e) {
		return "spec_config", "desired-state or configuration field changed"
	}
	if hasRuntimeStatusField(e) {
		return "runtime_status", "status field changed"
	}
	return "", ""
}

func hasSpecConfigField(e timeline.TimelineEvent) bool {
	if isConfigKind(e.Kind) {
		return true
	}
	for _, f := range e.Diff.Fields {
		p := strings.ToLower(f.Path)
		if strings.HasPrefix(p, "spec.") || strings.HasPrefix(p, "metadata.generation") || p == "immutable" || strings.HasPrefix(p, "data") {
			return true
		}
	}
	return false
}

func hasRuntimeStatusField(e timeline.TimelineEvent) bool {
	for _, f := range e.Diff.Fields {
		if strings.HasPrefix(strings.ToLower(f.Path), "status.") {
			return true
		}
	}
	return false
}

func score(c issuesapi.RecentChange) int {
	switch c.ChangeCategory {
	case "spec_config":
		return 100
	case "lifecycle":
		return 70
	case "runtime_status":
		return 40
	default:
		return 0
	}
}

func eventSummary(e timeline.TimelineEvent) string {
	if e.Diff != nil && e.Diff.Summary != "" {
		return e.Diff.Summary
	}
	if e.Message != "" {
		return truncate(e.Message, 160)
	}
	return string(e.EventType)
}

func compactKinds(kinds []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		kind = canonicalKind(kind)
		if kind == "" || seen[kind] {
			continue
		}
		seen[kind] = true
		out = append(out, kind)
	}
	return out
}

func dedupeEvents(events []timeline.TimelineEvent) []timeline.TimelineEvent {
	seen := map[string]bool{}
	out := make([]timeline.TimelineEvent, 0, len(events))
	for _, e := range events {
		key := e.ID
		if key == "" {
			key = fmt.Sprintf("%s/%s/%s/%s/%s", e.Timestamp.Format(time.RFC3339Nano), e.Kind, e.Namespace, e.Name, e.EventType)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, e)
	}
	return out
}

func isConfigKind(kind string) bool { return kind == "ConfigMap" }

func isSpecKind(kind string) bool {
	for _, item := range specKinds {
		if kind == item {
			return true
		}
	}
	return false
}

func isWorkloadKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "deployment", "deployments", "statefulset", "statefulsets", "daemonset", "daemonsets", "pod", "pods":
		return true
	default:
		return false
	}
}

func canonicalKind(kind string) string {
	// Discovery is the authority: lowercase-keyed by kind AND plural, yields
	// the exact PascalCase Kind for everything on the cluster, CRDs included.
	// The static table below only covers the window before discovery exists
	// (cold start, context switch) and kinds not installed on this cluster.
	if d := k8s.GetResourceDiscovery(); d != nil {
		if res, ok := d.GetResource(strings.TrimSpace(kind)); ok && res.Kind != "" {
			return res.Kind
		}
	}
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "cm", "configmap", "configmaps":
		return "ConfigMap"
	case "deploy", "deployment", "deployments":
		return "Deployment"
	case "statefulset", "statefulsets":
		return "StatefulSet"
	case "daemonset", "daemonsets":
		return "DaemonSet"
	case "svc", "service", "services":
		return "Service"
	case "ingress", "ingresses":
		return "Ingress"
	case "hpa", "horizontalpodautoscaler", "horizontalpodautoscalers":
		return "HorizontalPodAutoscaler"
	case "pod", "pods":
		return "Pod"
	case "application", "applications":
		return "Application"
	case "kustomization", "kustomizations":
		return "Kustomization"
	case "helmrelease", "helmreleases":
		return "HelmRelease"
	case "gitrepository", "gitrepositories":
		return "GitRepository"
	case "ocirepository", "ocirepositories":
		return "OCIRepository"
	case "helmrepository", "helmrepositories":
		return "HelmRepository"
	case "cronjob", "cronjobs":
		return "CronJob"
	case "job", "jobs":
		return "Job"
	case "replicaset", "replicasets", "rs":
		return "ReplicaSet"
	case "secret", "secrets":
		return "Secret"
	case "pvc", "persistentvolumeclaim", "persistentvolumeclaims":
		return "PersistentVolumeClaim"
	case "pv", "persistentvolume", "persistentvolumes":
		return "PersistentVolume"
	case "serviceaccount", "serviceaccounts", "sa":
		return "ServiceAccount"
	case "networkpolicy", "networkpolicies":
		return "NetworkPolicy"
	case "poddisruptionbudget", "poddisruptionbudgets", "pdb":
		return "PodDisruptionBudget"
	case "httproute", "httproutes":
		return "HTTPRoute"
	case "grpcroute", "grpcroutes":
		return "GRPCRoute"
	case "gateway", "gateways":
		return "Gateway"
	default:
		if kind == "" {
			return ""
		}
		// Mixed-case input is an exact kind (CRDs the table can't know) —
		// pass it through. Only best-effort capitalize all-lowercase input;
		// timeline events store Kubernetes PascalCase, so "Cronjob"-style
		// guesses on multi-word kinds would silently match nothing.
		if strings.ToLower(kind) != kind {
			return kind
		}
		return strings.ToUpper(kind[:1]) + kind[1:]
	}
}

func podSpecForObject(obj any) (corev1.PodSpec, bool) {
	switch o := obj.(type) {
	case *corev1.Pod:
		return o.Spec, true
	case *appsv1.Deployment:
		return o.Spec.Template.Spec, true
	case *appsv1.StatefulSet:
		return o.Spec.Template.Spec, true
	case *appsv1.DaemonSet:
		return o.Spec.Template.Spec, true
	default:
		return corev1.PodSpec{}, false
	}
}

func allContainers(spec corev1.PodSpec) []corev1.Container {
	out := make([]corev1.Container, 0, len(spec.InitContainers)+len(spec.Containers))
	out = append(out, spec.InitContainers...)
	out = append(out, spec.Containers...)
	return out
}

func changeKey(c issuesapi.RecentChange) string {
	return c.Kind + "/" + c.Namespace + "/" + c.Name + "/" + c.ChangeType
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}
