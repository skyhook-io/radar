package k8s

import (
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
)

const (
	TopMetricsKindPods      = "pods"
	TopMetricsKindNodes     = "nodes"
	TopMetricsKindWorkloads = "workloads"
	TopMetricsSortCPU       = "cpu"
	TopMetricsSortMemory    = "memory"
	DefaultTopMetricsLimit  = 20
	MaxTopMetricsLimit      = 100
)

type TopMetricsOptions struct {
	Kind      string
	Namespace string
	Sort      string
	Limit     int
}

type TopMetricsResponse struct {
	Kind             string               `json:"kind"`
	Sort             string               `json:"sort"`
	Namespace        string               `json:"namespace,omitempty"`
	MetricsAvailable bool                 `json:"metricsAvailable"`
	Reason           string               `json:"reason,omitempty"`
	Items            []TopMetricsItem     `json:"items,omitempty"`
	Workloads        []TopWorkloadMetrics `json:"workloads,omitempty"`
	// SkippedNoMetrics counts resources omitted from Items because
	// metrics-server has no sample for them yet (new pods, Pending pods,
	// scrape gap). Distinguishes "no top consumers" from "we have no
	// metrics data" so callers can surface a useful hint instead of
	// listing inventory pods with zero usage.
	SkippedNoMetrics int `json:"skippedNoMetrics,omitempty"`
}

type TopMetricsItem struct {
	Kind                string        `json:"kind"`
	Namespace           string        `json:"namespace,omitempty"`
	Name                string        `json:"name"`
	CPU                 int64         `json:"cpu"`
	CPUMilli            int64         `json:"cpuMilli"`
	Memory              int64         `json:"memory"`
	MemoryMi            int64         `json:"memoryMi"`
	CPURequest          int64         `json:"cpuRequest,omitempty"`
	CPURequestMilli     int64         `json:"cpuRequestMilli,omitempty"`
	CPULimit            int64         `json:"cpuLimit,omitempty"`
	CPULimitMilli       int64         `json:"cpuLimitMilli,omitempty"`
	MemoryRequest       int64         `json:"memoryRequest,omitempty"`
	MemoryRequestMi     int64         `json:"memoryRequestMi,omitempty"`
	MemoryLimit         int64         `json:"memoryLimit,omitempty"`
	MemoryLimitMi       int64         `json:"memoryLimitMi,omitempty"`
	CPUAllocatable      int64         `json:"cpuAllocatable,omitempty"`
	CPUAllocatableMilli int64         `json:"cpuAllocatableMilli,omitempty"`
	MemoryAllocatable   int64         `json:"memoryAllocatable,omitempty"`
	MemoryAllocatableMi int64         `json:"memoryAllocatableMi,omitempty"`
	PodCount            int           `json:"podCount,omitempty"`
	Ready               string        `json:"ready,omitempty"`
	Status              string        `json:"status,omitempty"`
	Restarts            int32         `json:"restarts,omitempty"`
	Node                string        `json:"node,omitempty"`
	Owner               *TopOwnerInfo `json:"owner,omitempty"`
}

type TopWorkloadMetrics struct {
	Kind            string `json:"kind"`
	Namespace       string `json:"namespace"`
	Name            string `json:"name"`
	Pods            int    `json:"pods"`
	ReadyPods       int    `json:"readyPods"`
	CPU             int64  `json:"cpu"`
	CPUMilli        int64  `json:"cpuMilli"`
	Memory          int64  `json:"memory"`
	MemoryMi        int64  `json:"memoryMi"`
	CPURequest      int64  `json:"cpuRequest,omitempty"`
	CPURequestMilli int64  `json:"cpuRequestMilli,omitempty"`
	CPULimit        int64  `json:"cpuLimit,omitempty"`
	CPULimitMilli   int64  `json:"cpuLimitMilli,omitempty"`
	MemoryRequest   int64  `json:"memoryRequest,omitempty"`
	MemoryRequestMi int64  `json:"memoryRequestMi,omitempty"`
	MemoryLimit     int64  `json:"memoryLimit,omitempty"`
	MemoryLimitMi   int64  `json:"memoryLimitMi,omitempty"`
	Restarts        int32  `json:"restarts,omitempty"`
}

type TopOwnerInfo struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

func NormalizeTopMetricsOptions(opts TopMetricsOptions) TopMetricsOptions {
	opts.Kind = strings.ToLower(strings.TrimSpace(opts.Kind))
	switch opts.Kind {
	case "", "pod", TopMetricsKindPods:
		opts.Kind = TopMetricsKindPods
	case "node", TopMetricsKindNodes:
		opts.Kind = TopMetricsKindNodes
	case "workload", TopMetricsKindWorkloads:
		opts.Kind = TopMetricsKindWorkloads
	}
	opts.Sort = strings.ToLower(strings.TrimSpace(opts.Sort))
	if opts.Sort != TopMetricsSortMemory {
		opts.Sort = TopMetricsSortCPU
	}
	if opts.Limit <= 0 {
		opts.Limit = DefaultTopMetricsLimit
	}
	if opts.Limit > MaxTopMetricsLimit {
		opts.Limit = MaxTopMetricsLimit
	}
	return opts
}

func BuildTopMetrics(opts TopMetricsOptions) TopMetricsResponse {
	opts = NormalizeTopMetricsOptions(opts)
	resp := TopMetricsResponse{
		Kind:      opts.Kind,
		Sort:      opts.Sort,
		Namespace: opts.Namespace,
	}
	store := GetMetricsHistory()
	if store == nil {
		resp.Reason = "metrics history is not initialized"
		return resp
	}

	switch opts.Kind {
	case TopMetricsKindNodes:
		resp.Items, resp.SkippedNoMetrics = buildTopNodeItems(store)
	case TopMetricsKindWorkloads:
		resp.Workloads = buildTopWorkloadItems(store, opts.Namespace)
	case TopMetricsKindPods:
		resp.Items, resp.SkippedNoMetrics = buildTopPodItems(store, opts.Namespace)
	default:
		resp.Reason = "unsupported kind"
		return resp
	}
	resp.MetricsAvailable = hasTopMetrics(resp)
	if !resp.MetricsAvailable && resp.Reason == "" {
		resp.Reason = "no metrics samples available"
	}
	sortAndLimitTopMetrics(&resp, opts.Sort, opts.Limit)
	return resp
}

func hasTopMetrics(resp TopMetricsResponse) bool {
	// buildTopPodItems / buildTopNodeItems now filter out resources
	// without a metrics-server sample (rows with no metricsMap entry are
	// reported via SkippedNoMetrics rather than included as zero-usage
	// inventory). After that filter, presence in Items/Workloads itself
	// is the signal that metrics-server delivered data — zero CPU+memory
	// rows now mean "real measurement of an idle workload," not "no data."
	// Checking CPU/Memory>0 here would incorrectly mark idle-but-scraped
	// clusters as having no metrics.
	return len(resp.Items) > 0 || len(resp.Workloads) > 0
}

func buildTopPodItems(store *MetricsHistoryStore, namespace string) (items []TopMetricsItem, skippedNoMetrics int) {
	metricsMap := map[string]TopPodMetrics{}
	for _, m := range store.GetAllPodMetricsLatest() {
		metricsMap[m.Namespace+"/"+m.Name] = m
	}

	cache := GetResourceCache()
	if cache == nil || cache.Pods() == nil {
		// Metrics-only path: every emitted row came from a metricsMap entry,
		// so nothing was skipped by definition.
		out := make([]TopMetricsItem, 0, len(metricsMap))
		for _, m := range metricsMap {
			if namespace != "" && m.Namespace != namespace {
				continue
			}
			out = append(out, topPodMetricsOnly(m))
		}
		return out, 0
	}

	var pods []*corev1.Pod
	var err error
	if namespace != "" {
		pods, err = cache.Pods().Pods(namespace).List(labels.Everything())
	} else {
		pods, err = cache.Pods().List(labels.Everything())
	}
	if err != nil {
		// Fall back to metrics-only rows on transient list errors —
		// mirrors the cache.Pods()==nil branch above so a brief apiserver
		// hiccup doesn't blank top_resources when usage samples are present.
		out := make([]TopMetricsItem, 0, len(metricsMap))
		for _, m := range metricsMap {
			if namespace != "" && m.Namespace != namespace {
				continue
			}
			out = append(out, topPodMetricsOnly(m))
		}
		return out, 0
	}

	// Only emit pods that have a metricsMap entry — "no entry" means
	// metrics-server hasn't scraped this pod (new pod, Pending/Failed,
	// scrape gap) and is distinct from "has entry, usage is 0" (rare
	// genuinely-idle pod, real data). Filling slots with no-data pods
	// would make agents read inventory as top consumers.
	items = make([]TopMetricsItem, 0, len(pods))
	for _, pod := range pods {
		m, ok := metricsMap[pod.Namespace+"/"+pod.Name]
		if !ok {
			skippedNoMetrics++
			continue
		}
		entry := topPodFromObject(pod)
		applyPodUsage(&entry, m)
		items = append(items, entry)
	}
	return items, skippedNoMetrics
}

func buildTopNodeItems(store *MetricsHistoryStore) (items []TopMetricsItem, skippedNoMetrics int) {
	metricsMap := map[string]TopNodeMetrics{}
	for _, m := range store.GetAllNodeMetricsLatest() {
		metricsMap[m.Name] = m
	}
	podCounts := nodePodCounts()

	cache := GetResourceCache()
	if cache == nil || cache.Nodes() == nil {
		out := make([]TopMetricsItem, 0, len(metricsMap))
		for _, m := range metricsMap {
			out = append(out, topNodeMetricsOnly(m))
		}
		return out, 0
	}

	nodes, err := cache.Nodes().List(labels.Everything())
	if err != nil {
		// Mirror cache.Nodes()==nil above — fall back to metrics-only rows
		// so a transient list error doesn't blank the response when usage
		// samples are available from metrics-server.
		out := make([]TopMetricsItem, 0, len(metricsMap))
		for _, m := range metricsMap {
			out = append(out, topNodeMetricsOnly(m))
		}
		return out, 0
	}
	// Same rule as buildTopPodItems: only emit nodes with a metricsMap
	// entry. Node metrics are normally more reliably present than pods,
	// but the same "no data ≠ idle" semantics apply.
	items = make([]TopMetricsItem, 0, len(nodes))
	for _, node := range nodes {
		m, ok := metricsMap[node.Name]
		if !ok {
			skippedNoMetrics++
			continue
		}
		entry := TopMetricsItem{
			Kind:     "Node",
			Name:     node.Name,
			PodCount: podCounts[node.Name],
			Status:   nodeReadyStatus(node),
		}
		entry.CPU = m.CPU
		entry.CPUMilli = nanoToMilli(m.CPU)
		entry.Memory = m.Memory
		entry.MemoryMi = bytesToMi(m.Memory)
		if cpu := node.Status.Allocatable[corev1.ResourceCPU]; !cpu.IsZero() {
			entry.CPUAllocatable = cpu.MilliValue() * 1000000
			entry.CPUAllocatableMilli = cpu.MilliValue()
		}
		if mem := node.Status.Allocatable[corev1.ResourceMemory]; !mem.IsZero() {
			entry.MemoryAllocatable = mem.Value()
			entry.MemoryAllocatableMi = bytesToMi(mem.Value())
		}
		items = append(items, entry)
	}
	return items, skippedNoMetrics
}

func buildTopWorkloadItems(store *MetricsHistoryStore, namespace string) []TopWorkloadMetrics {
	// Workload aggregation already drops no-metrics pods at the per-pod
	// layer (buildTopPodItems filters them out), so we don't need to track
	// a per-workload skipped count separately.
	pods, _ := buildTopPodItems(store, namespace)
	workloads := map[string]*TopWorkloadMetrics{}
	for _, pod := range pods {
		if pod.Owner == nil {
			continue
		}
		key := pod.Namespace + "/" + pod.Owner.Kind + "/" + pod.Owner.Name
		wl := workloads[key]
		if wl == nil {
			wl = &TopWorkloadMetrics{
				Kind:      pod.Owner.Kind,
				Namespace: pod.Namespace,
				Name:      pod.Owner.Name,
			}
			workloads[key] = wl
		}
		wl.Pods++
		if isReadyString(pod.Ready) {
			wl.ReadyPods++
		}
		wl.CPU += pod.CPU
		wl.Memory += pod.Memory
		wl.CPURequest += pod.CPURequest
		wl.CPULimit += pod.CPULimit
		wl.MemoryRequest += pod.MemoryRequest
		wl.MemoryLimit += pod.MemoryLimit
		wl.Restarts += pod.Restarts
	}
	items := make([]TopWorkloadMetrics, 0, len(workloads))
	for _, wl := range workloads {
		wl.CPUMilli = nanoToMilli(wl.CPU)
		wl.MemoryMi = bytesToMi(wl.Memory)
		wl.CPURequestMilli = nanoToMilli(wl.CPURequest)
		wl.CPULimitMilli = nanoToMilli(wl.CPULimit)
		wl.MemoryRequestMi = bytesToMi(wl.MemoryRequest)
		wl.MemoryLimitMi = bytesToMi(wl.MemoryLimit)
		items = append(items, *wl)
	}
	return items
}

func sortAndLimitTopMetrics(resp *TopMetricsResponse, sortBy string, limit int) {
	sort.SliceStable(resp.Items, func(i, j int) bool {
		if sortBy == TopMetricsSortMemory {
			if resp.Items[i].Memory != resp.Items[j].Memory {
				return resp.Items[i].Memory > resp.Items[j].Memory
			}
		} else if resp.Items[i].CPU != resp.Items[j].CPU {
			return resp.Items[i].CPU > resp.Items[j].CPU
		}
		return resp.Items[i].Namespace+"/"+resp.Items[i].Name < resp.Items[j].Namespace+"/"+resp.Items[j].Name
	})
	sort.SliceStable(resp.Workloads, func(i, j int) bool {
		if sortBy == TopMetricsSortMemory {
			if resp.Workloads[i].Memory != resp.Workloads[j].Memory {
				return resp.Workloads[i].Memory > resp.Workloads[j].Memory
			}
		} else if resp.Workloads[i].CPU != resp.Workloads[j].CPU {
			return resp.Workloads[i].CPU > resp.Workloads[j].CPU
		}
		a := resp.Workloads[i].Namespace + "/" + resp.Workloads[i].Kind + "/" + resp.Workloads[i].Name
		b := resp.Workloads[j].Namespace + "/" + resp.Workloads[j].Kind + "/" + resp.Workloads[j].Name
		return a < b
	})
	if limit > 0 {
		if len(resp.Items) > limit {
			resp.Items = resp.Items[:limit]
		}
		if len(resp.Workloads) > limit {
			resp.Workloads = resp.Workloads[:limit]
		}
	}
}

func topPodMetricsOnly(m TopPodMetrics) TopMetricsItem {
	item := TopMetricsItem{
		Kind:      "Pod",
		Namespace: m.Namespace,
		Name:      m.Name,
	}
	applyPodUsage(&item, m)
	return item
}

func topNodeMetricsOnly(m TopNodeMetrics) TopMetricsItem {
	return TopMetricsItem{
		Kind:     "Node",
		Name:     m.Name,
		CPU:      m.CPU,
		CPUMilli: nanoToMilli(m.CPU),
		Memory:   m.Memory,
		MemoryMi: bytesToMi(m.Memory),
		PodCount: m.PodCount,
		Status:   "Unknown",
	}
}

func topPodFromObject(pod *corev1.Pod) TopMetricsItem {
	item := TopMetricsItem{
		Kind:      "Pod",
		Namespace: pod.Namespace,
		Name:      pod.Name,
		Ready:     podReadyString(pod),
		Status:    string(pod.Status.Phase),
		Restarts:  podRestartCount(pod),
		Node:      pod.Spec.NodeName,
		Owner:     topOwnerForPod(pod),
	}
	for _, c := range pod.Spec.Containers {
		if req, ok := c.Resources.Requests[corev1.ResourceCPU]; ok {
			item.CPURequest += req.MilliValue() * 1000000
		}
		if lim, ok := c.Resources.Limits[corev1.ResourceCPU]; ok {
			item.CPULimit += lim.MilliValue() * 1000000
		}
		if req, ok := c.Resources.Requests[corev1.ResourceMemory]; ok {
			item.MemoryRequest += req.Value()
		}
		if lim, ok := c.Resources.Limits[corev1.ResourceMemory]; ok {
			item.MemoryLimit += lim.Value()
		}
	}
	item.CPURequestMilli = nanoToMilli(item.CPURequest)
	item.CPULimitMilli = nanoToMilli(item.CPULimit)
	item.MemoryRequestMi = bytesToMi(item.MemoryRequest)
	item.MemoryLimitMi = bytesToMi(item.MemoryLimit)
	return item
}

func applyPodUsage(item *TopMetricsItem, m TopPodMetrics) {
	item.CPU = m.CPU
	item.CPUMilli = nanoToMilli(m.CPU)
	item.Memory = m.Memory
	item.MemoryMi = bytesToMi(m.Memory)
}

func nodePodCounts() map[string]int {
	counts := map[string]int{}
	cache := GetResourceCache()
	if cache == nil || cache.Pods() == nil {
		return counts
	}
	pods, err := cache.Pods().List(labels.Everything())
	if err != nil {
		return counts
	}
	for _, pod := range pods {
		if pod.Spec.NodeName != "" && pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
			counts[pod.Spec.NodeName]++
		}
	}
	return counts
}

func nodeReadyStatus(node *corev1.Node) string {
	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			return string(cond.Status)
		}
	}
	return "Unknown"
}

func topOwnerForPod(pod *corev1.Pod) *TopOwnerInfo {
	for _, ref := range pod.OwnerReferences {
		if ref.Controller != nil && *ref.Controller {
			if ref.Kind == "ReplicaSet" {
				return &TopOwnerInfo{Kind: "Deployment", Name: stripReplicaSetHash(ref.Name)}
			}
			return &TopOwnerInfo{Kind: ref.Kind, Name: ref.Name}
		}
	}
	if len(pod.OwnerReferences) > 0 {
		ref := pod.OwnerReferences[0]
		if ref.Kind == "ReplicaSet" {
			return &TopOwnerInfo{Kind: "Deployment", Name: stripReplicaSetHash(ref.Name)}
		}
		return &TopOwnerInfo{Kind: ref.Kind, Name: ref.Name}
	}
	return nil
}

func stripReplicaSetHash(name string) string {
	idx := strings.LastIndex(name, "-")
	if idx <= 0 {
		return name
	}
	return name[:idx]
}

func podRestartCount(pod *corev1.Pod) int32 {
	var total int32
	for _, c := range pod.Status.ContainerStatuses {
		total += c.RestartCount
	}
	// Init container restarts matter — Init:CrashLoopBackOff pods are
	// exactly the ones an operator wants to see at the top of top_resources,
	// and they have zero restart counts on the main containers. Mirror
	// podTotalRestarts in internal/mcp/tools_diagnose.go.
	for _, c := range pod.Status.InitContainerStatuses {
		total += c.RestartCount
	}
	return total
}

func podReadyString(pod *corev1.Pod) string {
	// Match status entries to spec.Containers by name so that any entries
	// kubelet may surface outside the main-container set (sidecars in
	// initContainerStatuses, ephemerals, etc.) can't push `ready` above
	// `total` and produce nonsense readouts like "2/1".
	specNames := make(map[string]bool, len(pod.Spec.Containers))
	for _, c := range pod.Spec.Containers {
		specNames[c.Name] = true
	}
	total := len(pod.Spec.Containers)
	ready := 0
	for _, c := range pod.Status.ContainerStatuses {
		if specNames[c.Name] && c.Ready {
			ready++
		}
	}
	return strings.Join([]string{strconv.Itoa(ready), strconv.Itoa(total)}, "/")
}

func isReadyString(v string) bool {
	parts := strings.Split(v, "/")
	return len(parts) == 2 && parts[1] != "0" && parts[0] == parts[1]
}

func nanoToMilli(v int64) int64 {
	return v / 1000000
}

func bytesToMi(v int64) int64 {
	return v / (1024 * 1024)
}
