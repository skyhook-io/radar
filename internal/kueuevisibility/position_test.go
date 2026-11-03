package kueuevisibility

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

type fakeDiscovery struct {
	resources   []k8score.APIResource
	complete    bool
	groupFailed bool
	lastFailed  bool
}

func (d fakeDiscovery) Catalog() k8score.DiscoveryCatalog {
	return k8score.DiscoveryCatalog{Resources: d.resources, Complete: d.complete}
}
func (d fakeDiscovery) GroupHadPartialDiscovery(group string) bool {
	return d.groupFailed && group == Group
}
func (d fakeDiscovery) LastRefreshFailed() bool { return d.lastFailed }

func served(versions ...string) fakeDiscovery {
	d := fakeDiscovery{complete: true, resources: []k8score.APIResource{
		// The real CRD shares the plural; it must never be read as the visibility API.
		{Group: "kueue.x-k8s.io", Version: "v1beta2", Kind: "LocalQueue", Name: "localqueues", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
	}}
	for _, v := range versions {
		d.resources = append(d.resources,
			k8score.APIResource{Group: Group, Version: v, Kind: "PendingWorkloadsSummary", Name: "localqueues", Namespaced: true, IsCRD: true},
			k8score.APIResource{Group: Group, Version: v, Kind: "PendingWorkloadsSummary", Name: "clusterqueues", IsCRD: true},
		)
	}
	return d
}

// waits3 is the cached copy of job-waits-3 from the live capture in testdata.
func waits3() *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kueue.x-k8s.io/v1beta2", "kind": "Workload",
		"metadata": map[string]any{
			"name": "job-waits-3-eff69", "namespace": "gpu-night-845-a", "uid": "wl-uid",
			"creationTimestamp": "2026-11-03T04:22:03Z",
			"ownerReferences":   []any{map[string]any{"apiVersion": "batch/v1", "kind": "Job", "name": "waits-3", "uid": "cb152d03-1753-4bf8-8dcb-b07063ccef2e", "controller": true}},
		},
		"spec": map[string]any{"queueName": "pos"},
	}}
	return u
}

type server struct {
	t        *testing.T
	requests atomic.Int32
	lastPath atomic.Value
	handler  http.HandlerFunc
}

func newServer(t *testing.T, handler http.HandlerFunc) (*server, kubernetes.Interface) {
	t.Helper()
	s := &server{t: t, handler: handler}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		s.lastPath.Store(r.URL.RequestURI())
		s.handler(w, r)
	}))
	t.Cleanup(ts.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	return s, client
}

func liveAnswer(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile("testdata/lq-pos-gpu-night-845-a.json")
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func status(code int, reason metav1.StatusReason, message string, details *metav1.StatusDetails) metav1.Status {
	return metav1.Status{TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"}, Status: metav1.StatusFailure, Code: int32(code), Reason: reason, Message: message, Details: details}
}

func read(t *testing.T, req Request) *resourcecontext.KueuePendingPosition {
	t.Helper()
	if req.Workload == nil {
		req.Workload = waits3()
	}
	if req.Discovery == nil {
		req.Discovery = served("v1beta2", "v1beta1")
	}
	got := NewReader().Read(context.Background(), req)
	if got == nil {
		t.Fatal("Read returned nil")
	}
	return got
}

func TestReadListedFromLiveAnswer(t *testing.T) {
	body := liveAnswer(t)
	srv, client := newServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) })
	got := read(t, Request{Client: client})
	if got.Status != resourcecontext.KueuePositionListed || *got.AheadInLocalQueue != 1 || *got.AheadInClusterQueue != 2 {
		t.Fatalf("got %+v", got)
	}
	if got.LocalQueue != "pos" || got.APIVersion != "visibility.kueue.x-k8s.io/v1beta2" || *got.Rows != 2 || got.RowLimit != RowLimit || got.ReadAt == "" || got.Code != "" {
		t.Fatalf("metadata %+v", got)
	}
	want := "/apis/visibility.kueue.x-k8s.io/v1beta2/namespaces/gpu-night-845-a/localqueues/pos/pendingworkloads?limit=1000"
	if path := srv.lastPath.Load(); path != want {
		t.Fatalf("path %v, want %s", path, want)
	}
}

func TestReadMatchesOnlyThisIncarnation(t *testing.T) {
	body := liveAnswer(t)
	_, client := newServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) })

	recreated := waits3()
	recreated.SetCreationTimestamp(metav1.NewTime(time.Date(2026, 11, 3, 4, 30, 0, 0, time.UTC)))
	if got := read(t, Request{Client: client, Workload: recreated}); got.Status != resourcecontext.KueuePositionNotListed || got.AheadInLocalQueue != nil {
		t.Fatalf("same name, other creation time: %+v", got)
	}

	otherOwner := waits3()
	otherOwner.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: "waits-3", UID: "another", Controller: ptr(true)}})
	if got := read(t, Request{Client: client, Workload: otherOwner}); got.Status != resourcecontext.KueuePositionNotListed {
		t.Fatalf("owner UID mismatch: %+v", got)
	}

	ownerless := waits3()
	ownerless.SetOwnerReferences(nil)
	if got := read(t, Request{Client: client, Workload: ownerless}); got.Status != resourcecontext.KueuePositionListed {
		t.Fatalf("no controller owner still matches by name and creation: %+v", got)
	}
}

func ptr[T any](v T) *T { return &v }

func TestReadEmptyAnswerIsNotListedNotNotPending(t *testing.T) {
	_, client := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"kind":"PendingWorkloadsSummary","apiVersion":"visibility.kueue.x-k8s.io/v1beta2","metadata":{},"items":[]}`))
	})
	got := read(t, Request{Client: client})
	if got.Status != resourcecontext.KueuePositionNotListed || got.Rows == nil || *got.Rows != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestReadFullPageWithoutTheWorkloadIsLimitReached(t *testing.T) {
	items := make([]map[string]any, RowLimit)
	for i := range items {
		items[i] = map[string]any{"metadata": map[string]any{"name": fmt.Sprintf("other-%d", i), "namespace": "gpu-night-845-a", "creationTimestamp": "2026-11-03T04:00:00Z"}, "positionInLocalQueue": i, "positionInClusterQueue": i}
	}
	_, client := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"kind": "PendingWorkloadsSummary", "items": items})
	})
	got := read(t, Request{Client: client})
	if got.Status != resourcecontext.KueuePositionLimitReached || got.AheadInLocalQueue != nil || *got.Rows != RowLimit {
		t.Fatalf("got %+v", got)
	}
}

func TestReadVersionChoice(t *testing.T) {
	body := liveAnswer(t)
	srv, client := newServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) })

	if got := read(t, Request{Client: client, Discovery: served("v1beta1")}); got.APIVersion != "visibility.kueue.x-k8s.io/v1beta1" || !strings.Contains(srv.lastPath.Load().(string), "/v1beta1/") {
		t.Fatalf("v1beta1 only: %+v", got)
	}
	before := srv.requests.Load()
	cases := []struct {
		name      string
		discovery fakeDiscovery
		want      resourcecontext.KueuePendingPositionStatus
		code      readreason.Code
	}{
		{"only unverified versions", served("v1alpha1", "v1"), resourcecontext.KueuePositionUnsupported, ""},
		{"absent after complete discovery (real CRD with the same plural present)", served(), resourcecontext.KueuePositionNotServed, ""},
		{"absent, group discovery failed", func() fakeDiscovery { d := served(); d.groupFailed = true; return d }(), resourcecontext.KueuePositionUnavailable, readreason.ReadError},
		{"absent, discovery not complete", func() fakeDiscovery { d := served(); d.complete = false; return d }(), resourcecontext.KueuePositionUnavailable, readreason.ReadError},
		{"absent, last refresh failed", func() fakeDiscovery { d := served(); d.lastFailed = true; return d }(), resourcecontext.KueuePositionUnavailable, readreason.ReadError},
	}
	for _, tc := range cases {
		got := read(t, Request{Client: client, Discovery: tc.discovery})
		if got.Status != tc.want || got.Code != tc.code || got.LocalQueue != "pos" || got.ReadAt != "" {
			t.Errorf("%s: got %+v", tc.name, got)
		}
	}
	if srv.requests.Load() != before {
		t.Fatal("no read may be made when there is no supported served version")
	}
	if got := read(t, Request{Client: client, Discovery: served("v1alpha1")}); !strings.Contains(got.Message, "v1alpha1") {
		t.Fatalf("unsupported names the served versions: %+v", got)
	}
}

func TestReadWithoutQueueOrClientMakesNoRequest(t *testing.T) {
	srv, client := newServer(t, func(w http.ResponseWriter, r *http.Request) {})
	noQueue := waits3()
	unstructured.RemoveNestedField(noQueue.Object, "spec", "queueName")
	if got := read(t, Request{Client: client, Workload: noQueue}); got.Status != resourcecontext.KueuePositionNoLocalQueue {
		t.Fatalf("no queue: %+v", got)
	}
	if got := read(t, Request{Client: nil}); got.Status != resourcecontext.KueuePositionUnavailable || got.Code != readreason.ReadError {
		t.Fatalf("no client: %+v", got)
	}
	if srv.requests.Load() != 0 {
		t.Fatal("made a request")
	}
}

func TestReadClassifiesRefusals(t *testing.T) {
	apiServerDenied := status(403, metav1.StatusReasonForbidden, `localqueues.visibility.kueue.x-k8s.io "pos" is forbidden`, &metav1.StatusDetails{Name: "pos", Group: Group, Kind: "localqueues"})
	radarCheckDenied := status(403, metav1.StatusReasonForbidden, "svc is not allowed to get this resource", nil)
	tokenRequired := status(403, metav1.StatusReason(pkgauth.UserTokenRequiredReason), "Live actions need a user token, which only single sign-on sessions carry; this request has none.", nil)
	checkFailed := status(503, metav1.StatusReasonServiceUnavailable, "Radar could not check permissions for svc with the API server", nil)
	apiServiceDown := status(503, metav1.StatusReasonServiceUnavailable, "the server is currently unable to handle the request", nil)
	unauthorized := status(401, metav1.StatusReasonUnauthorized, "Unauthorized", nil)
	impersonationDenied := status(403, metav1.StatusReasonForbidden, `users "alice" is forbidden: User "system:serviceaccount:radar:radar" cannot impersonate resource "users" in API group "" at the cluster scope`, &metav1.StatusDetails{Name: "alice", Kind: "users"})
	queueUnknown := status(404, metav1.StatusReasonNotFound, `localqueue.visibility.kueue.x-k8s.io "pos" not found`, &metav1.StatusDetails{Name: "pos", Group: Group, Kind: "localqueue"})
	pathUnknown := status(404, metav1.StatusReasonNotFound, "the server could not find the requested resource", nil)

	cases := []struct {
		name     string
		identity Identity
		reply    metav1.Status
		want     resourcecontext.KueuePendingPositionStatus
		code     readreason.Code
	}{
		{"user denied", IdentityUser, apiServerDenied, resourcecontext.KueuePositionUnavailable, readreason.UserForbidden},
		{"radar denied", IdentityRadar, apiServerDenied, resourcecontext.KueuePositionUnavailable, readreason.RadarForbidden},
		{"system identity allowed by Radar, refused to Radar's account", IdentitySystem, apiServerDenied, resourcecontext.KueuePositionUnavailable, readreason.RadarForbidden},
		{"system identity refused by Radar's check", IdentitySystem, radarCheckDenied, resourcecontext.KueuePositionUnavailable, readreason.UserForbidden},
		{"Radar may not impersonate the user", IdentityUser, impersonationDenied, resourcecontext.KueuePositionUnavailable, readreason.RadarForbidden},
		{"user token required", IdentityUser, tokenRequired, resourcecontext.KueuePositionUnavailable, readreason.ReadError},
		{"system permission check failed", IdentitySystem, checkFailed, resourcecontext.KueuePositionUnavailable, readreason.UserPermissionCheckFailed},
		{"visibility server down", IdentityUser, apiServiceDown, resourcecontext.KueuePositionUnavailable, readreason.ReadError},
		{"user credentials rejected", IdentityUser, unauthorized, resourcecontext.KueuePositionUnavailable, readreason.ReadError},
		{"radar credentials rejected", IdentityRadar, unauthorized, resourcecontext.KueuePositionUnavailable, readreason.RadarCredentialsRejected},
		{"kueue does not know the LocalQueue", IdentityUser, queueUnknown, resourcecontext.KueuePositionQueueUnknown, ""},
		{"path not served", IdentityUser, pathUnknown, resourcecontext.KueuePositionUnavailable, readreason.ReadError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, client := newServer(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, int(tc.reply.Code), tc.reply) })
			got := read(t, Request{Client: client, Identity: tc.identity})
			if got.Status != tc.want || got.Code != tc.code {
				t.Fatalf("got %+v", got)
			}
			if got.Rows != nil || got.AheadInClusterQueue != nil || got.ReadAt == "" || got.Message == "" {
				t.Fatalf("a failed read carries no rows or positions, but its read time and message: %+v", got)
			}
		})
	}
}

func TestReadNamesTheMissingPermission(t *testing.T) {
	_, client := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 403, status(403, metav1.StatusReasonForbidden, "forbidden", &metav1.StatusDetails{Kind: "localqueues", Group: Group}))
	})
	got := read(t, Request{Client: client, Identity: IdentityUser})
	if !strings.Contains(got.Message, "localqueues/pendingworkloads") || !strings.Contains(got.Message, "gpu-night-845-a") {
		t.Fatalf("message %q", got.Message)
	}
}

func TestReadRejectsOversizedAndForeignAnswers(t *testing.T) {
	huge := `{"kind":"PendingWorkloadsSummary","items":[],"pad":"` + strings.Repeat("x", maxBodyBytes) + `"}`
	_, client := newServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(huge)) })
	if got := read(t, Request{Client: client}); got.Status != resourcecontext.KueuePositionUnavailable || !strings.Contains(got.Message, "larger") {
		t.Fatalf("oversized: %+v", got)
	}
	_, client = newServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"kind":"Status","items":[]}`)) })
	if got := read(t, Request{Client: client}); got.Status != resourcecontext.KueuePositionUnavailable || got.Code != readreason.ReadError {
		t.Fatalf("foreign kind: %+v", got)
	}
}

func TestReadTimesOut(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	_, client := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	reader := NewReader()
	reader.timeout = 50 * time.Millisecond
	got := reader.Read(context.Background(), Request{Workload: waits3(), Client: client, Discovery: served("v1beta2")})
	if got.Status != resourcecontext.KueuePositionUnavailable || !strings.Contains(got.Message, "did not answer") {
		t.Fatalf("got %+v", got)
	}
}

func TestReaderReusesOneIdentitysAnswerBriefly(t *testing.T) {
	body := liveAnswer(t)
	srv, client := newServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) })
	now := time.Date(2026, 11, 3, 5, 0, 0, 0, time.UTC)
	reader := NewReader()
	reader.now = func() time.Time { return now }
	req := Request{Workload: waits3(), Client: client, Discovery: served("v1beta2"), Identity: IdentityUser, MemoKey: "ctx\x00alice"}

	first := reader.Read(context.Background(), req)
	waits1 := waits3()
	waits1.SetName("job-waits-1-d33c0")
	waits1.SetCreationTimestamp(metav1.NewTime(time.Date(2026, 11, 3, 4, 22, 2, 0, time.UTC)))
	waits1.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: "waits-1", UID: "ad288548-86f2-4b88-80f2-5801599a2db6", Controller: ptr(true)}})
	sibling := req
	sibling.Workload = waits1
	second := reader.Read(context.Background(), sibling)
	if srv.requests.Load() != 1 {
		t.Fatalf("same identity and LocalQueue within the window: %d requests", srv.requests.Load())
	}
	if *first.AheadInLocalQueue != 1 || *second.AheadInLocalQueue != 0 || first.ReadAt != second.ReadAt {
		t.Fatalf("first %+v second %+v", first, second)
	}

	bob := req
	bob.MemoKey = "ctx\x00bob"
	reader.Read(context.Background(), bob)
	if srv.requests.Load() != 2 {
		t.Fatal("another identity must never reuse alice's answer")
	}

	now = now.Add(memoTTL)
	reader.Read(context.Background(), req)
	if srv.requests.Load() != 3 {
		t.Fatal("an expired answer must be read again")
	}

	unscoped := req
	unscoped.MemoKey = ""
	reader.Read(context.Background(), unscoped)
	reader.Read(context.Background(), unscoped)
	if srv.requests.Load() != 5 {
		t.Fatal("an empty memo key disables reuse")
	}
}

func TestIdentityOf(t *testing.T) {
	if IdentityOf(nil) != IdentityRadar {
		t.Fatal("no user reads as Radar")
	}
	if IdentityOf(&pkgauth.User{Username: "alice"}) != IdentityUser {
		t.Fatal("signed-in user")
	}
	if IdentityOf(&pkgauth.User{Username: "radar:system", System: true}) != IdentityUser {
		t.Fatal("outside user-token mode a system identity is impersonated like any user")
	}
}

func TestRequestForDoesNotReuseTokenAuthorizedAnswers(t *testing.T) {
	previous := pkgauth.CurrentCredentialMode()
	t.Cleanup(func() { pkgauth.SetCredentialMode(previous) })
	pkgauth.SetCredentialMode(pkgauth.CredentialsUserToken)
	for _, token := range []string{"valid-user-token", "", "different-token"} {
		ctx := pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: "alice", Token: token})
		req, _ := RequestFor(ctx, waits3())
		if req.MemoKey != "" {
			t.Fatal("a user-token request must perform its own credential-bound read")
		}
	}
}
