package mcp

import (
	"context"
	"sync"
	"time"

	collector "github.com/skyhook-io/radar/internal/runtimeevidence"
	"github.com/skyhook-io/radar/internal/trace"
	evidence "github.com/skyhook-io/radar/pkg/runtimeevidence"
	corev1 "k8s.io/api/core/v1"
)

type applicationActionsDisabledKey struct{}

type applicationEvidenceAction struct {
	CoverageLimited bool `json:"coverageLimited,omitempty"`
	collector.Candidate
	Tool                  string              `json:"tool"`
	Arguments             map[string]any      `json:"arguments"`
	WhyThisHelps          string              `json:"whyThisHelps"`
	SourceRefs            []trace.ResourceRef `json:"sourceRefs"`
	AuthorizationRequired string              `json:"authorizationRequired"`
	CandidatesTruncated   bool                `json:"candidatesTruncated,omitempty"`
}

func applicationActionsAllowed(ctx context.Context) bool {
	disabled, _ := ctx.Value(applicationActionsDisabledKey{}).(bool)
	return !disabled && runtimeEvidenceAllowed(ctx)
}

func applicationActions(ctx context.Context, deps trace.Deps, set collector.CandidateSet) []applicationEvidenceAction {
	if !applicationActionsAllowed(ctx) || deps.Cache == nil || deps.Cache.Pods() == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	client := deps.Client
	var pending []applicationEvidenceAction
	for _, candidate := range set.Candidates {
		if !deps.NamespaceAllowed(candidate.Target.Namespace) {
			continue
		}
		p, err := deps.Cache.Pods().Pods(candidate.Target.Namespace).Get(candidate.Target.Pod)
		if err != nil || string(p.UID) != candidate.Target.UID {
			continue
		}
		reason := applicationActionReason(p, candidate)
		if reason == "" {
			continue
		}
		pending = append(pending, applicationEvidenceAction{Candidate: candidate, Tool: "collect_application_evidence", Arguments: map[string]any{"application": candidate.Application, "namespace": candidate.Target.Namespace, "pod": candidate.Target.Pod, "pod_uid": candidate.Target.UID}, WhyThisHelps: reason, SourceRefs: []trace.ResourceRef{{Kind: "Pod", Namespace: p.Namespace, Name: p.Name}}, AuthorizationRequired: "Ask the operator before setting confirm_network_access=true; the declared endpoint may still be unavailable.", CandidatesTruncated: set.Truncated, CoverageLimited: set.CoverageLimited})
	}
	allowed := make([]bool, len(pending))
	var wg sync.WaitGroup
	for i := range pending {
		wg.Add(1)
		go func() { defer wg.Done(); allowed[i] = collector.CheckAccess(ctx, client, pending[i].Target) }()
	}
	wg.Wait()
	var actions []applicationEvidenceAction
	for i, action := range pending {
		if allowed[i] {
			actions = append(actions, action)
		}
	}
	return actions
}

func applicationActionReason(p *corev1.Pod, c collector.Candidate) string {
	if p == nil || c.Application == evidence.NATS {
		return ""
	}
	if !collector.RelevantReadinessFailure(p, c.Application, c.Target.Container) {
		return ""
	}
	switch c.Application {
	case evidence.Vault:
		return "The Vault container is not ready; its initialization and seal state may explain why it is not serving. Standby alone is not a fault."
	case evidence.RabbitMQ:
		return "The RabbitMQ alarm-specific readiness check is failing; collect its node-local disk and memory alarm state. An alarm-free node does not rule out problems on other nodes."
	}

	return ""
}
