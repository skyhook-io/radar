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
	CPU       int64     `json:"cpu"`       // CPU in nanocores
	Memory    int64     `json:"memory"`    // Memory in bytes
}

// ContainerMetricsHistory holds historical metrics for a container
type ContainerMetricsHistory struct {
	Name       string             `json:"name"`
	DataPoints []MetricsDataPoint `json:"dataPoints"`
}

// PodMetricsHistory holds historical metrics for a pod
type PodMetricsHistory struct {
	Namespace  string                    `json:"namespace"`
	Name       string                    `json:"name"`
	Containers []ContainerMetricsHistory `json:"containers"`
}

// NodeMetricsHistory holds historical metrics for a node
type NodeMetricsHistory struct {
	Name       string             `json:"name"`
	DataPoints []MetricsDataPoint `json:"dataPoints"`
}

// MetricsHistoryStore stores historical metrics data
type MetricsHistoryStore struct {
	mu sync.RWMutex

	// Pod metrics: key = "namespace/name"
	podMetrics map[string]*podMetricsBuffer

	// Node metrics: key = node name
	nodeMetrics map[string]*nodeMetricsBuffer

	// Control
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// podMetricsBuffer holds ring buffers for each container in a pod
type podMetricsBuffer struct {
	namespace  string
	name       string
	containers map[string]*ringBuffer // container name -> ring buffer
}

// nodeMetricsBuffer holds a ring buffer for a node
type nodeMetricsBuffer struct {
	name   string
	buffer *ringBuffer
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
	metricsHistoryOnce  sync.Once
)

// InitMetricsHistory initializes the metrics history store and starts polling
func InitMetricsHistory() {
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
		close(metricsHistoryStore.stopCh)
		metricsHistoryStore.wg.Wait()
		log.Println("Metrics history collection stopped")
	}
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

	// Collect pod metrics
	s.collectPodMetrics(ctx, now)

	// Collect node metrics
	s.collectNodeMetrics(ctx, now)
}

func (s *MetricsHistoryStore) collectPodMetrics(ctx context.Context, now time.Time) {
	client := GetDynamicClient()
	if client == nil {
		return
	}

	// List all pod metrics
	result, err := client.Resource(podMetricsGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		// Metrics server might not be installed, don't spam logs
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

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

		// Extract container metrics
		containers, ok := item.Object["containers"].([]interface{})
		if !ok {
			continue
		}

		for _, c := range containers {
			container, ok := c.(map[string]interface{})
			if !ok {
				continue
			}

			containerName, _ := container["name"].(string)
			if containerName == "" {
				continue
			}

			usage, ok := container["usage"].(map[string]interface{})
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
}

func (s *MetricsHistoryStore) collectNodeMetrics(ctx context.Context, now time.Time) {
	client := GetDynamicClient()
	if client == nil {
		return
	}

	// List all node metrics
	result, err := client.Resource(nodeMetricsGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

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

		usage, ok := item.Object["usage"].(map[string]interface{})
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
