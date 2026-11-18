package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
)

// The shape below is copied from a file Velero actually wrote on a kind cluster,
// not from the struct definition. It matters: the restore controller filed its
// cluster-scoped warning under `namespaces` with the key "cluster" rather than
// in the top-level `cluster` field, so a parser that only read the documented
// top-level buckets would have shown one warning where Velero reported two —
// and the count beside it would have said 2.
const realResultsJSON = `{
  "errors": {},
  "warnings": {
    "namespaces": {
      "cluster": ["failed to list stub VolumeGroupSnapshotContents: no matches for kind \"VolumeGroupSnapshotContent\""],
      "demo-app": ["could not restore, ConfigMap:kube-root-ca.crt already exists. Warning: the in-cluster version is different than the backed-up version"]
    }
  }
}`

func gzipped(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(body)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func serveResults(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchVeleroResultsReadsWhatVeleroWrote(t *testing.T) {
	srv := serveResults(t, gzipped(t, realResultsJSON))

	got, err := fetchVeleroResults(context.Background(), srv.URL, localVeleroDownloadPolicy)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(got.Warnings) != 2 {
		t.Fatalf("warnings = %d, want 2 — the count on the object says 2: %+v", len(got.Warnings), got.Warnings)
	}
	if len(got.Errors) != 0 {
		t.Errorf("errors = %d, want 0", len(got.Errors))
	}
	// Scope is kept, not flattened away: "could not restore X" against a
	// namespace is a different problem from the same words against the install.
	if got.Warnings[0].Scope != "cluster" || got.Warnings[1].Scope != "demo-app" {
		t.Errorf("scopes = %q/%q, want cluster/demo-app (sorted, so the order is stable)",
			got.Warnings[0].Scope, got.Warnings[1].Scope)
	}
}

// Errors come before warnings, and both keep a stable order across clicks. A
// list that reshuffles reads as data that changed.
func TestFetchVeleroResultsIsOrdered(t *testing.T) {
	body := `{
      "errors":   {"namespaces": {"zeta": ["e-zeta"], "alpha": ["e-alpha"]}},
      "warnings": {"velero": ["w-velero"], "cluster": ["w-cluster"], "namespaces": {"beta": ["w-beta"]}}
    }`
	srv := serveResults(t, gzipped(t, body))

	got, err := fetchVeleroResults(context.Background(), srv.URL, localVeleroDownloadPolicy)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(got.Errors) != 2 || got.Errors[0].Scope != "alpha" || got.Errors[1].Scope != "zeta" {
		t.Errorf("errors = %+v, want alpha then zeta", got.Errors)
	}
	// velero, then cluster, then namespaces — Velero's own order of specificity.
	wantScopes := []string{"velero", "cluster", "beta"}
	if len(got.Warnings) != len(wantScopes) {
		t.Fatalf("warnings = %+v, want %d", got.Warnings, len(wantScopes))
	}
	for i, want := range wantScopes {
		if got.Warnings[i].Scope != want {
			t.Errorf("warning %d scope = %q, want %q", i, got.Warnings[i].Scope, want)
		}
	}
}

// The URL is whatever the cluster's storage location was configured to hand out.
// That is the same trust boundary as every other field read here, but it still
// gets bounded — a results fetch has no business following a file:// or gopher://
// link out of the process.
func TestFetchVeleroResultsRefusesNonHTTP(t *testing.T) {
	for _, raw := range []string{"file:///etc/passwd", "gopher://example.com/", "ftp://example.com/x"} {
		if _, err := fetchVeleroResults(context.Background(), raw, localVeleroDownloadPolicy); err == nil {
			t.Errorf("fetching %q was allowed", raw)
		}
	}
}

// A results file that is not what it claims must fail loudly. Returning an empty
// list would render as "Velero reported 2 messages but the file holds none",
// which blames Velero for a parse failure here.
func TestFetchVeleroResultsRejectsGarbage(t *testing.T) {
	t.Run("not gzip", func(t *testing.T) {
		srv := serveResults(t, []byte("plain text, not gzip"))
		if _, err := fetchVeleroResults(context.Background(), srv.URL, localVeleroDownloadPolicy); err == nil {
			t.Error("a non-gzip body was accepted")
		}
	})
	t.Run("not json", func(t *testing.T) {
		srv := serveResults(t, gzipped(t, "<html>404</html>"))
		if _, err := fetchVeleroResults(context.Background(), srv.URL, localVeleroDownloadPolicy); err == nil {
			t.Error("a non-JSON body was accepted")
		}
	})
	t.Run("storage says no", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()
		if _, err := fetchVeleroResults(context.Background(), srv.URL, localVeleroDownloadPolicy); err == nil {
			t.Error("a 403 from object storage was treated as success")
		}
	})
}

// A run with thousands of per-item warnings must not be reported as though the
// screen showed all of them.
func TestFlattenVeleroResultReportsTruncation(t *testing.T) {
	msgs := make([]string, maxMessages+10)
	for i := range msgs {
		msgs[i] = "warning"
	}
	got, truncated := flattenVeleroResult(veleroResults{Velero: msgs})
	if !truncated {
		t.Error("truncated = false after capping the list")
	}
	if len(got) != maxMessages {
		t.Errorf("returned %d messages, want the cap of %d", len(got), maxMessages)
	}
}

// Go's default client follows redirects anywhere, including to another host.
// Radar does not always run where the cluster runs, so a storage location's URL
// is a way to point this process at a host the caller never named. Object
// storage answers a pre-signed GET directly; a redirect is not something to
// honour, and the scheme check alone does not stop one.
func TestFetchVeleroResultsDoesNotFollowRedirects(t *testing.T) {
	var elsewhereHit bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		elsewhereHit = true
		_, _ = w.Write(gzipped(t, realResultsJSON))
	}))
	defer elsewhere.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusFound)
	}))
	defer redirector.Close()

	_, err := fetchVeleroResults(context.Background(), redirector.URL, localVeleroDownloadPolicy)
	if err == nil {
		t.Fatal("a redirect was followed and the response accepted")
	}
	if elsewhereHit {
		t.Error("the redirect target was fetched")
	}
	// The handler collapses every fetch failure into one message for the caller,
	// so this is what reaches the server log — the only place a redirect can be
	// told apart from storage simply being unreachable.
	if !strings.Contains(err.Error(), "redirected") {
		t.Errorf("error = %q, want it to name the redirect", err)
	}
}

// awaitDownloadURL is the half of this endpoint that decides whether the reader
// is told "Velero is not answering" — the message that sends someone to look at
// the controller. Each branch below routes to a different answer, so a wrong one
// sends them to the wrong place.
func TestAwaitDownloadURL(t *testing.T) {
	req := func(status map[string]interface{}) *unstructured.Unstructured {
		obj := map[string]interface{}{
			"apiVersion": "velero.io/v1",
			"kind":       "DownloadRequest",
			"metadata":   map[string]interface{}{"name": "backup-abc", "namespace": "velero"},
		}
		if status != nil {
			obj["status"] = status
		}
		return &unstructured.Unstructured{Object: obj}
	}
	scheme := runtime.NewScheme()
	client := func(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
		return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
			map[schema.GroupVersionResource]string{downloadRequestGVR: "DownloadRequestList"}, objs...)
	}

	t.Run("returns the URL the controller filled in", func(t *testing.T) {
		got, err := awaitDownloadURL(context.Background(),
			client(req(map[string]interface{}{"downloadURL": "https://store.example/results.gz", "phase": "Processed"})),
			"velero", "backup-abc", time.Minute)
		if err != nil {
			t.Fatalf("await: %v", err)
		}
		if got != "https://store.example/results.gz" {
			t.Errorf("url = %q, want the one in status", got)
		}
	})

	// A controller that never fills the URL in is the case this endpoint exists
	// to report. The phase travels with the error so the log says what Velero
	// was doing rather than only that it took too long.
	t.Run("names the phase it gave up in", func(t *testing.T) {
		_, err := awaitDownloadURL(context.Background(),
			client(req(map[string]interface{}{"phase": "New"})), "velero", "backup-abc", 0)
		if err == nil {
			t.Fatal("err = nil, want a timeout once the deadline has passed")
		}
		if !strings.Contains(err.Error(), "New") {
			t.Errorf("err = %q, want the phase it was stuck in", err)
		}
	})

	// A DownloadRequest with no status at all is the controller never having
	// touched it — the strongest form of "Velero is not running". Saying
	// "still  after 20s" would lose that.
	t.Run("says so when the controller never touched it", func(t *testing.T) {
		_, err := awaitDownloadURL(context.Background(),
			client(req(nil)), "velero", "backup-abc", 0)
		if err == nil || !strings.Contains(err.Error(), "no status at all") {
			t.Errorf("err = %v, want the absent status called out", err)
		}
	})

	// A caller who can create but not read back a DownloadRequest must reach the
	// 403 branch in the handler, not the "Velero is down" one. That routing is
	// errors.IsForbidden on exactly this error, so the error has to survive
	// unwrapped.
	t.Run("passes a denial back as a denial", func(t *testing.T) {
		c := client()
		c.PrependReactor("get", "downloadrequests", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(
				schema.GroupResource{Group: veleroGroup, Resource: "downloadrequests"}, "backup-abc", errors.New("nope"))
		})
		_, err := awaitDownloadURL(context.Background(), c, "velero", "backup-abc", time.Minute)
		if !apierrors.IsForbidden(err) {
			t.Errorf("err = %v, want a Forbidden the handler can route to 403", err)
		}
	})

	t.Run("stops when the caller goes away", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := awaitDownloadURL(ctx, client(req(map[string]interface{}{"phase": "New"})),
			"velero", "backup-abc", time.Minute); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})
}

// The handler end to end, over a fake controller. What is being pinned is the
// routing: each failure mode sends the reader somewhere different, and telling
// someone their Velero controller is down when the truth is a denied read or an
// unreachable bucket costs them the time it takes to check the wrong thing.
func TestVeleroRunMessagesRoutesEachFailureToItsOwnAnswer(t *testing.T) {
	downloadRequest := func(status map[string]interface{}) *unstructured.Unstructured {
		obj := map[string]interface{}{
			"apiVersion": "velero.io/v1",
			"kind":       "DownloadRequest",
			"metadata":   map[string]interface{}{"name": "nightly-x", "namespace": "velero"},
		}
		if status != nil {
			obj["status"] = status
		}
		return &unstructured.Unstructured{Object: obj}
	}

	// The controller's half of the exchange: Velero fills status.downloadURL in
	// on the object Radar just created, so the fake answers every get with a
	// ready one.
	answerWith := func(t *testing.T, url string) *dynamicfake.FakeDynamicClient {
		t.Helper()
		dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
			map[schema.GroupVersionResource]string{downloadRequestGVR: "DownloadRequestList"})
		dyn.PrependReactor("get", "downloadrequests", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, downloadRequest(map[string]interface{}{"downloadURL": url, "phase": "Processed"}), nil
		})
		return dyn
	}

	post := func(t *testing.T, dyn dynamic.Interface, path string, modes ...string) *httptest.ResponseRecorder {
		t.Helper()
		// Sets the package-level dynamic client, which is what
		// getDynamicClientForRequest falls back to when auth is off.
		if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{
			Group: veleroGroup, Version: "v1", Kind: "DownloadRequest",
			Name: "downloadrequests", Namespaced: true, IsCRD: true,
			Verbs: []string{"get", "create", "delete"},
		}}); err != nil {
			t.Fatalf("seed download requests: %v", err)
		}
		t.Cleanup(k8s.ResetTestDynamicState)
		t.Cleanup(k8s.SetTestLocalMode())
		address := "localhost"
		if len(modes) > 0 && modes[0] == "shared" {
			address = "0.0.0.0"
		}
		if len(modes) > 0 && modes[0] == "in-cluster" {
			k8s.ForceInCluster = true
		}
		srv := New(Config{DevMode: true, ListenAddress: address})
		t.Cleanup(srv.Stop)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "http://localhost"+path, nil))
		return rec
	}

	for _, mode := range []string{"shared", "in-cluster"} {
		t.Run("refuses loopback in "+mode, func(t *testing.T) {
			store := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("forbidden store was contacted") }))
			defer store.Close()
			rec := post(t, answerWith(t, store.URL+"?signature=secret"), "/api/velero/backups/velero/nightly/messages", mode)
			if rec.Code != 503 || !strings.Contains(rec.Body.String(), "destination") || strings.Contains(rec.Body.String(), "secret") {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
		})
	}

	t.Run("returns the messages Velero wrote", func(t *testing.T) {
		store := serveResults(t, gzipped(t, realResultsJSON))
		rec := post(t, answerWith(t, store.URL), "/api/velero/backups/velero/nightly/messages")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		var got VeleroRunMessagesResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(got.Warnings) != 2 {
			t.Errorf("warnings = %d, want the 2 the results file holds", len(got.Warnings))
		}
	})

	// Storage answered, but not with a results file. Blaming the controller here
	// sends the reader to a controller that is running fine.
	t.Run("names the object store when the fetch fails", func(t *testing.T) {
		broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		t.Cleanup(broken.Close)
		rec := post(t, answerWith(t, broken.URL), "/api/velero/backups/velero/nightly/messages")
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
		body := rec.Body.String()
		// The store refused it, so the reader is sent to the store and to the
		// signing window — not to the Velero controller, which answered fine.
		if !strings.Contains(body, "the object store refused it") {
			t.Errorf("body = %s, want the object store named rather than the controller", body)
		}
		if strings.Contains(body, "controller") {
			t.Errorf("body = %s, blames the controller for a failure it had no part in", body)
		}
	})

	// A cluster with no DownloadRequest CRD has no Velero, which is a different
	// answer from a Velero that is not responding.
	t.Run("separates no Velero from a silent Velero", func(t *testing.T) {
		dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
			map[schema.GroupVersionResource]string{downloadRequestGVR: "DownloadRequestList"})
		dyn.PrependReactor("create", "downloadrequests", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewNotFound(
				schema.GroupResource{Group: veleroGroup, Resource: "downloadrequests"}, "")
		})
		rec := post(t, dyn, "/api/velero/backups/velero/nightly/messages")
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404 for a cluster with no Velero", rec.Code)
		}
	})

	t.Run("passes a denial back as a denial", func(t *testing.T) {
		dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
			map[schema.GroupVersionResource]string{downloadRequestGVR: "DownloadRequestList"})
		dyn.PrependReactor("create", "downloadrequests", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(
				schema.GroupResource{Group: veleroGroup, Resource: "downloadrequests"}, "", errors.New("nope"))
		})
		rec := post(t, dyn, "/api/velero/backups/velero/nightly/messages")
		if rec.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", rec.Code)
		}
	})

	// The server Deployment is readable and stopped: nothing can answer, so the
	// reader is told now, and nothing is created for Velero to clean up later.
	t.Run("answers at once when the Velero server is stopped", func(t *testing.T) {
		dep := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "velero", Namespace: "velero"},
			Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{
				{Name: "velero", Image: "velero/velero:v1.18.1", Command: []string{"/velero"}, Args: []string{"server"}},
			}}}},
		}
		if _, err := testFakeClient.AppsV1().Deployments("velero").Create(context.Background(), dep, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create deployment: %v", err)
		}
		t.Cleanup(func() {
			_ = testFakeClient.AppsV1().Deployments("velero").Delete(context.Background(), "velero", metav1.DeleteOptions{})
		})
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := k8s.GetResourceCache().Deployments().Deployments("velero").Get("velero"); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("deployment never reached the cache")
			}
			time.Sleep(20 * time.Millisecond)
		}
		dyn := answerWith(t, "http://unused")
		start := time.Now()
		rec := post(t, dyn, "/api/velero/backups/velero/nightly/messages")
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "has no running replicas") {
			t.Fatalf("status = %d body = %s, want 503 naming the stopped server", rec.Code, rec.Body.String())
		}
		if time.Since(start) > 2*time.Second {
			t.Errorf("took %s; a known-stopped server must not be waited on", time.Since(start))
		}
		for _, a := range dyn.Actions() {
			if a.GetVerb() == "create" {
				t.Errorf("created a %s Velero cannot serve", a.GetResource().Resource)
			}
		}
	})

	// Only Backups and Restores have a results target. A Schedule has none, and
	// answering with an empty list would read as "this ran cleanly".
	t.Run("refuses a kind that has no results", func(t *testing.T) {
		rec := post(t, answerWith(t, "http://unused"), "/api/velero/schedules/velero/nightly/messages")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})
}

// A pre-signed URL is a bearer credential: anything holding it can read the
// results file until the signature expires. Go wraps transport failures in
// *url.Error, whose message embeds the whole URL — so the natural thing to do
// with a fetch error, returning it, publishes the credential to whoever can
// read a response body or a log line.
func TestVeleroResultsErrorsNeverCarryTheSignedURL(t *testing.T) {
	const signature = "deadbeefcafe"
	signed := "https://bucket.s3.example.invalid:1/backups/nightly/nightly-results.gz" +
		"?X-Amz-Credential=AKIAEXAMPLE&X-Amz-Signature=" + signature

	// Unreachable host: the transport error is the one that embeds the URL.
	_, err := fetchVeleroResults(context.Background(), signed, localVeleroDownloadPolicy)
	if err == nil {
		t.Fatal("want a transport failure against an invalid host")
	}
	for _, secret := range []string{signature, "X-Amz-Signature", "AKIAEXAMPLE", "nightly-results.gz"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error %q carries %q from the signed URL", err, secret)
		}
	}
	if !errors.Is(err, errStorageUnreachable) {
		t.Errorf("err = %v, want it to classify as unreachable so the handler can route it", err)
	}
	// The message shown to the reader is built from the sentinel, so it cannot
	// carry the URL either.
	if msg := veleroFetchMessage(err); strings.Contains(msg, signature) {
		t.Errorf("message %q carries the signature", msg)
	}

	// Same guarantee when the store answers, but with the wrong bytes.
	plain := serveResults(t, []byte("not gzip"))
	_, err = fetchVeleroResults(context.Background(), plain.URL+"?X-Amz-Signature="+signature, localVeleroDownloadPolicy)
	if err == nil || strings.Contains(err.Error(), signature) {
		t.Errorf("err = %v, want a classified error with no signature in it", err)
	}
	if !errors.Is(err, errNotAResultsFile) {
		t.Errorf("err = %v, want errNotAResultsFile", err)
	}
}

func TestVeleroDownloadDestinations(t *testing.T) {
	for _, addr := range []string{"169.254.169.254", "169.254.0.1", "fe80::1", "0.0.0.0", "::", "224.0.0.1", "ff02::1", "::ffff:169.254.169.254"} {
		for _, local := range []bool{false, true} {
			if veleroDownloadAddressAllowed(net.ParseIP(addr), local) {
				t.Errorf("allowed %s (local=%v)", addr, local)
			}
		}
	}
	for _, addr := range []string{"127.0.0.1", "::1", "::ffff:127.0.0.1"} {
		if veleroDownloadAddressAllowed(net.ParseIP(addr), false) || !veleroDownloadAddressAllowed(net.ParseIP(addr), true) {
			t.Errorf("loopback policy %s", addr)
		}
	}
	for _, addr := range []string{"10.0.0.1", "172.16.0.1", "192.168.1.1", "fd00::1", "203.0.113.1"} {
		if !veleroDownloadAddressAllowed(net.ParseIP(addr), false) {
			t.Errorf("refused object storage %s", addr)
		}
	}
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; w.WriteHeader(403) }))
	defer srv.Close()
	// Resolve a hostname rather than checking the URL's literal host.
	hostname := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	_, err := fetchVeleroResults(t.Context(), hostname+"?signature=secret", func(ip net.IP) bool { return veleroDownloadAddressAllowed(ip, false) })
	if !errors.Is(err, errDownloadDestination) || hits != 0 {
		t.Fatalf("refusal %v, hits %d", err, hits)
	}
	if strings.Contains(veleroFetchMessage(err), "secret") || !strings.Contains(veleroFetchMessage(err), "destination") {
		t.Fatal(veleroFetchMessage(err))
	}
	_, err = fetchVeleroResults(t.Context(), srv.URL, localVeleroDownloadPolicy)
	if !errors.Is(err, errStorageRefused) || hits != 1 {
		t.Fatalf("local store %v, hits %d", err, hits)
	}
}

func localVeleroDownloadPolicy(ip net.IP) bool { return veleroDownloadAddressAllowed(ip, true) }

func TestVeleroResultsUsesDialTimePolicy(t *testing.T) {
	hits := 0
	store := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; w.WriteHeader(403) }))
	defer store.Close()
	checked := 0
	_, err := fetchVeleroResults(t.Context(), store.URL, func(ip net.IP) bool {
		checked++
		if ip == nil || !ip.IsLoopback() {
			t.Errorf("policy saw an unresolved address: %v", ip)
		}
		return false
	})
	if checked == 0 || hits != 0 || !errors.Is(err, errDownloadDestination) {
		t.Fatalf("checks=%d requests=%d err=%v", checked, hits, err)
	}
}
