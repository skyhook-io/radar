package argoapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestArgoRevisionManifests(t *testing.T) {
	sha := strings.Repeat("a", 40)
	for _, tc := range []struct {
		name, body string
		count      int
		rejected   bool
	}{
		{"aggregate response", `{"manifests":["one", "two"]}`, 2, false},
		{"empty aggregate", "{}", 0, false},
		{"explicit empty", `{"manifests":[]}`, 0, false},
		{"repo-server response", fmt.Sprintf(`{"revision":%q,"manifests":["one"]}`, sha), 1, false},
		{"different revision", `{"revision":"other","manifests":["one"]}`, 0, true},
		{"invalid", "null", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				if r.URL.Path != "/api/v1/applications/example/manifests" || q.Get("appNamespace") != "argocd" || q.Get("project") != "sample" || q.Get("revision") != sha {
					t.Errorf("wrong request %v", r.URL)
				}
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			res, err := New(Options{BaseURL: srv.URL}).RevisionManifests(t.Context(), ApplicationQuery{AppName: "example", AppNamespace: "argocd", Project: "sample"}, sha)
			if (err != nil) != tc.rejected || len(res) != tc.count {
				t.Errorf("result=%v err=%v", res, err)
			}
		})
	}
}

func TestArgoApplicationGeneration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/applications/demo" || r.URL.Query().Get("appNamespace") != "argocd" || r.URL.Query().Get("project") != "prod" || r.URL.Query().Get("fields") != "metadata.uid,metadata.generation" {
			t.Errorf("wrong request URL %v", r.URL)
		}
		fmt.Fprint(w, `{"metadata":{"uid":"app-uid","generation":7}}`)
	}))
	defer srv.Close()
	uid, generation, err := New(Options{BaseURL: srv.URL}).ApplicationGeneration(t.Context(), ApplicationQuery{AppName: "demo", AppNamespace: "argocd", Project: "prod"})
	if err != nil || uid != "app-uid" || generation != 7 {
		t.Errorf("identity=(%s,%d), err=%v", uid, generation, err)
	}
}
