package k8score

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
)

const (
	// MetricsHistorySize is the number of data points to keep (1 hour at 30s intervals).
	MetricsHistorySize = 120
	// MetricsPollInterval is how often to poll the metrics API.
	MetricsPollInterval = 30 * time.Second
)

// MetricsDataPoint represents a single metrics sample.
type MetricsDataPoint struct {
	Timestamp time.Time `json:"timestamp"`
	CPU       int64     `json:"cpu"`    // nanocores
	Memory    int64     `json:"memory"` // bytes
}

// ContainerMetricsHistory holds historical metrics for a container.
type ContainerMetricsHistory struct {
	Name       string             `json:"name"`
	DataPoints []MetricsDataPoint `json:"dataPoints"`
}

// PodMetricsHistory holds historical metrics for a pod.
type PodMetricsHistory struct {
	Namespace       string                    `json:"namespace"`
	Name            string                    `json:"name"`
	Containers      []ContainerMetricsHistory `json:"containers"`
	CollectionError string                    `json:"collectionError,omitempty"`
}

// NodeMetricsHistory holds historical metrics for a node.
type NodeMetricsHistory struct {
	Name            string             `json:"name"`
	DataPoints      []MetricsDataPoint `json:"dataPoints"`
	CollectionError string             `json:"collectionError,omitempty"`
}

// TopPodMetrics holds the latest metrics snapshot for a single pod.
type TopPodMetrics struct {
	Namespace     string `json:"namespace"`
	Name          string `json:"name"`
	CPU           int64  `json:"cpu"`
	Memory        int64  `json:"memory"`
	CPURequest    int64  `json:"cpuRequest"`
	CPULimit      int64  `json:"cpuLimit"`
	MemoryRequest int64  `json:"memoryRequest"`
	MemoryLimit   int64  `json:"memoryLimit"`
}

// TopNodeMetrics holds the latest metrics snapshot for a single node.
type TopNodeMetrics struct {
	Name              string `json:"name"`
	CPU               int64  `json:"cpu"`
	Memory            int64  `json:"memory"`
	PodCount          int    `json:"podCount"`
	CPUAllocatable    int64  `json:"cpuAllocatable"`
	MemoryAllocatable int64  `json:"memoryAllocatable"`
}

// MetricsCollectionHealth reports the health of the metrics collection loop.
type MetricsCollectionHealth struct {
	PodMetrics       MetricsSourceHealth `json:"podMetrics"`
	NodeMetrics      MetricsSourceHealth `json:"nodeMetrics"`
	LastAttempt      string              `json:"lastAttempt,omitempty"`
	TotalCollections int64               `json:"totalCollections"`
	BufferSize       int                 `json:"bufferSize"`
	PollIntervalSec  int                 `json:"pollIntervalSec"`
}

// MetricsSourceHealth reports health for a single metrics source.
type MetricsSourceHealth struct {
	Collecting        bool   `json:"collecting"`
	LastSuccess       string `json:"lastSuccess,omitempty"`
	ConsecutiveErrors int    `json:"consecutiveErrors"`
	LastError         string `json:"lastError,omitempty"`
	TrackedCount      int    `json:"trackedCount"`
	TotalDataPoints   int    `json:"totalDataPoints"`
}

// MetricsHistoryStore stores historical metrics data polled from the metrics.k8s.io API.
type MetricsHistoryStore struct {
	mu        sync.RWMutex
	dynClient dynamic.Interface

	podMetrics  map[string]*podMetricsBuffer
	nodeMetrics map[string]*nodeMetricsBuffer

	lastSuccessfulPodCollection  time.Time
	lastSuccessfulNodeCollection time.Time
	lastCollectionAttempt        time.Time
	totalCollections             int64
	consecutivePodErrors         int
	consecutiveNodeErrors        int
	lastPodError                 string
	lastNodeError                string

	// OnError is called when a metrics collection error is logged.
	// It allows callers to record errors in an external error log.
	OnError func(subsystem, level, format string, args ...any)

	stopCh    chan struct{}
	startOnce sync.Once
	stopOnce  sync.Once
	wg        sync.WaitGroup
}

type podMetricsBuffer struct {
	namespace  string
	name       string
	containers map[string]*ringBuffer
	lastSeen   time.Time
}

type nodeMetricsBuffer struct {
	name     string
	buffer   *ringBuffer
	lastSeen time.Time
}

type ringBuffer struct {
	data  []MetricsDataPoint
	head  int
	count int
	size  int
}

func newRingBuffer(size int) *ringBuffer {
	return &ringBuffer{data: make([]MetricsDataPoint, size), size: size}
}

func (rb *ringBuffer) Add(point MetricsDataPoint) {
	rb.data[rb.head] = point
	rb.head = (rb.head + 1) % rb.size
	if rb.count < rb.size {
		rb.count++
	}
}

func (rb *ringBuffer) GetAll() []MetricsDataPoint {
	if rb.count == 0 {
		return nil
	}
	result := make([]MetricsDataPoint, rb.count)
	if rb.count < rb.size {
		copy(result, rb.data[:rb.count])
	} else {
		start := rb.head
		for i := 0; i < rb.count; i++ {
			result[i] = rb.data[(start+i)%rb.size]
		}
	}
	return result
}

// NewMetricsHistoryStore creates a MetricsHistoryStore. Call Start() to begin polling.
func NewMetricsHistoryStore(client dynamic.Interface) *MetricsHistoryStore {
	return &MetricsHistoryStore{
		dynClient:   client,
		podMetrics:  make(map[string]*podMetricsBuffer),
		nodeMetrics: make(map[string]*nodeMetricsBuffer),
		stopCh:      make(chan struct{}),
	}
}

// Start begins background polling. Idempotent: subsequent calls are no-ops.
func (s *MetricsHistoryStore) Start() {
	s.startOnce.Do(func() {
		s.wg.Add(1)
		go s.pollLoop()
		log.Println("Metrics history collection started")
	})
}

// Stop halts background polling and waits for the goroutine to exit.
func (s *MetricsHistoryStore) Stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
	s.wg.Wait()
	log.Println("Metrics history collection stopped")
}

func (s *MetricsHistoryStore) pollLoop() {
	defer s.wg.Done()
	s.collectMetrics()
	ticker := time.NewTicker(MetricsPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.collectMetrics()
		}
	}
}

func (s *MetricsHistoryStore) collectMetrics() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := time.Now()
	s.mu.Lock()
	s.lastCollectionAttempt = now
	s.totalCollections++
	s.mu.Unlock()
	s.collectPodMetrics(ctx, now)
	s.collectNodeMetrics(ctx, now)
}

func (s *MetricsHistoryStore) collectPodMetrics(ctx context.Context, now time.Time) {
	if s.dynClient == nil {
		var shouldReport bool
		var count int
		s.mu.Lock()
		s.consecutivePodErrors++
		s.lastPodError = "dynamic client not initialized"
		shouldReport = s.consecutivePodErrors == 1 || s.consecutivePodErrors%20 == 0
		count = s.consecutivePodErrors
		s.mu.Unlock()
		if shouldReport {
			log.Printf("[metrics] Pod metrics collection failed (count=%d): %s", count, "dynamic client not initialized")
			if s.OnError != nil {
				s.OnError("metrics", "error", "pod metrics collection failed (count=%d): %s", count, "dynamic client not initialized")
			}
		}
		return
	}

	result, err := s.dynClient.Resource(PodMetricsGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		var shouldReport bool
		var count int
		errMsg := err.Error()
		s.mu.Lock()
		s.consecutivePodErrors++
		s.lastPodError = errMsg
		shouldReport = s.consecutivePodErrors == 1 || s.consecutivePodErrors%20 == 0
		count = s.consecutivePodErrors
		s.mu.Unlock()
		if shouldReport {
			log.Printf("[metrics] Pod metrics collection failed (count=%d): %v", count, err)
			if s.OnError != nil {
				s.OnError("metrics", "error", "pod metrics collection failed (count=%d): %v", count, err)
			}
		}
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.consecutivePodErrors > 0 {
		log.Printf("[metrics] Pod metrics collection recovered after %d failures", s.consecutivePodErrors)
	}
	s.consecutivePodErrors = 0
	s.lastPodError = ""
	s.lastSuccessfulPodCollection = now

	for _, item := range result.Items {
		namespace := item.GetNamespace()
		name := item.GetName()
		key := namespace + "/" + name

		podBuf, exists := s.podMetrics[key]
		if !exists {
			podBuf = &podMetricsBuffer{
				namespace:  namespace,
				name:       name,
				containers: make(map[string]*ringBuffer),
			}
			s.podMetrics[key] = podBuf
		}
		podBuf.lastSeen = now

		containers, ok := item.Object["containers"].([]any)
		if !ok {
			continue
		}

		for _, c := range containers {
			container, ok := c.(map[string]any)
			if !ok {
				continue
			}
			containerName, _ := container["name"].(string)
			if containerName == "" {
				continue
			}
			usage, ok := container["usage"].(map[string]any)
			if !ok {
				continue
			}

			containerBuf, exists := podBuf.containers[containerName]
			if !exists {
				containerBuf = newRingBuffer(MetricsHistorySize)
				podBuf.containers[containerName] = containerBuf
			}
			cpuStr, _ := usage["cpu"].(string)
			memStr, _ := usage["memory"].(string)
			containerBuf.Add(MetricsDataPoint{
				Timestamp: now,
				CPU:       parseCPU(cpuStr),
				Memory:    parseMemory(memStr),
			})
		}
	}

	staleThreshold := now.Add(-5 * time.Minute)
	for key, podBuf := range s.podMetrics {
		if podBuf.lastSeen.Before(staleThreshold) {
			delete(s.podMetrics, key)
		}
	}
}

func (s *MetricsHistoryStore) collectNodeMetrics(ctx context.Context, now time.Time) {
	if s.dynClient == nil {
		var shouldReport bool
		var count int
		s.mu.Lock()
		s.consecutiveNodeErrors++
		s.lastNodeError = "dynamic client not initialized"
		shouldReport = s.consecutiveNodeErrors == 1 || s.consecutiveNodeErrors%20 == 0
		count = s.consecutiveNodeErrors
		s.mu.Unlock()
		if shouldReport {
			log.Printf("[metrics] Node metrics collection failed (count=%d): %s", count, "dynamic client not initialized")
			if s.OnError != nil {
				s.OnError("metrics", "error", "node metrics collection failed (count=%d): %s", count, "dynamic client not initialized")
			}
		}
		return
	}

	result, err := s.dynClient.Resource(NodeMetricsGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		var shouldReport bool
		var count int
		errMsg := err.Error()
		s.mu.Lock()
		s.consecutiveNodeErrors++
		s.lastNodeError = errMsg
		shouldReport = s.consecutiveNodeErrors == 1 || s.consecutiveNodeErrors%20 == 0
		count = s.consecutiveNodeErrors
		s.mu.Unlock()
		if shouldReport {
			log.Printf("[metrics] Node metrics collection failed (count=%d): %v", count, err)
			if s.OnError != nil {
				s.OnError("metrics", "error", "node metrics collection failed (count=%d): %v", count, err)
			}
		}
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.consecutiveNodeErrors > 0 {
		log.Printf("[metrics] Node metrics collection recovered after %d failures", s.consecutiveNodeErrors)
	}
	s.consecutiveNodeErrors = 0
	s.lastNodeError = ""
	s.lastSuccessfulNodeCollection = now

	for _, item := range result.Items {
		name := item.GetName()
		nodeBuf, exists := s.nodeMetrics[name]
		if !exists {
			nodeBuf = &nodeMetricsBuffer{name: name, buffer: newRingBuffer(MetricsHistorySize)}
			s.nodeMetrics[name] = nodeBuf
		}
		nodeBuf.lastSeen = now

		usage, ok := item.Object["usage"].(map[string]any)
		if !ok {
			continue
		}
		cpuStr, _ := usage["cpu"].(string)
		memStr, _ := usage["memory"].(string)
		nodeBuf.buffer.Add(MetricsDataPoint{
			Timestamp: now,
			CPU:       parseCPU(cpuStr),
			Memory:    parseMemory(memStr),
		})
	}

	staleThreshold := now.Add(-5 * time.Minute)
	for name, nodeBuf := range s.nodeMetrics {
		if nodeBuf.lastSeen.Before(staleThreshold) {
			delete(s.nodeMetrics, name)
		}
	}
}

// GetPodMetricsHistory returns historical metrics for a specific pod.
func (s *MetricsHistoryStore) GetPodMetricsHistory(namespace, name string) *PodMetricsHistory {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	podBuf, exists := s.podMetrics[namespace+"/"+name]
	if !exists {
		return nil
	}

	history := &PodMetricsHistory{
		Namespace:  namespace,
		Name:       name,
		Containers: make([]ContainerMetricsHistory, 0, len(podBuf.containers)),
	}
	for containerName, buf := range podBuf.containers {
		history.Containers = append(history.Containers, ContainerMetricsHistory{
			Name:       containerName,
			DataPoints: buf.GetAll(),
		})
	}
	return history
}

// GetNodeMetricsHistory returns historical metrics for a specific node.
func (s *MetricsHistoryStore) GetNodeMetricsHistory(name string) *NodeMetricsHistory {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	nodeBuf, exists := s.nodeMetrics[name]
	if !exists {
		return nil
	}
	return &NodeMetricsHistory{Name: name, DataPoints: nodeBuf.buffer.GetAll()}
}

// GetAllPodMetricsLatest returns the latest metrics for all tracked pods.
func (s *MetricsHistoryStore) GetAllPodMetricsLatest() []TopPodMetrics {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]TopPodMetrics, 0, len(s.podMetrics))
	for _, podBuf := range s.podMetrics {
		var totalCPU, totalMem int64
		for _, containerBuf := range podBuf.containers {
			if points := containerBuf.GetAll(); len(points) > 0 {
				last := points[len(points)-1]
				totalCPU += last.CPU
				totalMem += last.Memory
			}
		}
		result = append(result, TopPodMetrics{
			Namespace: podBuf.namespace,
			Name:      podBuf.name,
			CPU:       totalCPU,
			Memory:    totalMem,
		})
	}
	return result
}

// GetAllNodeMetricsLatest returns the latest metrics for all tracked nodes.
func (s *MetricsHistoryStore) GetAllNodeMetricsLatest() []TopNodeMetrics {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]TopNodeMetrics, 0, len(s.nodeMetrics))
	for _, nodeBuf := range s.nodeMetrics {
		if points := nodeBuf.buffer.GetAll(); len(points) > 0 {
			last := points[len(points)-1]
			result = append(result, TopNodeMetrics{Name: nodeBuf.name, CPU: last.CPU, Memory: last.Memory})
		}
	}
	return result
}

// CollectionHealth returns the current health of metrics collection.
func (s *MetricsHistoryStore) CollectionHealth() MetricsCollectionHealth {
	if s == nil {
		return MetricsCollectionHealth{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	podDataPoints := 0
	for _, podBuf := range s.podMetrics {
		for _, containerBuf := range podBuf.containers {
			podDataPoints += containerBuf.count
		}
	}
	nodeDataPoints := 0
	for _, nodeBuf := range s.nodeMetrics {
		nodeDataPoints += nodeBuf.buffer.count
	}

	health := MetricsCollectionHealth{
		PodMetrics: MetricsSourceHealth{
			Collecting:        s.consecutivePodErrors == 0 && !s.lastSuccessfulPodCollection.IsZero(),
			ConsecutiveErrors: s.consecutivePodErrors,
			LastError:         s.lastPodError,
			TrackedCount:      len(s.podMetrics),
			TotalDataPoints:   podDataPoints,
		},
		NodeMetrics: MetricsSourceHealth{
			Collecting:        s.consecutiveNodeErrors == 0 && !s.lastSuccessfulNodeCollection.IsZero(),
			ConsecutiveErrors: s.consecutiveNodeErrors,
			LastError:         s.lastNodeError,
			TrackedCount:      len(s.nodeMetrics),
			TotalDataPoints:   nodeDataPoints,
		},
		TotalCollections: s.totalCollections,
		BufferSize:       MetricsHistorySize,
		PollIntervalSec:  int(MetricsPollInterval.Seconds()),
	}

	if !s.lastSuccessfulPodCollection.IsZero() {
		health.PodMetrics.LastSuccess = s.lastSuccessfulPodCollection.Format(time.RFC3339)
	}
	if !s.lastSuccessfulNodeCollection.IsZero() {
		health.NodeMetrics.LastSuccess = s.lastSuccessfulNodeCollection.Format(time.RFC3339)
	}
	if !s.lastCollectionAttempt.IsZero() {
		health.LastAttempt = s.lastCollectionAttempt.Format(time.RFC3339)
	}
	return health
}

// parseCPU converts a Kubernetes CPU string to nanocores.
func parseCPU(s string) int64 {
	if s == "" {
		return 0
	}
	if len(s) > 1 && s[len(s)-1] == 'n' {
		var n int64
		if _, err := fmt.Sscanf(s[:len(s)-1], "%d", &n); err == nil {
			return n
		}
		return 0
	}
	if len(s) > 1 && s[len(s)-1] == 'm' {
		var n int64
		if _, err := fmt.Sscanf(s[:len(s)-1], "%d", &n); err == nil {
			return n * 1000000
		}
		return 0
	}
	var n float64
	if _, err := fmt.Sscanf(s, "%f", &n); err == nil {
		return int64(n * 1000000000)
	}
	return 0
}

// parseMemory converts a Kubernetes memory string to bytes.
func parseMemory(s string) int64 {
	if s == "" {
		return 0
	}
	suffixes := map[string]int64{
		"Ki": 1024, "Mi": 1024 * 1024, "Gi": 1024 * 1024 * 1024, "Ti": 1024 * 1024 * 1024 * 1024,
		"K": 1000, "M": 1000 * 1000, "G": 1000 * 1000 * 1000, "T": 1000 * 1000 * 1000 * 1000,
	}
	for suffix, multiplier := range suffixes {
		if len(s) > len(suffix) && s[len(s)-len(suffix):] == suffix {
			var n int64
			if _, err := fmt.Sscanf(s[:len(s)-len(suffix)], "%d", &n); err == nil {
				return n * multiplier
			}
			return 0
		}
	}
	var n int64
	if _, err := fmt.Sscanf(s, "%d", &n); err == nil {
		return n
	}
	return 0
}
