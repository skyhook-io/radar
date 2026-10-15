package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	cnpgsvc "github.com/skyhook-io/radar/internal/cnpg"
	integration "github.com/skyhook-io/radar/internal/integration"
)

func TestCNPGActionErrorMapping(t *testing.T) {
	srv := &Server{}
	gr := schema.GroupResource{Group: cnpgsvc.Group, Resource: "clusters"}
	for _, tc := range []struct {
		name   string
		action string
		err    error
		status int
		code   string
		substr []string
	}{
		{"forbidden retains the exact denial", "configureArchiving", apierrors.NewForbidden(schema.GroupResource{Group: "barmancloud.cnpg.io", Resource: "objectstores"}, "store", errors.New(`User "reader" cannot get resource "objectstores" in namespace "db"`)), http.StatusForbidden, "",
			[]string{"cannot get resource", "objectstores", "reader"}},
		{"webhook down", "backup", apierrors.NewInternalError(errors.New(`failed calling webhook "vbackup.cnpg.io": connection refused`)), http.StatusServiceUnavailable, cnpgsvc.CodeWebhook,
			[]string{"admission webhook did not answer", "connection refused"}},
		{"invalid", "backup", apierrors.NewInvalid(schema.GroupKind{Group: cnpgsvc.Group, Kind: "Backup"}, "b", nil), http.StatusUnprocessableEntity, "", []string{"is invalid"}},
		{"not found", "restart", apierrors.NewNotFound(gr, "pg"), http.StatusNotFound, "", []string{"not found"}},
		{"refusal", "fence", integration.BlockedAction("It is fenced already"), http.StatusConflict, integration.ActionCodeBlocked, []string{"fenced already"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			srv.writeCNPGActionError(w, tc.err, tc.action, "db", "pg")
			if w.Code != tc.status {
				t.Errorf("status = %d, want %d", w.Code, tc.status)
			}
			var body map[string]any
			_ = json.Unmarshal(w.Body.Bytes(), &body)
			if tc.code != "" && body["code"] != tc.code {
				t.Errorf("code = %v, want %s", body["code"], tc.code)
			}
			msg, _ := body["error"].(string)
			if apierrors.IsForbidden(tc.err) && msg != tc.err.Error() {
				t.Errorf("denial was rewritten: %q", msg)
			}
			for _, s := range tc.substr {
				if !strings.Contains(msg, s) {
					t.Errorf("error %q does not contain %q", msg, s)
				}
			}
		})
	}
}
