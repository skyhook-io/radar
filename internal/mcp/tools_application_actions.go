package mcp

import (
	"context"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	collector "github.com/skyhook-io/radar/internal/runtimeevidence"
	"github.com/skyhook-io/radar/internal/trace"
	evidence "github.com/skyhook-io/radar/pkg/runtimeevidence"
	corev1 "k8s.io/api/core/v1"
)

type applicationActionsDisabledKey struct{}

type applicationEvidenceAction struct {
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
	client := k8s.ClientFromContext(ctx)
	var actions []applicationEvidenceAction
	for _, candidate := range set.Candidates {
		if !deps.NamespaceAllowed(candidate.Target.Namespace) {
			continue
		}
		p, err := deps.Cache.Pods().Pods(candidate.Target.Namespace).Get(candidate.Target.Pod)
		if err != nil || string(p.UID) != candidate.Target.UID {
			continue
		}
		reason := applicationActionReason(p, candidate)
		if reason == "" || !collector.CheckAccess(ctx, client, candidate.Target) {
			continue
		}
		actions = append(actions, applicationEvidenceAction{Candidate: candidate, Tool: "collect_application_evidence", Arguments: map[string]any{"application": candidate.Application, "namespace": candidate.Target.Namespace, "pod": candidate.Target.Pod}, WhyThisHelps: reason, SourceRefs: []trace.ResourceRef{{Kind: "Pod", Namespace: p.Namespace, Name: p.Name}}, AuthorizationRequired: "Ask the operator before setting confirm_network_access=true; the declared endpoint may still be unavailable.", CandidatesTruncated: set.Truncated || set.CoverageLimited})
	}
	return actions
}

func applicationActionReason(p *corev1.Pod, c collector.Candidate) string {
	if p == nil || c.Application == evidence.NATS {
		return ""
	}
	if !collector.ReadinessFailure(p, c.Target.Container) {
		return ""
	}
	switch c.Application {
	case evidence.Vault:
		return "The Vault container is not ready; its initialization and seal state may explain why it is not serving. Standby alone is not a fault."
	case evidence.RabbitMQ:
		return "The RabbitMQ container is not ready; its node-local disk and memory alarms may provide application-state evidence. An alarm-free node does not rule out problems on other nodes."
	}

	return ""
}
