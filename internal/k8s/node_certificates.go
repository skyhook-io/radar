package k8s

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/resourceid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

const (
	NodeCertificateIssueReason    = "NodeCertificateExpiry"
	nodeCertificateWarningReason  = "CertificateExpirationWarning"
	nodeCertificateOKReason       = "CertificateExpirationOK"
	nodeCertificateFreshness      = 2 * time.Hour
	nodeCertificateWarningWindow  = 30 * 24 * time.Hour
	nodeCertificateCriticalWindow = 7 * 24 * time.Hour
)

type NodeCertificateReport struct {
	// UID binds this response to the current Node; the monitor may record no incarnation.
	UID               string    `json:"uid"`
	EventID           string    `json:"event_id,omitempty"`
	State             string    `json:"state"`
	Reason            string    `json:"reason"`
	Message           string    `json:"message"`
	LastReportedAt    time.Time `json:"last_reported_at"`
	ExpiresAt         time.Time `json:"expires_at,omitzero"`
	NotBefore         time.Time `json:"not_before,omitzero"`
	HistoryIncomplete bool      `json:"history_incomplete,omitempty"`
	ValidityUnknown   bool      `json:"validity_unknown,omitempty"`
}

type nodeCertificateHistory struct {
	rows             []timeline.TimelineEvent
	incompleteBefore time.Time
	unavailable      bool
}

type nodeCertificateAssessment struct {
	detections []Detection
	reports    map[string]NodeCertificateReport
}

var nodeCertificateValidity = regexp.MustCompile(`(will expire within [0-9]+ days at|expired at|is not valid before) ([0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]+)?(?:Z|[+-][0-9]{2}:[0-9]{2}))`)

func nodeCertificateProgram(node *corev1.Node) string {
	version := node.Status.NodeInfo.KubeletVersion
	switch {
	case strings.Contains(version, "+rke2"):
		return "rke2"
	case strings.Contains(version, "+k3s"):
		return "k3s"
	default:
		return ""
	}
}

func readNodeCertificateHistory(nodes []*corev1.Node) nodeCertificateHistory {
	const limit = 10000
	h := nodeCertificateHistory{incompleteBefore: timeline.ObservationStart()}
	store := timeline.GetStore()
	if store == nil || h.incompleteBefore.IsZero() {
		h.unavailable = true
		return h
	}
	if boundary := timeline.HistoryEvictedThrough(); boundary.After(h.incompleteBefore) {
		h.incompleteBefore = boundary
	}
	if boundary := timeline.LastStoreFailure("K8sEvent:Node"); boundary.After(h.incompleteBefore) {
		h.incompleteBefore = boundary
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	opts := timeline.QueryOptions{
		Kinds: []string{"Node"}, APIGroups: []string{""}, ClusterContext: ActiveClusterContext(),
		Sources: []timeline.EventSource{timeline.SourceK8sEvent}, IncludeK8sEvents: true, IncludeManaged: true, Limit: limit,
	}
	for _, node := range nodes {
		opts.Names = append(opts.Names, node.Name)
	}
	// Separate reads prevent routine Node events from crowding warnings out.
	for _, eventType := range []timeline.EventType{timeline.EventTypeWarning, timeline.EventTypeNormal} {
		opts.EventTypes = []timeline.EventType{eventType}
		rows, err := store.Query(ctx, opts)
		if err != nil {
			h.unavailable = true
			break
		}
		h.rows = append(h.rows, rows...)
		h.incompleteBefore = historyQueryBoundary(h.incompleteBefore, rows, limit)
	}
	return h
}

func nodeCertificateReports(cache *ResourceCache, scope *DetectScope, now time.Time) nodeCertificateAssessment {
	return scope.get("node-certificate-reports", func() any {
		empty := nodeCertificateAssessment{}
		clusterContext := ActiveClusterContext()
		if cache == nil || cache != GetResourceCache() || cache.Nodes() == nil || !cache.IsKindReady(k8score.Nodes) {
			return empty
		}
		nodes, err := cache.Nodes().List(labels.Everything())
		if err != nil {
			return empty
		}
		var applicable []*corev1.Node
		for _, node := range nodes {
			if nodeCertificateProgram(node) != "" {
				applicable = append(applicable, node)
			}
		}
		if len(applicable) == 0 {
			return empty
		}
		history := readNodeCertificateHistory(applicable)
		var live []*corev1.Event
		liveComplete := false
		if cache.Events() == nil || !cache.IsKindReady(k8score.Events) {
			history.unavailable = true
		} else {
			live, err = cache.Events().Events(metav1.NamespaceDefault).List(labels.Everything())
			liveComplete = err == nil && cache.KindCoversNamespace(k8score.Events, metav1.NamespaceDefault)
			if !liveComplete {
				history.unavailable = true
			}
		}
		if cache != GetResourceCache() || clusterContext != ActiveClusterContext() {
			return empty
		}
		return assessNodeCertificates(applicable, live, history, liveComplete, now)
	}).(nodeCertificateAssessment)
}

func NodeCertificateReportFor(cache *ResourceCache, scope *DetectScope, name string) *NodeCertificateReport {
	assessment := nodeCertificateReports(cache, scope, time.Now())
	report, ok := assessment.reports[name]
	if !ok {
		return nil
	}
	return &report
}

func assessNodeCertificates(nodes []*corev1.Node, live []*corev1.Event, history nodeCertificateHistory, liveComplete bool, now time.Time) nodeCertificateAssessment {
	result := nodeCertificateAssessment{reports: map[string]NodeCertificateReport{}}
	subjects := make(map[string]*corev1.Node, len(nodes))
	for _, node := range nodes {
		if nodeCertificateProgram(node) != "" {
			subjects[node.Name] = node
		}
	}
	latest := map[string]timeline.TimelineEvent{}
	liveReports := map[string]time.Time{}
	consider := func(row timeline.TimelineEvent) {
		node := subjects[row.Name]
		warning := row.EventType == timeline.EventTypeWarning && row.Reason == nodeCertificateWarningReason
		ok := row.EventType == timeline.EventTypeNormal && row.Reason == nodeCertificateOKReason
		if node == nil || (!warning && !ok) || row.Source != timeline.SourceK8sEvent || row.Kind != "Node" || row.Namespace != "" ||
			(row.APIVersion != "" && resourceid.GroupFromAPIVersion(row.APIVersion) != "") ||
			(row.UID != "" && row.UID != string(node.UID)) || row.Timestamp.IsZero() || row.Timestamp.After(now) || row.Timestamp.Before(node.CreationTimestamp.Time) {
			return
		}
		previous, found := latest[row.Name]
		newer := !found || row.Timestamp.After(previous.Timestamp)
		if row.Timestamp.Equal(previous.Timestamp) {
			// A warning wins an unsequenced tie rather than inventing recovery.
			newer = row.Seq > previous.Seq || (row.Seq == previous.Seq && warning && previous.Reason != nodeCertificateWarningReason)
		}
		if newer {
			latest[row.Name] = row
		}
	}
	for _, row := range history.rows {
		consider(row)
	}
	for _, event := range live {
		if event.InvolvedObject.Kind != "Node" || (event.Reason != nodeCertificateWarningReason && event.Reason != nodeCertificateOKReason) {
			continue
		}
		row := timeline.NewK8sEventTimelineEvent(event, nil)
		liveReports[row.ID] = row.Timestamp
		consider(row)
	}
	for _, node := range nodes {
		row, ok := latest[node.Name]
		if !ok {
			continue
		}
		report := NodeCertificateReport{UID: string(node.UID), EventID: row.ID, State: "reported", Reason: row.Reason, Message: row.Message, LastReportedAt: row.Timestamp}
		if row.Reason == nodeCertificateOKReason {
			report.State = "ok"
			result.reports[node.Name] = report
			continue
		}
		reportedExpired := false
		for _, match := range nodeCertificateValidity.FindAllStringSubmatch(row.Message, -1) {
			at, err := time.Parse(time.RFC3339Nano, match[2])
			if err != nil {
				continue
			}
			reportedExpired = reportedExpired || match[1] == "expired at"
			if match[1] == "is not valid before" {
				if report.NotBefore.IsZero() || at.After(report.NotBefore) {
					report.NotBefore = at
				}
			} else if report.ExpiresAt.IsZero() || at.Before(report.ExpiresAt) {
				report.ExpiresAt = at
			}
		}
		report.ValidityUnknown = report.ExpiresAt.IsZero() && report.NotBefore.IsZero()
		// A complete live cache contains newer monitor outcomes too. History gaps
		// matter when the native Event has gone and retained rows are all we have.
		fromLive := liveComplete && liveReports[row.ID].Equal(row.Timestamp)
		report.HistoryIncomplete = !fromLive && (history.unavailable || !row.Timestamp.After(history.incompleteBefore))
		if report.HistoryIncomplete || now.Sub(row.Timestamp) > nodeCertificateFreshness {
			report.State = "unverified"
		}
		result.reports[node.Name] = report
		if report.State != "reported" {
			continue
		}
		severity, message := nodeCertificateUrgency(report, reportedExpired, now)
		if severity == "" {
			continue
		}
		result.detections = append(result.detections, Detection{
			Kind: "Node", Name: node.Name, Reason: NodeCertificateIssueReason, Severity: severity,
			Fingerprint: "node:certificate-expiry", Message: message, RawMessage: row.Message, MessageSource: issuesapi.MessageSourceRadar,
			Cause:             "The node's certificate monitor reported a certificate validity problem that can affect authentication.",
			Action:            fmt.Sprintf("Check this node's clock and the reported certificates. Follow %s certificate guidance: a rolling restart can renew eligible node certificates using existing keys, one node at a time. CA certificates require separate CA rotation.", nodeCertificateProgram(node)),
			ResourceCreatedAt: node.CreationTimestamp.Time, OnsetUnknown: true,
		})
	}
	return result
}

func nodeCertificateUrgency(report NodeCertificateReport, reportedExpired bool, now time.Time) (string, string) {
	stamp := report.LastReportedAt.UTC().Format(time.RFC3339)
	if !report.ExpiresAt.IsZero() && report.ExpiresAt.Sub(now) < nodeCertificateCriticalWindow {
		return "critical", fmt.Sprintf("Node monitor reported certificate expiry on %s (last reported %s)", report.ExpiresAt.UTC().Format(time.RFC3339), stamp)
	}
	if !report.NotBefore.IsZero() {
		if report.NotBefore.After(now) {
			return "critical", fmt.Sprintf("Node monitor reported a certificate not valid before %s (last reported %s)", report.NotBefore.UTC().Format(time.RFC3339), stamp)
		}
		return "warning", fmt.Sprintf("Node monitor reported a certificate validity or clock problem (last reported %s)", stamp)
	}
	if reportedExpired && report.ExpiresAt.After(now) {
		return "warning", fmt.Sprintf("Node monitor reported an expired certificate, but its expiry date is in the future on Radar's clock (last reported %s)", stamp)
	}
	if report.ExpiresAt.IsZero() || report.ExpiresAt.Sub(now) >= nodeCertificateWarningWindow {
		return "", ""
	}
	return "warning", fmt.Sprintf("Node monitor reported certificate expiry on %s (last reported %s)", report.ExpiresAt.UTC().Format(time.RFC3339), stamp)
}
