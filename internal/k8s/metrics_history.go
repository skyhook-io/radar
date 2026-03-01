package k8s

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// MetricsHistorySize is the number of data points to keep (1 hour at 30s intervals)
	MetricsHistorySize = 120
	// MetricsPollInterval is how often to poll metrics
	MetricsPollInterval = 30 * time.Second
)

// MetricsDataPoint represents a single metrics sample
type MetricsDataPoint struct {
	Timestamp time.Time `json:"timestamp"`
	CPU       int64     `json:"cpu"`    // CPU in nanocores
	Memory    int64     `json:"memory"` // Memory in bytes
}

// ContainerMetricsHistory holds historical metrics for a container
type ContainerMetricsHistory struct {
	Name       string             `json:"name"`
	DataPoints []MetricsDataPoint `json:"dataPoints"`
}

// PodMetricsHistory holds historical metrics for a pod
type PodMetricsHistory struct {
	Namespace       string                    `json:"namespace"`
	Name            string                    `json:"name"`
	Containers      []ContainerMetricsHistory `json:"containers"`
	CollectionError string                    `json:"collectionError,omitempty"`
}

// NodeMetricsHistory holds historical metrics for a node
type NodeMetricsHistory struct {
	Name            string             `json:"name"`
	DataPoints      []MetricsDataPoint `json:"dataPoints"`
	CollectionError string             `json:"collectionError,omitempty"`
}

// MetricsHistoryStore stores historical metrics data
type MetricsHistoryStore struct {
	mu sync.RWMutex

	// Pod metrics: key = "namespace/name"
	podMetrics map[string]*podMetricsBuffer

	// Node metrics: key = node name
	nodeMetrics map[string]*nodeMetricsBuffer

	// Collection health tracking
	lastSuccessfulPodCollection  time.Time
	lastSuccessfulNodeCollection time.Time
	lastCollectionAttempt        time.Time
	totalCollections             int64
	consecutivePodErrors         int
	consecutiveNodeErrors        int
	lastPodError                 string
	lastNodeError                string

	// Control
	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// podMetricsBuffer holds ring buffers for each container in a pod
type podMetricsBuffer struct {
	namespace  string
	name       string
	containers map[string]*ringBuffer // container name -> ring buffer
	lastSeen   time.Time              // last time this pod appeared in metrics API
}

// nodeMetricsBuffer holds a ring buffer for a node
type nodeMetricsBuffer struct {
	name     string
	buffer   *ringBuffer
	lastSeen time.Time // last time this node appeared in metrics API
}

// ringBuffer is a fixed-size circular buffer for metrics
type ringBuffer struct {
	data  []MetricsDataPoint
	head  int
	count int
	size  int
}

func newRingBuffer(size int) *ringBuffer {
	return &ringBuffer{
		data: make([]MetricsDataPoint, size),
		size: size,
	}
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
		// Buffer not full yet, data starts at 0
		copy(result, rb.data[:rb.count])
	} else {
		// Buffer is full, need to read in order starting from head
		start := rb.head
		for i := 0; i < rb.count; i++ {
			result[i] = rb.data[(start+i)%rb.size]
		}
	}
	return result
}

var (
	metricsHistoryStore *MetricsHistoryStore
	metricsHistoryOnce  = new(sync.Once)
	metricsHistoryMu    sync.Mutex
)

// InitMetricsHistory initializes the metrics history store and starts polling
func InitMetricsHistory() {
	metricsHistoryMu.Lock()
	defer metricsHistoryMu.Unlock()
	metricsHistoryOnce.Do(func() {
		metricsHistoryStore = &MetricsHistoryStore{
			podMetrics:  make(map[string]*podMetricsBuffer),
			nodeMetrics: make(map[string]*nodeMetricsBuffer),
			stopCh:      make(chan struct{}),
		}

		// Start polling goroutine
		metricsHistoryStore.wg.Add(1)
		go metricsHistoryStore.pollLoop()

		log.Println("Metrics history collection started")
	})
}

// GetMetricsHistory returns the metrics history store
func GetMetricsHistory() *MetricsHistoryStore {
	return metricsHistoryStore
}

// StopMetricsHistory stops the metrics polling
func StopMetricsHistory() {
	if metricsHistoryStore != nil {
		metricsHistoryStore.stopOnce.Do(func() {
			close(metricsHistoryStore.stopCh)
		})
		metricsHistoryStore.wg.Wait()
		log.Println("Metrics history collection stopped")
	}
}

// ResetMetricsHistory stops polling and clears the store so it can be
// reinitialized for a new cluster after context switch.
func ResetMetricsHistory() {
	metricsHistoryMu.Lock()
	defer metricsHistoryMu.Unlock()
	StopMetricsHistory()
	metricsHistoryStore = nil
	metricsHistoryOnce = new(sync.Once)
}

// pollLoop continuously polls metrics at the configured interval
func (s *MetricsHistoryStore) pollLoop() {
	defer s.wg.Done()

	// Initial poll
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

// collectMetrics fetches current metrics and adds them to history
func (s *MetricsHistoryStore) collectMetrics() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	now := time.Now()

	s.mu.Lock()
	s.lastCollectionAttempt = now
	s.totalCollections++
	s.mu.Unlock()

	// Collect pod metrics
	s.collectPodMetrics(ctx, now)

	// Collect node metrics
	s.collectNodeMetrics(ctx, now)
}

func (s *MetricsHistoryStore) collectPodMetrics(ctx context.Context, now time.Time) {
	client := GetDynamicClient()
	if client == nil {
		s.mu.Lock()
		s.consecutivePodErrors++
		s.lastPodError = "dynamic client not initialized"
		if s.consecutivePodErrors == 1 || s.consecutivePodErrors%20 == 0 {
			log.Printf("[metrics] Pod metrics collection failed (count=%d): %s", s.consecutivePodErrors, s.lastPodError)
		}
		s.mu.Unlock()
		return
	}

	// List all pod metrics
	result, err := client.Resource(podMetricsGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		s.mu.Lock()
		s.consecutivePodErrors++
		s.lastPodError = err.Error()
		// Log on first failure, then every 20th to avoid spam (every ~10 minutes at 30s intervals)
		if s.consecutivePodErrors == 1 || s.consecutivePodErrors%20 == 0 {
			log.Printf("[metrics] Pod metrics collection failed (count=%d): %v", s.consecutivePodErrors, err)
		}
		s.mu.Unlock()
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Reset error state on success
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

		// Get or create pod buffer
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

		// Extract container metrics
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

			cpuStr, _ := usage["cpu"].(string)
			memStr, _ := usage["memory"].(string)

			cpu := parseCPU(cpuStr)
			mem := parseMemory(memStr)

			// Get or create container buffer
			containerBuf, exists := podBuf.containers[containerName]
			if !exists {
				containerBuf = newRingBuffer(MetricsHistorySize)
				podBuf.containers[containerName] = containerBuf
			}

			containerBuf.Add(MetricsDataPoint{
				Timestamp: now,
				CPU:       cpu,
				Memory:    mem,
			})
		}
	}

	// Clean up stale entries for pods no longer reported by the metrics API.
	// Use 5 minutes as the threshold — enough to survive brief metrics-server blips
	// but short enough to avoid unbounded growth from deleted pods.
	staleThreshold := now.Add(-5 * time.Minute)
	for key, podBuf := range s.podMetrics {
		if podBuf.lastSeen.Before(staleThreshold) {
			delete(s.podMetrics, key)
		}
	}
}

func (s *MetricsHistoryStore) collectNodeMetrics(ctx context.Context, now time.Time) {
	client := GetDynamicClient()
	if client == nil {
		s.mu.Lock()
		s.consecutiveNodeErrors++
		s.lastNodeError = "dynamic client not initialized"
		if s.consecutiveNodeErrors == 1 || s.consecutiveNodeErrors%20 == 0 {
			log.Printf("[metrics] Node metrics collection failed (count=%d): %s", s.consecutiveNodeErrors, s.lastNodeError)
		}
		s.mu.Unlock()
		return
	}

	// List all node metrics
	result, err := client.Resource(nodeMetricsGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		s.mu.Lock()
		s.consecutiveNodeErrors++
		s.lastNodeError = err.Error()
		if s.consecutiveNodeErrors == 1 || s.consecutiveNodeErrors%20 == 0 {
			log.Printf("[metrics] Node metrics collection failed (count=%d): %v", s.consecutiveNodeErrors, err)
		}
		s.mu.Unlock()
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Reset error state on success
	if s.consecutiveNodeErrors > 0 {
		log.Printf("[metrics] Node metrics collection recovered after %d failures", s.consecutiveNodeErrors)
	}
	s.consecutiveNodeErrors = 0
	s.lastNodeError = ""
	s.lastSuccessfulNodeCollection = now

	for _, item := range result.Items {
		name := item.GetName()

		// Get or create node buffer
		nodeBuf, exists := s.nodeMetrics[name]
		if !exists {
			nodeBuf = &nodeMetricsBuffer{
				name:   name,
				buffer: newRingBuffer(MetricsHistorySize),
			}
			s.nodeMetrics[name] = nodeBuf
		}
		nodeBuf.lastSeen = now

		usage, ok := item.Object["usage"].(map[string]any)
		if !ok {
			continue
		}

		cpuStr, _ := usage["cpu"].(string)
		memStr, _ := usage["memory"].(string)

		cpu := parseCPU(cpuStr)
		mem := parseMemory(memStr)

		nodeBuf.buffer.Add(MetricsDataPoint{
			Timestamp: now,
			CPU:       cpu,
			Memory:    mem,
		})
	}

	// Clean up stale node entries
	staleThreshold := now.Add(-5 * time.Minute)
	for name, nodeBuf := range s.nodeMetrics {
		if nodeBuf.lastSeen.Before(staleThreshold) {
			delete(s.nodeMetrics, name)
		}
	}
}

// GetPodMetricsHistory returns historical metrics for a specific pod
func (s *MetricsHistoryStore) GetPodMetricsHistory(namespace, name string) *PodMetricsHistory {
	if s == nil {
		return nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	key := namespace + "/" + name
	podBuf, exists := s.podMetrics[key]
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

// GetNodeMetricsHistory returns historical metrics for a specific node
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

	return &NodeMetricsHistory{
		Name:       name,
		DataPoints: nodeBuf.buffer.GetAll(),
	}
}

// TopPodMetrics holds the latest metrics snapshot for a single pod
type TopPodMetrics struct {
	Namespace     string `json:"namespace"`
	Name          string `json:"name"`
	CPU           int64  `json:"cpu"`           // nanocores (usage)
	Memory        int64  `json:"memory"`        // bytes (usage)
	CPURequest    int64  `json:"cpuRequest"`    // nanocores (sum across containers)
	CPULimit      int64  `json:"cpuLimit"`      // nanocores (sum across containers)
	MemoryRequest int64  `json:"memoryRequest"` // bytes (sum across containers)
	MemoryLimit   int64  `json:"memoryLimit"`   // bytes (sum across containers)
}

// TopNodeMetrics holds the latest metrics snapshot for a single node
type TopNodeMetrics struct {
	Name              string `json:"name"`
	CPU               int64  `json:"cpu"`               // nanocores (usage)
	Memory            int64  `json:"memory"`             // bytes (usage)
	PodCount          int    `json:"podCount"`           // number of pods scheduled on this node
	CPUAllocatable    int64  `json:"cpuAllocatable"`     // nanocores
	MemoryAllocatable int64  `json:"memoryAllocatable"`  // bytes
}

// GetAllPodMetricsLatest returns the latest metrics for all tracked pods
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

// GetAllNodeMetricsLatest returns the latest metrics for all tracked nodes
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
			result = append(result, TopNodeMetrics{
				Name:   nodeBuf.name,
				CPU:    last.CPU,
				Memory: last.Memory,
			})
		}
	}
	return result
}

// MetricsCollectionHealth reports the health of the metrics collection loop
type MetricsCollectionHealth struct {
	PodMetrics       MetricsSourceHealth `json:"podMetrics"`
	NodeMetrics      MetricsSourceHealth `json:"nodeMetrics"`
	LastAttempt      string              `json:"lastAttempt,omitempty"`      // RFC3339 — when the poll loop last ran
	TotalCollections int64               `json:"totalCollections"`           // total poll loop iterations
	BufferSize       int                 `json:"bufferSize"`                 // ring buffer capacity per container/node
	PollIntervalSec  int                 `json:"pollIntervalSec"`            // seconds between polls
}

// MetricsSourceHealth reports health for a single metrics source (pods or nodes)
type MetricsSourceHealth struct {
	Collecting        bool   `json:"collecting"`
	LastSuccess       string `json:"lastSuccess,omitempty"` // RFC3339
	ConsecutiveErrors int    `json:"consecutiveErrors"`
	LastError         string `json:"lastError,omitempty"`
	TrackedCount      int    `json:"trackedCount"`      // number of pods/nodes with history buffers
	TotalDataPoints   int    `json:"totalDataPoints"`   // total data points stored across all buffers
}

// CollectionHealth returns the current health of metrics collection
func (s *MetricsHistoryStore) CollectionHealth() MetricsCollectionHealth {
	if s == nil {
		return MetricsCollectionHealth{}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	// Count total data points across all pod buffers
	podDataPoints := 0
	for _, podBuf := range s.podMetrics {
		for _, containerBuf := range podBuf.containers {
			podDataPoints += containerBuf.count
		}
	}

	// Count total data points across all node buffers
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

// parseCPU converts Kubernetes CPU string to nanocores
// e.g., "100m" -> 100000000, "1" -> 1000000000, "250n" -> 250
func parseCPU(s string) int64 {
	if s == "" {
		return 0
	}

	// Check for nanocores suffix
	if len(s) > 1 && s[len(s)-1] == 'n' {
		var n int64
		_, err := parseNumber(s[:len(s)-1], &n)
		if err == nil {
			return n
		}
		return 0
	}

	// Check for millicores suffix
	if len(s) > 1 && s[len(s)-1] == 'm' {
		var n int64
		_, err := parseNumber(s[:len(s)-1], &n)
		if err == nil {
			return n * 1000000 // millicores to nanocores
		}
		return 0
	}

	// Plain number (cores)
	var n float64
	_, err := parseFloat(s, &n)
	if err == nil {
		return int64(n * 1000000000) // cores to nanocores
	}

	return 0
}

// parseMemory converts Kubernetes memory string to bytes
func parseMemory(s string) int64 {
	if s == "" {
		return 0
	}

	// Binary suffixes
	suffixes := map[string]int64{
		"Ki": 1024,
		"Mi": 1024 * 1024,
		"Gi": 1024 * 1024 * 1024,
		"Ti": 1024 * 1024 * 1024 * 1024,
		"K":  1000,
		"M":  1000 * 1000,
		"G":  1000 * 1000 * 1000,
		"T":  1000 * 1000 * 1000 * 1000,
	}

	for suffix, multiplier := range suffixes {
		if len(s) > len(suffix) && s[len(s)-len(suffix):] == suffix {
			var n int64
			_, err := parseNumber(s[:len(s)-len(suffix)], &n)
			if err == nil {
				return n * multiplier
			}
			return 0
		}
	}

	// Plain bytes
	var n int64
	_, err := parseNumber(s, &n)
	if err == nil {
		return n
	}

	return 0
}

func parseNumber(s string, n *int64) (int, error) {
	return fmt.Sscanf(s, "%d", n)
}

func parseFloat(s string, f *float64) (int, error) {
	return fmt.Sscanf(s, "%f", f)
}
