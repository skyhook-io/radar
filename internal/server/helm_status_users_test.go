package server

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/release"
	helmtime "helm.sh/helm/v3/pkg/time"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/skyhook-io/radar/internal/auth"
)

// A release stored in default whose manifest also places a Deployment in the
// broken namespace (the shared fixture's stuck-app). A caller who can read
// default only must get no live status of stuck-app from any REST path that
// reports release health, while a caller who can read everything still does.
func TestHelmRESTShowsEachCallerOnlyTheStatusTheyCanRead(t *testing.T) {
	rel := &release.Release{
		Name: "payments", Namespace: "default", Version: 1,
		Info:  &release.Info{Status: release.StatusDeployed, Description: "Install complete", LastDeployed: helmtime.Unix(1, 0)},
		Chart: &chart.Chart{Metadata: &chart.Metadata{Name: "platform-app", Version: "0.1.0", AppVersion: "1.0.0"}},
		Manifest: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: nginx
  namespace: default
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: stuck-app
  namespace: broken
`,
	}
	payload, err := json.Marshal(rel)
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := json.Marshal(corev1.SecretList{
		TypeMeta: metav1.TypeMeta{Kind: "SecretList", APIVersion: "v1"},
		Items: []corev1.Secret{{
			ObjectMeta: metav1.ObjectMeta{Name: "sh.helm.release.v1.payments.v1", Namespace: "default", Labels: map[string]string{
				"owner": "helm", "name": "payments", "status": "deployed", "version": "1",
			}},
			Data: map[string][]byte{"release": []byte(base64.StdEncoding.EncodeToString(payload))},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	useHelmStorageAnswering(t, http.StatusOK, string(secrets))
	clearPackagesCache()
	t.Cleanup(clearPackagesCache)

	env := newAuthTestServer(t)
	devA := &auth.UserPermissions{AllowedNamespaces: []string{"default"}}
	admin := &auth.UserPermissions{}
	for _, perms := range []*auth.UserPermissions{devA, admin} {
		for _, verb := range []string{"get", "list"} {
			perms.SetCanI(verb, "", "secrets", "default", true)
		}
	}
	devA.SetCanI("list", "", "secrets", "", false)
	admin.SetCanI("list", "", "secrets", "", true)
	env.srv.permCache.Set("dev-a", nil, devA)
	env.srv.permCache.Set("admin", nil, admin)

	get := func(t *testing.T, path, user string) string {
		t.Helper()
		resp := env.authGet(t, path, user, "")
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s as %s: %d %s", path, user, resp.StatusCode, body)
		}
		return string(body)
	}
	// stuck-app has 0/3 ready replicas, so admin's release health is
	// "degraded". dev-a's must be neither that nor "healthy".
	for _, path := range []string{
		"/api/helm/releases/default/payments",
		"/api/helm/releases?namespace=default",
		"/api/helm/inventory?namespace=default",
		"/api/dashboard/helm?namespace=default",
		"/api/packages?namespace=default",
	} {
		t.Run(path, func(t *testing.T) {
			adminBody := get(t, path, "admin")
			if !strings.Contains(adminBody, `"degraded"`) {
				t.Fatalf("admin doesn't see stuck-app's failure: %s", adminBody)
			}
			devABody := get(t, path, "dev-a")
			if strings.Contains(devABody, `"degraded"`) || strings.Contains(devABody, `"healthy"`) || strings.Contains(devABody, "0/3") {
				t.Fatalf("dev-a's health covers stuck-app or claims healthy over it: %s", devABody)
			}
			if again := get(t, path, "admin"); !strings.Contains(again, `"degraded"`) {
				t.Fatalf("admin after dev-a: %s", again)
			}
		})
	}

	// The list, detail and Home card say how many objects dev-a's health left
	// out; admin's say nothing.
	for _, path := range []string{
		"/api/helm/releases/default/payments",
		"/api/helm/releases?namespace=default",
		"/api/dashboard/helm?namespace=default",
	} {
		if body := get(t, path, "dev-a"); !strings.Contains(body, `"statusUnreadCount":1`) {
			t.Fatalf("%s as dev-a has no not-checked count: %s", path, body)
		}
		if body := get(t, path, "admin"); strings.Contains(body, "statusUnreadCount") {
			t.Fatalf("%s as admin has a not-checked count: %s", path, body)
		}
	}

	detail := get(t, "/api/helm/releases/default/payments", "dev-a")
	var wire struct {
		Resources []struct {
			Name, Status, Ready, Issue, StatusUnread string
		} `json:"resources"`
	}
	if err := json.Unmarshal([]byte(detail), &wire); err != nil {
		t.Fatal(err)
	}
	for _, r := range wire.Resources {
		if r.Name == "stuck-app" && (r.Status != "" || r.Ready != "" || r.Issue != "" || r.StatusUnread == "") {
			t.Fatalf("dev-a's stuck-app row = %+v, want no status and a reason", r)
		}
	}
}
