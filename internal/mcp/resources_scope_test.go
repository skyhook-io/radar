package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	appsv1 "k8s.io/api/apps/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/readreason"
)

// Two teams share Radar's cluster-wide cache, plus an empty namespace. Every
// string that names team-b data contains "team-b".
func seedTwoTeamCache(t *testing.T) {
	t.Helper()
	objs := []runtime.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-c"}},
	}
	for _, team := range []string{"team-a", "team-b"} {
		zero := int32(0)
		objs = append(objs,
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: team}},
			&corev1.Event{
				ObjectMeta:     metav1.ObjectMeta{Name: team + "-marker", Namespace: team},
				Type:           corev1.EventTypeWarning,
				Reason:         "Marker",
				Message:        "warning marker in " + team,
				InvolvedObject: corev1.ObjectReference{Kind: "Service", Namespace: team, Name: team + "-svc"},
				Count:          1,
			},
			&corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: team + "-svc", Namespace: team},
				Spec: corev1.ServiceSpec{
					Selector: map[string]string{"app": team},
					Ports:    []corev1.ServicePort{{Port: 8080, TargetPort: intstr.FromInt32(8080)}},
				},
			},
			&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: team + "-workload", Namespace: team},
				Spec: appsv1.DeploymentSpec{
					Replicas: &zero,
					Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": team}},
					Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": team}}},
				},
			},
		)
	}
	if err := k8s.InitTestResourceCache(fake.NewClientset(objs...)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestState)
}

type resourceHandler func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error)

var clusterResources = []struct {
	name    string
	handler resourceHandler
	// tool is the same question asked through the matching tool.
	tool func(context.Context) (*mcp.CallToolResult, any, error)
}{
	{"events", handleResourceEvents, func(ctx context.Context) (*mcp.CallToolResult, any, error) {
		return handleGetEvents(ctx, nil, eventsInput{Type: "Warning", Limit: 50})
	}},
	{"topology", handleResourceTopology, func(ctx context.Context) (*mcp.CallToolResult, any, error) {
		return handleGetTopology(ctx, nil, topologyInput{})
	}},
	{"health", handleResourceHealth, func(ctx context.Context) (*mcp.CallToolResult, any, error) {
		return handleGetDashboard(ctx, nil, dashboardInput{})
	}},
}

func readResourceText(t *testing.T, ctx context.Context, handler resourceHandler) string {
	t.Helper()
	res, err := handler(ctx, nil)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(res.Contents) != 1 {
		t.Fatalf("want one content item, got %d", len(res.Contents))
	}
	return res.Contents[0].Text
}

type scopeMarker struct {
	PartialScope    *bool    `json:"partialScope"`
	ScopeNamespaces []string `json:"scopeNamespaces"`
	AccessDenied    bool     `json:"accessDenied"`
	Error           string   `json:"error"`
	ErrorCode       string   `json:"error_code"`
}

func decodeMarker(t *testing.T, text string) scopeMarker {
	t.Helper()
	var m scopeMarker
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatalf("not a JSON object: %v: %s", err, text)
	}
	return m
}

// A caller who reads only team-a gets team-a's events and topology, marked as
// covering only their namespaces, and is refused the health summary with a
// pointer to get_dashboard for their namespace.
func TestClusterResourcesNarrowToCallerNamespaces(t *testing.T) {
	seedTwoTeamCache(t)
	ctx := withTestUserPerms(t, "alice", nil, []string{"team-a"})

	events := readResourceText(t, ctx, handleResourceEvents)
	topo := readResourceText(t, ctx, handleResourceTopology)
	for name, text := range map[string]string{"events": events, "topology": topo} {
		if strings.Contains(text, "team-b") {
			t.Errorf("cluster://%s returned data the caller can't read: %s", name, text)
		}
		m := decodeMarker(t, text)
		if m.PartialScope == nil || !*m.PartialScope || !slices.Equal(m.ScopeNamespaces, []string{"team-a"}) {
			t.Errorf("cluster://%s marker = %+v, want partialScope and scopeNamespaces [team-a]: %s", name, m, text)
		}
	}
	if !strings.Contains(events, "warning marker in team-a") {
		t.Errorf("cluster://events lost the caller's own warning: %s", events)
	}
	if !strings.Contains(topo, "team-a-svc") {
		t.Errorf("cluster://topology lost the caller's own Service: %s", topo)
	}

	health := readResourceText(t, ctx, handleResourceHealth)
	m := decodeMarker(t, health)
	if !strings.Contains(m.Error, "get_dashboard") || !strings.Contains(m.Error, "team-a") || strings.Contains(health, "team-b") || strings.Contains(health, "marker") {
		t.Errorf("cluster://health should refuse and point to get_dashboard for team-a: %s", health)
	}
}

func TestDashboardExplainsClusterWidePermissionWithAllNamespaceGrants(t *testing.T) {
	seedTwoTeamCache(t)
	ctx := withTestUserPerms(t, "all-namespace-bindings", nil, []string{"team-a", "team-b"})
	_, _, err := handleGetDashboard(ctx, nil, dashboardInput{})
	if err == nil || !strings.Contains(err.Error(), "cluster-wide list pods permission") || strings.Contains(err.Error(), "access only some") {
		t.Fatalf("dashboard refusal must explain the cluster-wide permission requirement: %v", err)
	}
}

// The get_topology tool carries the same marker in both formats when it
// narrows a cluster-wide request, and none when the caller named a namespace.
func TestGetTopologyMarksNarrowedScope(t *testing.T) {
	seedTwoTeamCache(t)
	ctx := withTestUserPerms(t, "alice", nil, []string{"team-a"})
	for _, format := range []string{"graph", "summary"} {
		res, _, err := handleGetTopology(ctx, nil, topologyInput{Format: format})
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		text := extractText(t, res)
		m := decodeMarker(t, text)
		if m.PartialScope == nil || !*m.PartialScope || !slices.Equal(m.ScopeNamespaces, []string{"team-a"}) {
			t.Errorf("%s: marker = %+v: %s", format, m, text)
		}
		if strings.Contains(text, "team-b") {
			t.Errorf("%s: returned data the caller can't read: %s", format, text)
		}

		res, _, err = handleGetTopology(ctx, nil, topologyInput{Format: format, Namespace: "team-a"})
		if err != nil {
			t.Fatalf("%s with namespace: %v", format, err)
		}
		if text := extractText(t, res); strings.Contains(text, "partialScope") {
			t.Errorf("%s with namespace: a request for one namespace is not narrowed: %s", format, text)
		}
	}
}

// Callers who read every namespace, and local use with auth off, get the
// whole cluster with no marker, and the resource says exactly what the tool
// says.
func TestClusterResourcesServeFullCallerLikeTheTool(t *testing.T) {
	seedTwoTeamCache(t)
	want := map[string][]string{
		"events":   {"warning marker in team-a", "warning marker in team-b"},
		"topology": {"team-a-svc", "team-b-svc"},
		"health":   {"warning marker in team-a", "warning marker in team-b"},
	}
	callers := map[string]context.Context{
		"all namespaces": withTestUserPerms(t, "admin", nil, nil),
		"auth off":       context.Background(),
	}
	for who, ctx := range callers {
		for _, r := range clusterResources {
			got := readResourceText(t, ctx, r.handler)
			for _, w := range want[r.name] {
				if !strings.Contains(got, w) {
					t.Errorf("%s: cluster://%s lost %q: %s", who, r.name, w, got)
				}
			}
			if strings.Contains(got, "partialScope") || strings.Contains(got, "scopeNamespaces") {
				t.Errorf("%s: cluster://%s marked a full answer as partial: %s", who, r.name, got)
			}
			res, _, err := r.tool(ctx)
			if err != nil {
				t.Fatalf("%s: tool for %s: %v", who, r.name, err)
			}
			// Topology and health order equal-ranked items from map iteration,
			// so only events compare byte for byte.
			if r.name == "events" {
				if tool := extractText(t, res); tool != got {
					t.Errorf("%s: cluster://%s differs from its tool:\nresource %s\ntool     %s", who, r.name, got, tool)
				}
			}
		}
	}
}

// A caller who can read no namespace gets no team data from any resource.
func TestClusterResourcesNoNamespaceAccess(t *testing.T) {
	seedTwoTeamCache(t)
	ctx := withTestUserPerms(t, "bob", nil, []string{})
	for _, r := range clusterResources {
		got := readResourceText(t, ctx, r.handler)
		if strings.Contains(got, "team-") {
			t.Errorf("cluster://%s leaked team data: %s", r.name, got)
		}
		m := decodeMarker(t, got)
		switch r.name {
		case "events":
			if !m.AccessDenied {
				t.Errorf("cluster://events should mark the empty list as a denial: %s", got)
			}
		default:
			if m.Error == "" {
				t.Errorf("cluster://%s should refuse: %s", r.name, got)
			}
		}
	}
	if got := readResourceText(t, ctx, handleResourceHealth); !strings.Contains(got, "can't access any namespace") {
		t.Errorf("cluster://health should say the caller can read no namespace: %s", got)
	}
}

// An empty answer is an empty list, not null.
func TestGetEventsEmptyIsList(t *testing.T) {
	seedTwoTeamCache(t)
	ctx := withTestUserPerms(t, "carol", nil, []string{"team-c"})
	if got := readResourceText(t, ctx, handleResourceEvents); !strings.Contains(got, `"events":[]`) {
		t.Errorf("want an empty events list: %s", got)
	}
}

type sarAnswer int

const (
	sarDeny sarAnswer = iota
	sarAllow
	sarError
)

// useSARServer points Radar's client at a test API server that answers every
// SubjectAccessReview for user from answer, keyed on "resource/namespace"
// with "" for cluster-wide. Unlisted checks are denied. Any other request,
// such as a namespace LIST, fails with a server error.
func useSARServer(t *testing.T, user string, answer map[string]sarAnswer) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/subjectaccessreviews") {
			http.Error(w, "unavailable", http.StatusInternalServerError)
			return
		}
		var review authorizationv1.SubjectAccessReview
		if err := json.NewDecoder(r.Body).Decode(&review); err != nil || review.Spec.ResourceAttributes == nil {
			http.Error(w, "bad review", http.StatusBadRequest)
			return
		}
		if review.Spec.User != user {
			t.Errorf("review for %q, want %q", review.Spec.User, user)
		}
		attrs := review.Spec.ResourceAttributes
		switch answer[attrs.Resource+"/"+attrs.Namespace] {
		case sarError:
			http.Error(w, "apiserver unavailable", http.StatusInternalServerError)
			return
		case sarAllow:
			review.Status.Allowed = attrs.Verb == "list"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(review)
	}))
	t.Cleanup(server.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	previous := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previous) })
	getPermCache().Invalidate()
	t.Cleanup(getPermCache().Invalidate)
}

// Decided from real permission checks with nothing cached. A check that fails
// partway withholds every team's data and says Radar couldn't check, on the
// resources and on the tools behind them; it is never reported as a denial.
func TestClusterResourcesFromPermissionChecks(t *testing.T) {
	for _, tc := range []struct {
		name     string
		answer   map[string]sarAnswer
		served   []string // team markers that must appear in events
		withheld []string
		failed   bool
	}{
		{"pods in team-a", map[string]sarAnswer{"pods/team-a": sarAllow}, []string{"team-a"}, []string{"team-b"}, false},
		{"deployments in team-b", map[string]sarAnswer{"deployments/team-b": sarAllow}, []string{"team-b"}, []string{"team-a"}, false},
		// Same rule as the tools: cluster-wide pod access reads every
		// namespace, with or without the right to list namespace names.
		{"pods everywhere, no namespace list", map[string]sarAnswer{"pods/": sarAllow}, []string{"team-a", "team-b"}, nil, false},
		{"discovery errors partway", map[string]sarAnswer{"pods/team-a": sarAllow, "pods/team-b": sarError}, nil, []string{"team-a", "team-b"}, true},
		{"cluster-wide check errors", map[string]sarAnswer{"pods/": sarError}, nil, []string{"team-a", "team-b"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seedTwoTeamCache(t)
			useSARServer(t, "reviewed", tc.answer)
			ctx := pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: "reviewed"})
			for _, r := range clusterResources {
				got := readResourceText(t, ctx, r.handler)
				for _, team := range tc.withheld {
					if strings.Contains(got, team) {
						t.Errorf("cluster://%s returned %s data: %s", r.name, team, got)
					}
				}
				if tc.failed {
					m := decodeMarker(t, got)
					if m.ErrorCode != string(readreason.UserPermissionCheckFailed) || m.AccessDenied {
						t.Errorf("cluster://%s should say Radar couldn't check access: %s", r.name, got)
					}
					_, _, err := r.tool(ctx)
					var re *readError
					if !errors.As(err, &re) || re.code != readreason.UserPermissionCheckFailed {
						t.Errorf("tool behind cluster://%s: got %v, want user_permission_check_failed", r.name, err)
					}
				}
			}
			events := readResourceText(t, ctx, handleResourceEvents)
			for _, team := range tc.served {
				if !strings.Contains(events, "warning marker in "+team) {
					t.Errorf("cluster://events lost %s: %s", team, events)
				}
			}
		})
	}
}

// When Radar can't list namespaces for a reason other than RBAC, discovery
// has no candidates to check. That is a failed check, not "no access", and it
// is not cached.
func TestNamespaceInventoryFailureIsAFailedCheck(t *testing.T) {
	k8s.ResetTestState()
	t.Cleanup(k8s.ResetTestState)
	useSARServer(t, "reviewed", map[string]sarAnswer{"pods/team-a": sarAllow})
	ctx := pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: "reviewed"})
	allowed, failed := filterNamespacesForUserWithStatus(ctx, nil)
	if !failed || len(allowed) != 0 {
		t.Fatalf("got allowed=%v failed=%v, want a failed check with nothing allowed", allowed, failed)
	}
	if getPermCache().Get("reviewed", nil) != nil {
		t.Error("a failed check must not be cached")
	}
	// Cluster-wide pod access needs no namespace list, so it is still served.
	useSARServer(t, "wide", map[string]sarAnswer{"pods/": sarAllow})
	wide := pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: "wide"})
	if allowed, failed := filterNamespacesForUserWithStatus(wide, nil); failed || allowed != nil {
		t.Errorf("cluster-wide reader: got allowed=%v failed=%v, want unrestricted", allowed, failed)
	}
	// Other callers of the inventory keep the same best-effort list.
	if got, want := mcpAllNamespaceNames(ctx), func() []string { ns, _ := k8s.GetAccessibleNamespaces(ctx); return ns }(); !slices.Equal(got, want) {
		t.Errorf("inventory = %v, want the best-effort list %v", got, want)
	}
}
