package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"testing"
	"time"

	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/kueuequeues"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

var (
	kueueWorkloadsGVR    = schema.GroupVersionResource{Group: kueueGroup, Version: "v1beta2", Resource: "workloads"}
	kueueLocalQueuesGVR  = schema.GroupVersionResource{Group: kueueGroup, Version: "v1beta2", Resource: "localqueues"}
	kueueClusterQueueGVR = schema.GroupVersionResource{Group: kueueGroup, Version: "v1beta2", Resource: "clusterqueues"}
)

func kueueTestObject(kind, namespace, name string, fields map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "kueue.x-k8s.io/v1beta2", "kind": kind}}
	u.SetNamespace(namespace)
	u.SetName(name)
	u.SetUID(types.UID(kind + "-" + namespace + "-" + name))
	for k, v := range fields {
		u.Object[k] = v
	}
	return u
}

func queueWorkload(namespace, name, queueName, reservedIn string) *unstructured.Unstructured {
	fields := map[string]any{"spec": map[string]any{"queueName": queueName}}
	if reservedIn != "" {
		fields["status"] = map[string]any{
			"admission":  map[string]any{"clusterQueue": reservedIn},
			"conditions": []any{map[string]any{"type": "QuotaReserved", "status": "True", "reason": "QuotaReserved"}},
		}
	}
	return kueueTestObject("Workload", namespace, name, fields)
}

func localQueue(namespace, name, clusterQueue string, pending int64) *unstructured.Unstructured {
	return kueueTestObject("LocalQueue", namespace, name, map[string]any{
		"spec":   map[string]any{"clusterQueue": clusterQueue},
		"status": map[string]any{"pendingWorkloads": pending, "admittedWorkloads": int64(1)},
	})
}

// kueueQueueFixture: ClusterQueue cq with LocalQueues team-a/lq and
// team-b/lq pointing at it, team-c/lq pointing at other.
func kueueQueueFixture() []runtime.Object {
	finished := queueWorkload("team-a", "finished", "lq", "cq")
	_ = unstructured.SetNestedSlice(finished.Object, []any{map[string]any{"type": "Finished", "status": "True", "reason": "Succeeded"}}, "status", "conditions")
	deleting := queueWorkload("team-a", "deleting", "lq", "")
	now := metav1.Now()
	deleting.SetDeletionTimestamp(&now)
	deleting.SetFinalizers([]string{"kueue.x-k8s.io/resource-in-use"})
	return []runtime.Object{
		kueueTestObject("ClusterQueue", "", "cq", nil),
		kueueTestObject("ClusterQueue", "", "other", nil),
		localQueue("team-a", "lq", "cq", 1),
		localQueue("team-a", "empty", "cq", 0),
		localQueue("team-b", "lq", "cq", 3),
		localQueue("team-c", "lq", "other", 0),
		queueWorkload("team-a", "submitted", "lq", ""),
		queueWorkload("team-a", "both", "lq", "cq"),
		finished,
		deleting,
		queueWorkload("team-a", "missing-queue", "absent", ""),
		queueWorkload("team-b", "hidden", "lq", "cq"),
		// Reserved in cq, though its LocalQueue now points at other.
		queueWorkload("team-c", "reserved-only", "lq", "cq"),
	}
}

func initKueueQueueCache(t *testing.T, objects []runtime.Object, configure func(*dynamicfake.FakeDynamicClient)) {
	t.Helper()
	var namespaces []runtime.Object
	for _, ns := range []string{"team-a", "team-b", "team-c"} {
		namespaces = append(namespaces, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	}
	useTestResourceCache(t, fake.NewClientset(namespaces...))
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kueueWorkloadsGVR: "WorkloadList", kueueLocalQueuesGVR: "LocalQueueList", kueueClusterQueueGVR: "ClusterQueueList",
	}, objects...)
	if configure != nil {
		configure(dyn)
	}
	resources := []k8s.APIResource{
		{Group: kueueGroup, Version: "v1beta2", Name: "workloads", Kind: "Workload", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
		{Group: kueueGroup, Version: "v1beta2", Name: "localqueues", Kind: "LocalQueue", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
		{Group: kueueGroup, Version: "v1beta2", Name: "clusterqueues", Kind: "ClusterQueue", Namespaced: false, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
	}
	if err := k8s.InitTestDynamicResourceCache(dyn, resources); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	cache := k8s.GetDynamicResourceCache()
	discovered := make(chan struct{})
	cache.OnCRDDiscoveryComplete(func() { close(discovered) })
	cache.DiscoverAllCRDs()
	select {
	case <-discovered:
	case <-time.After(5 * time.Second):
		t.Fatal("discovery did not complete")
	}
	if err := cache.EnsureWatching(kueueClusterQueueGVR); err != nil {
		t.Fatal(err)
	}
	if !cache.WaitForSync(kueueClusterQueueGVR, 5*time.Second) {
		t.Fatal("ClusterQueue cache did not sync")
	}
}

func decodeQueueWorkloads(t *testing.T, response *http.Response, wantStatus int) KueueQueueWorkloadsResponse {
	t.Helper()
	defer response.Body.Close()
	var got KueueQueueWorkloadsResponse
	if response.StatusCode != wantStatus {
		var body map[string]any
		_ = json.NewDecoder(response.Body).Decode(&body)
		t.Fatalf("status %d, want %d: %v", response.StatusCode, wantStatus, body)
	}
	if wantStatus == http.StatusOK {
		if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
	}
	return got
}

func memberNames(m *KueueQueueMembers) []string {
	var out []string
	for _, item := range m.Items {
		out = append(out, item.Namespace+"/"+item.Name)
	}
	return out
}

func validCoverage(t *testing.T, c resourceapi.Coverage) {
	t.Helper()
	if err := c.Validate(); err != nil {
		t.Fatalf("coverage %+v: %v", c, err)
	}
}

// Without authentication every reader sees what Radar's identity collects.
func TestKueueClusterQueueWorkloadsWithoutAuth(t *testing.T) {
	initKueueQueueCache(t, kueueQueueFixture(), nil)
	response, err := http.Get(testServer.URL + "/api/kueue/clusterqueues/cq/workloads")
	if err != nil {
		t.Fatal(err)
	}
	got := decodeQueueWorkloads(t, response, http.StatusOK)
	if got.Queue.Kind != "ClusterQueue" || got.Queue.Name != "cq" || got.Queue.UID == "" {
		t.Fatalf("queue = %+v", got.Queue)
	}
	want := []string{"team-a/both", "team-a/deleting", "team-a/finished", "team-a/submitted", "team-b/hidden", "team-c/reserved-only"}
	if !slices.Equal(memberNames(got.Workloads), want) {
		t.Fatalf("members = %v, want %v", memberNames(got.Workloads), want)
	}
	if got.Workloads.Counts != (kueuequeues.Counts{Members: 6, Submission: 5, Reservation: 4}) || got.Workloads.Matching != 6 {
		t.Fatalf("counts = %+v, matching %d", got.Workloads.Counts, got.Workloads.Matching)
	}
	validCoverage(t, got.Workloads.Coverage)
	if got.Workloads.Coverage.Status != resourceapi.CoverageComplete || got.Workloads.Coverage.Scope.Type != resourceapi.ScopeAllNamespaces {
		t.Fatalf("coverage = %+v", got.Workloads.Coverage)
	}
	if got.Workloads.SubmissionUnresolved != nil {
		t.Fatalf("submission unresolved = %+v", got.Workloads.SubmissionUnresolved)
	}
	for _, row := range got.Workloads.Items {
		switch row.Name {
		case "reserved-only":
			if !slices.Equal(row.Membership, []kueuequeues.Kind{kueuequeues.Reservation}) || row.Submission == nil || row.Submission.ClusterQueue != "other" || row.Reservation.ClusterQueue != "cq" {
				t.Fatalf("reserved-only = %+v", row)
			}
		case "deleting":
			if !row.Deleting || !slices.Equal(row.Membership, []kueuequeues.Kind{kueuequeues.Submission}) {
				t.Fatalf("deleting = %+v", row)
			}
		case "finished":
			if row.Phase != "finished" {
				t.Fatalf("finished = %+v", row)
			}
		case "both":
			if len(row.Membership) != 2 || row.Phase != "quota_reserved" {
				t.Fatalf("both = %+v", row)
			}
		}
	}
	var lqs []string
	for _, lq := range got.LocalQueues.Items {
		lqs = append(lqs, lq.Namespace+"/"+lq.Name)
		if !lq.WorkloadsRead || lq.Reported.Pending == nil || lq.Reported.Reserving != nil {
			t.Fatalf("local queue %+v", lq)
		}
	}
	if !slices.Equal(lqs, []string{"team-a/empty", "team-a/lq", "team-b/lq"}) || got.LocalQueues.Total != 3 {
		t.Fatalf("local queues = %v (total %d)", lqs, got.LocalQueues.Total)
	}
	validCoverage(t, got.LocalQueues.Coverage)

	// A page on another site gets nothing.
	request, _ := http.NewRequest(http.MethodGet, testServer.URL+"/api/kueue/clusterqueues/cq/workloads", nil)
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	crossSite, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	crossSite.Body.Close()
	if crossSite.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site read = %d", crossSite.StatusCode)
	}
}

func TestKueueQueueWorkloadsPaging(t *testing.T) {
	initKueueQueueCache(t, kueueQueueFixture(), nil)
	get := func(path string, status int) KueueQueueWorkloadsResponse {
		t.Helper()
		response, err := http.Get(testServer.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		return decodeQueueWorkloads(t, response, status)
	}
	var read []string
	path := "/api/kueue/clusterqueues/cq/workloads?limit=2&membership=reservation"
	var cursor string
	for page := 0; ; page++ {
		got := get(path, http.StatusOK)
		read = append(read, memberNames(got.Workloads)...)
		if got.Workloads.Matching != 4 || got.Workloads.Counts.Members != 6 {
			t.Fatalf("page %d: matching %d, counts %+v", page, got.Workloads.Matching, got.Workloads.Counts)
		}
		if got.Workloads.Next == "" {
			break
		}
		cursor = got.Workloads.Next
		path = "/api/kueue/clusterqueues/cq/workloads?limit=2&membership=reservation&cursor=" + url.QueryEscape(cursor)
	}
	if !slices.Equal(read, []string{"team-a/both", "team-a/finished", "team-b/hidden", "team-c/reserved-only"}) {
		t.Fatalf("paged reservation members = %v", read)
	}
	// A cursor belongs to its queue and filter.
	get("/api/kueue/clusterqueues/cq/workloads?limit=2&cursor="+url.QueryEscape(cursor), http.StatusBadRequest)
	get("/api/kueue/clusterqueues/other/workloads?limit=2&membership=reservation&cursor="+url.QueryEscape(cursor), http.StatusBadRequest)
	get("/api/kueue/clusterqueues/cq/workloads?cursor=garbage", http.StatusBadRequest)
	get("/api/kueue/clusterqueues/cq/workloads?limit=0", http.StatusBadRequest)
	get("/api/kueue/localqueues/team-a/lq/workloads?membership=reservation", http.StatusBadRequest)
	get("/api/kueue/clusterqueues/missing/workloads", http.StatusNotFound)
	get("/api/kueue/localqueues/team-a/missing/workloads", http.StatusNotFound)
}

func TestKueueLocalQueueWorkloads(t *testing.T) {
	initKueueQueueCache(t, kueueQueueFixture(), nil)
	response, err := http.Get(testServer.URL + "/api/kueue/localqueues/team-a/lq/workloads")
	if err != nil {
		t.Fatal(err)
	}
	got := decodeQueueWorkloads(t, response, http.StatusOK)
	if got.Queue.ClusterQueue != "cq" || got.LocalQueues != nil {
		t.Fatalf("queue = %+v, local queues %+v", got.Queue, got.LocalQueues)
	}
	if want := []string{"team-a/both", "team-a/deleting", "team-a/finished", "team-a/submitted"}; !slices.Equal(memberNames(got.Workloads), want) {
		t.Fatalf("members = %v, want %v", memberNames(got.Workloads), want)
	}
	validCoverage(t, got.Workloads.Coverage)
	if got.Workloads.Coverage.Scope.Type != resourceapi.ScopeNamespaces || got.Workloads.Coverage.Status != resourceapi.CoverageComplete {
		t.Fatalf("coverage = %+v", got.Workloads.Coverage)
	}

	// Scenario 7: a queue confirmed empty.
	response, err = http.Get(testServer.URL + "/api/kueue/localqueues/team-a/empty/workloads")
	if err != nil {
		t.Fatal(err)
	}
	got = decodeQueueWorkloads(t, response, http.StatusOK)
	if len(got.Workloads.Items) != 0 || got.Workloads.Items == nil || got.Workloads.Coverage.Status != resourceapi.CoverageComplete {
		t.Fatalf("empty queue = %+v", got.Workloads)
	}
}

func TestProxyAuth_KueueQueueWorkloads(t *testing.T) {
	type grants struct {
		getCQ, getLQ bool
		workloads    map[string]bool
		localQueues  map[string]bool
	}
	all := map[string]bool{"team-a": true, "team-b": true, "team-c": true}
	without := func(ns string) map[string]bool {
		out := map[string]bool{}
		for k, v := range all {
			out[k] = v && k != ns
		}
		return out
	}
	setup := func(t *testing.T, g grants) *authTestEnv {
		initKueueQueueCache(t, kueueQueueFixture(), nil)
		env := newAuthTestServer(t)
		permissions := &auth.UserPermissions{AllowedNamespaces: []string{"team-a", "team-b", "team-c"}}
		permissions.SetCanI("get", kueueGroup, "clusterqueues", "", g.getCQ)
		permissions.SetCanI("list", kueueGroup, "workloads", "", false)
		permissions.SetCanI("list", kueueGroup, "localqueues", "", false)
		for ns := range all {
			permissions.SetCanI("get", kueueGroup, "localqueues", ns, g.getLQ)
			permissions.SetCanI("list", kueueGroup, "workloads", ns, g.workloads[ns])
			permissions.SetCanI("list", kueueGroup, "localqueues", ns, g.localQueues[ns])
		}
		env.srv.permCache.Set("alice", nil, permissions)
		return env
	}

	t.Run("scenario 2: a namespace whose Workloads the viewer can't list", func(t *testing.T) {
		env := setup(t, grants{getCQ: true, workloads: without("team-b"), localQueues: all})
		got := decodeQueueWorkloads(t, env.authGet(t, "/api/kueue/clusterqueues/cq/workloads", "alice", ""), http.StatusOK)
		if want := []string{"team-a/both", "team-a/deleting", "team-a/finished", "team-a/submitted", "team-c/reserved-only"}; !slices.Equal(memberNames(got.Workloads), want) {
			t.Fatalf("members = %v, want %v", memberNames(got.Workloads), want)
		}
		cov := got.Workloads.Coverage
		validCoverage(t, cov)
		if cov.Status != resourceapi.CoveragePartial || !slices.Contains(cov.NarrowedBy, resourceapi.NarrowedByUserAccess) || len(cov.Gaps) != 1 || cov.Gaps[0].Code != readreason.UserForbidden || !slices.Equal(cov.Gaps[0].Namespaces.Names, []string{"team-b"}) {
			t.Fatalf("coverage = %+v", cov)
		}
		// The independent evidence: team-b's LocalQueue reports its own counts.
		var teamB *KueueQueueLocalQueue
		for i, lq := range got.LocalQueues.Items {
			if lq.Namespace == "team-b" {
				teamB = &got.LocalQueues.Items[i]
			}
		}
		if teamB == nil || teamB.WorkloadsRead || teamB.WorkloadsGap != readreason.UserForbidden || teamB.Reported.Pending == nil || *teamB.Reported.Pending != 3 {
			t.Fatalf("team-b's LocalQueue = %+v", teamB)
		}
	})

	t.Run("LocalQueues unreadable: reservation still established, submission unresolved", func(t *testing.T) {
		env := setup(t, grants{getCQ: true, workloads: all, localQueues: without("team-a")})
		got := decodeQueueWorkloads(t, env.authGet(t, "/api/kueue/clusterqueues/cq/workloads", "alice", ""), http.StatusOK)
		// team-a's submitted-only and deleting Workloads can't be placed;
		// its reserved ones still can.
		if want := []string{"team-a/both", "team-a/finished", "team-b/hidden", "team-c/reserved-only"}; !slices.Equal(memberNames(got.Workloads), want) {
			t.Fatalf("members = %v, want %v", memberNames(got.Workloads), want)
		}
		if u := got.Workloads.SubmissionUnresolved; u == nil || !slices.Equal(u.Names, []string{"team-a"}) {
			t.Fatalf("submission unresolved = %+v", u)
		}
		for _, row := range got.Workloads.Items {
			if row.Namespace == "team-a" && (row.Submission != nil || !slices.Equal(row.Membership, []kueuequeues.Kind{kueuequeues.Reservation})) {
				t.Fatalf("team-a row claims a submission it can't know: %+v", row)
			}
		}
		validCoverage(t, got.LocalQueues.Coverage)
		if got.LocalQueues.Coverage.Status != resourceapi.CoveragePartial {
			t.Fatalf("local queue coverage = %+v", got.LocalQueues.Coverage)
		}
	})

	t.Run("Workloads denied everywhere still answers with the LocalQueues", func(t *testing.T) {
		env := setup(t, grants{getCQ: true, workloads: map[string]bool{}, localQueues: all})
		got := decodeQueueWorkloads(t, env.authGet(t, "/api/kueue/clusterqueues/cq/workloads", "alice", ""), http.StatusOK)
		if got.Workloads != nil || got.WorkloadsError == nil || got.WorkloadsError.ErrorCode != readreason.UserForbidden {
			t.Fatalf("workloads = %+v, error %+v", got.Workloads, got.WorkloadsError)
		}
		if got.LocalQueues == nil || len(got.LocalQueues.Items) != 3 || got.LocalQueues.Items[0].WorkloadsGap != readreason.UserForbidden {
			t.Fatalf("local queues = %+v", got.LocalQueues)
		}
	})

	t.Run("ClusterQueue denied before existence is checked", func(t *testing.T) {
		env := setup(t, grants{getCQ: false, workloads: all, localQueues: all})
		decodeQueueWorkloads(t, env.authGet(t, "/api/kueue/clusterqueues/cq/workloads", "alice", ""), http.StatusForbidden)
		decodeQueueWorkloads(t, env.authGet(t, "/api/kueue/clusterqueues/missing/workloads", "alice", ""), http.StatusForbidden)
	})

	t.Run("scenario 6: a LocalQueue whose Workloads the viewer can't list", func(t *testing.T) {
		env := setup(t, grants{getLQ: true, workloads: without("team-b"), localQueues: all})
		got := decodeQueueWorkloads(t, env.authGet(t, "/api/kueue/localqueues/team-b/lq/workloads", "alice", ""), http.StatusOK)
		if got.Workloads != nil || got.WorkloadsError == nil || got.WorkloadsError.ErrorCode != readreason.UserForbidden || got.WorkloadsError.Coverage == nil {
			t.Fatalf("workloads = %+v, error %+v", got.Workloads, got.WorkloadsError)
		}
		got = decodeQueueWorkloads(t, env.authGet(t, "/api/kueue/localqueues/team-a/lq/workloads", "alice", ""), http.StatusOK)
		if len(got.Workloads.Items) != 4 {
			t.Fatalf("team-a/lq = %v", memberNames(got.Workloads))
		}
	})

	t.Run("LocalQueue denied", func(t *testing.T) {
		env := setup(t, grants{getLQ: false, workloads: all, localQueues: all})
		decodeQueueWorkloads(t, env.authGet(t, "/api/kueue/localqueues/team-a/lq/workloads", "alice", ""), http.StatusForbidden)
	})

	t.Run("LocalQueue in a namespace the viewer can't see", func(t *testing.T) {
		env := setup(t, grants{getLQ: true, workloads: all, localQueues: all})
		decodeQueueWorkloads(t, env.authGet(t, "/api/kueue/localqueues/private/lq/workloads", "alice", ""), http.StatusForbidden)
	})
}

// A cluster connection replaced between the reads of one request must not
// answer with rows from two clusters: here it is replaced while the caller's
// Workload permission is being checked, after the LocalQueues were read.
func TestKueueQueueWorkloadsConnectionChangedMidRead(t *testing.T) {
	initKueueQueueCache(t, kueueQueueFixture(), nil)
	var once sync.Once
	fakeSARServer(t, func(attrs authv1.ResourceAttributes) bool {
		if attrs.Resource == "workloads" {
			once.Do(k8s.ResetTestDynamicState)
		}
		return true
	})
	env := newAuthTestServer(t)
	permissions := &auth.UserPermissions{AllowedNamespaces: []string{"team-a"}}
	permissions.SetCanI("get", kueueGroup, "clusterqueues", "", true)
	permissions.SetCanI("list", kueueGroup, "localqueues", "", true)
	env.srv.permCache.Set("alice", nil, permissions)
	response := env.authGet(t, "/api/kueue/clusterqueues/cq/workloads", "alice", "")
	defer response.Body.Close()
	var body resourceapi.ReadError
	_ = json.NewDecoder(response.Body).Decode(&body)
	if response.StatusCode != http.StatusConflict || body.ErrorCode != readreason.ConnectionChanged {
		t.Fatalf("got %d %+v, want 409 connection_changed", response.StatusCode, body)
	}
}
