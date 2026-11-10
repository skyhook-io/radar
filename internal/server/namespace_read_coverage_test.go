package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestNamespaceGapsPartitionBeyondResponseNameLimit(t *testing.T) {
	useTestResourceCache(t, fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "present", Namespace: "default"}},
	))
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{}
	perms.SetCanI("list", "", "secrets", "default", true)
	names := []string{"default"}
	for i := range resourceapi.RESTSetLimit + 1 {
		ns := fmt.Sprintf("missing-%02d", i)
		names = append(names, ns)
		perms.SetCanI("list", "", "secrets", ns, false)
	}
	env.srv.permCache.Set("reader", nil, perms)
	query := "?namespaces=" + strings.Join(names, ",")
	var list testResourceListEnvelope
	assertOK(t, env.authGet(t, "/api/resources/secrets"+query, "reader", ""), &list)
	if len(list.Items) != 1 {
		t.Fatalf("items = %+v, want the readable Secret", list.Items)
	}
	cov := list.Coverage
	if cov.Scope.Namespaces.Count != 66 || cov.Scope.Namespaces.Names != nil || cov.Read.Namespaces.Count != 1 || len(cov.Gaps) != 1 {
		t.Fatalf("coverage = %+v, want one read namespace and one gap in a capped scope", cov)
	}
	gap := cov.Gaps[0]
	if gap.Code != readreason.NamespaceNotFound || gap.Namespaces.Count != 65 || gap.Namespaces.Names != nil || cov.Read.Namespaces.Count+gap.Namespaces.Count != cov.Scope.Namespaces.Count {
		t.Fatalf("gap = %+v, want exactly the 65 missing namespaces", gap)
	}

	resp := env.authGet(t, "/api/ai/resources/secrets"+query, "reader", "")
	defer resp.Body.Close()
	var failure resourceapi.ReadError
	if err := json.NewDecoder(resp.Body).Decode(&failure); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound || failure.Coverage == nil {
		t.Fatalf("AI incomplete list = %d %+v", resp.StatusCode, failure)
	}
	if err := failure.Coverage.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(failure.Coverage.Gaps) != 1 || failure.Coverage.Gaps[0].Namespaces.Count != 65 || failure.Coverage.Gaps[0].Namespaces.Names != nil {
		t.Fatalf("AI gap was not capped after reconciliation: %+v", failure.Coverage)
	}
}

func TestEmptyNamespaceListMatchesOmittedFilter(t *testing.T) {
	useTestResourceCache(t, fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "other"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "present", Namespace: "default"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "neighbor", Namespace: "other"}},
	))
	env := newAuthTestServer(t)
	for user, namespaces := range map[string][]string{
		"restricted": {"default"}, "unrestricted": nil, "no-access": {},
	} {
		env.srv.permCache.Set(user, nil, &auth.UserPermissions{AllowedNamespaces: namespaces})
	}
	for _, pinned := range []bool{false, true} {
		t.Run(fmt.Sprintf("pinned=%v", pinned), func(t *testing.T) {
			k8s.ForceNamespaceScope = pinned
			k8s.SetFallbackNamespace("default")
			t.Cleanup(func() { k8s.ForceNamespaceScope = false; k8s.SetFallbackNamespace("") })
			for _, user := range []string{"restricted", "unrestricted", "no-access"} {
				for _, path := range []string{
					"/api/resources/pods?include=raw", "/api/resources/pods/default/present?include=raw",
					"/api/resource-counts?", "/api/ai/resources/pods?context=none",
					"/api/ai/resources/pods/default/present?context=none",
				} {
					t.Run(user+path, func(t *testing.T) {
						var baseline any
						var baselineStatus int
						for i, suffix := range []string{"", "&namespaces=,,"} {
							resp := env.authGet(t, path+suffix, user, "")
							body, err := io.ReadAll(resp.Body)
							resp.Body.Close()
							if err != nil {
								t.Fatal(err)
							}
							var value any
							if err := json.Unmarshal(body, &value); err != nil {
								t.Fatal(err)
							}
							rows, _ := value.([]any)
							if object, ok := value.(map[string]any); ok {
								rows, _ = object["items"].([]any)
							}
							slices.SortFunc(rows, func(a, b any) int { return strings.Compare(fmt.Sprint(a), fmt.Sprint(b)) })
							if strings.HasPrefix(path, "/api/resource-counts") && resp.StatusCode == http.StatusOK {
								var counts testCounts
								if err := json.Unmarshal(body, &counts); err != nil {
									t.Fatal(err)
								}
							} else if object, ok := value.(map[string]any); ok && object["coverage"] != nil {
								var result struct{ Coverage resourceapi.Coverage }
								if err := json.Unmarshal(body, &result); err != nil {
									t.Fatal(err)
								}
								if err := result.Coverage.Validate(); err != nil {
									t.Fatalf("%v: %s", err, body)
								}
							}
							if i == 0 {
								if user != "no-access" && resp.StatusCode != http.StatusOK {
									t.Fatalf("omitted filter failed: %d %s", resp.StatusCode, body)
								}
								baseline, baselineStatus = value, resp.StatusCode
							} else if resp.StatusCode != baselineStatus || !reflect.DeepEqual(value, baseline) {
								t.Fatalf("empty filter = %d %s, omitted = %d %+v", resp.StatusCode, body, baselineStatus, baseline)
							}
						}
					})
				}
			}
		})
	}
}
