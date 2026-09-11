package ai

import (
	"encoding/json"
	"strings"
)

// applyMutationTracker derives mutation truth from Radar write-tool results.
// Agent prose and process exit are deliberately excluded: a zero exit cannot
// turn a failed/missing tool result into a confirmed Kubernetes mutation.
//
// Steps are correlated by ID because Claude's terminal tool-result row omits the
// tool name. An incomplete/unknown terminal result remains unknown; a mixture of
// successes and failures is also unknown because the requested change may be
// only partially applied.
type applyMutationTracker struct {
	steps map[string]applyMutationStep
	// Anonymous write steps are not expected from supported agents, but treating
	// them explicitly keeps a missing correlation ID honest instead of silently
	// converting it to "no write attempted".
	anonymousStarted bool
}

type applyMutationStep struct {
	tool      string
	summary   string
	result    string
	done      bool
	isError   *bool
	truncated bool
}

func (t *applyMutationTracker) observe(ev StreamEvent) {
	step := ev.Step
	if ev.Type != "step" || step == nil {
		return
	}
	state, tracked := t.steps[step.ID]
	if !tracked && !isRadarWriteTool(step.Tool) {
		return
	}
	if step.ID == "" {
		// Without a correlation id, a later terminal row cannot be tied to one
		// particular write call. Even a producer-shaped success is therefore not
		// enough to claim that the complete requested mutation landed.
		t.anonymousStarted = true
		return
	}
	if t.steps == nil {
		t.steps = make(map[string]applyMutationStep)
	}
	switch step.Status {
	case "running":
		state.tool = normalizeRadarToolName(step.Tool)
		state.summary = step.Summary
	case "done":
		state.done = true
		state.isError = step.IsError
		state.result = step.Result
		state.truncated = step.Truncated
		if step.Tool != "" {
			state.tool = normalizeRadarToolName(step.Tool)
		}
		if step.Summary != "" {
			state.summary = step.Summary
		}
	}
	t.steps[step.ID] = state
}

type mutationStepEvidence uint8

const (
	mutationEvidenceUnknown mutationStepEvidence = iota
	mutationEvidenceNone
	mutationEvidenceConfirmed
)

// evidence interprets the write producer's result contract. The agent host's
// `is_error=false` only says that the MCP call completed; it does not say that a
// real mutation occurred (dry-run and no-op calls also complete successfully),
// nor that a multi-part operation completed in full.
func (s applyMutationStep) evidence() mutationStepEvidence {
	if !s.done || s.isError == nil || *s.isError || s.truncated {
		// A tool error can follow a partial write (node drain, Rollout status+spec,
		// Flux source+target), so the host error bit cannot prove "nothing landed".
		return mutationEvidenceUnknown
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(s.result)), &result); err != nil {
		return mutationEvidenceUnknown
	}
	status, _ := result["status"].(string)
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "partial", "partial_failure":
		return mutationEvidenceUnknown
	case "ok":
		// Continue below: the producer accepted the operation, but explicit
		// non-mutating modes still override that transport-level success.
	default:
		return mutationEvidenceUnknown
	}

	if resultIsDryRun(result) || boolResultField(result, "noChange") {
		return mutationEvidenceNone
	}

	switch s.tool {
	case "manage_gitops":
		// Unlike apply_resource and patch_resource, manage_gitops does not echo
		// dry_run in its result. Its current input contract is authoritative, so
		// an absent/unparseable running payload cannot safely become confirmed.
		var input struct {
			Action string `json:"action"`
			Tool   string `json:"tool"`
			DryRun *bool  `json:"dry_run"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(s.summary)), &input); err != nil {
			return mutationEvidenceUnknown
		}
		if strings.TrimSpace(input.Action) == "" || strings.TrimSpace(input.Tool) == "" {
			return mutationEvidenceUnknown
		}
		if input.DryRun != nil && *input.DryRun {
			return mutationEvidenceNone
		}
	case "apply_resource":
		// A multi-document dry run carries dry_run on each resource rather than
		// on the top-level envelope. The running args are the simplest complete
		// signal, while the result field above covers a single document.
		if inputDryRun(s.summary) {
			return mutationEvidenceNone
		}
	}

	return mutationEvidenceConfirmed
}

func boolResultField(result map[string]any, field string) bool {
	value, _ := result[field].(bool)
	return value
}

func resultIsDryRun(result map[string]any) bool {
	if boolResultField(result, "dry_run") {
		return true
	}
	resources, _ := result["resources"].([]any)
	for _, resource := range resources {
		entry, _ := resource.(map[string]any)
		if boolResultField(entry, "dry_run") {
			return true
		}
	}
	return false
}

func inputDryRun(summary string) bool {
	var input struct {
		DryRun bool `json:"dry_run"`
	}
	return json.Unmarshal([]byte(strings.TrimSpace(summary)), &input) == nil && input.DryRun
}

func (t *applyMutationTracker) outcome(profile ExecutionProfile) ApplyMutationOutcome {
	if profile == ExecutionProfileFullLocal {
		// Full-local agents may use user-configured MCP servers whose bare tool
		// names collide with Radar's write tools. Until the write transport carries
		// the same exact-payload provenance as read-only investigations, no observed
		// full-local call can authoritatively confirm the mutation. Verification is
		// still scheduled because an unobserved write may have landed.
		return ApplyMutationUnknown
	}
	confirmed := 0
	unknown := t.anonymousStarted
	for _, step := range t.steps {
		switch step.evidence() {
		case mutationEvidenceUnknown:
			unknown = true
		case mutationEvidenceNone:
			// A preview/no-op does not weaken a separate, producer-confirmed
			// mutation. It matters only when no mutation was confirmed at all.
		case mutationEvidenceConfirmed:
			confirmed++
		}
	}
	if unknown {
		return ApplyMutationUnknown
	}
	if confirmed > 0 {
		return ApplyMutationConfirmed
	}
	// No write call, or only producer-confirmed non-mutating calls (dry run / no
	// change): no mutation could have landed through the safeguarded profile's
	// only mutation surface. Tool failures stay unknown above because several
	// producers can fail after partially mutating.
	return ApplyMutationFailed
}
