package server

import (
	cnpgsvc "github.com/skyhook-io/radar/internal/cnpg"

	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	integration "github.com/skyhook-io/radar/internal/integration"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/auth"
)

func TestParseCNPGReportOptions(t *testing.T) {
	for q, ok := range map[string]bool{"": true, "logs=true&tailLines=500": true, "queryText=true": false, "logs=true&tailLines=0": false, "logs=true&tailLines=99999": false} {
		_, err := parseCNPGReportOptions(httptest.NewRequest(http.MethodGet, "/?"+q, nil))
		if (err == nil) != ok {
			t.Errorf("%q: err=%v", q, err)
		}
	}
}

func apiForbidden(resource string) error {
	return apierrors.NewForbidden(schema.GroupResource{Resource: resource}, "", errors.New("denied"))
}

func TestHandleCNPGRestoreCapability(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		srv := &Server{permCache: auth.NewPermissionCache()}
		perms := &auth.UserPermissions{AllowedNamespaces: []string{"db"}}
		perms.SetCanI("create", cnpgsvc.Group, "clusters", "db", allowed)
		srv.permCache.Set("alice", nil, perms)
		r := httptest.NewRequest(http.MethodGet, "/api/cnpg/restore/capability?namespace=db", nil)
		r = r.WithContext(auth.ContextWithUser(r.Context(), &auth.User{Username: "alice"}))
		w := httptest.NewRecorder()
		srv.handleCNPGRestoreCapability(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
		var got integration.ActionCapability
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if allowed && (!got.Allowed || got.Permission != integration.PermissionAllowed) {
			t.Errorf("allowed = %+v", got)
		}
		if !allowed && (got.Allowed || got.Permission != integration.PermissionDenied || got.Grant == nil || *got.Grant != (auth.Grant{Verb: "create", Group: cnpgsvc.Group, Resource: "clusters", Namespace: "db"}) || got.Grant.String() != "create clusters (postgresql.cnpg.io) in namespace db" || !strings.Contains(got.Reason, got.Grant.String())) {
			t.Errorf("denied = %+v, want the grant named", got)
		}
	}
	for _, q := range []string{"", "?namespace=", "?namespace=Not_A_Namespace"} {
		w := httptest.NewRecorder()
		(&Server{}).handleCNPGRestoreCapability(w, httptest.NewRequest(http.MethodGet, "/api/cnpg/restore/capability"+q, nil))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%q: status %d, want 400", q, w.Code)
		}
	}
}
