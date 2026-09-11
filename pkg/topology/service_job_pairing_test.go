package topology

import (
	"fmt"
	"os"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Pairing Services with Jobs is the one workload branch that used to rescan
// the whole Job list per Service. On a cluster that runs CronJobs the Job list
// is dominated by completed runs which can never back a Service, so the scan
// cost grew as services x completed-jobs while the useful work stayed flat.
//
// The tests below pin both halves of the fix: the edges it must still produce,
// and the cost it must no longer pay.

func pairingService(ns, name string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": name}},
	}
}

func pairingJob(ns, name string, labels map[string]string, active int32) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}},
		},
		Status: batchv1.JobStatus{Active: active},
	}
}

func serviceJobEdges(t *testing.T, provider *mockProvider) map[string]bool {
	t.Helper()
	opts := DefaultBuildOptions()
	opts.ForRelationshipCache = true
	topo, err := NewBuilder(provider).Build(opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	edges := map[string]bool{}
	for _, e := range topo.Edges {
		if e.Type == EdgeExposes {
			edges[e.Source+" -> "+e.Target] = true
		}
	}
	return edges
}

func TestServiceExposesActiveJobInSameNamespace(t *testing.T) {
	provider := &mockProvider{
		services: []*corev1.Service{pairingService("team-a", "web")},
		jobs:     []*batchv1.Job{pairingJob("team-a", "web-run", map[string]string{"app": "web"}, 1)},
	}
	if edges := serviceJobEdges(t, provider); !edges["service/team-a/web -> job/team-a/web-run"] {
		t.Fatalf("expected the Service to expose its active Job; got edges %v", edges)
	}
}

func TestServiceIgnoresJobsItCannotBack(t *testing.T) {
	tests := []struct {
		name string
		job  *batchv1.Job
	}{
		{
			// A finished run has no pods left to receive traffic.
			name: "completed job in the same namespace",
			job:  pairingJob("team-a", "web-run", map[string]string{"app": "web"}, 0),
		},
		{
			// Services never select across a namespace boundary, however well
			// the labels line up.
			name: "active job with matching labels in another namespace",
			job:  pairingJob("team-b", "web-run", map[string]string{"app": "web"}, 1),
		},
		{
			name: "active job in the same namespace with unrelated labels",
			job:  pairingJob("team-a", "batch-run", map[string]string{"app": "batch"}, 1),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &mockProvider{
				services: []*corev1.Service{pairingService("team-a", "web")},
				jobs:     []*batchv1.Job{tt.job},
			}
			for edge := range serviceJobEdges(t, provider) {
				if edge == "service/team-a/web -> job/"+tt.job.Namespace+"/"+tt.job.Name {
					t.Fatalf("Service must not expose %s", edge)
				}
			}
		})
	}
}

// pairingProvider builds a cluster of the given shape. The Jobs that can
// actually back a Service are held at one per namespace, so everything beyond
// that is a completed run that pairing must never look at twice.
func pairingProvider(services, namespaces, completedJobs int) *mockProvider {
	ns := func(i int) string { return fmt.Sprintf("ns-%03d", i%namespaces) }

	p := &mockProvider{}
	for i := 0; i < services; i++ {
		p.services = append(p.services, pairingService(ns(i), fmt.Sprintf("svc-%05d", i)))
	}
	// One live Job per namespace: the useful pairing work, constant across sizes.
	for i := 0; i < namespaces; i++ {
		p.jobs = append(p.jobs, pairingJob(ns(i), fmt.Sprintf("live-%05d", i), map[string]string{"app": fmt.Sprintf("svc-%05d", i)}, 1))
	}
	for i := 0; i < completedJobs; i++ {
		p.jobs = append(p.jobs, pairingJob(ns(i), fmt.Sprintf("done-%05d", i), map[string]string{"app": "cron"}, 0))
	}
	return p
}

// Wall-clock ratios of a sub-second build are not a reliable signal on a shared
// runner, and CI runs `go test ./...` without -short so the -short guard below
// never fires there. Measured on unchanged code, this ratio ranged 1.61 to 3.49
// across twelve consecutive local runs — a spread no threshold can sit inside
// while still separating linear (~2x) from a per-Service rescan (~4x). Left as
// an opt-in check rather than a test that fails randomly in CI, which only
// teaches people to re-run red.
//
// Run with: RADAR_TIMING_TESTS=1 go test ./pkg/topology/ -run Scales
func requireTimingTestsEnabled(t *testing.T) {
	t.Helper()
	if os.Getenv("RADAR_TIMING_TESTS") == "" {
		t.Skip("timing-sensitive; set RADAR_TIMING_TESTS=1 to run")
	}
}

func buildOnce(tb testing.TB, provider *mockProvider) time.Duration {
	tb.Helper()
	opts := DefaultBuildOptions()
	opts.ForRelationshipCache = true

	start := time.Now()
	if _, err := NewBuilder(provider).Build(opts); err != nil {
		tb.Fatalf("Build: %v", err)
	}
	return time.Since(start)
}

// buildDurationPair measures two shapes interleaved rather than one after the
// other.
//
// These tests assert a *ratio*, and measuring one shape to completion before
// starting the other lets a change in background load land entirely on one side
// of it: the numerator gets a noisy window, the denominator a quiet one, and the
// ratio blows past the limit while the code under test is unchanged. That is
// what made this test flaky on a loaded machine. Alternating the samples makes
// both shapes see the same conditions, so shared noise largely divides out.
func buildDurationPair(tb testing.TB, a, b *mockProvider, runs int) (time.Duration, time.Duration) {
	tb.Helper()
	bestA := time.Duration(1<<63 - 1)
	bestB := time.Duration(1<<63 - 1)
	for i := 0; i < runs; i++ {
		if d := buildOnce(tb, a); d < bestA {
			bestA = d
		}
		if d := buildOnce(tb, b); d < bestB {
			bestB = d
		}
	}
	return bestA, bestB
}

// Adding Services must not re-walk the Job list. Job count is held fixed so
// the Job nodes — a legitimate linear cost — are identical in both builds and
// cancel out of the ratio; only the pairing differs.
//
// The bound is a complexity guard, not a latency budget: a per-Service rescan
// grows with the Service multiplier, so there is several-fold headroom before
// this can fire on timing noise alone.
func TestServiceJobPairingDoesNotRescanJobsPerService(t *testing.T) {
	requireTimingTestsEnabled(t)

	const (
		namespaces    = 200
		completedJobs = 20000
		baseServices  = 500
		factor        = 8
		maxGrowth     = 2.5
	)

	base, scaled := buildDurationPair(t,
		pairingProvider(baseServices, namespaces, completedJobs),
		pairingProvider(baseServices*factor, namespaces, completedJobs),
		3)

	growth := float64(scaled) / float64(base)
	t.Logf("%5d services x %d completed jobs: %v", baseServices, completedJobs, base)
	t.Logf("%5d services x %d completed jobs: %v", baseServices*factor, completedJobs, scaled)
	t.Logf("services x%d -> build time x%.2f (a per-Service rescan would approach x%d)", factor, growth, factor)

	if growth > maxGrowth {
		t.Errorf("build time grew %.2fx for %dx the Services against an unchanged Job list (limit %.1fx) — Service/Job pairing is rescanning every Job per Service",
			growth, factor, maxGrowth)
	}
}

// The large-cluster census this pairing has to survive: 432 namespaces
// carrying Services and Jobs in the tens of thousands, most Jobs completed.
const (
	largeClusterServices      = 23547
	largeClusterNamespaces    = 432
	largeClusterCompletedJobs = 19861
)

// Doubling the whole cluster must roughly double the build, not square it.
// Ratios rather than a wall-clock budget keep this meaningful on any machine:
// per-namespace density is identical at both sizes, so linear growth tracks
// the object count while a per-Service rescan of the Job list grows with the
// product and lands near 4x.
func TestServiceJobPairingScalesAtTwiceTheLargeCluster(t *testing.T) {
	requireTimingTestsEnabled(t)

	const maxGrowth = 2.6

	large, doubled := buildDurationPair(t,
		pairingProvider(largeClusterServices, largeClusterNamespaces, largeClusterCompletedJobs),
		pairingProvider(2*largeClusterServices, 2*largeClusterNamespaces, 2*largeClusterCompletedJobs),
		5)

	growth := float64(doubled) / float64(large)
	t.Logf("large cluster    (%d svc / %d jobs / %d ns): %v",
		largeClusterServices, largeClusterCompletedJobs, largeClusterNamespaces, large)
	t.Logf("2x that cluster  (%d svc / %d jobs / %d ns): %v",
		2*largeClusterServices, 2*largeClusterCompletedJobs, 2*largeClusterNamespaces, doubled)
	t.Logf("cluster x2 -> build time x%.2f (linear ~x2, per-Service rescan ~x4)", growth)

	if growth > maxGrowth {
		t.Errorf("doubling the cluster grew build time %.2fx (limit %.1fx) — Service/Job pairing is superlinear at large-cluster scale",
			growth, maxGrowth)
	}
}

// Reports CPU and allocation cost for the pairing at the large-cluster shape.
func BenchmarkBuildTopologyServiceJobPairing(b *testing.B) {
	provider := pairingProvider(largeClusterServices, largeClusterNamespaces, largeClusterCompletedJobs)
	opts := DefaultBuildOptions()
	opts.ForRelationshipCache = true

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := NewBuilder(provider).Build(opts); err != nil {
			b.Fatalf("Build: %v", err)
		}
	}
}

func BenchmarkBuildTopologyServiceJobPairingTwiceLargeCluster(b *testing.B) {
	provider := pairingProvider(2*largeClusterServices, 2*largeClusterNamespaces, 2*largeClusterCompletedJobs)
	opts := DefaultBuildOptions()
	opts.ForRelationshipCache = true

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := NewBuilder(provider).Build(opts); err != nil {
			b.Fatalf("Build: %v", err)
		}
	}
}

// The complexity guard without a clock: the pairing loop's iteration count is
// exact and machine-independent where its wall-clock time is not (see
// requireTimingTestsEnabled). With one live Job per namespace, an indexed
// pairing considers exactly one candidate per Service; the per-Service rescan
// this pins against multiplies that by the completed runs.
func TestServiceJobPairingConsidersOnlyActiveJobs(t *testing.T) {
	const (
		services      = 300
		namespaces    = 30
		completedJobs = 5000
	)
	b := NewBuilder(pairingProvider(services, namespaces, completedJobs))
	opts := DefaultBuildOptions()
	opts.ForRelationshipCache = true
	if _, err := b.Build(opts); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if b.serviceJobComparisons == 0 {
		t.Fatal("the pairing loop never ran — the counter is not wired to the code under test")
	}
	if b.serviceJobComparisons > services {
		t.Fatalf("pairing considered %d candidates for %d Services with one live Job per namespace — completed Jobs are being scanned",
			b.serviceJobComparisons, services)
	}
}
