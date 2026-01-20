package k8s

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ChangeRecord represents a recorded resource change with optional diff
type ChangeRecord struct {
	ID          string     `json:"id"`
	Kind        string     `json:"kind"`
	Namespace   string     `json:"namespace"`
	Name        string     `json:"name"`
	Operation   string     `json:"operation"` // add, update, delete
	Timestamp   time.Time  `json:"timestamp"`
	Diff        *DiffInfo  `json:"diff,omitempty"`
	HealthState string     `json:"healthState"` // healthy, degraded, unhealthy, unknown
	Owner       *OwnerInfo `json:"owner,omitempty"`
}

// OwnerInfo represents the owner/controller of a resource
type OwnerInfo struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// IsManaged returns true if this resource is managed by another (RS, Pod)
func (c *ChangeRecord) IsManaged() bool {
	return c.Owner != nil || c.Kind == "ReplicaSet" || c.Kind == "Pod" || c.Kind == "Event"
}

// IsToplevelWorkload returns true if this is a top-level workload
func (c *ChangeRecord) IsToplevelWorkload() bool {
	switch c.Kind {
	case "Deployment", "DaemonSet", "StatefulSet", "Service", "Ingress", "ConfigMap", "Secret", "Job", "CronJob":
		return true
	}
	return false
}

// DiffInfo contains the diff details for an update operation
type DiffInfo struct {
	Fields  []FieldChange `json:"fields"`
	Summary string        `json:"summary"`
}

// FieldChange represents a single field that changed
type FieldChange struct {
	Path     string `json:"path"`
	OldValue any    `json:"oldValue"`
	NewValue any    `json:"newValue"`
}

// ChangeHistory stores resource changes in a ring buffer
type ChangeHistory struct {
	records       []ChangeRecord
	maxSize       int
	head          int // next write position
	count         int
	mu            sync.RWMutex
	previousSpecs map[string]any // key: kind/namespace/name -> previous spec for diff
	specMu        sync.RWMutex
	persistPath   string // if set, persist to this file
}

var (
	changeHistory     *ChangeHistory
	changeHistoryOnce sync.Once
)

// InitChangeHistory initializes the global change history store
func InitChangeHistory(maxSize int, persistPath string) {
	changeHistoryOnce.Do(func() {
		changeHistory = &ChangeHistory{
			records:       make([]ChangeRecord, maxSize),
			maxSize:       maxSize,
			previousSpecs: make(map[string]any),
			persistPath:   persistPath,
		}
		if persistPath != "" {
			changeHistory.loadFromFile()
		}
	})
}

// GetChangeHistory returns the global change history instance
func GetChangeHistory() *ChangeHistory {
	return changeHistory
}

// resourceKey generates a unique key for a resource
func resourceKey(kind, namespace, name string) string {
	return fmt.Sprintf("%s/%s/%s", kind, namespace, name)
}

// RecordChange records a resource change and computes diff if applicable
func (h *ChangeHistory) RecordChange(kind, namespace, name, operation string, oldObj, newObj any) *ChangeRecord {
	if h == nil {
		return nil
	}

	record := ChangeRecord{
		ID:          uuid.New().String(),
		Kind:        kind,
		Namespace:   namespace,
		Name:        name,
		Operation:   operation,
		Timestamp:   time.Now(),
		HealthState: "unknown",
	}

	key := resourceKey(kind, namespace, name)

	// Compute diff for updates
	if operation == "update" && oldObj != nil && newObj != nil {
		record.Diff = h.computeDiff(kind, oldObj, newObj)
	}

	// Determine health state from new object
	if newObj != nil {
		record.HealthState = determineHealthState(kind, newObj)
	}

	// Extract owner reference
	obj := newObj
	if obj == nil {
		obj = oldObj
	}
	if obj != nil {
		record.Owner = extractOwner(obj)
	}

	// Store current spec for future diffs
	if operation != "delete" && newObj != nil {
		h.specMu.Lock()
		h.previousSpecs[key] = extractKeySpec(kind, newObj)
		h.specMu.Unlock()
	} else if operation == "delete" {
		h.specMu.Lock()
		delete(h.previousSpecs, key)
		h.specMu.Unlock()
	}

	// Add to ring buffer
	h.mu.Lock()
	h.records[h.head] = record
	h.head = (h.head + 1) % h.maxSize
	if h.count < h.maxSize {
		h.count++
	}
	h.mu.Unlock()

	// Persist if enabled
	if h.persistPath != "" {
		h.appendToFile(record)
	}

	return &record
}

// GetChangesOptions configures the GetChanges query
type GetChangesOptions struct {
	Namespace      string
	Kind           string
	Since          time.Time
	Limit          int
	IncludeManaged bool // If false, filter out ReplicaSets, Pods, etc.
}

// GetChanges retrieves changes matching the given filters
func (h *ChangeHistory) GetChanges(opts GetChangesOptions) []ChangeRecord {
	if h == nil {
		return nil
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	limit := opts.Limit
	if limit <= 0 {
		limit = 200
	}

	results := make([]ChangeRecord, 0, limit)

	// Iterate backwards from most recent
	for i := 0; i < h.count && len(results) < limit; i++ {
		idx := (h.head - 1 - i + h.maxSize) % h.maxSize
		record := h.records[idx]

		// Skip empty records
		if record.ID == "" {
			continue
		}

		// Skip if before since time
		if !opts.Since.IsZero() && record.Timestamp.Before(opts.Since) {
			continue
		}

		// Filter by namespace
		if opts.Namespace != "" && record.Namespace != opts.Namespace {
			continue
		}

		// Filter by kind
		if opts.Kind != "" && record.Kind != opts.Kind {
			continue
		}

		// Filter out managed resources unless explicitly requested
		if !opts.IncludeManaged && record.IsManaged() {
			continue
		}

		results = append(results, record)
	}

	return results
}

// GetChangesForOwner retrieves changes for resources owned by the given owner
func (h *ChangeHistory) GetChangesForOwner(ownerKind, ownerNamespace, ownerName string, since time.Time, limit int) []ChangeRecord {
	if h == nil {
		return nil
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	if limit <= 0 {
		limit = 100
	}

	results := make([]ChangeRecord, 0, limit)

	for i := 0; i < h.count && len(results) < limit; i++ {
		idx := (h.head - 1 - i + h.maxSize) % h.maxSize
		record := h.records[idx]

		if record.ID == "" {
			continue
		}

		if !since.IsZero() && record.Timestamp.Before(since) {
			continue
		}

		if record.Namespace != ownerNamespace {
			continue
		}

		// Check if this record's owner matches
		if record.Owner != nil && record.Owner.Kind == ownerKind && record.Owner.Name == ownerName {
			results = append(results, record)
		}
	}

	return results
}

// computeDiff computes the diff between old and new objects based on kind
func (h *ChangeHistory) computeDiff(kind string, oldObj, newObj any) *DiffInfo {
	var changes []FieldChange
	var summaryParts []string

	switch kind {
	case "Deployment":
		changes, summaryParts = diffDeployment(oldObj, newObj)
	case "Pod":
		changes, summaryParts = diffPod(oldObj, newObj)
	case "Service":
		changes, summaryParts = diffService(oldObj, newObj)
	case "ConfigMap":
		changes, summaryParts = diffConfigMap(oldObj, newObj)
	case "Ingress":
		changes, summaryParts = diffIngress(oldObj, newObj)
	case "ReplicaSet":
		changes, summaryParts = diffReplicaSet(oldObj, newObj)
	case "DaemonSet":
		changes, summaryParts = diffDaemonSet(oldObj, newObj)
	case "StatefulSet":
		changes, summaryParts = diffStatefulSet(oldObj, newObj)
	default:
		return nil
	}

	if len(changes) == 0 {
		return nil
	}

	summary := ""
	if len(summaryParts) > 0 {
		for i, part := range summaryParts {
			if i > 0 {
				summary += ", "
			}
			summary += part
		}
	}

	return &DiffInfo{
		Fields:  changes,
		Summary: summary,
	}
}

// diffDeployment computes diff for Deployment resources
func diffDeployment(oldObj, newObj any) ([]FieldChange, []string) {
	oldDep, ok1 := oldObj.(*appsv1.Deployment)
	newDep, ok2 := newObj.(*appsv1.Deployment)
	if !ok1 || !ok2 {
		return nil, nil
	}

	var changes []FieldChange
	var summary []string

	// Check replicas
	oldReplicas := int32(1)
	newReplicas := int32(1)
	if oldDep.Spec.Replicas != nil {
		oldReplicas = *oldDep.Spec.Replicas
	}
	if newDep.Spec.Replicas != nil {
		newReplicas = *newDep.Spec.Replicas
	}
	if oldReplicas != newReplicas {
		changes = append(changes, FieldChange{
			Path:     "spec.replicas",
			OldValue: oldReplicas,
			NewValue: newReplicas,
		})
		summary = append(summary, fmt.Sprintf("replicas: %d→%d", oldReplicas, newReplicas))
	}

	// Check container images
	oldImages := getContainerImages(oldDep.Spec.Template.Spec.Containers)
	newImages := getContainerImages(newDep.Spec.Template.Spec.Containers)
	if !equalStringMaps(oldImages, newImages) {
		for name, oldImg := range oldImages {
			if newImg, ok := newImages[name]; ok && oldImg != newImg {
				changes = append(changes, FieldChange{
					Path:     fmt.Sprintf("spec.template.spec.containers[%s].image", name),
					OldValue: oldImg,
					NewValue: newImg,
				})
				summary = append(summary, fmt.Sprintf("image(%s): %s→%s", name, truncateImage(oldImg), truncateImage(newImg)))
			}
		}
	}

	// Check resource limits/requests
	oldResources := getContainerResources(oldDep.Spec.Template.Spec.Containers)
	newResources := getContainerResources(newDep.Spec.Template.Spec.Containers)
	if !equalResourceMaps(oldResources, newResources) {
		changes = append(changes, FieldChange{
			Path:     "spec.template.spec.containers[*].resources",
			OldValue: oldResources,
			NewValue: newResources,
		})
		summary = append(summary, "resources changed")
	}

	return changes, summary
}

// diffPod computes diff for Pod resources
func diffPod(oldObj, newObj any) ([]FieldChange, []string) {
	oldPod, ok1 := oldObj.(*corev1.Pod)
	newPod, ok2 := newObj.(*corev1.Pod)
	if !ok1 || !ok2 {
		return nil, nil
	}

	var changes []FieldChange
	var summary []string

	// Check phase
	if oldPod.Status.Phase != newPod.Status.Phase {
		changes = append(changes, FieldChange{
			Path:     "status.phase",
			OldValue: string(oldPod.Status.Phase),
			NewValue: string(newPod.Status.Phase),
		})
		summary = append(summary, fmt.Sprintf("phase: %s→%s", oldPod.Status.Phase, newPod.Status.Phase))
	}

	// Check restart counts
	oldRestarts := getTotalRestarts(oldPod.Status.ContainerStatuses)
	newRestarts := getTotalRestarts(newPod.Status.ContainerStatuses)
	if oldRestarts != newRestarts {
		changes = append(changes, FieldChange{
			Path:     "status.containerStatuses[*].restartCount",
			OldValue: oldRestarts,
			NewValue: newRestarts,
		})
		summary = append(summary, fmt.Sprintf("restarts: %d→%d", oldRestarts, newRestarts))
	}

	return changes, summary
}

// diffService computes diff for Service resources
func diffService(oldObj, newObj any) ([]FieldChange, []string) {
	oldSvc, ok1 := oldObj.(*corev1.Service)
	newSvc, ok2 := newObj.(*corev1.Service)
	if !ok1 || !ok2 {
		return nil, nil
	}

	var changes []FieldChange
	var summary []string

	// Check type
	if oldSvc.Spec.Type != newSvc.Spec.Type {
		changes = append(changes, FieldChange{
			Path:     "spec.type",
			OldValue: string(oldSvc.Spec.Type),
			NewValue: string(newSvc.Spec.Type),
		})
		summary = append(summary, fmt.Sprintf("type: %s→%s", oldSvc.Spec.Type, newSvc.Spec.Type))
	}

	// Check ports
	oldPorts := getServicePorts(oldSvc.Spec.Ports)
	newPorts := getServicePorts(newSvc.Spec.Ports)
	if !equalStringSlices(oldPorts, newPorts) {
		changes = append(changes, FieldChange{
			Path:     "spec.ports",
			OldValue: oldPorts,
			NewValue: newPorts,
		})
		summary = append(summary, "ports changed")
	}

	// Check selector
	if !equalStringMaps(oldSvc.Spec.Selector, newSvc.Spec.Selector) {
		changes = append(changes, FieldChange{
			Path:     "spec.selector",
			OldValue: oldSvc.Spec.Selector,
			NewValue: newSvc.Spec.Selector,
		})
		summary = append(summary, "selector changed")
	}

	return changes, summary
}

// diffConfigMap computes diff for ConfigMap resources
func diffConfigMap(oldObj, newObj any) ([]FieldChange, []string) {
	oldCM, ok1 := oldObj.(*corev1.ConfigMap)
	newCM, ok2 := newObj.(*corev1.ConfigMap)
	if !ok1 || !ok2 {
		return nil, nil
	}

	var changes []FieldChange
	var summary []string

	// Check data keys (not values for security)
	oldKeys := getMapKeys(oldCM.Data)
	newKeys := getMapKeys(newCM.Data)

	addedKeys := diffStringSlices(newKeys, oldKeys)
	removedKeys := diffStringSlices(oldKeys, newKeys)
	modifiedKeys := getModifiedKeys(oldCM.Data, newCM.Data)

	if len(addedKeys) > 0 {
		changes = append(changes, FieldChange{
			Path:     "data (added keys)",
			OldValue: nil,
			NewValue: addedKeys,
		})
		summary = append(summary, fmt.Sprintf("added keys: %v", addedKeys))
	}
	if len(removedKeys) > 0 {
		changes = append(changes, FieldChange{
			Path:     "data (removed keys)",
			OldValue: removedKeys,
			NewValue: nil,
		})
		summary = append(summary, fmt.Sprintf("removed keys: %v", removedKeys))
	}
	if len(modifiedKeys) > 0 {
		changes = append(changes, FieldChange{
			Path:     "data (modified keys)",
			OldValue: modifiedKeys,
			NewValue: modifiedKeys,
		})
		summary = append(summary, fmt.Sprintf("modified keys: %v", modifiedKeys))
	}

	return changes, summary
}

// diffIngress computes diff for Ingress resources
func diffIngress(oldObj, newObj any) ([]FieldChange, []string) {
	oldIng, ok1 := oldObj.(*networkingv1.Ingress)
	newIng, ok2 := newObj.(*networkingv1.Ingress)
	if !ok1 || !ok2 {
		return nil, nil
	}

	var changes []FieldChange
	var summary []string

	// Check rules count
	if len(oldIng.Spec.Rules) != len(newIng.Spec.Rules) {
		changes = append(changes, FieldChange{
			Path:     "spec.rules",
			OldValue: len(oldIng.Spec.Rules),
			NewValue: len(newIng.Spec.Rules),
		})
		summary = append(summary, fmt.Sprintf("rules: %d→%d", len(oldIng.Spec.Rules), len(newIng.Spec.Rules)))
	}

	// Check TLS
	oldTLS := len(oldIng.Spec.TLS)
	newTLS := len(newIng.Spec.TLS)
	if oldTLS != newTLS {
		changes = append(changes, FieldChange{
			Path:     "spec.tls",
			OldValue: oldTLS,
			NewValue: newTLS,
		})
		summary = append(summary, fmt.Sprintf("tls: %d→%d", oldTLS, newTLS))
	}

	return changes, summary
}

// diffReplicaSet computes diff for ReplicaSet resources
func diffReplicaSet(oldObj, newObj any) ([]FieldChange, []string) {
	oldRS, ok1 := oldObj.(*appsv1.ReplicaSet)
	newRS, ok2 := newObj.(*appsv1.ReplicaSet)
	if !ok1 || !ok2 {
		return nil, nil
	}

	var changes []FieldChange
	var summary []string

	// Check replicas
	oldReplicas := int32(1)
	newReplicas := int32(1)
	if oldRS.Spec.Replicas != nil {
		oldReplicas = *oldRS.Spec.Replicas
	}
	if newRS.Spec.Replicas != nil {
		newReplicas = *newRS.Spec.Replicas
	}
	if oldReplicas != newReplicas {
		changes = append(changes, FieldChange{
			Path:     "spec.replicas",
			OldValue: oldReplicas,
			NewValue: newReplicas,
		})
		summary = append(summary, fmt.Sprintf("replicas: %d→%d", oldReplicas, newReplicas))
	}

	// Check ready replicas
	if oldRS.Status.ReadyReplicas != newRS.Status.ReadyReplicas {
		changes = append(changes, FieldChange{
			Path:     "status.readyReplicas",
			OldValue: oldRS.Status.ReadyReplicas,
			NewValue: newRS.Status.ReadyReplicas,
		})
		summary = append(summary, fmt.Sprintf("ready: %d→%d", oldRS.Status.ReadyReplicas, newRS.Status.ReadyReplicas))
	}

	return changes, summary
}

// diffDaemonSet computes diff for DaemonSet resources
func diffDaemonSet(oldObj, newObj any) ([]FieldChange, []string) {
	oldDS, ok1 := oldObj.(*appsv1.DaemonSet)
	newDS, ok2 := newObj.(*appsv1.DaemonSet)
	if !ok1 || !ok2 {
		return nil, nil
	}

	var changes []FieldChange
	var summary []string

	// Check container images
	oldImages := getContainerImages(oldDS.Spec.Template.Spec.Containers)
	newImages := getContainerImages(newDS.Spec.Template.Spec.Containers)
	if !equalStringMaps(oldImages, newImages) {
		for name, oldImg := range oldImages {
			if newImg, ok := newImages[name]; ok && oldImg != newImg {
				changes = append(changes, FieldChange{
					Path:     fmt.Sprintf("spec.template.spec.containers[%s].image", name),
					OldValue: oldImg,
					NewValue: newImg,
				})
				summary = append(summary, fmt.Sprintf("image(%s): %s→%s", name, truncateImage(oldImg), truncateImage(newImg)))
			}
		}
	}

	// Check desired/ready
	if oldDS.Status.DesiredNumberScheduled != newDS.Status.DesiredNumberScheduled {
		changes = append(changes, FieldChange{
			Path:     "status.desiredNumberScheduled",
			OldValue: oldDS.Status.DesiredNumberScheduled,
			NewValue: newDS.Status.DesiredNumberScheduled,
		})
		summary = append(summary, fmt.Sprintf("desired: %d→%d", oldDS.Status.DesiredNumberScheduled, newDS.Status.DesiredNumberScheduled))
	}

	return changes, summary
}

// diffStatefulSet computes diff for StatefulSet resources
func diffStatefulSet(oldObj, newObj any) ([]FieldChange, []string) {
	oldSTS, ok1 := oldObj.(*appsv1.StatefulSet)
	newSTS, ok2 := newObj.(*appsv1.StatefulSet)
	if !ok1 || !ok2 {
		return nil, nil
	}

	var changes []FieldChange
	var summary []string

	// Check replicas
	oldReplicas := int32(1)
	newReplicas := int32(1)
	if oldSTS.Spec.Replicas != nil {
		oldReplicas = *oldSTS.Spec.Replicas
	}
	if newSTS.Spec.Replicas != nil {
		newReplicas = *newSTS.Spec.Replicas
	}
	if oldReplicas != newReplicas {
		changes = append(changes, FieldChange{
			Path:     "spec.replicas",
			OldValue: oldReplicas,
			NewValue: newReplicas,
		})
		summary = append(summary, fmt.Sprintf("replicas: %d→%d", oldReplicas, newReplicas))
	}

	// Check container images
	oldImages := getContainerImages(oldSTS.Spec.Template.Spec.Containers)
	newImages := getContainerImages(newSTS.Spec.Template.Spec.Containers)
	if !equalStringMaps(oldImages, newImages) {
		for name, oldImg := range oldImages {
			if newImg, ok := newImages[name]; ok && oldImg != newImg {
				changes = append(changes, FieldChange{
					Path:     fmt.Sprintf("spec.template.spec.containers[%s].image", name),
					OldValue: oldImg,
					NewValue: newImg,
				})
				summary = append(summary, fmt.Sprintf("image(%s): %s→%s", name, truncateImage(oldImg), truncateImage(newImg)))
			}
		}
	}

	return changes, summary
}

// Helper functions

// extractOwner gets the controller owner reference from an object
func extractOwner(obj any) *OwnerInfo {
	meta, ok := obj.(metav1.Object)
	if !ok {
		return nil
	}

	for _, ref := range meta.GetOwnerReferences() {
		if ref.Controller != nil && *ref.Controller {
			return &OwnerInfo{
				Kind: ref.Kind,
				Name: ref.Name,
			}
		}
	}
	return nil
}

func determineHealthState(kind string, obj any) string {
	switch kind {
	case "Pod":
		if pod, ok := obj.(*corev1.Pod); ok {
			switch pod.Status.Phase {
			case corev1.PodRunning:
				// Check if all containers are ready
				for _, cs := range pod.Status.ContainerStatuses {
					if !cs.Ready {
						return "degraded"
					}
				}
				return "healthy"
			case corev1.PodSucceeded:
				return "healthy"
			case corev1.PodFailed:
				return "unhealthy"
			case corev1.PodPending:
				return "degraded"
			}
		}
	case "Deployment":
		if dep, ok := obj.(*appsv1.Deployment); ok {
			desired := int32(1)
			if dep.Spec.Replicas != nil {
				desired = *dep.Spec.Replicas
			}
			if dep.Status.ReadyReplicas == desired && dep.Status.AvailableReplicas == desired {
				return "healthy"
			}
			if dep.Status.ReadyReplicas > 0 {
				return "degraded"
			}
			return "unhealthy"
		}
	case "ReplicaSet":
		if rs, ok := obj.(*appsv1.ReplicaSet); ok {
			desired := int32(1)
			if rs.Spec.Replicas != nil {
				desired = *rs.Spec.Replicas
			}
			if rs.Status.ReadyReplicas == desired {
				return "healthy"
			}
			if rs.Status.ReadyReplicas > 0 {
				return "degraded"
			}
			return "unhealthy"
		}
	}
	return "unknown"
}

func extractKeySpec(_ string, obj any) any {
	// Return a simplified version of the spec for future diff comparison
	// This is stored in memory, so we keep it minimal
	return obj
}

func getContainerImages(containers []corev1.Container) map[string]string {
	images := make(map[string]string)
	for _, c := range containers {
		images[c.Name] = c.Image
	}
	return images
}

func getContainerResources(containers []corev1.Container) map[string]any {
	resources := make(map[string]any)
	for _, c := range containers {
		resources[c.Name] = map[string]any{
			"limits":   c.Resources.Limits,
			"requests": c.Resources.Requests,
		}
	}
	return resources
}

func getTotalRestarts(statuses []corev1.ContainerStatus) int32 {
	var total int32
	for _, s := range statuses {
		total += s.RestartCount
	}
	return total
}

func getServicePorts(ports []corev1.ServicePort) []string {
	result := make([]string, len(ports))
	for i, p := range ports {
		result[i] = fmt.Sprintf("%s/%d→%d", p.Protocol, p.Port, p.TargetPort.IntVal)
	}
	return result
}

func getMapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func getModifiedKeys(old, new map[string]string) []string {
	var modified []string
	for k, oldV := range old {
		if newV, ok := new[k]; ok && oldV != newV {
			modified = append(modified, k)
		}
	}
	return modified
}

func diffStringSlices(a, b []string) []string {
	bMap := make(map[string]bool)
	for _, s := range b {
		bMap[s] = true
	}
	var diff []string
	for _, s := range a {
		if !bMap[s] {
			diff = append(diff, s)
		}
	}
	return diff
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalStringMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func equalResourceMaps(a, b map[string]any) bool {
	// Simple comparison - could be more sophisticated
	aJSON, _ := json.Marshal(a)
	bJSON, _ := json.Marshal(b)
	return string(aJSON) == string(bJSON)
}

func truncateImage(image string) string {
	// Show just the tag or digest if image is long
	if len(image) > 40 {
		// Try to find tag
		for i := len(image) - 1; i >= 0; i-- {
			if image[i] == ':' || image[i] == '@' {
				return "..." + image[i:]
			}
		}
		return image[:37] + "..."
	}
	return image
}

// File persistence

func (h *ChangeHistory) loadFromFile() {
	if h.persistPath == "" {
		return
	}

	data, err := os.ReadFile(h.persistPath)
	if err != nil {
		return // File doesn't exist yet
	}

	// Parse JSON lines
	var records []ChangeRecord
	for _, line := range splitLines(data) {
		if len(line) == 0 {
			continue
		}
		var record ChangeRecord
		if err := json.Unmarshal(line, &record); err == nil {
			records = append(records, record)
		}
	}

	// Load most recent records up to maxSize
	h.mu.Lock()
	defer h.mu.Unlock()

	start := 0
	if len(records) > h.maxSize {
		start = len(records) - h.maxSize
	}

	for i := start; i < len(records); i++ {
		h.records[h.head] = records[i]
		h.head = (h.head + 1) % h.maxSize
		if h.count < h.maxSize {
			h.count++
		}
	}
}

func (h *ChangeHistory) appendToFile(record ChangeRecord) {
	if h.persistPath == "" {
		return
	}

	// Ensure directory exists
	dir := filepath.Dir(h.persistPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}

	// Append JSON line
	data, err := json.Marshal(record)
	if err != nil {
		return
	}

	f, err := os.OpenFile(h.persistPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()

	f.Write(data)
	f.WriteString("\n")
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}
