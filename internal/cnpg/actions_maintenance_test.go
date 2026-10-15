package cnpg

import (
	"context"
	"net/http"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"

	integration "github.com/skyhook-io/radar/internal/integration"
)

func cnpgMaintenanceFactsMap(declared, inProgress, reusePVC bool) map[string]any {
	f := cnpgActionFacts()
	f["maintenance"] = map[string]any{"declared": declared, "inProgress": inProgress, "reusePVC": reusePVC}
	return f
}

// Set and unset write exactly the two fields `kubectl cnpg maintenance`
// writes, locked on the resourceVersion, with the reusePVC the dialog showed.
func TestCNPGActionMaintenanceWritesLikeKubectlCNPG(t *testing.T) {
	inMaintenance := cnpgActionCluster(func(o map[string]any) {
		o["spec"].(map[string]any)["nodeMaintenanceWindow"] = map[string]any{"inProgress": true, "reusePVC": false}
	})
	for _, tc := range []struct {
		action     string
		cluster    runtime.Object
		facts      map[string]any
		reusePVC   bool
		inProgress bool
	}{
		{"setMaintenance", cnpgActionCluster(nil), cnpgMaintenanceFactsMap(false, false, true), true, true},
		{"unsetMaintenance", inMaintenance, cnpgMaintenanceFactsMap(true, true, false), false, false},
	} {
		t.Run(tc.action, func(t *testing.T) {
			env := newCNPGActionEnv(t, []runtime.Object{tc.cluster})
			_, err := RunCNPGClusterAction(context.Background(), env.clients(), "db", "pg", tc.action, cnpgActionReq(t, tc.facts, map[string]any{"reusePVC": tc.reusePVC}))
			if err != nil {
				t.Fatalf("%s: %v", tc.action, err)
			}
			if len(env.patches) != 1 || env.patches[0].GetSubresource() != "" {
				t.Fatalf("patches = %v, want one spec patch", env.patches)
			}
			body := cnpgActionPatchBody(t, env.patches[0])
			if md, _ := body["metadata"].(map[string]any); md["resourceVersion"] != "42" || md["annotations"] != nil {
				t.Errorf("metadata = %v, want only the resourceVersion lock", md)
			}
			win := body["spec"].(map[string]any)["nodeMaintenanceWindow"].(map[string]any)
			if len(win) != 2 || win["inProgress"] != tc.inProgress || win["reusePVC"] != tc.reusePVC {
				t.Errorf("nodeMaintenanceWindow = %v", win)
			}
		})
	}
}

func TestCNPGActionMaintenanceGuardsAndBinding(t *testing.T) {
	env := newCNPGActionEnv(t, []runtime.Object{cnpgActionCluster(nil)})
	ctx := context.Background()

	// Nothing is in progress: unset is refused.
	_, err := RunCNPGClusterAction(ctx, env.clients(), "db", "pg", "unsetMaintenance", cnpgActionReq(t, cnpgMaintenanceFactsMap(false, false, true), map[string]any{"reusePVC": true}))
	if ae, ok := cnpgActionStatus(t, err); !ok || ae.Code != integration.ActionCodeBlocked {
		t.Errorf("unset while not in progress = %v, want blocked", err)
	}
	// The reviewed values differ from the cluster's: 409 changed, nothing written.
	_, err = RunCNPGClusterAction(ctx, env.clients(), "db", "pg", "setMaintenance", cnpgActionReq(t, cnpgMaintenanceFactsMap(true, false, false), map[string]any{"reusePVC": true}))
	if ae, ok := cnpgActionStatus(t, err); !ok || ae.Status != http.StatusConflict || ae.Code != integration.ActionCodeChanged {
		t.Errorf("stale maintenance facts = %v, want 409 changed", err)
	}
	// Missing binding or missing reusePVC is a malformed request.
	_, err = RunCNPGClusterAction(ctx, env.clients(), "db", "pg", "setMaintenance", cnpgActionReq(t, cnpgActionFacts(), map[string]any{"reusePVC": true}))
	if ae, ok := cnpgActionStatus(t, err); !ok || ae.Status != http.StatusBadRequest {
		t.Errorf("unbound maintenance = %v, want 400", err)
	}
	_, err = RunCNPGClusterAction(ctx, env.clients(), "db", "pg", "setMaintenance", cnpgActionReq(t, cnpgMaintenanceFactsMap(false, false, true), nil))
	if ae, ok := cnpgActionStatus(t, err); !ok || ae.Status != http.StatusBadRequest {
		t.Errorf("missing reusePVC = %v, want 400", err)
	}
	if len(env.patches) != 0 {
		t.Errorf("refused requests wrote: %v", env.patches)
	}
	if g, ok := GrantFor("setMaintenance"); !ok || g != GrantPatchClusters {
		t.Errorf("setMaintenance grant = %v", g)
	}
}
