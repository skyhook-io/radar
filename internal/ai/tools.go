package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	aicontext "github.com/skyhook-io/radar/internal/ai/context"
	"github.com/skyhook-io/radar/internal/k8s"
)

// GetToolDefinitions returns the set of read-only tools available to the AI.
func GetToolDefinitions() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        "list_resources",
			Description: "List Kubernetes resources of a given kind. Returns minified summaries. Supports pods, deployments, services, daemonsets, statefulsets, replicasets, jobs, cronjobs, configmaps, secrets, ingresses, nodes, namespaces, events, persistentvolumeclaims, horizontalpodautoscalers, and CRDs.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"kind": map[string]any{
						"type":        "string",
						"description": "Resource kind to list, e.g. pods, deployments, services",
					},
					"namespace": map[string]any{
						"type":        "string",
						"description": "Filter to a specific namespace (optional, empty = all namespaces)",
					},
				},
				"required": []string{"kind"},
			},
		},
		{
			Name:        "get_resource",
			Description: "Get detailed information about a single Kubernetes resource including spec, status, and metadata.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"kind": map[string]any{
						"type":        "string",
						"description": "Resource kind, e.g. pod, deployment, service",
					},
					"namespace": map[string]any{
						"type":        "string",
						"description": "Resource namespace",
					},
					"name": map[string]any{
						"type":        "string",
						"description": "Resource name",
					},
				},
				"required": []string{"kind", "namespace", "name"},
			},
		},
		{
			Name:        "get_pod_logs",
			Description: "Get log lines from a pod, prioritizing errors and warnings. Returns diagnostically relevant lines or the last lines if no error patterns match.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"namespace": map[string]any{
						"type":        "string",
						"description": "Pod namespace",
					},
					"name": map[string]any{
						"type":        "string",
						"description": "Pod name",
					},
					"container": map[string]any{
						"type":        "string",
						"description": "Container name (optional, defaults to first container)",
					},
					"tail_lines": map[string]any{
						"type":        "integer",
						"description": "Number of lines from the end (default 100)",
					},
				},
				"required": []string{"namespace", "name"},
			},
		},
		{
			Name:        "get_events",
			Description: "Get recent Kubernetes events, deduplicated and sorted by recency. Useful for diagnosing issues. Can filter by namespace.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"namespace": map[string]any{
						"type":        "string",
						"description": "Filter to a specific namespace (optional)",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum number of events to return (default 20)",
					},
				},
			},
		},
		{
			Name:        "get_metrics",
			Description: "Get current CPU and memory metrics for pods or nodes. Returns latest metrics snapshot.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"type": map[string]any{
						"type":        "string",
						"enum":        []string{"pods", "nodes"},
						"description": "Type of metrics to get: pods or nodes",
					},
					"namespace": map[string]any{
						"type":        "string",
						"description": "Filter pod metrics to a namespace (optional, only for pods)",
					},
					"name": map[string]any{
						"type":        "string",
						"description": "Get metrics for a specific pod or node by name (optional)",
					},
				},
				"required": []string{"type"},
			},
		},
		{
			Name:        "list_namespaces",
			Description: "List all Kubernetes namespaces with their status.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{},
			},
		},
	}
}

// ExecuteTool executes a tool call and returns the JSON result.
func ExecuteTool(ctx context.Context, call ToolCall) ToolResult {
	log.Printf("[ai-tools] Executing tool: %s args=%s", call.Name, call.Arguments)
	start := time.Now()

	result, isErr := executeTool(ctx, call.Name, call.Arguments)

	dur := time.Since(start).Round(time.Millisecond)
	if isErr {
		log.Printf("[ai-tools] %s ERROR (%s): %s", call.Name, dur, result)
	} else {
		// Truncate result for logging
		logResult := result
		if len(logResult) > 200 {
			logResult = logResult[:200] + "..."
		}
		log.Printf("[ai-tools] %s OK (%s) %d bytes", call.Name, dur, len(result))
		_ = logResult
	}

	return ToolResult{
		ToolCallID: call.ID,
		Content:    result,
		IsError:    isErr,
	}
}

func executeTool(ctx context.Context, name, argsJSON string) (string, bool) {
	switch name {
	case "list_resources":
		return executeListResources(ctx, argsJSON)
	case "get_resource":
		return executeGetResource(ctx, argsJSON)
	case "get_pod_logs":
		return executeGetPodLogs(ctx, argsJSON)
	case "get_events":
		return executeGetEvents(ctx, argsJSON)
	case "get_metrics":
		return executeGetMetrics(ctx, argsJSON)
	case "list_namespaces":
		return executeListNamespaces(ctx)
	default:
		return fmt.Sprintf("unknown tool: %s", name), true
	}
}

func executeListResources(ctx context.Context, argsJSON string) (string, bool) {
	var args struct {
		Kind      string `json:"kind"`
		Namespace string `json:"namespace"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return fmt.Sprintf("invalid arguments: %v", err), true
	}

	cache := k8s.GetResourceCache()
	if cache == nil {
		return "not connected to cluster", true
	}

	kind := strings.ToLower(args.Kind)
	var namespaces []string
	if args.Namespace != "" {
		namespaces = []string{args.Namespace}
	}

	objs, err := k8s.FetchResourceList(cache, kind, namespaces)
	if err == k8s.ErrUnknownKind {
		// Try dynamic cache for CRDs
		return executeListDynamicResources(ctx, cache, kind, namespaces)
	}
	if err != nil {
		return fmt.Sprintf("failed to list %s: %v", kind, err), true
	}

	results, err := aicontext.MinifyList(objs, aicontext.LevelSummary)
	if err != nil {
		return fmt.Sprintf("failed to minify: %v", err), true
	}

	return marshalResult(results)
}

func executeListDynamicResources(ctx context.Context, cache *k8s.ResourceCache, kind string, namespaces []string) (string, bool) {
	var allItems []any
	if len(namespaces) > 0 {
		for _, ns := range namespaces {
			items, err := cache.ListDynamicWithGroup(ctx, kind, ns, "")
			if err != nil {
				return fmt.Sprintf("failed to list %s: %v", kind, err), true
			}
			for _, item := range items {
				allItems = append(allItems, aicontext.MinifyUnstructured(item, aicontext.LevelSummary))
			}
		}
	} else {
		items, err := cache.ListDynamicWithGroup(ctx, kind, "", "")
		if err != nil {
			return fmt.Sprintf("failed to list %s: %v", kind, err), true
		}
		for _, item := range items {
			allItems = append(allItems, aicontext.MinifyUnstructured(item, aicontext.LevelSummary))
		}
	}

	return marshalResult(allItems)
}

func executeGetResource(ctx context.Context, argsJSON string) (string, bool) {
	var args struct {
		Kind      string `json:"kind"`
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return fmt.Sprintf("invalid arguments: %v", err), true
	}

	cache := k8s.GetResourceCache()
	if cache == nil {
		return "not connected to cluster", true
	}

	kind := strings.ToLower(args.Kind)

	obj, err := k8s.FetchResource(cache, kind, args.Namespace, args.Name)
	if err == k8s.ErrUnknownKind {
		u, dynErr := cache.GetDynamicWithGroup(ctx, kind, args.Namespace, args.Name, "")
		if dynErr != nil {
			return fmt.Sprintf("resource not found: %v", dynErr), true
		}
		return marshalResult(aicontext.MinifyUnstructured(u, aicontext.LevelDetail))
	}
	if err != nil {
		return fmt.Sprintf("resource not found: %v", err), true
	}

	k8s.SetTypeMeta(obj)
	result, err := aicontext.Minify(obj, aicontext.LevelDetail)
	if err != nil {
		return fmt.Sprintf("failed to minify: %v", err), true
	}

	return marshalResult(result)
}

func executeGetPodLogs(ctx context.Context, argsJSON string) (string, bool) {
	var args struct {
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
		Container string `json:"container"`
		TailLines int    `json:"tail_lines"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return fmt.Sprintf("invalid arguments: %v", err), true
	}

	clientset := k8s.GetClient()
	if clientset == nil {
		return "not connected to cluster", true
	}

	tailLines := int64(100)
	if args.TailLines > 0 {
		tailLines = int64(args.TailLines)
	}

	opts := &corev1.PodLogOptions{
		TailLines: &tailLines,
	}
	if args.Container != "" {
		opts.Container = args.Container
	}

	stream, err := clientset.CoreV1().Pods(args.Namespace).GetLogs(args.Name, opts).Stream(ctx)
	if err != nil {
		return fmt.Sprintf("failed to get logs for %s/%s: %v", args.Namespace, args.Name, err), true
	}
	defer stream.Close()

	data, err := io.ReadAll(stream)
	if err != nil {
		return fmt.Sprintf("failed to read logs: %v", err), true
	}

	filtered := aicontext.FilterLogs(string(data))
	return marshalResult(filtered)
}

func executeGetEvents(ctx context.Context, argsJSON string) (string, bool) {
	var args struct {
		Namespace string `json:"namespace"`
		Limit     int    `json:"limit"`
	}
	if argsJSON != "" && argsJSON != "{}" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return fmt.Sprintf("invalid arguments: %v", err), true
		}
	}

	cache := k8s.GetResourceCache()
	if cache == nil {
		return "not connected to cluster", true
	}

	eventLister := cache.Events()
	if eventLister == nil {
		return "insufficient permissions to list events", true
	}

	var events []*corev1.Event
	var err error
	if args.Namespace != "" {
		events, err = eventLister.Events(args.Namespace).List(labels.Everything())
	} else {
		events, err = eventLister.List(labels.Everything())
	}
	if err != nil {
		return fmt.Sprintf("failed to list events: %v", err), true
	}

	eventValues := make([]corev1.Event, len(events))
	for i, e := range events {
		eventValues[i] = *e
	}

	deduplicated := aicontext.DeduplicateEvents(eventValues)

	limit := 20
	if args.Limit > 0 && args.Limit < limit {
		limit = args.Limit
	}
	if len(deduplicated) > limit {
		deduplicated = deduplicated[:limit]
	}

	return marshalResult(deduplicated)
}

func executeGetMetrics(ctx context.Context, argsJSON string) (string, bool) {
	var args struct {
		Type      string `json:"type"`
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return fmt.Sprintf("invalid arguments: %v", err), true
	}

	switch args.Type {
	case "pods":
		if args.Name != "" && args.Namespace != "" {
			metrics, err := k8s.GetPodMetrics(ctx, args.Namespace, args.Name)
			if err != nil {
				return fmt.Sprintf("failed to get pod metrics: %v", err), true
			}
			return marshalResult(metrics)
		}
		store := k8s.GetMetricsHistory()
		if store == nil {
			return "metrics not available (metrics-server may not be installed)", true
		}
		allMetrics := store.GetAllPodMetricsLatest()
		if args.Namespace != "" {
			var filtered []k8s.TopPodMetrics
			for _, m := range allMetrics {
				if m.Namespace == args.Namespace {
					filtered = append(filtered, m)
				}
			}
			allMetrics = filtered
		}
		return marshalResult(allMetrics)

	case "nodes":
		if args.Name != "" {
			metrics, err := k8s.GetNodeMetrics(ctx, args.Name)
			if err != nil {
				return fmt.Sprintf("failed to get node metrics: %v", err), true
			}
			return marshalResult(metrics)
		}
		store := k8s.GetMetricsHistory()
		if store == nil {
			return "metrics not available (metrics-server may not be installed)", true
		}
		return marshalResult(store.GetAllNodeMetricsLatest())

	default:
		return fmt.Sprintf("invalid metrics type: %s (use 'pods' or 'nodes')", args.Type), true
	}
}

func executeListNamespaces(ctx context.Context) (string, bool) {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return "not connected to cluster", true
	}

	lister := cache.Namespaces()
	if lister == nil {
		return "insufficient permissions to list namespaces", true
	}

	namespaces, err := lister.List(labels.Everything())
	if err != nil {
		return fmt.Sprintf("failed to list namespaces: %v", err), true
	}

	result := make([]map[string]string, 0, len(namespaces))
	for _, ns := range namespaces {
		result = append(result, map[string]string{
			"name":   ns.Name,
			"status": string(ns.Status.Phase),
		})
	}

	return marshalResult(result)
}

func marshalResult(data any) (string, bool) {
	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Sprintf("failed to marshal result: %v", err), true
	}
	// Limit tool result size to avoid overwhelming the LLM context
	s := string(b)
	if len(s) > 50000 {
		s = s[:50000] + "...(truncated)"
	}
	return s, false
}
