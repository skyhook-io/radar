package mcp

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/rollouts"
)

var (
	waitMaxTimeout   = 45 * time.Second
	waitPollInterval = 2 * time.Second
	waitEmptyGrace   = 10 * time.Second
	waitBlockerGrace = 10 * time.Second
	waitNow          = time.Now
)

type waitForInput struct {
	Kind           string `json:"kind" jsonschema:"resource kind: deployment, statefulset, daemonset, rollout, job, pod, namespace, or any kind including CRDs"`
	Group          string `json:"group,omitempty" jsonschema:"API group when the kind is ambiguous"`
	Namespace      string `json:"namespace,omitempty" jsonschema:"namespace for namespaced kinds"`
	Name           string `json:"name,omitempty" jsonschema:"object name; or set label_selector or all"`
	UID            string `json:"uid,omitempty" jsonschema:"with name: the uid a previous wait_for call returned, so a replacement with the same name doesn't count as this object"`
	LabelSelector  string `json:"label_selector,omitempty" jsonschema:"every matching object must meet the condition; with deleted, none may remain; to wait for replacements, wait on their controller's rollout"`
	All            bool   `json:"all,omitempty" jsonschema:"every object of the kind in the namespace"`
	Condition      string `json:"condition,omitempty" jsonschema:"rollout, complete, ready, deleted, a condition type (Type or Type=Status), or jsonpath={.status.path}=value; defaults by kind"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"seconds this call waits, 1-45 (default 45); larger values wait 45"`
}

type waitObjectState struct {
	Name               string           `json:"name"`
	UID                types.UID        `json:"uid"`
	Met                bool             `json:"met"`
	Generation         int64            `json:"generation,omitempty"`
	ObservedGeneration any              `json:"observedGeneration,omitempty"`
	Observed           string           `json:"observed"`
	Replicas           map[string]int64 `json:"replicas,omitempty"`
	InStateSeconds     *int64           `json:"inStateSeconds,omitempty"`
	state              string
	next               string
	controllerObserved bool
}

type waitCheck func(obj *unstructured.Unstructured) (met bool, message, failure string)

type waitStepResult[T any] struct {
	value T
	err   error
}

// Cache and discovery helpers are synchronous; the call deadline also bounds those steps.
func waitStep[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	if err := ctx.Err(); err != nil {
		var zero T
		return zero, err
	}
	ch := make(chan waitStepResult[T], 1)
	go func() { v, err := fn(); ch <- waitStepResult[T]{v, err} }()
	select {
	case r := <-ch:
		return r.value, r.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

func handleWaitFor(ctx context.Context, req *mcp.CallToolRequest, input waitForInput) (*mcp.CallToolResult, any, error) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, waitTimeout(input.TimeoutSeconds))
	defer cancel()
	if err := requireConnected(); err != nil {
		return nil, nil, err
	}
	dyn := k8s.DynamicClientFromContext(ctx)
	if dyn == nil {
		return nil, nil, errNotConnected()
	}
	return waitForUntil(ctx, dyn, input, start)
}

func waitTimeout(seconds int) time.Duration {
	if seconds <= 0 || seconds >= 45 {
		return waitMaxTimeout
	}
	return min(time.Duration(seconds)*time.Second, waitMaxTimeout)
}

func waitFor(ctx context.Context, dyn dynamic.Interface, input waitForInput) (*mcp.CallToolResult, any, error) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, waitTimeout(input.TimeoutSeconds))
	defer cancel()
	return waitForUntil(ctx, dyn, input, start)
}

func waitForUntil(ctx context.Context, dyn dynamic.Interface, input waitForInput, start time.Time) (*mcp.CallToolResult, any, error) {
	kind, name, selector := strings.TrimSpace(input.Kind), strings.TrimSpace(input.Name), strings.TrimSpace(input.LabelSelector)
	if kind == "" {
		return nil, nil, fmt.Errorf("kind is required")
	}
	if targets := btoi(name != "") + btoi(selector != "") + btoi(input.All); targets != 1 {
		return nil, nil, fmt.Errorf("set exactly one of name, label_selector or all")
	}
	if input.UID != "" && name == "" {
		return nil, nil, fmt.Errorf("uid needs name")
	}
	if input.TimeoutSeconds < 0 {
		return nil, nil, fmt.Errorf("timeout_seconds must be positive")
	}
	type resolved struct {
		gvr        schema.GroupVersionResource
		namespaced bool
	}
	r, err := waitStep(ctx, func() (resolved, error) {
		g, ns, e := resolveMutationGVR(kind, strings.TrimSpace(input.Group))
		return resolved{g, ns}, e
	})
	if err != nil {
		return nil, nil, err
	}
	gvr := r.gvr
	if prefix, suffix, ok := strings.Cut(name, "/"); ok {
		other, err := waitStep(ctx, func() (schema.GroupVersionResource, error) {
			g, _, e := resolveMutationGVR(prefix, strings.TrimSpace(input.Group))
			return g, e
		})
		if err != nil || other != gvr {
			return nil, nil, fmt.Errorf("name %q names a different kind; set kind=%s", name, prefix)
		}
		if suffix == "" || strings.Contains(suffix, "/") {
			return nil, nil, fmt.Errorf("name must be a non-empty object name or kind/name")
		}
		name = suffix
	}
	namespace := strings.TrimSpace(input.Namespace)
	if r.namespaced && namespace == "" {
		return nil, nil, fmt.Errorf("namespace is required for namespaced kind %q", kind)
	}
	if !r.namespaced {
		namespace = ""
	}
	condition := strings.TrimSpace(input.Condition)
	if condition == "" {
		condition = defaultWaitCondition(gvr)
		if condition == "" {
			return nil, nil, fmt.Errorf("condition is required for kind %q: deleted, a condition type (Type=Status), or jsonpath={.status.field}=value", kind)
		}
	}
	if strings.EqualFold(condition, "delete") {
		condition = "deleted"
	}
	deleted := strings.EqualFold(condition, "deleted")
	if gvr.Group == "" && gvr.Resource == "secrets" && !deleted {
		return nil, nil, fmt.Errorf("Secrets support only condition=deleted")
	}
	var check waitCheck
	var jp *waitJSONPath
	if strings.HasPrefix(strings.ToLower(condition), "jsonpath=") {
		jp, err = parseWaitJSONPath(condition[len("jsonpath="):])
	} else if !deleted {
		check, err = waitCheckFor(gvr, condition)
	}
	if err != nil {
		return nil, nil, err
	}
	var client dynamic.ResourceInterface = dyn.Resource(gvr)
	if r.namespaced {
		client = dyn.Resource(gvr).Namespace(namespace)
	}
	result := map[string]any{"kind": kind, "apiVersion": gvr.GroupVersion().String(), "condition": condition}
	if namespace != "" {
		result["namespace"] = namespace
	}
	if name != "" {
		result["name"] = name
	} else if input.All {
		result["all"] = true
	} else {
		result["labelSelector"] = selector
	}
	if input.TimeoutSeconds > 45 {
		result["note"] = "one call waits at most 45s; call again to keep waiting"
	}
	pinnedUID := types.UID(input.UID)
	successful, strikes := false, 0
	var lastErr error
	var states []waitObjectState
	var emptySince time.Time
	blockerSince := map[waitBlockerKey]time.Time{}
	finish := func(state, next string) (*mcp.CallToolResult, any, error) {
		result["state"] = state
		result["waitedSeconds"] = int64(time.Since(start) / time.Second)
		sort.SliceStable(states, func(i, j int) bool { return waitStateRank(states[i]) < waitStateRank(states[j]) })
		if len(states) > 10 {
			count := 0
			for _, s := range states {
				if s.Met {
					count++
				}
			}
			result["total"] = len(states)
			result["metCount"] = count
		}
		if states == nil {
			states = []waitObjectState{}
		}
		result["objects"] = states[:min(len(states), 10)]
		if state == "met" {
			delete(result, "next")
		} else {
			result["next"] = truncateCrashLogLine(next, 300)
		}
		return toJSONResult(result)
	}
	for {
		if r.namespaced {
			allowed, e := waitStep(ctx, func() (bool, error) {
				return namespaceWithinPin(namespace) && checkNamespaceAccess(ctx, namespace), nil
			})
			if e != nil {
				lastErr = e
				break
			}
			if !allowed {
				return nil, nil, fmt.Errorf("forbidden: no access to namespace %q", namespace)
			}
		}
		objs, readErr := waitStep(ctx, func() ([]*unstructured.Unstructured, error) { return readWaitTargets(ctx, client, name, selector) })
		if readErr != nil {
			lastErr = readErr
			if apierrors.IsForbidden(readErr) {
				return nil, nil, fmt.Errorf("cannot get %s in %q: %v. get_subject_permissions shows what this identity can do", gvr.Resource, namespace, readErr)
			}
			if _, ok := readErr.(*waitInputError); ok {
				return nil, nil, &waitInputError{fmt.Sprintf("more than 200 %s match; narrow with label_selector", gvr.Resource)}
			}
			strikes++
			if strikes >= 3 {
				return nil, nil, fmt.Errorf("failed to read %s after three consecutive errors: %w", kind, readErr)
			}
		} else {
			successful = true
			strikes = 0
			previousStates := states
			states = nil
			interrupted := false
			replaced := false
			delete(result, "blockers")
			delete(result, "blockersUnavailable")
			delete(result, "observed")
			if len(objs) == 0 {
				if deleted {
					return finish("met", "")
				}
				result["observed"] = "nothing matches"
				if emptySince.IsZero() {
					emptySince = waitNow()
				}
			} else {
				emptySince = time.Time{}
			}
			allMet := len(objs) > 0
			verdict, next := "waiting", "not met yet; call wait_for again with the same arguments until your own time limit"
			if len(objs) == 0 {
				next = fmt.Sprintf("nothing matches %s in %s; check the name or selector with list_resources", waitTargetLabel(name, selector), namespace)
			}
			var blockers []waitBlocker
			seenBlockers := map[waitBlockerKey]bool{}
			checked := 0
			for _, obj := range objs {
				result["kind"] = obj.GetKind()
				result["apiVersion"] = obj.GetAPIVersion()
				s := waitObjectState{Name: obj.GetName(), UID: obj.GetUID(), Generation: obj.GetGeneration()}
				if v, ok, _ := unstructured.NestedFieldNoCopy(obj.Object, "status", "observedGeneration"); ok {
					s.ObservedGeneration = v
				}
				if deleted {
					if name != "" && pinnedUID == "" {
						pinnedUID = obj.GetUID()
					}
					if name != "" && pinnedUID != obj.GetUID() {
						continue
					}
					s.Observed = deletionState(obj)
					s.InStateSeconds = waitAge(obj.GetDeletionTimestamp())
					if fs := obj.GetFinalizers(); obj.GetDeletionTimestamp() != nil && len(fs) > 0 {
						s.next = fmt.Sprintf("terminating for %ds, waiting on finalizers [%s]; the controller that owns them must finish (get_resource shows them)", *s.InStateSeconds, strings.Join(fs[:min(len(fs), 5)], ", "))
					}
				} else if name != "" && input.UID != "" && types.UID(input.UID) != obj.GetUID() {
					s.Observed = "replaced by uid " + string(obj.GetUID())
					s.next = "drop uid to follow the new object"
					replaced = true
				} else {
					evaluated, e := waitStep(ctx, func() (waitObjectState, error) {
						return evaluateWaitObject(ctx, dyn, gvr, obj, condition, check, jp, s)
					})
					if e != nil {
						if ctx.Err() != nil {
							lastErr = e
							interrupted = true
							break
						}
						return nil, nil, e
					}
					s = evaluated
				}
				s.Observed = truncateCrashLogLine(s.Observed, 300)
				states = append(states, s)
				allMet = allMet && s.Met
				if s.state == "failed" || (s.state == "blocked" && verdict != "failed") {
					verdict = s.state
					next = s.next
				} else if verdict == "waiting" && s.next != "" {
					next = s.next
				}
				apply, coverage := false, ""
				if !s.Met && !deleted && checked < 3 {
					apply, coverage = waitBlockersApply(ctx, dyn, gvr, obj, s)
				}
				if coverage != "" {
					result["blockersUnavailable"] = coverage
				}
				if apply {
					checked++
					b, e := waitStep(ctx, func() (waitBlockerResult, error) { return readWaitBlockers(ctx, dyn, gvr, obj), nil })
					if e != nil {
						lastErr = e
						result["blockersUnavailable"] = "blocker check did not finish within the call"
						break
					}
					if b.unavailable != "" {
						result["blockersUnavailable"] = b.unavailable
					}
					for _, problem := range b.blockers {
						blockers = append(blockers, problem)
						key := problem.key
						seenBlockers[key] = true
						if problem.Reason != "Unschedulable" {
							if _, ok := blockerSince[key]; !ok {
								blockerSince[key] = waitNow()
							}
							if waitNow().Sub(blockerSince[key]) >= waitBlockerGrace && verdict != "failed" {
								verdict = "blocked"
								next = waitDiagnoseNext(obj) + "; the rollout is stalled on this pod problem"
								if gvr.Group == "batch" {
									next = waitLogsNext(obj)
								}
								states[len(states)-1].state = "blocked"
							}
						} else if verdict == "waiting" {
							target := waitDiagnoseNext(obj)
							if gvr.Group == "batch" {
								// diagnose doesn't take Jobs; the pod carries the scheduler's reason.
								target = fmt.Sprintf("diagnose Pod %s in %s", problem.Pod, obj.GetNamespace())
							}
							next = "pods can't be scheduled; unless nodes are being added, " + target
						}
					}
				}
			}
			for key := range blockerSince {
				if !seenBlockers[key] {
					delete(blockerSince, key)
				}
			}
			if _, unavailable := result["blockersUnavailable"]; !unavailable && len(blockers) > 0 {
				result["blockers"] = capWaitBlockers(blockers)
			}
			if interrupted {
				states = previousStates
				break
			}
			if replaced {
				return finish("waiting", next)
			}
			if deleted {
				allMet = len(states) == 0
			}
			if allMet {
				return finish("met", "")
			}
			if deleted && name != "" && pinnedUID != "" {
				next += "; uid=" + string(pinnedUID)
			}
			result["next"] = truncateCrashLogLine(next, 300)
			if verdict != "waiting" || (!emptySince.IsZero() && waitNow().Sub(emptySince) >= waitEmptyGrace) {
				return finish(verdict, next)
			}
		}
		if ctx.Err() != nil {
			break
		}
		timer := time.NewTimer(waitPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
		if ctx.Err() != nil {
			break
		}
	}
	if !successful {
		return nil, nil, fmt.Errorf("no successful read of %s: %w", kind, lastErr)
	}
	next, _ := result["next"].(string)
	if next == "" {
		next = "not met yet; call wait_for again with the same arguments until your own time limit"
	}
	return finish("waiting", next)
}

type waitInputError struct{ message string }

func (e *waitInputError) Error() string { return e.message }

func readWaitTargets(ctx context.Context, client dynamic.ResourceInterface, name, selector string) ([]*unstructured.Unstructured, error) {
	if name != "" {
		obj, err := client.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return []*unstructured.Unstructured{obj}, nil
	}
	var out []*unstructured.Unstructured
	token := ""
	for {
		list, err := client.List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: 100, Continue: token})
		if err != nil {
			return nil, err
		}
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		for i := range list.Items {
			out = append(out, &list.Items[i])
		}
		if len(out) > 200 {
			return nil, &waitInputError{fmt.Sprintf("more than 200 objects match; narrow with label_selector")}
		}
		token = list.GetContinue()
		if token == "" {
			return out, nil
		}
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
func waitTargetLabel(name, selector string) string {
	if name != "" {
		return "name " + name
	}
	if selector == "" {
		return "all"
	}
	return "selector " + selector
}
func deletionState(obj *unstructured.Unstructured) string {
	if obj.GetDeletionTimestamp() == nil {
		return "not being deleted"
	}
	if fs := obj.GetFinalizers(); len(fs) > 0 {
		return "terminating; waiting on finalizers " + strings.Join(fs[:min(len(fs), 5)], ", ")
	}
	return "terminating"
}
func defaultWaitCondition(gvr schema.GroupVersionResource) string {
	switch gvr.GroupResource() {
	case schema.GroupResource{Group: "apps", Resource: "deployments"}, schema.GroupResource{Group: "apps", Resource: "statefulsets"}, schema.GroupResource{Group: "apps", Resource: "daemonsets"}, schema.GroupResource{Group: "argoproj.io", Resource: "rollouts"}:
		return "rollout"
	case schema.GroupResource{Group: "batch", Resource: "jobs"}:
		return "complete"
	case schema.GroupResource{Resource: "pods"}:
		return "ready"
	}
	return ""
}
func isWaitWorkload(gvr schema.GroupVersionResource) bool { return defaultWaitCondition(gvr) != "" }

// capWaitBlockers keeps at most three blockers, fatal reasons first: a fatal
// reason is what makes the call blocked, so it must survive the cap.
func capWaitBlockers(blockers []waitBlocker) []waitBlocker {
	sort.SliceStable(blockers, func(i, j int) bool {
		return blockers[i].Reason != "Unschedulable" && blockers[j].Reason == "Unschedulable"
	})
	return blockers[:min(len(blockers), 3)]
}

// waitBlockersApply reports whether pod blockers can describe this object now:
// a built-in workload or pod that isn't being replaced or deleted. An Argo
// Rollout also needs its controller to have observed the current template,
// whichever condition the caller waits on; when that can't be read, coverage
// says why instead of the blockers silently going missing.
func waitBlockersApply(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, obj *unstructured.Unstructured, s waitObjectState) (apply bool, coverage string) {
	if !isWaitWorkload(gvr) || obj.GetDeletionTimestamp() != nil || strings.HasPrefix(s.Observed, "replaced by uid ") {
		return false, ""
	}
	if gvr.Group != "argoproj.io" || s.controllerObserved {
		return true, ""
	}
	observed, err := rollouts.ControllerObservedSpec(ctx, dyn, obj)
	if err != nil {
		return false, "the rollout's template source is not readable"
	}
	gen, present := waitGeneration(obj)
	return observed && (!present || gen == obj.GetGeneration()), ""
}
func waitCheckFor(gvr schema.GroupVersionResource, condition string) (waitCheck, error) {
	switch strings.ToLower(condition) {
	case "rollout":
		if defaultWaitCondition(gvr) != "rollout" {
			return nil, fmt.Errorf("condition rollout applies to Deployment, StatefulSet, DaemonSet and Argo Rollout, not %s", gvr.Resource)
		}
		return rolloutStatus, nil
	case "complete":
		if gvr.Group != "batch" || gvr.Resource != "jobs" {
			return nil, fmt.Errorf("condition complete applies to Job, not %s; name a status condition type instead", gvr.Resource)
		}
		return jobComplete, nil
	case "ready":
		if gvr.Group == "" && gvr.Resource == "pods" {
			return podReadyCheck, nil
		}
		return statusConditionCheck("Ready"), nil
	}
	c := condition
	if strings.HasPrefix(strings.ToLower(c), "condition=") {
		c = c[len("condition="):]
	}
	// Complete=True on a Job and Ready=True on a Pod mean the same as complete
	// and ready, which also stop on the terminal states those conditions miss.
	if typ, status, found := strings.Cut(c, "="); !found || strings.EqualFold(status, "True") {
		switch {
		case gvr.Group == "batch" && gvr.Resource == "jobs" && strings.EqualFold(typ, "Complete"):
			return jobComplete, nil
		case gvr.Group == "" && gvr.Resource == "pods" && strings.EqualFold(typ, "Ready"):
			return podReadyCheck, nil
		}
	}
	parts := strings.Split(c, "=")
	if len(parts) > 2 || parts[0] == "" || (len(parts) == 2 && parts[1] == "") || strings.ContainsAny(parts[0], " /{}") {
		return nil, fmt.Errorf("condition must be rollout, complete, ready, deleted, Type[=Status], or jsonpath={.status.field}=value")
	}
	return statusConditionCheck(c), nil
}

func waitStateRank(s waitObjectState) int {
	if s.state == "failed" {
		return 0
	}
	if s.state == "blocked" {
		return 1
	}
	if !s.Met {
		return 2
	}
	return 3
}
func waitAge(t *metav1.Time) *int64 {
	if t == nil || t.IsZero() {
		return nil
	}
	n := max(int64(0), int64(waitNow().Sub(t.Time)/time.Second))
	return &n
}
func waitDiagnoseNext(obj *unstructured.Unstructured) string {
	return fmt.Sprintf("diagnose %s %s in %s", obj.GetKind(), obj.GetName(), obj.GetNamespace())
}
func waitLogsNext(obj *unstructured.Unstructured) string {
	return fmt.Sprintf("get_workload_logs for Job %s in %s", obj.GetName(), obj.GetNamespace())
}

func evaluateWaitObject(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, obj *unstructured.Unstructured, condition string, check waitCheck, jp *waitJSONPath, s waitObjectState) (waitObjectState, error) {
	if obj.GetDeletionTimestamp() != nil {
		s.Observed = "terminating"
		return s, nil
	}
	if strings.EqualFold(condition, "rollout") {
		strategy, _, _ := unstructured.NestedString(obj.Object, "spec", "updateStrategy", "type")
		if gvr.Group == "apps" && (gvr.Resource == "statefulsets" || gvr.Resource == "daemonsets") && strategy == "OnDelete" {
			return s, fmt.Errorf("rollout status needs the RollingUpdate strategy; %s uses OnDelete, so wait on its pods (kind=pod, label_selector=…, condition=ready)", obj.GetKind())
		}
		if gvr.Group == "argoproj.io" {
			observed, err := rollouts.ControllerObservedSpec(ctx, dyn, obj)
			if err != nil {
				return s, err
			}
			gen, present := waitGeneration(obj)
			if !observed || (present && gen != obj.GetGeneration()) {
				s.Observed = "waiting for the rollout controller to observe the new spec"
				return s, nil
			}
			s.controllerObserved = true
			phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
			message, _, _ := unstructured.NestedString(obj.Object, "status", "message")
			s.Observed = phase + ": " + message
			switch phase {
			case "Healthy":
				s.Met = true
			case "Degraded":
				s.state = "blocked"
				s.next = waitDiagnoseNext(obj)
			case "Paused":
				s.Observed = "paused: " + message
				s.next = s.Observed + "; a pause with a duration resumes by itself, one without needs a promote"
			}
			return s, nil
		}
	}
	if jp != nil {
		met, observed, missing, err := jp.evaluate(obj)
		if err != nil {
			return s, err
		}
		s.Met = met
		s.Observed = observed
		if gen, present := waitGeneration(obj); present && gen < obj.GetGeneration() {
			s.Met = false
			s.Observed = "waiting for the controller to observe the new spec"
		}
		if missing {
			s.next = jp.path + " not present; status has [" + strings.Join(waitStatusFields(obj), ", ") + "]"
		}
		return s, nil
	}
	met, message, failure := check(obj)
	s.Met = met
	s.Observed = message
	if failure != "" {
		s.Observed = failure
		s.state = "blocked"
		s.next = waitDiagnoseNext(obj)
		if gvr.Group == "batch" && gvr.Resource == "jobs" {
			s.state = "failed"
			s.next = waitLogsNext(obj)
		}
		if gvr.Group == "" && gvr.Resource == "pods" {
			s.state = "failed"
			s.next = "the pod ran to completion; wait on its controller (rollout) or replacement pods (label_selector)"
		}
	}
	cond := condition
	if strings.HasPrefix(strings.ToLower(cond), "condition=") {
		cond = cond[len("condition="):]
	}
	cond, _, _ = strings.Cut(cond, "=")
	switch strings.ToLower(condition) {
	case "ready":
		cond = "Ready"
	case "complete":
		cond = "Complete"
	}
	if strings.EqualFold(condition, "rollout") {
		if observed, present := waitGeneration(obj); present && observed >= obj.GetGeneration() {
			if c := waitCondition(obj, "Progressing"); c != nil && gvr.Resource == "deployments" {
				timestamp, _ := c["lastUpdateTime"].(string)
				s.InStateSeconds = waitTimestampAge(timestamp)
				if !s.Met && s.state == "" && s.InStateSeconds != nil && *s.InStateSeconds > 120 {
					s.next = fmt.Sprintf("no progress for %ds; %s", *s.InStateSeconds, waitDiagnoseNext(obj))
				}
			}
		}
		if summary := summarizeWorkloadRollout(obj); summary != nil {
			s.Replicas = map[string]int64{"desired": nestedInt64Default(obj, 1, "spec", "replicas"), "updated": nestedInt64Default(obj, 0, "status", "updatedReplicas"), "ready": nestedInt64Default(obj, 0, "status", "readyReplicas"), "available": nestedInt64Default(obj, 0, "status", "availableReplicas")}
			if gvr.Resource == "daemonsets" {
				s.Replicas = map[string]int64{"desired": nestedInt64Default(obj, 0, "status", "desiredNumberScheduled"), "updated": nestedInt64Default(obj, 0, "status", "updatedNumberScheduled"), "ready": nestedInt64Default(obj, 0, "status", "numberReady"), "available": nestedInt64Default(obj, 0, "status", "numberAvailable")}
			}
		}
	} else if c := waitCondition(obj, cond); c != nil {
		timestamp, _ := c["lastTransitionTime"].(string)
		s.InStateSeconds = waitTimestampAge(timestamp)
	} else if !s.Met && failure == "" && !strings.EqualFold(condition, "ready") && !strings.EqualFold(condition, "complete") {
		// Only an explicitly named condition can be the wrong one to wait on.
		names := []string{}
		raw, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
		for _, v := range raw {
			if c, ok := v.(map[string]any); ok {
				if n, ok := c["type"].(string); ok {
					names = append(names, n)
				}
			}
		}
		sort.Strings(names)
		has := "no conditions"
		if len(names) > 0 {
			has = "[" + strings.Join(names, ", ") + "]"
		}
		s.next = fmt.Sprintf("no %s condition on %s; it has %s; for state outside conditions use jsonpath={.status.<field>}=value (status has [%s])", cond, obj.GetName(), has, strings.Join(waitStatusFields(obj), ", "))
	}
	return s, nil
}
func waitTimestampAge(s string) *int64 {
	t, e := time.Parse(time.RFC3339, s)
	if e != nil {
		return nil
	}
	v := metav1.NewTime(t)
	return waitAge(&v)
}
func waitStatusFields(obj *unstructured.Unstructured) []string {
	status, _, _ := unstructured.NestedMap(obj.Object, "status")
	keys := make([]string, 0, len(status))
	for k := range status {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
func waitGeneration(obj *unstructured.Unstructured) (int64, bool) {
	v, ok, _ := unstructured.NestedFieldNoCopy(obj.Object, "status", "observedGeneration")
	if !ok {
		return 0, false
	}
	switch g := v.(type) {
	case int64:
		return g, true
	case string:
		n, e := strconv.ParseInt(g, 10, 64)
		return n, e == nil
	}
	return 0, false
}

// rolloutStatus mirrors kubectl rollout status for the built-in workloads
// (k8s.io/kubectl/pkg/polymorphichelpers/rollout_status.go): the controller
// has observed the spec, every replica is updated, old replicas are gone and
// the updated ones are available. An exceeded progress deadline is a stall.
func rolloutStatus(obj *unstructured.Unstructured) (bool, string, string) {
	switch obj.GetKind() {
	case "Deployment":
		d := &appsv1.Deployment{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, d); err != nil {
			return false, "", err.Error()
		}
		if d.Generation > d.Status.ObservedGeneration {
			return false, "waiting for the deployment controller to observe the new spec", ""
		}
		for _, c := range d.Status.Conditions {
			if c.Type == appsv1.DeploymentProgressing && c.Status == "False" && c.Reason == "ProgressDeadlineExceeded" {
				return false, "", "progress deadline exceeded: " + c.Message
			}
		}
		desired := int32(1)
		if d.Spec.Replicas != nil {
			desired = *d.Spec.Replicas
		}
		s := d.Status
		switch {
		case s.UpdatedReplicas < desired:
			return false, fmt.Sprintf("%d of %d new replicas updated", s.UpdatedReplicas, desired), ""
		case s.Replicas > s.UpdatedReplicas:
			return false, fmt.Sprintf("%d old replica(s) pending termination", s.Replicas-s.UpdatedReplicas), ""
		case s.AvailableReplicas < s.UpdatedReplicas:
			return false, fmt.Sprintf("%d of %d updated replicas available", s.AvailableReplicas, s.UpdatedReplicas), ""
		}
		return true, fmt.Sprintf("rolled out: %d/%d available", s.AvailableReplicas, desired), ""
	case "StatefulSet":
		sts := &appsv1.StatefulSet{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, sts); err != nil {
			return false, "", err.Error()
		}
		if sts.Spec.UpdateStrategy.Type == appsv1.OnDeleteStatefulSetStrategyType {
			return false, "", "rollout status needs the RollingUpdate strategy; this StatefulSet uses OnDelete, so wait on its pods instead"
		}
		if sts.Status.ObservedGeneration == 0 || sts.Generation > sts.Status.ObservedGeneration {
			return false, "waiting for the statefulset controller to observe the new spec", ""
		}
		desired := int32(1)
		if sts.Spec.Replicas != nil {
			desired = *sts.Spec.Replicas
		}
		if sts.Status.ReadyReplicas < desired {
			return false, fmt.Sprintf("%d of %d pods ready", sts.Status.ReadyReplicas, desired), ""
		}
		if ru := sts.Spec.UpdateStrategy.RollingUpdate; ru != nil && ru.Partition != nil {
			if target := desired - *ru.Partition; sts.Status.UpdatedReplicas < target {
				return false, fmt.Sprintf("%d of %d partitioned pods updated", sts.Status.UpdatedReplicas, target), ""
			}
			return true, fmt.Sprintf("partitioned rollout done: %d pods updated", sts.Status.UpdatedReplicas), ""
		}
		if sts.Status.UpdateRevision != sts.Status.CurrentRevision {
			return false, fmt.Sprintf("%d pods at revision %s", sts.Status.UpdatedReplicas, sts.Status.UpdateRevision), ""
		}
		return true, fmt.Sprintf("rolled out: %d/%d ready", sts.Status.ReadyReplicas, desired), ""
	case "DaemonSet":
		ds := &appsv1.DaemonSet{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, ds); err != nil {
			return false, "", err.Error()
		}
		if ds.Spec.UpdateStrategy.Type == appsv1.OnDeleteDaemonSetStrategyType {
			return false, "", "rollout status needs the RollingUpdate strategy; this DaemonSet uses OnDelete, so wait on its pods instead"
		}
		if ds.Generation > ds.Status.ObservedGeneration {
			return false, "waiting for the daemonset controller to observe the new spec", ""
		}
		s := ds.Status
		if s.UpdatedNumberScheduled < s.DesiredNumberScheduled {
			return false, fmt.Sprintf("%d of %d new pods updated", s.UpdatedNumberScheduled, s.DesiredNumberScheduled), ""
		}
		if s.NumberAvailable < s.DesiredNumberScheduled {
			return false, fmt.Sprintf("%d of %d updated pods available", s.NumberAvailable, s.DesiredNumberScheduled), ""
		}
		return true, fmt.Sprintf("rolled out: %d/%d available", s.NumberAvailable, s.DesiredNumberScheduled), ""
	}
	return false, "", "unsupported rollout kind"
}

func jobComplete(obj *unstructured.Unstructured) (bool, string, string) {
	if status, message := conditionStatus(obj, "Failed"); status == "True" && waitConditionFresh(obj, "Failed") {
		return false, "", "job failed: " + message
	}
	if met, message, _ := statusConditionCheck("Complete")(obj); met {
		return true, message, ""
	}
	return statusConditionCheck("Complete")(obj)
}
func podReadyCheck(obj *unstructured.Unstructured) (bool, string, string) {
	phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
	if phase == "Succeeded" || phase == "Failed" {
		return false, "", "pod is " + phase + " and will not become Ready"
	}
	return statusConditionCheck("Ready")(obj)
}
func statusConditionCheck(condition string) waitCheck {
	typ, want, ok := strings.Cut(condition, "=")
	if !ok {
		want = "True"
	}
	return func(obj *unstructured.Unstructured) (bool, string, string) {
		if obj.GetDeletionTimestamp() != nil {
			return false, "terminating", ""
		}
		status, message := conditionStatus(obj, typ)
		if !waitConditionFresh(obj, typ) {
			g, _ := conditionObservedGeneration(obj, typ)
			return false, fmt.Sprintf("%s=%s was reported for generation %d; waiting for the controller to observe generation %d", typ, status, g, obj.GetGeneration()), ""
		}
		if status == "" {
			return false, "no " + typ + " condition yet", ""
		}
		if strings.EqualFold(status, want) {
			observed := typ + "=" + status
			if message != "" {
				observed += ": " + message
			}
			return true, observed, ""
		}
		return false, typ + "=" + status + ": " + message, ""
	}
}
func waitConditionFresh(obj *unstructured.Unstructured, typ string) bool {
	g, present := conditionObservedGeneration(obj, typ)
	return !present || g >= obj.GetGeneration()
}
func conditionObservedGeneration(obj *unstructured.Unstructured, typ string) (int64, bool) {
	if c := waitCondition(obj, typ); c != nil {
		if g, ok := c["observedGeneration"].(int64); ok {
			return g, true
		}
	}
	return waitGeneration(obj)
}
func waitCondition(obj *unstructured.Unstructured, typ string) map[string]any {
	rawConditions, _, _ := unstructured.NestedFieldNoCopy(obj.Object, "status", "conditions")
	conditions, _ := rawConditions.([]any)
	for _, raw := range conditions {
		if c, ok := raw.(map[string]any); ok {
			if t, _ := c["type"].(string); strings.EqualFold(t, typ) {
				return c
			}
		}
	}
	return nil
}
func conditionStatus(obj *unstructured.Unstructured, typ string) (string, string) {
	c := waitCondition(obj, typ)
	if c == nil {
		return "", ""
	}
	s, _ := c["status"].(string)
	r, _ := c["reason"].(string)
	m, _ := c["message"].(string)
	return s, strings.Trim(strings.Join([]string{r, m}, ": "), ": ")
}
