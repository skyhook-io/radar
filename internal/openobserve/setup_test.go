package openobserve

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/openobserve/oofake"
	"github.com/skyhook-io/radar/pkg/prom"
	oo "github.com/skyhook-io/radar/pkg/prom/openobserve"
)

const (
	podDB  = "5d8da521-0fc8-4ae1-8fa3-472e4ae52619"
	podWeb = "0b9d1d1e-4b8a-4a59-9a2a-1f0c4a1b2c3d"
)

func runningPod(name, uid string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "demo", Name: name, UID: types.UID(uid)},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

// setupHarness records what Setup saves into an in-memory config file.
type setupHarness struct {
	file  config.Config
	saves int
}

func (h *setupHarness) deps(existing *config.OpenObserveConfig, uid string, pods ...*corev1.Pod) SetupDeps {
	h.file.OpenObserve = existing
	return SetupDeps{
		Existing:    existing,
		ContextName: "kind-test",
		Kube:        kubeWithUID(uid, pods...),
		Save: func(update func(*config.Config)) error {
			h.saves++
			update(&h.file)
			return nil
		},
		Now: func() time.Time { return time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC) },
	}
}

func fieldsFor(url string) SetupRequest {
	return SetupRequest{BaseURL: url + "/", User: "sa@example.com", Token: "s3cret-token"}
}

// interceptTransport answers queries containing a key itself and sends the
// rest to OpenObserve.
func interceptTransport(answers map[string][]map[string]string) func(oo.Connection) (prom.Transport, error) {
	return func(c oo.Connection) (prom.Transport, error) {
		inner, err := c.NewHTTPTransport(SetupRequestTimeout)
		if err != nil {
			return nil, err
		}
		return interceptor{inner: inner, answers: answers}, nil
	}
}

type interceptor struct {
	inner   prom.Transport
	answers map[string][]map[string]string
}

func (i interceptor) Do(ctx context.Context, method, path string, params url.Values) ([]byte, error) {
	q := params.Get("query")
	for key, rows := range i.answers {
		if strings.Contains(q, key) {
			rec := &bodyRecorder{header: http.Header{}}
			oofake.WriteVector(rec, rows)
			return rec.body, nil
		}
	}
	return i.inner.Do(ctx, method, path, params)
}

func (i interceptor) Address() string { return i.inner.Address() }

type bodyRecorder struct {
	header http.Header
	body   []byte
}

func (b *bodyRecorder) Header() http.Header { return b.header }
func (b *bodyRecorder) Write(p []byte) (int, error) {
	b.body = append(b.body, p...)
	return len(p), nil
}
func (b *bodyRecorder) WriteHeader(int) {}

func TestSetupSavesAnIDMatch(t *testing.T) {
	srv := oofake.Start(t, &oofake.Server{IDs: map[string]string{uidB: "oo-b"}})
	h := &setupHarness{}
	res, err := Setup(context.Background(), h.deps(nil, uidB), fieldsFor(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != SetupSaved || res.Match == nil || res.Match.By != "id" || res.Match.Value != uidB || res.ClusterUID != uidB {
		t.Fatalf("result = %+v", res)
	}
	if res.Notes.Missing == nil {
		t.Fatal("missing must be a list, not null")
	}
	saved := h.file.OpenObserve
	if h.saves != 1 || saved == nil || saved.BaseURL != srv.URL || saved.Org != "default" || saved.Token != "s3cret-token" {
		t.Fatalf("saved = %+v (saves %d)", saved, h.saves)
	}
	if got := saved.Matches[uidB]; got.By != "id" || got.Label != oo.IDLabel || got.Context != "kind-test" {
		t.Fatalf("match = %+v", got)
	}
}

func TestSetupNameFoundAsksToConfirmThenSavesByName(t *testing.T) {
	srv := oofake.Start(t, &oofake.Server{PodCluster: "oo-c", Pods: []oo.PodRef{{Name: "db-0", UID: podDB}, {Name: "web-1", UID: podWeb}}})
	h := &setupHarness{}
	deps := h.deps(nil, uidC, runningPod("db-0", podDB), runningPod("web-1", podWeb))

	res, err := Setup(context.Background(), deps, fieldsFor(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != SetupConfirm || res.Suggestion == nil || *res.Suggestion != (oo.LabelValue{Label: "cluster", Value: "oo-c"}) || res.Reason == "" {
		t.Fatalf("result = %+v", res)
	}
	if h.saves != 0 {
		t.Fatal("a name found without confirmation was saved")
	}

	req := fieldsFor(srv.URL)
	req.Confirm = res.Suggestion
	res, err = Setup(context.Background(), deps, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != SetupSaved || res.Match == nil || res.Match.By != "name" || res.Notes.Warning != oo.NameMatchWarning {
		t.Fatalf("result = %+v", res)
	}
	got := h.file.OpenObserve.Matches[uidC]
	if got.By != "name" || got.Label != "cluster" || got.Value != "oo-c" || got.ConfirmedAt != "2026-11-24T10:00:00Z" || got.Context != "kind-test" {
		t.Fatalf("match = %+v", got)
	}

	// Only the name OpenObserve suggests can be confirmed.
	h2 := &setupHarness{}
	req.Confirm = &oo.LabelValue{Label: "cluster", Value: "someone-else"}
	if _, err := Setup(context.Background(), h2.deps(nil, uidC, runningPod("db-0", podDB)), req); err == nil || h2.saves != 0 {
		t.Fatalf("confirming a name OpenObserve didn't suggest: err %v, saves %d", err, h2.saves)
	}
}

func TestSetupRefusesDefaultAndConflictingNames(t *testing.T) {
	pods := []*corev1.Pod{runningPod("db-0", podDB)}
	refs := []oo.PodRef{{Name: "db-0", UID: podDB}}
	for _, tc := range []struct {
		name      string
		f         *oofake.Server
		transport func(oo.Connection) (prom.Transport, error)
		value     string
	}{
		{name: "cluster1", f: &oofake.Server{PodCluster: "cluster1", Pods: refs}, value: "cluster1"},
		{name: "conflict", f: &oofake.Server{PodCluster: "shop", Pods: refs}, value: "shop", transport: interceptTransport(map[string][]map[string]string{
			"count by (uid, pod, cluster)": {
				{"uid": podDB, "pod": "db-0", "cluster": "shop"},
				{"uid": "ffffffff-0000-0000-0000-000000000000", "pod": "db-0", "cluster": "shop"},
			},
		})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := oofake.Start(t, tc.f)
			h := &setupHarness{}
			deps := h.deps(nil, uidC, pods...)
			deps.Transport = tc.transport
			res, err := Setup(context.Background(), deps, fieldsFor(srv.URL))
			if err != nil {
				t.Fatal(err)
			}
			if res.Outcome != SetupNotFound || res.Suggestion != nil || !strings.Contains(res.Reason, "Radar won't use") {
				t.Fatalf("result = %+v", res)
			}
			req := fieldsFor(srv.URL)
			req.Confirm = &oo.LabelValue{Label: "cluster", Value: tc.value}
			var setupErr *SetupError
			if _, err := Setup(context.Background(), deps, req); !errors.As(err, &setupErr) {
				t.Fatalf("confirming %q: err = %v", tc.value, err)
			}
			if h.saves != 0 {
				t.Fatal("a refused name was saved")
			}
		})
	}
}

func TestSetupNotFoundListsClusterNames(t *testing.T) {
	srv := oofake.Start(t, &oofake.Server{})
	h := &setupHarness{}
	deps := h.deps(nil, uidC, runningPod("db-0", podDB))
	deps.Transport = interceptTransport(map[string][]map[string]string{
		"count by (cluster, cluster_name": {{"cluster": "oo-b"}, {"cluster": "prod-eu"}},
	})
	res, err := Setup(context.Background(), deps, fieldsFor(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	want := []oo.LabelValue{{Label: "cluster", Value: "oo-b"}, {Label: "cluster", Value: "prod-eu"}}
	if res.Outcome != SetupNotFound || !strings.Contains(res.Reason, "None of the 1 sampled Pods") || len(res.ClusterNames) != 2 || res.ClusterNames[0] != want[0] || res.ClusterNames[1] != want[1] {
		t.Fatalf("result = %+v", res)
	}
	if h.saves != 0 {
		t.Fatal("nothing should be saved when the cluster isn't found")
	}
}

func TestSetupLoginFailureSavesNothingAndHidesTheToken(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{
		{status: http.StatusUnauthorized, want: "OpenObserve rejected the login. Check the user and token."},
		{status: http.StatusInternalServerError, want: "HTTP 500"},
	} {
		srv := oofake.Start(t, &oofake.Server{FailStatus: tc.status, FailBody: `{"error":"bad credentials for s3cret-token"}`})
		h := &setupHarness{}
		_, err := Setup(context.Background(), h.deps(nil, uidB), fieldsFor(srv.URL))
		var setupErr *SetupError
		if !errors.As(err, &setupErr) || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("HTTP %d: err = %v, want %q", tc.status, err, tc.want)
		}
		if strings.Contains(err.Error(), "s3cret-token") {
			t.Fatalf("token leaked: %v", err)
		}
		if h.saves != 0 {
			t.Fatal("a failed login was saved")
		}
	}

	h := &setupHarness{}
	_, err := Setup(context.Background(), h.deps(nil, uidB), fieldsFor("http://127.0.0.1:1"))
	if err == nil || !strings.HasPrefix(err.Error(), "Radar couldn't reach OpenObserve at http://127.0.0.1:1") {
		t.Fatalf("unreachable: err = %v", err)
	}
}

func TestSetupSaysWhenTheLoginWorksButCannotReadMetrics(t *testing.T) {
	srv := oofake.Start(t, &oofake.Server{IDs: map[string]string{uidB: "oo-b"}, MetricsForbidden: true})
	h := &setupHarness{}
	_, err := Setup(context.Background(), h.deps(nil, uidB), fieldsFor(srv.URL))
	var setupErr *SetupError
	if !errors.As(err, &setupErr) || !strings.Contains(err.Error(), "The login works, but OpenObserve doesn't let this account read metrics") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "rejected the login") {
		t.Fatalf("a permission problem was reported as a bad login: %v", err)
	}
	if h.saves != 0 {
		t.Fatal("a connection that can't read metrics was saved")
	}

	forbidden := oofake.Start(t, &oofake.Server{FailStatus: http.StatusForbidden})
	if _, err := Setup(context.Background(), h.deps(nil, uidB), fieldsFor(forbidden.URL)); err == nil || !strings.Contains(err.Error(), "OpenObserve rejected the login") {
		t.Fatalf("a refused login check: err = %v", err)
	}
}

func TestSetupTokenRules(t *testing.T) {
	srv := oofake.Start(t, &oofake.Server{IDs: map[string]string{uidB: "oo-b"}})
	t.Setenv("OO_TOKEN_TEST", "from-env")
	existing := func() *config.OpenObserveConfig {
		return &config.OpenObserveConfig{BaseURL: srv.URL, Org: "default", User: "sa@example.com", TokenEnv: "OO_TOKEN_TEST", Token: "old",
			Matches: map[string]config.OpenObserveMatch{uidC: {Label: "cluster", Value: "oo-c", By: "name"}}}
	}

	// Same address, organization and user, empty token: the saved token and
	// variable stay, and so do other clusters' matches.
	h := &setupHarness{}
	req := fieldsFor(srv.URL)
	req.Token = ""
	if _, err := Setup(context.Background(), h.deps(existing(), uidB), req); err != nil {
		t.Fatal(err)
	}
	saved := h.file.OpenObserve
	if saved.TokenEnv != "OO_TOKEN_TEST" || saved.Token != "old" || len(saved.Matches) != 2 {
		t.Fatalf("saved = %+v", saved)
	}

	// Another user without a token: refused before anything is sent.
	h = &setupHarness{}
	req.User = "other@example.com"
	_, err := Setup(context.Background(), h.deps(existing(), uidB), req)
	if err == nil || err.Error() != "Enter the token." || h.saves != 0 {
		t.Fatalf("different target without token: err = %v, saves %d", err, h.saves)
	}

	// A new organization drops the matches, which belong to the old one's data.
	h = &setupHarness{}
	other := &config.OpenObserveConfig{BaseURL: srv.URL, Org: "elsewhere", User: "sa@example.com", Token: "old",
		Matches: map[string]config.OpenObserveMatch{uidC: {Label: "cluster", Value: "oo-c", By: "name"}}}
	if _, err := Setup(context.Background(), h.deps(other, uidB), fieldsFor(srv.URL)); err != nil {
		t.Fatal(err)
	}
	if m := h.file.OpenObserve.Matches; len(m) != 1 || m[uidB].By != "id" {
		t.Fatalf("matches after an organization change = %+v", m)
	}
	if h.file.OpenObserve.TokenEnv != "" || h.file.OpenObserve.Token != "s3cret-token" {
		t.Fatalf("typed token not saved: %+v", h.file.OpenObserve)
	}

	// The saved connection, reused as is.
	h = &setupHarness{}
	if res, err := Setup(context.Background(), h.deps(existing(), uidB), SetupRequest{UseSaved: true}); err != nil || res.Outcome != SetupSaved {
		t.Fatalf("useSaved: %+v, %v", res, err)
	}
	h = &setupHarness{}
	if _, err := Setup(context.Background(), h.deps(nil, uidB), SetupRequest{UseSaved: true}); err == nil || h.saves != 0 {
		t.Fatalf("useSaved without a saved connection: err = %v", err)
	}
}

func TestSetupRejectsInvalidFields(t *testing.T) {
	for _, req := range []SetupRequest{
		{User: "sa@example.com", Token: "x"},
		{BaseURL: "https://oo.example.com", Token: "x"},
		{BaseURL: "ftp://oo.example.com", User: "sa@example.com", Token: "x"},
		{BaseURL: "https://oo.example.com", User: "sa@example.com", Token: "x", ClusterLabel: "bad-label"},
	} {
		h := &setupHarness{}
		var setupErr *SetupError
		if _, err := Setup(context.Background(), h.deps(nil, uidB), req); !errors.As(err, &setupErr) || h.saves != 0 {
			t.Fatalf("%+v: err = %v", req, err)
		}
	}
}
