package opencost

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

const kubecostCurrentWindow = "1h"

type KubecostCurrentOptions struct {
	Currency  string
	ClusterID string
	Owners    PodOwnerLookup
}

type kubecostWorkloadAccumulator struct {
	row             WorkloadCost
	cpuCost         float64
	memoryCost      float64
	cpuUsageCost    float64
	memoryUsageCost float64
	durationHours   float64
	start           time.Time
	end             time.Time
	pods            map[string]struct{}
}

func ComputeKubecostSummary(ctx context.Context, client *KubecostClient, opts KubecostCurrentOptions) (*CostSummary, error) {
	if client == nil {
		return nil, fmt.Errorf("kubecost client is not configured")
	}
	if opts.Currency == "" {
		opts.Currency = DefaultCurrency
	}
	resp, window, err := kubecostAllocationWithFallback(ctx, client, KubecostAllocationOptions{
		Aggregate:  "cluster,namespace",
		Accumulate: "true",
		Idle:       true,
		ShareIdle:  false,
		Filter:     kubecostFilter(opts.ClusterID, ""),
	})
	if err != nil {
		return nil, err
	}
	if !hasKubecostAllocationData(resp) {
		return &CostSummary{Available: false, Reason: ReasonNoMetrics, Currency: opts.Currency, Source: "kubecost"}, nil
	}

	out := &CostSummary{Available: true, Currency: opts.Currency, Window: window, Source: "kubecost"}
	var totalAlloc, totalUsage float64
	for key, allocation := range kubecostAllocationRows(resp) {
		if allocation == nil {
			continue
		}
		if kubecostIdleRow(key, allocation) {
			hours, err := kubecostRowHours(allocation.Start, allocation.End, allocation.Minutes)
			if err != nil {
				return nil, fmt.Errorf("allocation %q: %w", key, err)
			}
			out.TotalIdleCost += maxZero((allocation.CPUCost + allocation.RAMCost) / hours)
			out.DataThrough = LatestKubecostTimestamp(out.DataThrough, allocation.End)
			continue
		}
		if err := requireKubecostCluster(allocation.Properties, opts.ClusterID); err != nil {
			return nil, err
		}
		namespace := propertyString(allocation.Properties, "namespace")
		if namespace == "" {
			return nil, fmt.Errorf("allocation %q is missing namespace identity", key)
		}
		if namespace == "__unallocated__" {
			continue
		}
		hours, err := kubecostRowHours(allocation.Start, allocation.End, allocation.Minutes)
		if err != nil {
			return nil, fmt.Errorf("allocation %q: %w", key, err)
		}
		cpu := allocation.CPUCost / hours
		ram := allocation.RAMCost / hours
		cpuUsage, cpuAvailable := kubecostUsageCost(cpu, allocation.CPUCoreRequestAverage, allocation.CPUCoreUsageAverage)
		ramUsage, ramAvailable := kubecostUsageCost(ram, allocation.RAMByteRequestAverage, allocation.RAMByteUsageAverage)
		row := NamespaceCost{
			Name:            namespace,
			Kind:            "namespace",
			HourlyCost:      allocation.TotalCost / hours,
			CPUCost:         cpu,
			MemoryCost:      ram,
			StorageCost:     allocation.PVCost / hours,
			NetworkCost:     (allocation.NetworkCost + allocation.LoadBalancerCost) / hours,
			CPUUsageCost:    cpuUsage,
			MemoryUsageCost: ramUsage,
		}
		if row.HourlyCost == 0 {
			row.HourlyCost = (allocation.CPUCost + allocation.RAMCost + allocation.PVCost + allocation.NetworkCost + allocation.LoadBalancerCost + allocation.SharedCost + allocation.ExternalCost) / hours
		}
		if cpuAvailable && ramAvailable {
			usage := cpuUsage + ramUsage
			allocated := cpu + ram
			row.Efficiency = efficiencyPct(usage, allocated)
			row.IdleCost = idleFromUsage(usage, allocated)
			totalAlloc += allocated
			totalUsage += usage
		}
		roundNamespaceCost(&row)
		out.TotalHourlyCost += row.HourlyCost
		out.TotalStorageCost += row.StorageCost
		out.TotalNetworkCost += row.NetworkCost
		out.TotalIdleCost += row.IdleCost
		out.Namespaces = append(out.Namespaces, row)
		out.DataThrough = LatestKubecostTimestamp(out.DataThrough, allocation.End)
	}
	if len(out.Namespaces) == 0 {
		return &CostSummary{Available: false, Reason: ReasonNoMetrics, Currency: opts.Currency, Source: "kubecost", DataThrough: out.DataThrough}, nil
	}
	sort.Slice(out.Namespaces, func(i, j int) bool { return out.Namespaces[i].HourlyCost > out.Namespaces[j].HourlyCost })
	out.TotalHourlyCost = roundTo(out.TotalHourlyCost, 4)
	out.TotalStorageCost = roundTo(out.TotalStorageCost, 4)
	out.TotalNetworkCost = roundTo(out.TotalNetworkCost, 4)
	out.TotalIdleCost = roundTo(out.TotalIdleCost, 4)
	out.ClusterEfficiency = efficiencyPct(totalUsage, totalAlloc)
	return out, nil
}

func ComputeKubecostWorkloads(ctx context.Context, client *KubecostClient, namespace string, opts KubecostCurrentOptions) (*WorkloadCostResponse, error) {
	if namespace == "" {
		return nil, fmt.Errorf("namespace is required")
	}
	responses, err := computeKubecostWorkloads(ctx, client, map[string]PodOwnerLookup{namespace: opts.Owners}, namespace, opts)
	if err != nil {
		return nil, err
	}
	return responses[namespace], nil
}

func ComputeKubecostWorkloadsForNamespaces(ctx context.Context, client *KubecostClient, ownersByNamespace map[string]PodOwnerLookup, opts KubecostCurrentOptions) (map[string]*WorkloadCostResponse, error) {
	if len(ownersByNamespace) == 0 {
		return nil, fmt.Errorf("at least one namespace is required")
	}
	namespaces := make([]string, 0, len(ownersByNamespace))
	for namespace := range ownersByNamespace {
		namespaces = append(namespaces, namespace)
	}
	sort.Strings(namespaces)
	responses := make(map[string]*WorkloadCostResponse, len(namespaces))
	for _, namespace := range namespaces {
		response, err := computeKubecostWorkloads(ctx, client, map[string]PodOwnerLookup{namespace: ownersByNamespace[namespace]}, namespace, opts)
		if err != nil {
			return nil, err
		}
		responses[namespace] = response[namespace]
	}
	return responses, nil
}

func computeKubecostWorkloads(ctx context.Context, client *KubecostClient, ownersByNamespace map[string]PodOwnerLookup, queryNamespace string, opts KubecostCurrentOptions) (map[string]*WorkloadCostResponse, error) {
	if client == nil {
		return nil, fmt.Errorf("kubecost client is not configured")
	}
	if opts.Currency == "" {
		opts.Currency = DefaultCurrency
	}
	resp, _, err := kubecostAllocationWithFallback(ctx, client, KubecostAllocationOptions{
		Aggregate:  "cluster,namespace,pod,controllerKind,controller",
		Accumulate: "true",
		Idle:       false,
		ShareIdle:  false,
		Filter:     kubecostFilter(opts.ClusterID, queryNamespace),
	})
	if err != nil {
		return nil, err
	}
	responses := make(map[string]*WorkloadCostResponse, len(ownersByNamespace))
	for namespace := range ownersByNamespace {
		responses[namespace] = &WorkloadCostResponse{Namespace: namespace, Currency: opts.Currency, Source: "kubecost"}
	}
	if !hasKubecostAllocationData(resp) {
		for _, response := range responses {
			response.Reason = ReasonNoMetrics
		}
		return responses, nil
	}

	workloadsByNamespace := make(map[string]map[string]*kubecostWorkloadAccumulator, len(ownersByNamespace))
	for key, allocation := range kubecostAllocationRows(resp) {
		if allocation == nil || kubecostIdleRow(key, allocation) {
			continue
		}
		if err := requireKubecostCluster(allocation.Properties, opts.ClusterID); err != nil {
			return nil, err
		}
		rowNamespace := propertyString(allocation.Properties, "namespace")
		if rowNamespace == "" {
			return nil, fmt.Errorf("allocation %q is missing namespace identity", key)
		}
		owners, wanted := ownersByNamespace[rowNamespace]
		if !wanted {
			if queryNamespace != "" {
				return nil, fmt.Errorf("allocation %q has namespace %q, expected %q", key, rowNamespace, queryNamespace)
			}
			continue
		}
		kind, name, skip, err := kubecostWorkloadIdentity(key, allocation, owners)
		if err != nil {
			return nil, err
		}
		if skip {
			continue
		}
		hours, err := kubecostRowHours(allocation.Start, allocation.End, allocation.Minutes)
		if err != nil {
			return nil, fmt.Errorf("allocation %q: %w", key, err)
		}
		cpuUsage, cpuAvailable := kubecostUsageCost(allocation.CPUCost, allocation.CPUCoreRequestAverage, allocation.CPUCoreUsageAverage)
		ramUsage, ramAvailable := kubecostUsageCost(allocation.RAMCost, allocation.RAMByteRequestAverage, allocation.RAMByteUsageAverage)
		identity := kind + "/" + name
		workloads := workloadsByNamespace[rowNamespace]
		if workloads == nil {
			workloads = map[string]*kubecostWorkloadAccumulator{}
			workloadsByNamespace[rowNamespace] = workloads
		}
		row := workloads[identity]
		if row == nil {
			row = &kubecostWorkloadAccumulator{
				row:  WorkloadCost{Name: name, Kind: kind, CPUUsageAvailable: true, MemoryUsageAvailable: true},
				pods: map[string]struct{}{},
			}
			workloads[identity] = row
		}
		row.cpuCost += allocation.CPUCost
		row.memoryCost += allocation.RAMCost
		row.cpuUsageCost += cpuUsage
		row.memoryUsageCost += ramUsage
		row.row.CPUUsageAvailable = row.row.CPUUsageAvailable && cpuAvailable
		row.row.MemoryUsageAvailable = row.row.MemoryUsageAvailable && ramAvailable
		row.includeDuration(allocation.Start, allocation.End, hours)
		if pod := propertyString(allocation.Properties, "pod"); pod != "" {
			row.pods[pod] = struct{}{}
		}
		response := responses[rowNamespace]
		response.DataThrough = LatestKubecostTimestamp(response.DataThrough, allocation.End)
	}
	for namespace, response := range responses {
		for _, workload := range workloadsByNamespace[namespace] {
			hours := workload.durationHours
			if hours <= 0 {
				return nil, fmt.Errorf("workload %s/%s is missing valid allocation duration", workload.row.Kind, workload.row.Name)
			}
			row := &workload.row
			row.CPUCost = workload.cpuCost / hours
			row.MemoryCost = workload.memoryCost / hours
			row.HourlyCost = row.CPUCost + row.MemoryCost
			row.CPUUsageCost = workload.cpuUsageCost / hours
			row.MemoryUsageCost = workload.memoryUsageCost / hours
			row.Replicas = len(workload.pods)
			if row.CPUUsageAvailable {
				row.CPUAllocationUse = efficiencyPct(row.CPUUsageCost, row.CPUCost)
			}
			if row.MemoryUsageAvailable {
				row.MemoryAllocationUse = efficiencyPct(row.MemoryUsageCost, row.MemoryCost)
			}
			if row.CPUUsageAvailable && row.MemoryUsageAvailable {
				row.Efficiency = efficiencyPct(row.CPUUsageCost+row.MemoryUsageCost, row.CPUCost+row.MemoryCost)
				row.IdleCost = idleFromUsage(row.CPUUsageCost+row.MemoryUsageCost, row.CPUCost+row.MemoryCost)
			}
			roundWorkloadCost(row)
			response.Workloads = append(response.Workloads, *row)
		}
		response.Available = len(response.Workloads) > 0
		if !response.Available {
			response.Reason = ReasonNoMetrics
		}
		sort.Slice(response.Workloads, func(i, j int) bool { return response.Workloads[i].HourlyCost > response.Workloads[j].HourlyCost })
	}
	return responses, nil
}

func ComputeKubecostNodes(ctx context.Context, client *KubecostClient, opts KubecostCurrentOptions) (*NodeCostResponse, error) {
	if client == nil {
		return nil, fmt.Errorf("kubecost client is not configured")
	}
	if opts.Currency == "" {
		opts.Currency = DefaultCurrency
	}
	filter := `assetType:"node"`
	if clusterFilter := kubecostFilter(opts.ClusterID, ""); clusterFilter != "" {
		filter = clusterFilter + "+" + filter
	}
	resp, err := kubecostAssetsWithFallback(ctx, client, KubecostAssetOptions{Accumulate: "true", Filter: filter})
	if err != nil {
		return nil, err
	}
	if !hasKubecostAssetData(resp) {
		return &NodeCostResponse{Available: false, Reason: ReasonNoMetrics, Currency: opts.Currency, Source: "kubecost"}, nil
	}
	out := &NodeCostResponse{Available: true, Currency: opts.Currency, Source: "kubecost"}
	for key, asset := range kubecostAssetRows(resp) {
		if asset == nil || !strings.EqualFold(asset.Type, "Node") {
			continue
		}
		if err := requireKubecostCluster(asset.Properties, opts.ClusterID); err != nil {
			return nil, err
		}
		name := propertyString(asset.Properties, "name")
		if name == "" {
			return nil, fmt.Errorf("node asset %q is missing name identity", key)
		}
		hours, err := kubecostRowHours(asset.Start, asset.End, asset.Minutes)
		if err != nil {
			return nil, fmt.Errorf("node asset %q: %w", key, err)
		}
		total := asset.TotalCost
		if total == 0 {
			total = asset.CPUCost + asset.RAMCost + asset.GPUCost
		}
		out.Nodes = append(out.Nodes, NodeCost{
			Name:         name,
			ProviderID:   propertyString(asset.Properties, "providerID"),
			InstanceType: firstNonEmpty(asset.NodeType, asset.Labels["node.kubernetes.io/instance-type"], asset.Labels["node_kubernetes_io_instance_type"], asset.Labels["beta_kubernetes_io_instance_type"]),
			Region:       firstNonEmpty(propertyString(asset.Properties, "region"), asset.Labels["topology.kubernetes.io/region"], asset.Labels["topology_kubernetes_io_region"]),
			HourlyCost:   roundTo(total/hours, 4),
			CPUCost:      roundTo(asset.CPUCost/hours, 4),
			MemoryCost:   roundTo(asset.RAMCost/hours, 4),
		})
		out.DataThrough = LatestKubecostTimestamp(out.DataThrough, asset.End)
	}
	if len(out.Nodes) == 0 {
		out.Available = false
		out.Reason = ReasonNoMetrics
	}
	sort.Slice(out.Nodes, func(i, j int) bool { return out.Nodes[i].HourlyCost > out.Nodes[j].HourlyCost })
	return out, nil
}

func kubecostAllocationWithFallback(ctx context.Context, client *KubecostClient, opts KubecostAllocationOptions) (*KubecostAllocationResponse, string, error) {
	var lastErr error
	sawEmpty := false
	for _, window := range []string{kubecostCurrentWindow, "1d"} {
		opts.Window = window
		resp, err := client.GetAllocation(ctx, opts)
		if err != nil {
			if ctx.Err() != nil {
				return nil, window, ctx.Err()
			}
			lastErr = err
			continue
		}
		if hasKubecostAllocationData(resp) {
			return resp, window, nil
		}
		sawEmpty = true
	}
	if sawEmpty {
		return nil, "1d", nil
	}
	if lastErr != nil {
		return nil, "1d", lastErr
	}
	return nil, "1d", nil
}

func kubecostAssetsWithFallback(ctx context.Context, client *KubecostClient, opts KubecostAssetOptions) (*KubecostAssetsResponse, error) {
	var lastErr error
	sawEmpty := false
	for _, window := range []string{kubecostCurrentWindow, "1d"} {
		opts.Window = window
		resp, err := client.GetAssets(ctx, opts)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = err
			continue
		}
		if hasKubecostAssetData(resp) {
			return resp, nil
		}
		sawEmpty = true
	}
	if sawEmpty {
		return nil, nil
	}
	return nil, lastErr
}

func kubecostAllocationRows(resp *KubecostAllocationResponse) map[string]*KubecostAllocation {
	out := map[string]*KubecostAllocation{}
	if resp == nil {
		return out
	}
	for _, bucket := range resp.Data {
		for key, allocation := range bucket {
			if allocation != nil {
				out[key] = allocation
			}
		}
	}
	return out
}

func kubecostAssetRows(resp *KubecostAssetsResponse) map[string]*KubecostAsset {
	out := map[string]*KubecostAsset{}
	if resp == nil {
		return out
	}
	for _, bucket := range resp.Data {
		for key, asset := range bucket {
			if asset != nil {
				out[key] = asset
			}
		}
	}
	return out
}

func hasKubecostAllocationData(resp *KubecostAllocationResponse) bool {
	return len(kubecostAllocationRows(resp)) > 0
}

func hasKubecostAssetData(resp *KubecostAssetsResponse) bool {
	return len(kubecostAssetRows(resp)) > 0
}

func kubecostFilter(clusterID, namespace string) string {
	parts := make([]string, 0, 2)
	if clusterID != "" {
		parts = append(parts, `cluster:"`+escapeKubecostFilterValue(clusterID)+`"`)
	}
	if namespace != "" {
		parts = append(parts, `namespace:"`+escapeKubecostFilterValue(namespace)+`"`)
	}
	return strings.Join(parts, "+")
}

func escapeKubecostFilterValue(value string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value)
}

func requireKubecostCluster(properties map[string]interface{}, expected string) error {
	actual := propertyString(properties, "cluster")
	if actual == "" {
		return fmt.Errorf("Kubecost row is missing cluster identity")
	}
	if expected != "" && actual != expected {
		return fmt.Errorf("Kubecost row belongs to cluster %q, expected %q", actual, expected)
	}
	return nil
}

func propertyString(properties map[string]interface{}, key string) string {
	if properties == nil {
		return ""
	}
	value, ok := properties[key]
	if !ok || value == nil {
		return ""
	}
	if str, ok := value.(string); ok {
		return str
	}
	return fmt.Sprint(value)
}

func kubecostWorkloadIdentity(key string, allocation *KubecostAllocation, owners PodOwnerLookup) (string, string, bool, error) {
	rawKind := propertyString(allocation.Properties, "controllerKind")
	rawName := propertyString(allocation.Properties, "controller")
	pod := propertyString(allocation.Properties, "pod")
	unallocated := rawName == "__unallocated__" || strings.Contains(key, "__unallocated__")

	if kind, ok := kubecostCostWorkloadKind(rawKind); ok && rawName != "__unallocated__" {
		name := rawName
		if kind == "standalone" && pod != "" {
			name = stripPodSuffix(pod)
		}
		if kind == "staticpod" && pod != "" {
			name = pod
		}
		if name != "" {
			return kind, name, false, nil
		}
	}
	if owners != nil && pod != "" {
		if owner, found := owners(pod); found {
			if kind, ok := kubecostCostWorkloadKind(owner.Kind); ok {
				name := owner.Name
				if kind == "standalone" {
					name = stripPodSuffix(pod)
				}
				if kind == "staticpod" {
					name = pod
				}
				if name != "" {
					return kind, name, false, nil
				}
			}
		}
	}
	if unallocated {
		return "", "", true, nil
	}
	if pod != "" {
		return "standalone", stripPodSuffix(pod), false, nil
	}
	if rawName != "" {
		return "", "", true, nil
	}
	return "", "", false, fmt.Errorf("allocation %q is missing supported controller identity", key)
}

func kubecostCostWorkloadKind(kind string) (string, bool) {
	if canonical, ok := CanonicalWorkloadKind(kind); ok {
		return canonical, true
	}
	switch strings.ToLower(kind) {
	case "job", "jobs":
		return "Job", true
	case "cronjob", "cronjobs":
		return "CronJob", true
	case "pod", "pods", "standalone":
		return "standalone", true
	case "node", "nodes", "staticpod":
		return "staticpod", true
	default:
		return "", false
	}
}

func (row *kubecostWorkloadAccumulator) includeDuration(start, end string, hours float64) {
	if hours > row.durationHours {
		row.durationHours = hours
	}
	startTime, startErr := parseKubecostTimestamp(start)
	endTime, endErr := parseKubecostTimestamp(end)
	if startErr != nil || endErr != nil || !endTime.After(startTime) {
		return
	}
	if row.start.IsZero() || startTime.Before(row.start) {
		row.start = startTime
	}
	if row.end.IsZero() || endTime.After(row.end) {
		row.end = endTime
	}
	if span := row.end.Sub(row.start).Hours(); span > row.durationHours {
		row.durationHours = span
	}
}

func kubecostIdleRow(key string, allocation *KubecostAllocation) bool {
	return key == "__idle__" || strings.HasSuffix(key, "/__idle__") || allocation.Name == "__idle__" || propertyString(allocation.Properties, "namespace") == "__idle__"
}

func kubecostRowHours(start, end string, minutes float64) (float64, error) {
	if minutes > 0 {
		return minutes / 60, nil
	}
	startTime, startErr := parseKubecostTimestamp(start)
	endTime, endErr := parseKubecostTimestamp(end)
	if startErr == nil && endErr == nil && endTime.After(startTime) {
		return endTime.Sub(startTime).Hours(), nil
	}
	return 0, fmt.Errorf("missing valid row duration")
}

func parseKubecostTimestamp(value string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05-0700"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid Kubecost timestamp %q", value)
}

func kubecostUsageCost(cost float64, request, usage *float64) (float64, bool) {
	if request == nil || usage == nil {
		return 0, false
	}
	if *request <= 0 {
		if *usage > 0 {
			return cost, true
		}
		return 0, true
	}
	ratio := *usage / *request
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	return cost * ratio, true
}

func roundNamespaceCost(row *NamespaceCost) {
	row.HourlyCost = roundTo(row.HourlyCost, 4)
	row.CPUCost = roundTo(row.CPUCost, 4)
	row.MemoryCost = roundTo(row.MemoryCost, 4)
	row.StorageCost = roundTo(row.StorageCost, 4)
	row.NetworkCost = roundTo(row.NetworkCost, 4)
	row.CPUUsageCost = roundTo(row.CPUUsageCost, 4)
	row.MemoryUsageCost = roundTo(row.MemoryUsageCost, 4)
	row.IdleCost = roundTo(row.IdleCost, 4)
}

func roundWorkloadCost(row *WorkloadCost) {
	row.HourlyCost = roundTo(row.HourlyCost, 4)
	row.CPUCost = roundTo(row.CPUCost, 4)
	row.MemoryCost = roundTo(row.MemoryCost, 4)
	row.CPUUsageCost = roundTo(row.CPUUsageCost, 4)
	row.MemoryUsageCost = roundTo(row.MemoryUsageCost, 4)
	row.IdleCost = roundTo(row.IdleCost, 4)
}

func LatestKubecostTimestamp(current, candidate string) string {
	if candidate == "" {
		return current
	}
	if current == "" {
		return candidate
	}
	currentTime, currentErr := parseKubecostTimestamp(current)
	candidateTime, candidateErr := parseKubecostTimestamp(candidate)
	if currentErr != nil || (candidateErr == nil && candidateTime.After(currentTime)) {
		return candidate
	}
	return current
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func maxZero(value float64) float64 {
	if value < 0 {
		return 0
	}
	return value
}
