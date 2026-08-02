package mcp

import (
	"context"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
)

func warningEvent(name, reason, message string, count int32, last time.Time, objKind, objNS, objName string) *corev1.Event {
	return &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: name, Namespace: objNS},
		Reason:         reason,
		Message:        message,
		Type:           "Warning",
		Count:          count,
		LastTimestamp:  metav1.Time{Time: last},
		InvolvedObject: corev1.ObjectReference{Kind: objKind, Namespace: objNS, Name: objName},
	}
}

// warningGroups must order by recency (not lifetime count) and carry the
// facts a consumer needs to judge each row: lastSeen and the involved
// objects. The old count-first ordering promoted a long-stale high-count
// group over the live incident, and the old shape gave no way to tell the
// two apart.
func TestBuildDashboard_WarningGroups_RecencyAndObjects(t *testing.T) {
	defer k8s.ResetTestState()

	ns := "shop"
	now := time.Now()

	client := fake.NewSimpleClientset(
		// Stale but noisy: a mount failure that stopped 2h ago, count 500.
		warningEvent("ev-noisy", "FailedMount", "MountVolume.SetUp failed for volume data", 500, now.Add(-2*time.Hour), "Pod", ns, "legacy-worker-0"),
		// Live incident: two pods of one workload, same normalized message,
		// most recent occurrences.
		warningEvent("ev-live-a", "BackOff", "Back-off restarting failed container in pod cart-5d4f7c9b8-aaaaa", 2, now.Add(-2*time.Minute), "Pod", ns, "cart-5d4f7c9b8-aaaaa"),
		warningEvent("ev-live-b", "BackOff", "Back-off restarting failed container in pod cart-5d4f7c9b8-bbbbb", 1, now.Add(-1*time.Minute), "Pod", ns, "cart-5d4f7c9b8-bbbbb"),
	)
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := k8s.GetResourceCache()

	var dashboard mcpDashboard
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		dashboard = buildDashboard(context.Background(), cache, ns, false, false)
		if len(dashboard.WarningGroups) >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(dashboard.WarningGroups) < 2 {
		t.Fatalf("warningGroups never populated: %+v", dashboard.WarningGroups)
	}

	live := dashboard.WarningGroups[0]
	if live.Reason != "BackOff" {
		t.Fatalf("warningGroups[0] = %+v, want the RECENT BackOff group first despite the stale group's higher count", dashboard.WarningGroups)
	}
	if live.Count != 3 {
		t.Errorf("live count = %d, want 3 (aggregated across both pods)", live.Count)
	}
	if live.LastSeen.IsZero() || now.Sub(live.LastSeen) > 5*time.Minute {
		t.Errorf("live lastSeen = %v, want ~1m ago", live.LastSeen)
	}
	if live.ObjectCount != 2 || len(live.Objects) != 2 {
		t.Fatalf("live objects = %+v (count %d), want both pods", live.Objects, live.ObjectCount)
	}
	if want := (mcpWarningObject{Kind: "Pod", Namespace: "shop", Name: "cart-5d4f7c9b8-bbbbb"}); live.Objects[0] != want {
		t.Errorf("objects[0] = %+v, want most-recent pod %+v", live.Objects[0], want)
	}

	stale := dashboard.WarningGroups[1]
	if stale.Reason != "FailedMount" {
		t.Fatalf("warningGroups[1] = %+v, want the stale FailedMount group", stale)
	}
	if stale.LastSeen.IsZero() || now.Sub(stale.LastSeen) < time.Hour {
		t.Errorf("stale lastSeen = %v — without an old lastSeen a consumer cannot tell this group is stale", stale.LastSeen)
	}
}

// The dashboard emits the FULL dedup window with no row selection: any
// pick-N heuristic creates unrecoverable omissions (a live incident or an
// active storm not making the cut — both observed in benchmark transcripts).
// Seven distinct groups must yield seven rows, recency-ordered, with the
// high-count storm present even though it is the oldest.
func TestBuildDashboard_WarningGroups_FullWindowNoSelection(t *testing.T) {
	defer k8s.ResetTestState()

	ns := "shop"
	now := time.Now()

	objs := make([]runtime.Object, 0, 7)
	for i := 0; i < 6; i++ {
		objs = append(objs, warningEvent(
			fmt.Sprintf("ev-fresh-%d", i), fmt.Sprintf("FreshReason%d", i),
			fmt.Sprintf("distinct message %d", i), 1,
			now.Add(-time.Duration(i+1)*time.Minute), "Pod", ns, fmt.Sprintf("pod-%d", i)))
	}
	// Oldest group in the window, but an active storm by count — a 5-row
	// selection under pure recency would have dropped it.
	objs = append(objs, warningEvent("ev-storm", "StormReason", "storm message", 400,
		now.Add(-10*time.Minute), "Pod", ns, "storm-pod"))

	client := fake.NewSimpleClientset(objs...)
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := k8s.GetResourceCache()

	var dashboard mcpDashboard
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		dashboard = buildDashboard(context.Background(), cache, ns, false, false)
		if len(dashboard.WarningGroups) >= 7 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(dashboard.WarningGroups) != 7 {
		t.Fatalf("warningGroups = %d rows, want all 7 groups (no selection layer): %+v",
			len(dashboard.WarningGroups), dashboard.WarningGroups)
	}
	for i := 1; i < len(dashboard.WarningGroups); i++ {
		if dashboard.WarningGroups[i].LastSeen.After(dashboard.WarningGroups[i-1].LastSeen) {
			t.Errorf("rows not recency-ordered at %d: %v after %v",
				i, dashboard.WarningGroups[i].LastSeen, dashboard.WarningGroups[i-1].LastSeen)
		}
	}
	if last := dashboard.WarningGroups[6]; last.Reason != "StormReason" || last.Count != 400 {
		t.Errorf("oldest row = %+v, want the storm group present with count 400", last)
	}
}

// Past the dashboard-owned 20-group cap, truncation must be SIGNALED, never
// silent: 21 distinct groups -> 20 rows, totalWarningGroups 21, truncated
// true — and the invariant truncated == (total > len(rows)) holds.
func TestBuildDashboard_WarningGroups_TruncationSignaled(t *testing.T) {
	defer k8s.ResetTestState()

	ns := "shop"
	now := time.Now()
	objs := make([]runtime.Object, 0, dashboardWarningGroupCap+1)
	for i := 0; i <= dashboardWarningGroupCap; i++ {
		objs = append(objs, warningEvent(
			fmt.Sprintf("ev-%02d", i), fmt.Sprintf("Reason%02d", i),
			fmt.Sprintf("distinct message %02d", i), 1,
			now.Add(-time.Duration(i)*time.Second), "Pod", ns, fmt.Sprintf("pod-%02d", i)))
	}
	if err := k8s.InitTestResourceCache(fake.NewSimpleClientset(objs...)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := k8s.GetResourceCache()

	var dashboard mcpDashboard
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		dashboard = buildDashboard(context.Background(), cache, ns, false, false)
		if len(dashboard.WarningGroups) >= dashboardWarningGroupCap {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if len(dashboard.WarningGroups) != dashboardWarningGroupCap {
		t.Fatalf("rows = %d, want capped at %d", len(dashboard.WarningGroups), dashboardWarningGroupCap)
	}
	if dashboard.TotalWarningGroups != dashboardWarningGroupCap+1 {
		t.Errorf("totalWarningGroups = %d, want %d (pre-cap distinct total)", dashboard.TotalWarningGroups, dashboardWarningGroupCap+1)
	}
	if !dashboard.WarningGroupsTruncated {
		t.Error("warningGroupsTruncated = false, want true")
	}
	if got, want := dashboard.WarningGroupsTruncated, dashboard.TotalWarningGroups > len(dashboard.WarningGroups); got != want {
		t.Errorf("invariant broken: truncated=%v but total>len(rows)=%v", got, want)
	}
}

// The emitted object ref derives the API group from apiVersion so agents can
// disambiguate colliding kinds when feeding it into get_resource.
func TestWarningObjectFromRef_GroupDerivation(t *testing.T) {
	cases := []struct {
		apiVersion string
		wantGroup  string
	}{
		{"apps/v1", "apps"},
		{"v1", ""},
		{"", ""},
		{"serving.knative.dev/v1", "serving.knative.dev"},
	}
	for _, tt := range cases {
		got := warningObjectFromRef(aicontext.EventObjectRef{Kind: "Service", APIVersion: tt.apiVersion, Namespace: "ns", Name: "x"})
		if got.Group != tt.wantGroup {
			t.Errorf("apiVersion %q: group = %q, want %q", tt.apiVersion, got.Group, tt.wantGroup)
		}
	}
}
