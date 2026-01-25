package timeline

import (
	"testing"
	"time"
)

func TestCompiledFilter_ExcludeKinds(t *testing.T) {
	preset := &FilterPreset{
		Name:           "test",
		ExcludeKinds:   []string{"Lease", "Endpoints"},
		IncludeManaged: true, // Include managed so Pod isn't filtered out
	}

	cf, err := CompileFilter(preset)
	if err != nil {
		t.Fatalf("CompileFilter failed: %v", err)
	}

	tests := []struct {
		kind     string
		expected bool // true = should pass filter
	}{
		{"Deployment", true},
		{"Pod", true},          // Pod passes (not in exclude list)
		{"Lease", false},       // excluded
		{"Endpoints", false},   // excluded
		{"EndpointSlice", true}, // not in exclude list
	}

	for _, tt := range tests {
		event := &TimelineEvent{Kind: tt.kind}
		result := cf.Matches(event)
		if result != tt.expected {
			t.Errorf("Kind=%s: expected %v, got %v", tt.kind, tt.expected, result)
		}
	}
}

func TestCompiledFilter_IncludeKinds(t *testing.T) {
	preset := &FilterPreset{
		Name:         "workloads-only",
		IncludeKinds: []string{"Deployment", "StatefulSet", "DaemonSet"},
	}

	cf, err := CompileFilter(preset)
	if err != nil {
		t.Fatalf("CompileFilter failed: %v", err)
	}

	tests := []struct {
		kind     string
		expected bool
	}{
		{"Deployment", true},
		{"StatefulSet", true},
		{"DaemonSet", true},
		{"Pod", false},         // not in include list
		{"Service", false},     // not in include list
		{"ConfigMap", false},   // not in include list
	}

	for _, tt := range tests {
		event := &TimelineEvent{Kind: tt.kind}
		result := cf.Matches(event)
		if result != tt.expected {
			t.Errorf("Kind=%s: expected %v, got %v", tt.kind, tt.expected, result)
		}
	}
}

func TestCompiledFilter_ExcludeNamePatterns(t *testing.T) {
	preset := &FilterPreset{
		Name:                "test",
		ExcludeNamePatterns: []string{"-lock$", "-lease$", "^kube-"},
	}

	cf, err := CompileFilter(preset)
	if err != nil {
		t.Fatalf("CompileFilter failed: %v", err)
	}

	tests := []struct {
		name     string
		expected bool
	}{
		{"my-app", true},
		{"nginx-deployment", true},
		{"controller-lock", false},      // ends with -lock
		{"leader-lease", false},         // ends with -lease
		{"kube-proxy", false},           // starts with kube-
		{"kube-system-lock", false},     // starts with kube-
		{"my-app-locked", true},         // doesn't match -lock$ pattern
	}

	for _, tt := range tests {
		event := &TimelineEvent{Name: tt.name}
		result := cf.Matches(event)
		if result != tt.expected {
			t.Errorf("Name=%s: expected %v, got %v", tt.name, tt.expected, result)
		}
	}
}

func TestCompiledFilter_IncludeManaged(t *testing.T) {
	// Note: CompiledFilter.Matches() no longer checks IncludeManaged.
	// IncludeManaged is now handled by the store's Query method to allow
	// query options (opts.IncludeManaged) to override preset settings.
	// This test verifies that cf.Matches() passes all events regardless of IsManaged().

	// Preset with IncludeManaged=false (default behavior)
	presetWithout := &FilterPreset{
		Name:           "without-managed",
		IncludeManaged: false,
	}

	cfWithout, _ := CompileFilter(presetWithout)

	// Event that is managed (has owner)
	managedEvent := &TimelineEvent{
		Kind:  "Pod",
		Name:  "my-pod",
		Owner: &OwnerInfo{Kind: "Deployment", Name: "my-deploy"},
	}

	// Event that is not managed
	unmanagedEvent := &TimelineEvent{
		Kind: "Deployment",
		Name: "my-deploy",
	}

	// Pod is always considered managed (even without owner)
	podEvent := &TimelineEvent{
		Kind: "Pod",
		Name: "standalone-pod",
	}

	// ReplicaSet is always considered managed
	rsEvent := &TimelineEvent{
		Kind: "ReplicaSet",
		Name: "my-rs",
	}

	// cf.Matches() should pass all events - IncludeManaged is checked at Query level
	if !cfWithout.Matches(managedEvent) {
		t.Error("cf.Matches should pass managed events (filtering happens at Query level)")
	}
	if !cfWithout.Matches(unmanagedEvent) {
		t.Error("Unmanaged event should pass filter")
	}
	if !cfWithout.Matches(podEvent) {
		t.Error("cf.Matches should pass Pod events (filtering happens at Query level)")
	}
	if !cfWithout.Matches(rsEvent) {
		t.Error("cf.Matches should pass ReplicaSet events (filtering happens at Query level)")
	}
}

func TestIsManaged(t *testing.T) {
	tests := []struct {
		event    TimelineEvent
		expected bool
	}{
		// Resources with owners are managed
		{TimelineEvent{Kind: "Pod", Owner: &OwnerInfo{Kind: "Deployment", Name: "x"}}, true},

		// Pods are always managed (even without owner)
		{TimelineEvent{Kind: "Pod"}, true},

		// ReplicaSets are always managed
		{TimelineEvent{Kind: "ReplicaSet"}, true},

		// Events are always managed
		{TimelineEvent{Kind: "Event"}, true},

		// Deployments without owner are not managed
		{TimelineEvent{Kind: "Deployment"}, false},

		// Services without owner are not managed
		{TimelineEvent{Kind: "Service"}, false},

		// ConfigMaps without owner are not managed
		{TimelineEvent{Kind: "ConfigMap"}, false},
	}

	for _, tt := range tests {
		result := tt.event.IsManaged()
		if result != tt.expected {
			t.Errorf("Kind=%s, HasOwner=%v: expected IsManaged()=%v, got %v",
				tt.event.Kind, tt.event.Owner != nil, tt.expected, result)
		}
	}
}

func TestDefaultFilterPreset(t *testing.T) {
	presets := DefaultFilterPresets()

	// Verify "default" preset exists
	defaultPreset, ok := presets["default"]
	if !ok {
		t.Fatal("default preset should exist")
	}

	// Verify it excludes expected kinds
	expectedExcluded := []string{"Lease", "Endpoints", "EndpointSlice"}
	for _, kind := range expectedExcluded {
		found := false
		for _, k := range defaultPreset.ExcludeKinds {
			if k == kind {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("default preset should exclude %s", kind)
		}
	}

	// Verify it has name patterns
	if len(defaultPreset.ExcludeNamePatterns) == 0 {
		t.Error("default preset should have ExcludeNamePatterns")
	}

	// Verify IncludeManaged is false by default
	if defaultPreset.IncludeManaged {
		t.Error("default preset should have IncludeManaged=false")
	}

	// Verify "all" preset exists and includes managed
	allPreset, ok := presets["all"]
	if !ok {
		t.Fatal("all preset should exist")
	}
	if !allPreset.IncludeManaged {
		t.Error("all preset should have IncludeManaged=true")
	}
}

func TestCompileFilter_InvalidRegex(t *testing.T) {
	preset := &FilterPreset{
		Name:                "invalid",
		ExcludeNamePatterns: []string{"[invalid"},
	}

	_, err := CompileFilter(preset)
	if err == nil {
		t.Error("Expected error for invalid regex pattern")
	}
}

func TestCompiledFilter_NilFilter(t *testing.T) {
	var cf *CompiledFilter = nil

	event := &TimelineEvent{Kind: "Pod", Name: "test"}

	// Nil filter should always return true (no filtering)
	if !cf.Matches(event) {
		t.Error("Nil filter should match all events")
	}
}

func TestCompiledFilter_NilPreset(t *testing.T) {
	cf, err := CompileFilter(nil)
	if err != nil {
		t.Fatalf("CompileFilter(nil) failed: %v", err)
	}
	if cf != nil {
		t.Error("CompileFilter(nil) should return nil")
	}
}

func TestCompiledFilter_IncludeEventTypes(t *testing.T) {
	preset := &FilterPreset{
		Name:              "warnings-only",
		IncludeEventTypes: []EventType{EventTypeWarning},
		IncludeManaged:    true,
	}

	cf, err := CompileFilter(preset)
	if err != nil {
		t.Fatalf("CompileFilter failed: %v", err)
	}

	tests := []struct {
		eventType EventType
		expected  bool
	}{
		{EventTypeWarning, true},
		{EventTypeNormal, false},
		{EventTypeAdd, false},
		{EventTypeUpdate, false},
		{EventTypeDelete, false},
	}

	for _, tt := range tests {
		event := &TimelineEvent{Kind: "Event", EventType: tt.eventType}
		result := cf.Matches(event)
		if result != tt.expected {
			t.Errorf("EventType=%s: expected %v, got %v", tt.eventType, tt.expected, result)
		}
	}
}

func TestCompiledFilter_ExcludeOperations(t *testing.T) {
	preset := &FilterPreset{
		Name:              "no-updates",
		ExcludeOperations: []EventType{EventTypeUpdate},
		IncludeManaged:    true,
	}

	cf, err := CompileFilter(preset)
	if err != nil {
		t.Fatalf("CompileFilter failed: %v", err)
	}

	tests := []struct {
		eventType EventType
		expected  bool
	}{
		{EventTypeAdd, true},
		{EventTypeDelete, true},
		{EventTypeUpdate, false}, // excluded
	}

	for _, tt := range tests {
		event := &TimelineEvent{Kind: "Deployment", EventType: tt.eventType}
		result := cf.Matches(event)
		if result != tt.expected {
			t.Errorf("EventType=%s: expected %v, got %v", tt.eventType, tt.expected, result)
		}
	}
}

func TestResourceKey(t *testing.T) {
	tests := []struct {
		kind, namespace, name string
		expected              string
	}{
		{"Pod", "default", "nginx", "Pod/default/nginx"},
		{"Deployment", "prod", "api", "Deployment/prod/api"},
		{"Node", "", "node-1", "Node//node-1"},
	}

	for _, tt := range tests {
		result := ResourceKey(tt.kind, tt.namespace, tt.name)
		if result != tt.expected {
			t.Errorf("ResourceKey(%s, %s, %s) = %s, expected %s",
				tt.kind, tt.namespace, tt.name, result, tt.expected)
		}
	}
}

func TestTimelineEvent_IsToplevelWorkload(t *testing.T) {
	tests := []struct {
		kind     string
		expected bool
	}{
		{"Deployment", true},
		{"DaemonSet", true},
		{"StatefulSet", true},
		{"Service", true},
		{"Job", true},
		{"CronJob", true},
		{"Rollout", true},
		{"Workflow", true},
		{"CronWorkflow", true},
		{"Pod", false},
		{"ReplicaSet", false},
		{"ConfigMap", false},
		{"Secret", false},
	}

	for _, tt := range tests {
		event := TimelineEvent{Kind: tt.kind}
		result := event.IsToplevelWorkload()
		if result != tt.expected {
			t.Errorf("Kind=%s: expected IsToplevelWorkload()=%v, got %v", tt.kind, tt.expected, result)
		}
	}
}

func TestTimelineEvent_GetAppLabel(t *testing.T) {
	tests := []struct {
		labels   map[string]string
		expected string
	}{
		{nil, ""},
		{map[string]string{}, ""},
		{map[string]string{"other": "value"}, ""},
		{map[string]string{"app": "myapp"}, "myapp"},
		{map[string]string{"app.kubernetes.io/name": "myapp"}, "myapp"},
		// app.kubernetes.io/name takes precedence
		{map[string]string{"app": "legacyapp", "app.kubernetes.io/name": "newapp"}, "newapp"},
	}

	for _, tt := range tests {
		event := TimelineEvent{Labels: tt.labels}
		result := event.GetAppLabel()
		if result != tt.expected {
			t.Errorf("Labels=%v: expected GetAppLabel()=%q, got %q", tt.labels, tt.expected, result)
		}
	}
}

func TestGroupEvents_ByNamespace(t *testing.T) {
	events := []TimelineEvent{
		{ID: "1", Kind: "Deployment", Namespace: "default", Name: "deploy-1", Timestamp: time.Now()},
		{ID: "2", Kind: "Deployment", Namespace: "prod", Name: "deploy-2", Timestamp: time.Now()},
		{ID: "3", Kind: "Deployment", Namespace: "default", Name: "deploy-3", Timestamp: time.Now()},
	}

	groups := groupEvents(events, GroupByNamespace)

	if len(groups) != 2 {
		t.Errorf("Expected 2 groups, got %d", len(groups))
	}

	// Find groups by namespace
	defaultGroup := findGroup(groups, "default")
	prodGroup := findGroup(groups, "prod")

	if defaultGroup == nil {
		t.Error("Expected group for 'default' namespace")
	} else if defaultGroup.EventCount != 2 {
		t.Errorf("Expected 2 events in default, got %d", defaultGroup.EventCount)
	}

	if prodGroup == nil {
		t.Error("Expected group for 'prod' namespace")
	} else if prodGroup.EventCount != 1 {
		t.Errorf("Expected 1 event in prod, got %d", prodGroup.EventCount)
	}
}

func TestGroupEvents_ByApp(t *testing.T) {
	events := []TimelineEvent{
		{ID: "1", Kind: "Deployment", Namespace: "default", Name: "deploy-1", Labels: map[string]string{"app": "frontend"}, Timestamp: time.Now()},
		{ID: "2", Kind: "Deployment", Namespace: "default", Name: "deploy-2", Labels: map[string]string{"app": "backend"}, Timestamp: time.Now()},
		{ID: "3", Kind: "Deployment", Namespace: "default", Name: "deploy-3", Labels: map[string]string{"app": "frontend"}, Timestamp: time.Now()},
		{ID: "4", Kind: "ConfigMap", Namespace: "default", Name: "config-1", Timestamp: time.Now()}, // no app label
	}

	groups := groupEvents(events, GroupByApp)

	if len(groups) != 3 {
		t.Errorf("Expected 3 groups, got %d", len(groups))
	}

	// Find frontend group
	frontendGroup := findGroupByName(groups, "frontend")
	if frontendGroup == nil {
		t.Error("Expected group for 'frontend' app")
	} else if frontendGroup.EventCount != 2 {
		t.Errorf("Expected 2 events in frontend, got %d", frontendGroup.EventCount)
	}
}

func findGroup(groups []EventGroup, id string) *EventGroup {
	for i := range groups {
		if groups[i].ID == id {
			return &groups[i]
		}
	}
	return nil
}

func findGroupByName(groups []EventGroup, name string) *EventGroup {
	for i := range groups {
		if groups[i].Name == name {
			return &groups[i]
		}
	}
	return nil
}
