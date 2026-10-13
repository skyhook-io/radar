package igdebug

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/inspektor-gadget/inspektor-gadget/pkg/datasource"
	"github.com/inspektor-gadget/inspektor-gadget/pkg/operators"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	domain "github.com/skyhook-io/radar/pkg/igdebug"
)

func TestResolveTargetDefaultsToFirstRunningContainer(t *testing.T) {
	pod := targetPod("old-uid", "containerd://first-id", "containerd://second-id")
	gadget := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "gadget-node-a", Namespace: "gadget", Labels: map[string]string{"k8s-app": "gadget"}},
		Spec:       corev1.PodSpec{NodeName: "node-a"},
		Status:     corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}
	client := fake.NewSimpleClientset(pod, gadget)

	target, err := resolveTarget(context.Background(), client, "gadget", domain.Target{Namespace: "app", Pod: "api"})
	if err != nil {
		t.Fatal(err)
	}
	if target.Container != "first" || target.ContainerID != "first-id" || target.PodUID != "old-uid" || target.Node != "node-a" {
		t.Fatalf("unexpected target: %#v", target)
	}
}

func TestResolveTargetHonorsDefaultContainerAnnotation(t *testing.T) {
	pod := targetPod("old-uid", "containerd://proxy-id", "containerd://app-id")
	pod.Annotations = map[string]string{"kubectl.kubernetes.io/default-container": "second"}
	gadget := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "gadget-node-a", Namespace: "gadget", Labels: map[string]string{"k8s-app": "gadget"}},
		Spec:       corev1.PodSpec{NodeName: "node-a"},
		Status:     corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}
	target, err := resolveTarget(context.Background(), fake.NewSimpleClientset(pod, gadget), "gadget", domain.Target{Namespace: "app", Pod: "api"})
	if err != nil {
		t.Fatal(err)
	}
	if target.Container != "second" || target.ContainerID != "app-id" {
		t.Fatalf("unexpected target: %#v", target)
	}
}

func TestResolveTargetRejectsTerminatedContainerWithRetainedID(t *testing.T) {
	pod := targetPod("old-uid", "containerd://first-id", "")
	pod.Status.ContainerStatuses[0].State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}
	client := fake.NewSimpleClientset(pod)

	_, err := resolveTarget(context.Background(), client, "gadget", domain.Target{Namespace: "app", Pod: "api", Container: "first"})
	if err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("error = %v", err)
	}
}

func TestWatchTargetMarksRecreatedPodPartial(t *testing.T) {
	pod := targetPod("new-uid", "containerd://new-id", "")
	client := fake.NewSimpleClientset(pod)
	reporter := &captureReporter{partial: make(chan string, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go watchTarget(ctx, client, domain.Target{Namespace: "app", Pod: "api", PodUID: "old-uid", Node: "node-a", Container: "first", ContainerID: "old-id"}, reporter)

	select {
	case reason := <-reporter.partial:
		if !strings.Contains(reason, "replaced") {
			t.Fatalf("reason = %q", reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("target replacement was not detected")
	}
}

func TestWatchTargetToleratesTransientGetFailures(t *testing.T) {
	pod := targetPod("old-uid", "containerd://old-id", "")
	client := fake.NewSimpleClientset(pod)
	attempts := 0
	client.PrependReactor("get", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		attempts++
		if attempts < 3 {
			return true, nil, errors.New("temporary apiserver failure")
		}
		return false, nil, nil
	})
	reporter := &captureReporter{partial: make(chan string, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		watchTargetEvery(ctx, client, domain.Target{Namespace: "app", Pod: "api", PodUID: "old-uid", Node: "node-a", Container: "first", ContainerID: "old-id"}, reporter, time.Millisecond)
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	<-done
	select {
	case reason := <-reporter.partial:
		t.Fatalf("transient failures ended capture: %s", reason)
	default:
	}
	if attempts < 3 {
		t.Fatalf("attempts = %d, want at least 3", attempts)
	}
}

func TestVisibleEnricherStripsResourcesOutsideVisibleSet(t *testing.T) {
	enricher := &visibleEnricher{
		services: []corev1.Service{{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "app"}, Spec: corev1.ServiceSpec{ClusterIPs: []string{"10.0.0.10"}}}},
		pods:     []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "app"}, Status: corev1.PodStatus{PodIP: "10.0.0.2"}}},
	}
	rows := []domain.Connection{{
		Local:  domain.Endpoint{Address: "10.0.0.2", Resource: &domain.EndpointRef{Kind: "pod", Namespace: "other", Name: "secret"}},
		Remote: domain.Endpoint{Address: "10.0.0.10"},
	}}
	got := enricher.connections(rows)
	if got[0].Local.Resource == nil || got[0].Local.Resource.Namespace != "app" || got[0].Local.Resource.Name != "api" {
		t.Fatalf("local resource = %#v", got[0].Local.Resource)
	}
	if got[0].Remote.Resource == nil || got[0].Remote.Resource.Name != "db" {
		t.Fatalf("remote resource = %#v", got[0].Remote.Resource)
	}
}

func TestSnapshotDeadlineReportsPartialAfterStart(t *testing.T) {
	started := make(chan struct{})
	completed := make(chan struct{})
	reporter := &captureReporter{partial: make(chan string, 1)}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	go enforceSnapshotDeadline(ctx, started, completed, time.Millisecond, reporter, cancel)
	close(started)
	select {
	case reason := <-reporter.partial:
		if !strings.Contains(reason, "timed out") {
			t.Fatalf("reason = %q", reason)
		}
	case <-time.After(time.Second):
		t.Fatal("snapshot deadline did not fire")
	}
}

func targetPod(uid, firstID, secondID string) *corev1.Pod {
	statuses := []corev1.ContainerStatus{{
		Name: "first", ContainerID: firstID,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	}}
	if secondID != "" {
		statuses = append(statuses, corev1.ContainerStatus{
			Name: "second", ContainerID: secondID,
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		})
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "app", UID: types.UID(uid)},
		Spec: corev1.PodSpec{
			NodeName: "node-a",
			Containers: []corev1.Container{
				{Name: "first"},
				{Name: "second"},
			},
		},
		Status: corev1.PodStatus{ContainerStatuses: statuses},
	}
}

type captureReporter struct {
	partial chan string
	results []domain.Result
}

func (r *captureReporter) Started(domain.Target)       {}
func (r *captureReporter) Event(json.RawMessage)       {}
func (r *captureReporter) Result(result domain.Result) { r.results = append(r.results, result) }
func (r *captureReporter) Interrupted(reason string)   { r.partial <- reason }
func (r *captureReporter) Partial(reason string)       { r.partial <- reason }

func TestResolveTargetRejectsAChangedIncarnationBeforeInstrumenting(t *testing.T) {
	pod := targetPod("new-uid", "containerd://new-id", "")
	for _, target := range []domain.Target{
		{Namespace: "app", Pod: "api", Container: "first", PodUID: "old-uid"},
		{Namespace: "app", Pod: "api", Container: "first", PodUID: "new-uid", ContainerID: "containerd://old-id"},
	} {
		client := fake.NewSimpleClientset(pod)
		_, err := resolveTarget(context.Background(), client, "gadget", target)
		if !errors.Is(err, domain.ErrTargetChanged) {
			t.Fatalf("changed target = %v", err)
		}
		for _, action := range client.Actions() {
			if action.GetVerb() != "get" || action.GetResource().Resource != "pods" {
				t.Fatalf("accessed Gadget before validating the target: %#v", action)
			}
		}
	}
}

func TestEnrichmentMarksPaginationAsPartial(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		return true, &corev1.PodList{ListMeta: metav1.ListMeta{Continue: "next-page"}}, nil
	})
	result := loadVisibleEnricher(context.Background(), client, domain.Target{Namespace: "app"})
	if result.coverage != "partial" {
		t.Fatalf("a capped inventory claimed complete enrichment: %#v", result)
	}
}

func TestSocketTargetVerificationRejectsARecreatedPodWithTheSameName(t *testing.T) {
	pod := targetPod("new-uid", "containerd://same-id", "")
	client := fake.NewSimpleClientset(pod)
	target := domain.Target{Namespace: "app", Pod: "api", PodUID: "old-uid", Node: "node-a", Container: "first", ContainerID: "same-id"}
	if err := verifyTarget(context.Background(), client, target); !errors.Is(err, domain.ErrTargetChanged) {
		t.Fatalf("accepted another Pod incarnation: %v", err)
	}
}

type snapshotTestContext struct {
	operators.GadgetContext
	sources   map[string]datasource.DataSource
	cancelled bool
}

func (c *snapshotTestContext) GetDataSources() map[string]datasource.DataSource { return c.sources }
func (c *snapshotTestContext) Cancel()                                          { c.cancelled = true }

func TestSnapshotPublicationChecksTargetAndPreservesOnlyIdentifiableEvidence(t *testing.T) {
	for _, aspect := range []domain.Aspect{domain.AspectConnections, domain.AspectProcesses} {
		t.Run(string(aspect), func(t *testing.T) {
			source, err := datasource.New(datasource.TypeArray, "snapshot")
			if err != nil {
				t.Fatal(err)
			}
			ctx := &snapshotTestContext{sources: map[string]datasource.DataSource{"snapshot": source}}
			reporter := &captureReporter{partial: make(chan string, 1)}
			checks := 0
			op := snapshotOperator(aspect, domain.Target{}, &visibleEnricher{}, reporter, func() {}, func() {}, func() error { checks++; return domain.ErrTargetChanged })
			if _, err := op.InstantiateDataOperator(ctx, nil); err != nil {
				t.Fatal(err)
			}
			packet, err := source.NewPacketArray()
			if err != nil {
				t.Fatal(err)
			}
			if err := source.EmitAndRelease(packet); err != nil {
				t.Fatal(err)
			}
			wantResults := 1
			if aspect == domain.AspectConnections {
				wantResults = 0
			}
			if checks != 1 || len(reporter.results) != wantResults || !ctx.cancelled {
				t.Fatalf("checks=%d results=%d cancelled=%t", checks, len(reporter.results), ctx.cancelled)
			}
			select {
			case reason := <-reporter.partial:
				if !strings.Contains(reason, "before publishing") {
					t.Fatal(reason)
				}
			default:
				t.Fatal("target replacement was not reported")
			}
		})
	}
}

func TestCancelledSocketVerificationDoesNotInventAPartialReason(t *testing.T) {
	source, err := datasource.New(datasource.TypeArray, "snapshot")
	if err != nil {
		t.Fatal(err)
	}
	ctx := &snapshotTestContext{sources: map[string]datasource.DataSource{"snapshot": source}}
	reporter := &captureReporter{partial: make(chan string, 1)}
	op := snapshotOperator(domain.AspectConnections, domain.Target{}, &visibleEnricher{}, reporter, func() {}, func() {}, func() error { return context.Canceled })
	if _, err := op.InstantiateDataOperator(ctx, nil); err != nil {
		t.Fatal(err)
	}
	packet, err := source.NewPacketArray()
	if err != nil {
		t.Fatal(err)
	}
	if err := source.EmitAndRelease(packet); err != nil {
		t.Fatal(err)
	}
	if len(reporter.results) != 0 || !ctx.cancelled {
		t.Fatalf("unverified socket result published: %#v", reporter.results)
	}
	select {
	case reason := <-reporter.partial:
		t.Fatalf("cancellation became partial: %s", reason)
	default:
	}
}

func TestDNSPublicationRetainsFilteredEvidenceWhenTargetVerificationFails(t *testing.T) {
	source, err := datasource.New(datasource.TypeSingle, "dns")
	if err != nil {
		t.Fatal(err)
	}
	ctx := &snapshotTestContext{sources: map[string]datasource.DataSource{"dns": source}}
	reporter := &captureReporter{partial: make(chan string, 1)}
	checks := 0
	op := dnsOperator(domain.Target{ContainerID: "old-id"}, time.Second, &visibleEnricher{}, reporter, func() {}, func() {}, func() error { checks++; return domain.ErrTargetChanged })
	instance, err := op.InstantiateDataOperator(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.(interface {
		PostStop(operators.GadgetContext) error
	}).PostStop(ctx); err != nil {
		t.Fatal(err)
	}
	if checks != 1 || len(reporter.results) != 1 {
		t.Fatalf("checks=%d results=%d", checks, len(reporter.results))
	}
	select {
	case reason := <-reporter.partial:
		if !strings.Contains(reason, "before publishing") {
			t.Fatal(reason)
		}
	default:
		t.Fatal("target replacement was not qualified")
	}
}

func TestResolvedNetworkScopeUsesTargetContainersInsteadOfGadgetContainers(t *testing.T) {
	pod := targetPod("target-uid", "containerd://app-id", "containerd://sidecar-id")
	pod.Status.InitContainerStatuses = []corev1.ContainerStatus{
		{Name: "native-sidecar", ContainerID: "containerd://native-id", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
		{Name: "finished-init", ContainerID: "containerd://finished-id", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{}}},
	}
	pod.Status.EphemeralContainerStatuses = []corev1.ContainerStatus{
		{Name: "debug", ContainerID: "containerd://debug-id", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
	}
	gadget := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "gadget-node-a", Namespace: "gadget", Labels: map[string]string{"k8s-app": "gadget"}},
		Spec:       corev1.PodSpec{NodeName: "node-a"},
		Status: corev1.PodStatus{
			Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			ContainerStatuses: []corev1.ContainerStatus{{Name: "gadget", ContainerID: "containerd://gadget-id", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}},
		},
	}
	target, err := resolveTarget(context.Background(), fake.NewSimpleClientset(pod, gadget), "gadget", domain.Target{Namespace: "app", Pod: "api"})
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`[
		{"runtime":{"containerId":"app-id"},"src":{"port":8080}},
		{"runtime":{"containerId":"sidecar-id"},"src":{"port":8081}},
		{"runtime":{"containerId":"native-id"},"src":{"port":8082}},
		{"runtime":{"containerId":"debug-id"},"src":{"port":8083}},
		{"runtime":{"containerId":"gadget-id"},"src":{"port":9999}},
		{"runtime":{"containerId":"finished-id"},"src":{"port":9998}},
		{"runtime":{"containerId":"other-id"},"src":{"port":9997}},
		{"k8s":{"namespace":"app","podName":"api"},"src":{"port":8084}},
		{"k8s":{"namespace":"other","podName":"api"},"src":{"port":9996}}
	]`)
	rows, err := domain.ParseConnections(raw, target)
	if err != nil {
		t.Fatal(err)
	}
	if len(target.NetworkContainerIDs) != 4 || len(rows) != 5 {
		t.Fatalf("resolved target %#v, rows %#v", target, rows)
	}
	for i, row := range rows {
		if int(row.Local.Port) != 8080+i {
			t.Fatalf("socket scope includes a daemon/foreign container or drops a target container: %#v", rows)
		}
	}
}
