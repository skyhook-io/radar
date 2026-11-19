package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/issuecorrelation"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// rowRef is one issue= value for /api/issues/recent-changes.
type rowRef struct {
	kind, name, group string
	category          issuesapi.Category
}

func (ref rowRef) param(id string) string {
	value := id + "/" + ref.kind + "/shop/" + ref.name
	if ref.group != "" {
		value += "/" + ref.group
	}
	return value
}

func getRecentChanges(t *testing.T, env *authTestEnv, user string, values ...string) (int, IssueRecentChangesResponse) {
	t.Helper()
	query := url.Values{"issue": values}
	response := env.authGet(t, "/api/issues/recent-changes?"+query.Encode(), user, "")
	defer response.Body.Close()
	var body IssueRecentChangesResponse
	if response.StatusCode == http.StatusOK {
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return response.StatusCode, body
}

// issueID finds the id of the subject's issue in category, as a caller who
// reads everything sees it.
func issueID(t *testing.T, env *authTestEnv, user string, ref rowRef) string {
	t.Helper()
	status, listed := getResourceIssues(t, env, user, "/api/issues/resource/"+ref.kind+"/shop/"+ref.name+"?coverage=1&group="+ref.group)
	if status != http.StatusOK {
		t.Fatalf("%s/%s: status %d", ref.kind, ref.name, status)
	}
	for _, iss := range listed.Issues {
		if iss.Category == ref.category {
			return iss.ID
		}
	}
	t.Fatalf("%s/%s: no %s issue", ref.kind, ref.name, ref.category)
	return ""
}

// singleIssueRowChange is what the per-resource route's correlation of id
// says the row names for user, or false when that route gives the row
// nothing (refused, not found, or no change before first seen).
func singleIssueRowChange(t *testing.T, env *authTestEnv, user string, ref rowRef, id string) (IssueRowChange, bool) {
	t.Helper()
	status, body := getResourceIssues(t, env, user, "/api/issues/resource/"+ref.kind+"/shop/"+ref.name+"?group="+ref.group+"&correlate="+id)
	if status != http.StatusOK || body.CorrelationStatus != "" {
		return IssueRowChange{}, false
	}
	for _, iss := range body.Issues {
		if iss.ID != id {
			continue
		}
		// The answer's history is the caller's filtered copy; the fixture's
		// history is complete, so it decides the same as the server's own.
		var history issuesapi.HistoryCoverage
		if iss.Correlation != nil && iss.Correlation.History != nil {
			history = *iss.Correlation.History
		}
		change, ok := issuecorrelation.RowChange(iss, history)
		return IssueRowChange{FirstSeen: iss.FirstSeen, ResourceCreatedAt: iss.ResourceCreatedAt, Change: change}, ok
	}
	return IssueRowChange{}, false
}

// Every row gets exactly what the per-resource route's correlation gives
// the same caller, in one mixed batch: readable rows their change, rows the
// caller can't read nothing.
func TestProxyAuth_IssueRecentChangesMatchesSingleIssue(t *testing.T) {
	store := correlationFixture(t)
	recordChanges(t, store)
	env := newAuthTestServer(t)
	correlationUser(env, "full", []string{"shop"})
	correlationUser(env, "no-config", []string{"shop"}, "configmaps", "secrets")
	correlationUser(env, "no-service", []string{"shop"}, "services")
	correlationUser(env, "no-quota", []string{"shop"}, "resourcequotas")
	correlationUser(env, "other-namespace", []string{"other"})

	refs := []rowRef{
		{"Deployment", "web", "apps", issuesapi.CategoryCrashLoop},
		{"Service", "cart", "", issuesapi.CategoryServiceNoEndpoints},
		{"Deployment", "search", "apps", issuesapi.CategoryQuotaExceeded},
		{"Deployment", "hooked", "apps", issuesapi.CategoryAdmissionWebhookBlocking},
		{"Deployment", "ghost", "apps", issuesapi.CategoryCrashLoop},
	}
	ids := make([]string, len(refs))
	var values []string
	for i, ref := range refs {
		ids[i] = issueID(t, env, "full", ref)
		values = append(values, ref.param(ids[i]))
	}

	for _, user := range []string{"full", "no-config", "no-service", "no-quota", "other-namespace"} {
		t.Run(user, func(t *testing.T) {
			status, body := getRecentChanges(t, env, user, values...)
			if status != http.StatusOK {
				t.Fatalf("status %d", status)
			}
			for i, ref := range refs {
				want, ok := singleIssueRowChange(t, env, user, ref, ids[i])
				got, present := body.Changes[ids[i]]
				if present != ok {
					t.Fatalf("%s/%s: present %v, per-resource route gives a change: %v (%+v)", ref.kind, ref.name, present, ok, got)
				}
				if !ok {
					continue
				}
				if !got.FirstSeen.Equal(want.FirstSeen) || !got.ResourceCreatedAt.Equal(want.ResourceCreatedAt) {
					t.Fatalf("%s/%s: occurrence %v|%v, want %v|%v", ref.kind, ref.name, got.FirstSeen, got.ResourceCreatedAt, want.FirstSeen, want.ResourceCreatedAt)
				}
				if got.Change.Kind != want.Change.Kind || got.Change.Name != want.Change.Name || got.Change.Timestamp != want.Change.Timestamp || got.Change.EventID != want.Change.EventID {
					t.Fatalf("%s/%s: change %+v, want %+v", ref.kind, ref.name, got.Change, want.Change)
				}
			}
		})
	}

	t.Run("what each caller gets", func(t *testing.T) {
		for _, tc := range []struct {
			user      string
			ref       int
			wantEvent string
		}{
			// The image change four minutes ago is the newest before the
			// crash loop, which has no first-seen time.
			{"full", 0, "Deployment/web"},
			// The caller can't read the ConfigMaps and Secrets, which may hold
			// a newer change, so the row names nothing.
			{"no-config", 0, ""},
			{"full", 1, "Service/cart"},
			{"no-service", 1, ""},
			{"full", 2, "ResourceQuota/compute"},
			{"no-quota", 2, ""},
			{"other-namespace", 0, ""},
		} {
			_, body := getRecentChanges(t, env, tc.user, values...)
			got, present := body.Changes[ids[tc.ref]]
			if tc.wantEvent == "" {
				if present {
					t.Fatalf("%s/%s: a row the caller can't read got %+v", tc.user, refs[tc.ref].name, got)
				}
				continue
			}
			if !present || got.Change.EventID != tc.wantEvent {
				t.Fatalf("%s/%s: %+v, want event %s", tc.user, refs[tc.ref].name, got, tc.wantEvent)
			}
		}
	})
}

// A row whose subject the caller can't read looks exactly like a row with
// no change: the response doesn't depend on whether the hidden change
// exists.
func TestProxyAuth_IssueRecentChangesHiddenSubjectLooksLikeNoChange(t *testing.T) {
	answer := func(except ...string) string {
		store := correlationFixture(t)
		recordChanges(t, store, except...)
		env := newAuthTestServer(t)
		correlationUser(env, "full", []string{"shop"})
		correlationUser(env, "no-service", []string{"shop"}, "services")
		ref := rowRef{"Service", "cart", "", issuesapi.CategoryServiceNoEndpoints}
		id := issueID(t, env, "full", ref)
		response := env.authGet(t, "/api/issues/recent-changes?issue="+url.QueryEscape(ref.param(id)), "no-service", "")
		defer response.Body.Close()
		var raw json.RawMessage
		if err := json.NewDecoder(response.Body).Decode(&raw); err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	changed, unchanged := answer(), answer("cart")
	if changed != unchanged || !strings.Contains(changed, `"changes":{}`) {
		t.Fatalf("hidden Service changed: %s; unchanged: %s", changed, unchanged)
	}
}

func TestProxyAuth_IssueRecentChangesRequests(t *testing.T) {
	store := correlationFixture(t)
	recordChanges(t, store)
	env := newAuthTestServer(t)
	correlationUser(env, "full", []string{"shop"})
	web := rowRef{"Deployment", "web", "apps", issuesapi.CategoryCrashLoop}
	id := issueID(t, env, "full", web)

	many := make([]string, maxIssueRecentChanges+1)
	for i := range many {
		many[i] = web.param(id)
	}
	if status, _ := getRecentChanges(t, env, "full", many...); status != http.StatusBadRequest {
		t.Fatalf("%d issues: status %d, want 400", len(many), status)
	}
	if status, body := getRecentChanges(t, env, "full", many[:maxIssueRecentChanges]...); status != http.StatusOK || len(body.Changes) != 1 {
		t.Fatalf("%d issues: status %d, %+v", maxIssueRecentChanges, status, body)
	}
	for _, bad := range [][]string{nil, {"only-an-id"}, {id + "/Deployment//web"}, {id + "/Deployment/shop/web/apps/extra"}} {
		if status, _ := getRecentChanges(t, env, "full", bad...); status != http.StatusBadRequest {
			t.Fatalf("%q: status %d, want 400", bad, status)
		}
	}
	// An id the subject doesn't have, or an issue that resolved, is absent.
	if status, body := getRecentChanges(t, env, "full", web.param("no-such-issue")); status != http.StatusOK || len(body.Changes) != 0 {
		t.Fatalf("unknown id: %d %+v", status, body)
	}
}

// A native Helm release's row names its release operation, which comes from
// release records rather than a timeline event, so it has no event id.
func TestProxyAuth_IssueRecentChangesHelmRowHasNoEventID(t *testing.T) {
	correlationFixture(t)
	helmStorage(t, "denied", "")
	env := newAuthTestServer(t)
	correlationUser(env, "reader", []string{"shop"})
	correlationUser(env, "denied", []string{"shop"})
	_, listed := getResourceIssues(t, env, "reader", "/api/issues/resource/HelmRelease/shop/payments?group=helm.sh&coverage=1")
	if len(listed.Issues) != 1 {
		t.Fatalf("issues %+v", listed.Issues)
	}
	value := listed.Issues[0].ID + "/HelmRelease/shop/payments/helm.sh"
	_, body := getRecentChanges(t, env, "reader", value)
	got, ok := body.Changes[listed.Issues[0].ID]
	if !ok || got.Change.Source != issuecorrelation.HelmChangeSource || got.Change.EventID != "" {
		t.Fatalf("reader: %+v", body)
	}
	revision := false
	for _, field := range got.Change.Fields {
		revision = revision || field.Path == "helm.revision" && field.NewValue != nil
	}
	if !revision {
		t.Fatalf("Helm row lost its revision: %+v", got.Change.Fields)
	}
	if _, body := getRecentChanges(t, env, "denied", value); len(body.Changes) != 0 {
		t.Fatalf("denied: %+v", body)
	}
}

// Shared without authentication: the batched lookup answers a direct read
// and the Hub tunnel, and refuses a page on another site.
func TestIssueRecentChangesWithoutAuth(t *testing.T) {
	store := correlationFixture(t)
	recordChanges(t, store)
	srv := New(Config{DevMode: true})
	handler := srv.Handler()
	ts := httptest.NewServer(handler)
	tunnel := httptest.NewServer(cloud.AuthenticatedTunnelHandler(handler))
	t.Cleanup(func() { ts.Close(); tunnel.Close(); srv.Stop() })

	get := func(base string, crossSite bool, path string) (int, IssueRecentChangesResponse, ResourceIssuesResponse) {
		req, _ := http.NewRequest(http.MethodGet, base+path, nil)
		if crossSite {
			req.Header.Set("Sec-Fetch-Site", "cross-site")
			req.Header.Set("Origin", "https://evil.example")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var rows IssueRecentChangesResponse
		var issues ResourceIssuesResponse
		if resp.StatusCode == http.StatusOK {
			if strings.Contains(path, "recent-changes") {
				_ = json.NewDecoder(resp.Body).Decode(&rows)
			} else {
				_ = json.NewDecoder(resp.Body).Decode(&issues)
			}
		}
		return resp.StatusCode, rows, issues
	}
	_, _, listed := get(ts.URL, false, "/api/issues/resource/Service/shop/cart?coverage=1")
	if len(listed.Issues) == 0 {
		t.Fatal("no issue")
	}
	id := listed.Issues[0].ID
	path := "/api/issues/recent-changes?issue=" + url.QueryEscape(id+"/Service/shop/cart")
	if status, rows, _ := get(ts.URL, false, path); status != http.StatusOK || rows.Changes[id].Change.Kind != "Service" {
		t.Fatalf("direct read: %d %+v", status, rows)
	}
	if status, _, _ := get(ts.URL, true, path); status != http.StatusForbidden {
		t.Fatalf("cross-site browser read: %d, want 403", status)
	}
	if status, rows, _ := get(tunnel.URL, true, path); status != http.StatusOK || rows.Changes[id].Change.Kind != "Service" {
		t.Fatalf("over the Hub tunnel: %d %+v", status, rows)
	}
}
