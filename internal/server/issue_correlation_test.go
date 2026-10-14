package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/meaningfulchanges"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

func initIssueCorrelationState(t *testing.T) timeline.EventStore {
	t.Helper()
	previous := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	t.Cleanup(func() { k8s.SetConnectionStatus(previous) })

	timeline.ResetStore()
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 200}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	t.Cleanup(func() {
		timeline.ResetStore()
		if err := timeline.InitStore(timeline.DefaultStoreConfig()); err != nil {
			t.Fatalf("re-init global store: %v", err)
		}
	})
	timeline.SetObservationStartForTest(time.Now().Add(-2 * time.Hour))
	store := timeline.GetStore()
	if err := store.Append(context.Background(), timeline.TimelineEvent{
		ID: "web-spec", Timestamp: time.Now().Add(-5 * time.Minute),
		Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
		Kind: "Deployment", APIVersion: "apps/v1", Namespace: "shop", Name: "web",
		EventType: timeline.EventTypeUpdate,
		Diff:      &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "spec.template.spec.containers[web].image", OldValue: "web:v1", NewValue: "web:v2"}}, Summary: "image changed"},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	return store
}

func getCorrelation(t *testing.T, s *Server, user *auth.User, subjects ...string) (*httptest.ResponseRecorder, issuesapi.IssueCorrelationResponse) {
	t.Helper()
	q := url.Values{}
	for _, subj := range subjects {
		q.Add("subject", subj)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/issues/correlation?"+q.Encode(), nil)
	if user != nil {
		r = r.WithContext(auth.ContextWithUser(r.Context(), user))
	}
	w := httptest.NewRecorder()
	s.handleIssueCorrelation(w, r)
	var resp issuesapi.IssueCorrelationResponse
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
	}
	return w, resp
}

func TestIssueCorrelation_RejectsInvalidRequests(t *testing.T) {
	initIssueCorrelationState(t)
	s := newAuthServer(auth.Config{Mode: "none"})

	tooMany := make([]string, maxCorrelationSubjects+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("Deployment/apps/shop/d-%d", i)
	}
	cases := []struct {
		name     string
		subjects []string
	}{
		{"too many subjects", tooMany},
		{"too few parts", []string{"Deployment/shop/web"}},
		{"too many parts", []string{"Deployment/apps/shop/web/extra"}},
		{"no name", []string{"Deployment/apps/shop/"}},
		{"no kind", []string{"/apps/shop/web"}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if w, _ := getCorrelation(t, s, nil, tt.subjects...); w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (%s)", w.Code, w.Body.String())
			}
		})
	}
	if w, resp := getCorrelation(t, s, nil); w.Code != http.StatusOK || len(resp.Results) != 0 {
		t.Fatalf("no subjects: status=%d results=%+v, want 200 and none", w.Code, resp.Results)
	}
}

// Each subject gets its own answer in request order, and "can't tell" is
// stated with a reason rather than left blank.
func TestIssueCorrelation_AnswersEachSubject(t *testing.T) {
	initIssueCorrelationState(t)
	s := newAuthServer(auth.Config{Mode: "none"})

	w, resp := getCorrelation(t, s, nil,
		"Deployment/apps/shop/web",
		"Service//other/api",
		"Pod//shop/web-0",
	)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if len(resp.Results) != 3 {
		t.Fatalf("results = %+v, want 3", resp.Results)
	}
	if web := resp.Results[0]; web.Name != "web" || len(web.CorrelatedChanges) != 1 || web.NoRecentChanges != nil || web.UnknownReason != "" {
		t.Fatalf("changed Deployment: %+v", web)
	}
	if api := resp.Results[1]; api.Name != "api" || api.NoRecentChanges == nil || api.NoRecentChanges.WindowSeconds != 3600 || api.UnknownReason != "" {
		t.Fatalf("quiet Service: %+v", api)
	}
	if pod := resp.Results[2]; pod.UnknownReason != issuesapi.CorrelationUntrackedKind || pod.NoRecentChanges != nil {
		t.Fatalf("untracked Pod: %+v", pod)
	}
}

func TestIssueCorrelation_ReportsShortObservation(t *testing.T) {
	initIssueCorrelationState(t)
	timeline.SetObservationStartForTest(time.Now().Add(-time.Minute))
	s := newAuthServer(auth.Config{Mode: "none"})

	_, resp := getCorrelation(t, s, nil, "Deployment//shop/web")
	if len(resp.Results) != 1 || resp.Results[0].UnknownReason != issuesapi.CorrelationObservationTooShort {
		t.Fatalf("results = %+v, want observation_too_short", resp.Results)
	}
}

// A subject the caller cannot list — wrong kind in an allowed namespace, or a
// namespace outside their access — is answered not_permitted and nothing
// about its history is returned.
func TestIssueCorrelation_AuthorizesEachSubject(t *testing.T) {
	initIssueCorrelationState(t)
	s := newAuthServer(auth.Config{Mode: "proxy"})
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"shop"}}
	perms.SetCanI("list", "apps", "deployments", "shop", true)
	perms.SetCanI("list", "", "services", "shop", false)
	s.permCache.Set("alice", nil, perms)

	_, resp := getCorrelation(t, s, &auth.User{Username: "alice"},
		"Deployment//shop/web",
		"Service//shop/api",
		"Deployment//other/web",
	)
	if len(resp.Results) != 3 {
		t.Fatalf("results = %+v, want 3", resp.Results)
	}
	if web := resp.Results[0]; len(web.CorrelatedChanges) != 1 {
		t.Fatalf("readable Deployment should carry its change: %+v", web)
	}
	for _, denied := range resp.Results[1:] {
		if denied.UnknownReason != issuesapi.CorrelationNotPermitted || denied.NoRecentChanges != nil || len(denied.CorrelatedChanges) != 0 {
			t.Fatalf("unreadable subject leaked history: %+v", denied)
		}
	}
}

// Native Helm rows carry no GVR; the REST filter must let them through (they
// were read as the caller) instead of dropping them and turning every Helm
// issue into "can't tell" once auth is on. The MCP filter's twin assertion
// lives in TestFilterRecentChangesRBAC.
func TestIssueCorrelation_HelmChangesSurviveRESTFilter(t *testing.T) {
	initIssueCorrelationState(t)
	s := newAuthServer(auth.Config{Mode: "proxy"})
	s.permCache.Set("alice", nil, &auth.UserPermissions{AllowedNamespaces: []string{"shop"}})
	ctx := auth.ContextWithUser(context.Background(), &auth.User{Username: "alice"})

	release := issuesapi.RecentChange{
		Source: meaningfulchanges.HelmChangeSource, Kind: "HelmRelease", Namespace: "shop", Name: "cart",
		ChangeType: "upgrade", ChangeCategory: issuesapi.ChangeCategorySpecConfig,
	}
	src := meaningfulchanges.CorrelationSources{
		Visible: s.filterRecentChangesByRBAC,
		HelmChanges: func(context.Context, string, string, time.Duration) ([]issuesapi.RecentChange, error) {
			return []issuesapi.RecentChange{release}, nil
		},
	}
	window, _ := meaningfulchanges.CorrelationWindow()
	got := meaningfulchanges.CorrelateSubject(ctx, "HelmRelease", "helm.sh", "shop", "cart", window, src)
	if len(got.Changes) != 1 || got.Unknown != "" {
		t.Fatalf("Helm release change dropped by the REST filter: %+v", got)
	}

	unreadable := s.filterRecentChangesByRBAC(ctx, []issuesapi.RecentChange{
		{Kind: "Secret", APIVersion: "v1", Namespace: "shop", Name: "db"},
	})
	if len(unreadable) != 0 {
		t.Fatalf("the Helm pass-through must not open the gate for real kinds: %+v", unreadable)
	}
}

func TestIssueCorrelation_HelmSubjectWithoutHelmClient(t *testing.T) {
	initIssueCorrelationState(t)
	s := newAuthServer(auth.Config{Mode: "none"})
	w, resp := getCorrelation(t, s, nil, "HelmRelease/helm.sh/shop/cart")
	if w.Code != http.StatusOK || len(resp.Results) != 1 || resp.Results[0].UnknownReason != issuesapi.CorrelationLookupFailed {
		t.Fatalf("status=%d results=%+v, want lookup_failed", w.Code, resp.Results)
	}
}

// The lookup only reads, so it is served as a GET: callers that authorize any
// other method as a write must still be able to use it.
func TestIssueCorrelation_RoutedAsGET(t *testing.T) {
	var body issuesapi.IssueCorrelationResponse
	assertOK(t, get(t, "/api/issues/correlation?subject=Pod//default/web-0"), &body)
	if len(body.Results) != 1 || body.Results[0].UnknownReason != issuesapi.CorrelationUntrackedKind {
		t.Fatalf("results = %+v, want one untracked_kind answer", body.Results)
	}
}
