package servinginsight

import (
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var kserveInferenceServiceV1beta1 = schema.GroupVersionKind{Group: "serving.kserve.io", Version: "v1beta1", Kind: "InferenceService"}

const (
	maxKServeTraffic  = 8
	maxKServeCanaries = 8
)

// forKServeInferenceService follows the KServe v0.20.0 status writers for
// Standard (RawDeployment) and Knative modes. It reads only the root object.
// ModelMesh status is written by a separate controller, so its model state is
// not projected.
func forKServeInferenceService(u *unstructured.Unstructured) *resourcecontext.KServeInferenceServiceServing {
	out := &resourcecontext.KServeInferenceServiceServing{}
	status, ok := u.Object["status"].(map[string]any)
	if !ok {
		return out
	}
	out.DeploymentMode = stringAt(status, "deploymentMode")
	out.ServingRuntimeName = stringAt(status, "servingRuntimeName")
	out.ClusterServingRuntimeName = stringAt(status, "clusterServingRuntimeName")
	if out.DeploymentMode == "ModelMesh" {
		return out
	}
	out.Model = kserveModelState(status)
	out.FieldNotes = kserveFieldNotes(status, out.DeploymentMode)
	if len(out.FieldNotes) > 0 {
		out.ReferenceWriterRelease = kserveReferenceWriterRelease
	}
	out.Components = kserveComponents(status)
	out.Canaries, out.CanariesTruncated = kserveCanaries(status)
	return out
}

func kserveModelState(status map[string]any) *resourcecontext.KServeModelState {
	modelStatus, ok := status["modelStatus"].(map[string]any)
	if !ok {
		return nil
	}
	model := &resourcecontext.KServeModelState{TransitionStatus: stringPointerAt(modelStatus, "transitionStatus")}
	if states, ok := modelStatus["states"].(map[string]any); ok {
		model.TargetModelState = stringAt(states, "targetModelState")
	}
	if failure, ok := modelStatus["lastFailureInfo"].(map[string]any); ok {
		lastFailure := &resourcecontext.KServeModelFailure{Reason: stringAt(failure, "reason"), ExitCode: int64PointerAt(failure, "exitCode")}
		if lastFailure.Reason != "" || lastFailure.ExitCode != nil {
			model.LastFailure = lastFailure
		}
	}
	if model.TransitionStatus == nil && model.TargetModelState == "" && model.LastFailure == nil {
		return nil
	}
	return model
}

func kserveComponents(status map[string]any) *resourcecontext.KServeComponents {
	components, ok := status["components"].(map[string]any)
	if !ok {
		return nil
	}
	out := &resourcecontext.KServeComponents{
		Predictor:   kserveComponent(components, "predictor"),
		Transformer: kserveComponent(components, "transformer"),
		Explainer:   kserveComponent(components, "explainer"),
	}
	if out.Predictor == nil && out.Transformer == nil && out.Explainer == nil {
		return nil
	}
	return out
}

func kserveComponent(components map[string]any, name string) *resourcecontext.KServeComponent {
	source, ok := components[name].(map[string]any)
	if !ok {
		return nil
	}
	out := &resourcecontext.KServeComponent{
		LatestReadyRevision:       stringAt(source, "latestReadyRevision"),
		LatestCreatedRevision:     stringAt(source, "latestCreatedRevision"),
		PreviousRolledoutRevision: stringAt(source, "previousRolledoutRevision"),
		LatestRolledoutRevision:   stringAt(source, "latestRolledoutRevision"),
	}
	traffic, _ := source["traffic"].([]any)
	for i, entry := range traffic {
		if i == maxKServeTraffic {
			out.TrafficTruncated = true
			break
		}
		target, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		out.Traffic = append(out.Traffic, resourcecontext.KServeRevisionTraffic{
			RevisionName:   stringAt(target, "revisionName"),
			LatestRevision: boolPointerAt(target, "latestRevision"),
			Percent:        int64PointerAt(target, "percent"),
			Tag:            stringAt(target, "tag"),
		})
	}
	if out.LatestReadyRevision == "" && out.LatestCreatedRevision == "" && out.PreviousRolledoutRevision == "" &&
		out.LatestRolledoutRevision == "" && len(out.Traffic) == 0 && !out.TrafficTruncated {
		return nil
	}
	return out
}

func kserveCanaries(status map[string]any) ([]resourcecontext.KServeCanary, bool) {
	source, _ := status["canaryStatuses"].([]any)
	var out []resourcecontext.KServeCanary
	for i, entry := range source {
		if i == maxKServeCanaries {
			return out, true
		}
		canary, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, resourcecontext.KServeCanary{
			Name:           stringAt(canary, "name"),
			Ready:          boolPointerAt(canary, "ready"),
			TrafficPercent: int64PointerAt(canary, "trafficPercent"),
		})
	}
	return out, false
}

func stringAt(object map[string]any, field string) string {
	value, _ := object[field].(string)
	return value
}

func stringPointerAt(object map[string]any, field string) *string {
	value, ok := object[field].(string)
	if !ok {
		return nil
	}
	return &value
}

func boolPointerAt(object map[string]any, field string) *bool {
	value, ok := object[field].(bool)
	if !ok {
		return nil
	}
	return &value
}

// kserveReferenceWriterRelease is the controller release whose status writers the field
// notes describe; the same API version can be served by other releases.
const kserveReferenceWriterRelease = "KServe v0.20.0"

const (
	noteActiveModelState  = "Set to Loaded when the controller marks a revision loaded and kept through later transitions (only a stop clears status); it does not show that any model is serving now."
	noteLastFailureInfo   = "Kept after recovery and through later transitions (only a stop clears status); it may describe an earlier attempt rather than the current one."
	noteObservedGen       = "Copied from a child Deployment or Knative Service (whichever component was reconciled last), not this InferenceService's own; do not compare it with metadata.generation."
	noteTransitionStd     = "Derived from the newest predictor Pod, falling back to the predictor Deployment's availability; UpToDate and Loaded can be reported while a new rollout's Pod has not started."
	noteTransitionKnative = "Uses the latest created revision's Pods and Knative conditions; Loaded requires root Ready and matching latest-created/latest-ready revisions."
	noteCopies            = "failedCopies is never written (always 0); totalCopies counts ready predictor Pods, not model copies."
	noteCanaryReady       = "True when the canary's Deployment has at least one available replica."
	noteCanaryTraffic     = "The declared spec value, not measured traffic."
	noteCanaryModelStatus = "Never written for canaries; it does not describe the canary's model."
)

// kserveFieldNotes explains raw status fields whose names suggest more than the
// controller writes. Notes appear only for fields present on the object, so an
// agent reads them next to the field they qualify.
func kserveFieldNotes(status map[string]any, mode string) []resourcecontext.ServingFieldNote {
	var notes []resourcecontext.ServingFieldNote
	add := func(field, note string) {
		notes = append(notes, resourcecontext.ServingFieldNote{Field: field, Note: note})
	}
	if _, ok := status["observedGeneration"]; ok {
		add("status.observedGeneration", noteObservedGen)
	}
	if modelStatus, ok := status["modelStatus"].(map[string]any); ok {
		if transition, _ := modelStatus["transitionStatus"].(string); transition != "" && transition != "InvalidSpec" {
			switch mode {
			case "Standard", "RawDeployment":
				add("status.modelStatus.transitionStatus", noteTransitionStd)
			case "Knative", "Serverless":
				add("status.modelStatus.transitionStatus", noteTransitionKnative)
			}
		}
		if states, ok := modelStatus["states"].(map[string]any); ok {
			if _, ok := states["activeModelState"]; ok {
				add("status.modelStatus.states.activeModelState", noteActiveModelState)
			}
		}
		if _, ok := modelStatus["lastFailureInfo"]; ok {
			add("status.modelStatus.lastFailureInfo", noteLastFailureInfo)
		}
		if _, ok := modelStatus["copies"]; ok {
			add("status.modelStatus.copies", noteCopies)
		}
	}
	canaries, _ := status["canaryStatuses"].([]any)
	for _, canaryField := range []struct{ name, note string }{
		{"ready", noteCanaryReady}, {"trafficPercent", noteCanaryTraffic}, {"modelStatus", noteCanaryModelStatus},
	} {
		for _, entry := range canaries {
			if canary, ok := entry.(map[string]any); ok {
				if _, ok := canary[canaryField.name]; ok {
					add("status.canaryStatuses[]."+canaryField.name, canaryField.note)
					break
				}
			}
		}
	}
	return notes
}
