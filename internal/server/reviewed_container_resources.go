package server

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"strconv"

	"github.com/skyhook-io/radar/internal/integration"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation"
)

type containerResourceChange struct {
	Container string `json:"container"`
	Resource  string `json:"resource"`
	containerResourcePair
}

func resolveContainerResources(in workloadActionInput) (*containerResourceChange, error) {
	dimension := string(in.Resource)
	if len(validation.IsDNS1123Label(string(in.Container))) != 0 || (dimension != "cpu" && dimension != "memory") || (in.Request == "" && in.Limit == "") {
		return nil, errors.New("setContainerResources requires a regular container name, resource cpu or memory, and a request or limit")
	}
	request, err := desiredResourceValue(in.Request, dimension)
	if err != nil {
		return nil, err
	}
	limit, err := desiredResourceValue(in.Limit, dimension)
	if err != nil {
		return nil, err
	}
	return &containerResourceChange{string(in.Container), dimension, containerResourcePair{request, limit}}, nil
}

func (c containerResourceChange) field() string {
	return "spec.template.spec.containers[name=" + strconv.Quote(c.Container) + "].resources"
}

func (c containerResourceChange) container(obj *unstructured.Unstructured) (map[string]any, error) {
	raw, _, err := unstructured.NestedFieldNoCopy(obj.Object, "spec", "template", "spec", "containers")
	if err != nil {
		return nil, err
	}
	containers, _ := raw.([]any)
	for _, item := range containers {
		container, ok := item.(map[string]any)
		if ok && container["name"] == c.Container {
			return container, nil
		}
	}
	return nil, integration.RefuseAction(http.StatusConflict, "resource_container_missing", "Deployment has no regular container named %q. Inspect its current container names", c.Container)
}

func (c containerResourceChange) values(obj *unstructured.Unstructured) (containerResourcePair, error) {
	target, err := c.container(obj)
	if err != nil {
		return containerResourcePair{}, err
	}
	var pair containerResourcePair
	for _, item := range []struct {
		name  string
		value *workloadResourceValue
	}{{"requests", &pair.Request}, {"limits", &pair.Limit}} {
		raw, found, err := unstructured.NestedString(target, "resources", item.name, c.Resource)
		if err != nil {
			return pair, err
		}
		if found {
			*item.value, err = liveResourceValue(raw, c.Resource)
			if err != nil {
				return pair, integration.RefuseAction(http.StatusConflict, "resource_quantity_unsupported", "%s.%s in container %q: %v", item.name, c.Resource, c.Container, err)
			}
		}
	}
	return pair, nil
}

func (c containerResourceChange) plan(obj *unstructured.Unstructured, before containerResourcePair) (containerResourcePair, string, error) {
	paused, _, err := unstructured.NestedBool(obj.Object, "spec", "paused")
	if err != nil {
		return before, "", err
	}
	if paused {
		return before, "", integration.RefuseAction(http.StatusConflict, errCodeWorkloadPaused, "the Deployment is paused. Resume it before changing container resources")
	}
	for _, name := range []string{"requests", "limits"} {
		_, found, err := unstructured.NestedFieldNoCopy(obj.Object, "spec", "template", "spec", "resources", name, c.Resource)
		if err != nil {
			return before, "", err
		}
		if found {
			return before, "", integration.RefuseAction(http.StatusConflict, "resource_pod_budget", "the Pod template already sets Pod-level %s.%s. Review that budget before resizing one container", name, c.Resource)
		}
	}
	after := before
	if c.Request != "" {
		after.Request = c.Request
	}
	if c.Limit != "" {
		if after.Request == "" {
			return before, "", integration.RefuseAction(http.StatusConflict, "resource_request_required", "include an explicit %s request with this limit. A new Pod can otherwise default its request to its limit", c.Resource)
		}
		after.Limit = c.Limit
	}
	if after.Request != "" && after.Limit != "" {
		req, lim := resource.MustParse(string(after.Request)), resource.MustParse(string(after.Limit))
		if req.Cmp(lim) > 0 {
			return before, "", integration.RefuseAction(http.StatusConflict, "resource_request_exceeds_limit", "the resulting %s request %s exceeds the limit %s. Adjust the request and limit together", c.Resource, after.Request, after.Limit)
		}
	}
	replicas, known, err := unstructured.NestedInt64(obj.Object, "spec", "replicas")
	if err != nil {
		return before, "", err
	}
	strategy, _, err := unstructured.NestedString(obj.Object, "spec", "strategy", "type")
	if err != nil {
		return before, "", err
	}
	effect := "Changing the Pod template starts a Deployment rollout."
	if known && replicas == 0 {
		effect = "The Deployment has zero replicas. Future Pods use the new template when it scales up."
	} else if strategy == "Recreate" {
		effect = "The Recreate rollout stops existing Pods before starting replacements. Expect a service interruption."
	}
	label := c.Resource
	if c.Resource == "cpu" {
		label = "CPU"
	}
	if after.Request != before.Request {
		effect += c.requestEffect(before, after, label, replicas)
	}
	if after.Limit != before.Limit {
		if c.Resource == "cpu" {
			effect += " A container that reaches its CPU limit is throttled."
		} else {
			effect += " Compare actual memory usage with the new limit. Exceeding it can OOM-kill the container."
		}
	}
	if before.Limit != "" && (before.Request == before.Limit || before.Request == "") && after.Request != after.Limit {
		effect += " If current Pods have Guaranteed QoS, splitting request and limit can make replacements Burstable."
	}
	if known && replicas == 0 {
		effect += " Check admission and readiness when the Deployment scales up."
	} else {
		effect += " Pod admission can change or reject replacements. Check rollout readiness and events."
	}
	return after, effect, nil
}

func (c containerResourceChange) requestEffect(before, after containerResourcePair, label string, replicas int64) string {
	effect := " The request can change scheduling and node capacity requirements."
	previous := before.Request
	if previous == "" && before.Limit != "" {
		previous = before.Limit
		effect += fmt.Sprintf(" Where admission inherited the prior %s limit as the request, the previous effective request was %s.", label, previous)
	}
	if previous == "" || previous == "0" {
		return effect + " Setting the request can change utilization-based HPA decisions."
	}
	old, next := resource.MustParse(string(previous)), resource.MustParse(string(after.Request))
	scale := resource.Scale(0)
	if c.Resource == "cpu" {
		scale = resource.Milli
	}
	delta := next.ScaledValue(scale) - old.ScaledValue(scale)
	if delta == 0 {
		return effect + " The explicit request equals the inherited value."
	}
	amount, utilization := "more", "lowers"
	if delta < 0 {
		delta, amount, utilization = -delta, "less", "raises"
	}
	if replicas > 0 && delta <= math.MaxInt64/replicas {
		effect += fmt.Sprintf(" Across %d desired replicas, %s requests %s %s %s before surge or admission changes.", replicas, c.Container, canonicalResourceValue(delta*replicas, c.Resource), amount, label)
	}
	return effect + " At the same usage, this " + utilization + " measured utilization for an HPA using this " + label + " request."
}

// Strategic merge on a native Deployment merges containers by name. An RFC
// 7396 merge patch with this array would replace every other container.
func (c containerResourceChange) patch(uid string) map[string]any {
	settings := map[string]any{}
	if c.Request != "" {
		settings["requests"] = map[string]any{c.Resource: string(c.Request)}
	}
	if c.Limit != "" {
		settings["limits"] = map[string]any{c.Resource: string(c.Limit)}
	}
	return map[string]any{
		"metadata": map[string]any{"uid": uid},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"containers": []any{map[string]any{"name": c.Container, "resources": settings}},
		}}},
	}
}

// Compare the complete spec, including unknown native fields. The selected
// quantities were compared semantically already. Use Kubernetes' returned
// spelling for those approved positions when checking everything else.
func (c containerResourceChange) preservesSpec(before, stored *unstructured.Unstructured) bool {
	expected := before.DeepCopy()
	want, err := c.container(expected)
	if err != nil {
		return false
	}
	got, err := c.container(stored)
	if err != nil {
		return false
	}
	for _, key := range []string{"requests", "limits"} {
		value, found, err := unstructured.NestedString(got, "resources", key, c.Resource)
		if err != nil {
			return false
		}
		if found {
			if err := unstructured.SetNestedField(want, value, "resources", key, c.Resource); err != nil {
				return false
			}
		}
	}
	return reflect.DeepEqual(expected.Object["spec"], stored.Object["spec"])
}
