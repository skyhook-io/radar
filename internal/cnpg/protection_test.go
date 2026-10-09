package cnpg

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/integration"
)

func protectionStore(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "barmancloud.cnpg.io/v1", "kind": "ObjectStore", "metadata": map[string]any{"name": name, "namespace": "db", "uid": name + "-uid", "resourceVersion": "3"}, "spec": map[string]any{"configuration": map[string]any{"destinationPath": "s3://bucket/prefix/", "endpointURL": "https://storage.example", "s3Credentials": map[string]any{"inheritFromIAMRole": true}}}}}
}

func unprotectedCluster() *unstructured.Unstructured {
	return cnpgActionCluster(func(o map[string]any) {
		spec := o["spec"].(map[string]any)
		delete(spec, "backup")
		spec["plugins"] = []any{map[string]any{"name": "metrics.example", "parameters": map[string]any{"mode": "keep"}}}
	})
}

func archivingRequest(t *testing.T, p *ArchivingPreview) integration.ActionRequest {
	t.Helper()
	facts, _ := json.Marshal(p.Facts)
	params, _ := json.Marshal(ArchivingParams{ObjectStore: p.Facts.ObjectStore, ServerName: p.Facts.ServerName, AcknowledgeArchive: true})
	return integration.ActionRequest{ReviewedContext: p.Context, UID: p.UID, Facts: facts, Params: params}
}

func TestArchivingReviewAndConditionalWrite(t *testing.T) {
	ctx := context.Background()
	env := newCNPGActionEnv(t, []runtime.Object{unprotectedCluster(), protectionStore("store")})
	p, err := newTestReader(nil).PreviewArchiving(ctx, env.clients(), "ctx", "db", "pg", ArchivingParams{ObjectStore: "store", ServerName: "pg-new"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Destination != "s3://bucket/prefix/pg-new" || p.Endpoint != "https://storage.example" {
		t.Fatalf("comparison keys leaked into review: %+v", p)
	}
	if len(env.patches) != 1 || len(env.patches[0].(k8stesting.PatchActionImpl).GetPatchOptions().DryRun) != 1 {
		t.Fatal("preview must be dry-run")
	}
	// Status churn is deliberately outside the reviewed configuration.
	fresh, err := env.dyn.Resource(ClusterGVR).Namespace("db").Get(ctx, "pg", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	fresh.Object["status"] = map[string]any{"phase": "changing"}
	fresh.SetResourceVersion("99")
	if err := env.dyn.Tracker().Update(ClusterGVR, fresh, "db"); err != nil {
		t.Fatal(err)
	}
	res, err := RunCNPGClusterAction(ctx, env.clients(), "db", "pg", "configureArchiving", archivingRequest(t, p))
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != "configureArchiving" || len(env.patches) != 2 {
		t.Fatalf("result=%+v writes=%d", res, len(env.patches))
	}
	body := cnpgActionPatchBody(t, env.patches[1])
	if body["metadata"].(map[string]any)["resourceVersion"] != "99" {
		t.Fatal("must bind fresh version")
	}
	plugins := body["spec"].(map[string]any)["plugins"].([]any)
	if len(plugins) != 2 || plugins[0].(map[string]any)["parameters"].(map[string]any)["mode"] != "keep" {
		t.Fatalf("unrelated plugin changed: %v", plugins)
	}
	if len(env.patches[1].(k8stesting.PatchActionImpl).GetPatchOptions().DryRun) != 0 {
		t.Fatal("confirmed write remained a dry-run")
	}
}

func TestArchivingExistingPluginEnablement(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		enabled bool
		wal     bool
	}{
		{name: "base-backup-only", enabled: true},
		{name: "disabled"},
		{name: "already-archiving", enabled: true, wal: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := context.Background()
			cluster := unprotectedCluster()
			plugin := map[string]any{
				"name": "barman-cloud.cloudnative-pg.io", "enabled": scenario.enabled, "isWALArchiver": scenario.wal,
				"parameters": map[string]any{"barmanObjectName": "store", "serverName": "pg", "custom": "keep"},
			}
			cluster.Object["spec"].(map[string]any)["plugins"] = []any{plugin}
			env := newCNPGActionEnv(t, []runtime.Object{cluster, protectionStore("store")})
			preview, err := newTestReader(nil).PreviewArchiving(ctx, env.clients(), "ctx", "db", "pg", ArchivingParams{ObjectStore: "store", ServerName: "pg"})
			if err != nil {
				t.Fatal(err)
			}
			if preview.Unchanged != (scenario.enabled && scenario.wal) {
				t.Fatalf("unchanged=%v for %+v", preview.Unchanged, scenario)
			}
			if plugin["enabled"] != scenario.enabled || plugin["isWALArchiver"] != scenario.wal {
				t.Fatal("preview mutated the source plugin")
			}
			env.patches = nil
			if _, err := RunCNPGClusterAction(ctx, env.clients(), "db", "pg", "configureArchiving", archivingRequest(t, preview)); err != nil {
				t.Fatal(err)
			}
			if preview.Unchanged {
				if len(env.patches) != 0 {
					t.Fatal("already enabled archiving should be a no-op")
				}
				return
			}
			if len(env.patches) != 1 {
				t.Fatalf("expected one enablement write, got %d", len(env.patches))
			}
			patch := env.patches[0].(k8stesting.PatchActionImpl)
			if len(patch.GetPatchOptions().DryRun) != 0 {
				t.Fatal("enablement remained a dry-run")
			}
			plugins := cnpgActionPatchBody(t, env.patches[0])["spec"].(map[string]any)["plugins"].([]any)
			updated := plugins[0].(map[string]any)
			if updated["enabled"] != true || updated["isWALArchiver"] != true || updated["parameters"].(map[string]any)["custom"] != "keep" {
				t.Fatalf("enablement or preservation failed: %v", updated)
			}
		})
	}
}

func TestArchivingRefusesReviewedConfigurationChanges(t *testing.T) {
	for _, changed := range []string{"cluster", "store", "recreated", "race"} {
		t.Run(changed, func(t *testing.T) {
			ctx := context.Background()
			env := newCNPGActionEnv(t, []runtime.Object{unprotectedCluster(), protectionStore("store")})
			p, err := newTestReader(nil).PreviewArchiving(ctx, env.clients(), "ctx", "db", "pg", ArchivingParams{ObjectStore: "store", ServerName: "pg-new"})
			if err != nil {
				t.Fatal(err)
			}
			env.patches = nil
			if changed == "race" {
				env.dyn.PrependReactor("patch", "clusters", func(k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, apierrors.NewConflict(ClusterGVR.GroupResource(), "pg", nil)
				})
			} else if changed == "store" {
				s := protectionStore("store")
				s.Object["spec"].(map[string]any)["retentionPolicy"] = "8d"
				if err := env.dyn.Tracker().Update(cnpgObjectStoreGVR, s, "db"); err != nil {
					t.Fatal(err)
				}
			} else {
				c := unprotectedCluster()
				if changed == "recreated" {
					c.SetUID(types.UID("new-uid"))
				} else {
					c.Object["spec"].(map[string]any)["plugins"] = []any{map[string]any{"name": "different.example"}}
				}
				if err := env.dyn.Tracker().Update(ClusterGVR, c, "db"); err != nil {
					t.Fatal(err)
				}
			}
			_, err = RunCNPGClusterAction(ctx, env.clients(), "db", "pg", "configureArchiving", archivingRequest(t, p))
			if ae, ok := cnpgActionStatus(t, err); !ok || ae.Code != integration.ActionCodeChanged || len(env.patches) != 0 {
				t.Fatalf("err=%v patches=%d", err, len(env.patches))
			}
		})
	}
}

func TestArchivingIdentityAndMigrationGuards(t *testing.T) {
	for _, scenario := range []string{"recovery-alias", "other-cluster-alias", "other-base-backup-only", "origin-alias", "historical-server", "in-tree", "other-archiver", "duplicate", "store-server"} {
		t.Run(scenario, func(t *testing.T) {
			c, store, alias := unprotectedCluster(), protectionStore("store"), protectionStore("alias")
			objs := []runtime.Object{c, store, alias}
			spec := c.Object["spec"].(map[string]any)
			switch scenario {
			case "recovery-alias":
				spec["bootstrap"] = map[string]any{"recovery": map[string]any{"source": "origin"}}
				spec["externalClusters"] = []any{map[string]any{"name": "origin", "plugin": map[string]any{"name": "barman-cloud.cloudnative-pg.io", "parameters": map[string]any{"barmanObjectName": "alias", "serverName": "pg-new"}}}}
			case "other-cluster-alias", "other-base-backup-only", "origin-alias":
				if scenario == "origin-alias" {
					config := alias.Object["spec"].(map[string]any)["configuration"].(map[string]any)
					config["destinationPath"], config["endpointURL"] = "s3://BUCKET/prefix", "https://STORAGE.EXAMPLE:443/"
				}
				other := unprotectedCluster()
				other.SetName("other")
				other.SetUID("other-uid")
				other.Object["spec"].(map[string]any)["plugins"] = []any{map[string]any{"name": "barman-cloud.cloudnative-pg.io", "isWALArchiver": scenario != "other-base-backup-only", "parameters": map[string]any{"barmanObjectName": "alias", "serverName": "pg-new"}}}
				objs = append(objs, other)
			case "historical-server":
				store.Object["status"] = map[string]any{"serverRecoveryWindow": map[string]any{"pg-new": map[string]any{}}}
			case "in-tree":
				spec["backup"] = map[string]any{"barmanObjectStore": map[string]any{"destinationPath": "s3://old/"}}
			case "other-archiver":
				spec["plugins"] = []any{map[string]any{"name": "other", "isWALArchiver": true}}
			case "duplicate":
				spec["plugins"] = []any{map[string]any{"name": "barman-cloud.cloudnative-pg.io"}, map[string]any{"name": "barman-cloud.cloudnative-pg.io"}}
			case "store-server":
				store.Object["spec"].(map[string]any)["configuration"].(map[string]any)["serverName"] = "old"
			}
			env := newCNPGActionEnv(t, objs)
			_, err := newTestReader(nil).PreviewArchiving(context.Background(), env.clients(), "ctx", "db", "pg", ArchivingParams{ObjectStore: "store", ServerName: "pg-new"})
			if ae, ok := cnpgActionStatus(t, err); !ok || ae.Code != integration.ActionCodeBlocked || len(env.patches) != 0 {
				t.Fatalf("err=%v patches=%d", err, len(env.patches))
			}
			if scenario == "origin-alias" && !strings.Contains(err.Error(), "already archives to this destination") {
				t.Fatalf("alias failed for the wrong reason: %v", err)
			}
		})
	}
}

func TestArchivingPartialInventoryAndForbiddenWrite(t *testing.T) {
	env := newCNPGActionEnv(t, []runtime.Object{unprotectedCluster(), protectionStore("store")})
	env.dyn.PrependReactor("list", "clusters", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(ClusterGVR.GroupResource(), "", nil)
	})
	p, err := newTestReader(nil).PreviewArchiving(context.Background(), env.clients(), "ctx", "db", "pg", ArchivingParams{ObjectStore: "store", ServerName: "pg-new"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(p.Warnings, " "), "incomplete") {
		t.Fatalf("warnings=%v", p.Warnings)
	}
	env.patches = nil
	env.dyn.PrependReactor("patch", "clusters", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(ClusterGVR.GroupResource(), "pg", nil)
	})
	_, err = RunCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "configureArchiving", archivingRequest(t, p))
	if !apierrors.IsForbidden(err) {
		t.Fatalf("err=%v", err)
	}
}

func TestScheduleMethodRepairPreservesOtherSettings(t *testing.T) {
	c := cnpgActionCluster(nil)
	s := cnpgActionSchedule(func(o map[string]any) {
		spec := o["spec"].(map[string]any)
		spec["method"] = "barmanObjectStore"
		spec["online"] = false
		spec["target"] = "prefer-standby"
	})
	env := newCNPGActionEnv(t, []runtime.Object{c, s, protectionStore("store")})
	p, err := newTestReader(nil).PreviewScheduleMethod(context.Background(), env.clients(), "ctx", "db", "nightly")
	if err != nil {
		t.Fatal(err)
	}
	facts, _ := json.Marshal(p.Facts)
	env.patches = nil
	_, err = RunCNPGScheduleAction(context.Background(), env.clients(), "db", "nightly", "repairMethod", integration.ActionRequest{ReviewedContext: p.Context, UID: p.UID, Facts: facts})
	if err != nil {
		t.Fatal(err)
	}
	body := cnpgActionPatchBody(t, env.patches[0])
	spec := body["spec"].(map[string]any)
	if len(spec) != 2 || spec["method"] != "plugin" || spec["pluginConfiguration"].(map[string]any)["name"] != "barman-cloud.cloudnative-pg.io" {
		t.Fatalf("patch=%v", spec)
	}
	if body["metadata"].(map[string]any)["resourceVersion"] != "7" {
		t.Fatal("must bind version")
	}
}

func TestScheduleMethodRepairRefusesChangedFactsAndMigrations(t *testing.T) {
	for _, scenario := range []string{"schedule", "cluster", "recreated", "conflict", "snapshot", "other-plugin", "cluster-deleting", "store-deleting"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			cluster := cnpgActionCluster(nil)
			schedule := cnpgActionSchedule(func(o map[string]any) { o["spec"].(map[string]any)["method"] = "barmanObjectStore" })
			store := protectionStore("store")
			env := newCNPGActionEnv(t, []runtime.Object{cluster, schedule, store})
			preview, err := newTestReader(nil).PreviewScheduleMethod(ctx, env.clients(), "ctx", "db", "nightly")
			if err != nil {
				t.Fatal(err)
			}
			facts, _ := json.Marshal(preview.Facts)
			env.patches = nil
			want := integration.ActionCodeChanged
			switch scenario {
			case "schedule":
				schedule.Object["spec"].(map[string]any)["cluster"] = map[string]any{"name": "other"}
				if err := env.dyn.Tracker().Add(cnpgActionCluster(func(o map[string]any) { o["metadata"].(map[string]any)["name"] = "other" })); err != nil {
					t.Fatal(err)
				}
			case "cluster":
				cluster.Object["spec"].(map[string]any)["plugins"].([]any)[0].(map[string]any)["parameters"].(map[string]any)["serverName"] = "different"
			case "recreated":
				schedule.SetUID("new-uid")
			case "conflict":
				env.dyn.PrependReactor("patch", "scheduledbackups", func(k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, apierrors.NewConflict(ScheduleGVR.GroupResource(), "nightly", nil)
				})
			case "snapshot":
				schedule.Object["spec"].(map[string]any)["method"] = "volumeSnapshot"
				want = integration.ActionCodeBlocked
			case "other-plugin":
				schedule.Object["spec"].(map[string]any)["pluginConfiguration"] = map[string]any{"name": "another.example"}
				want = integration.ActionCodeBlocked
			case "cluster-deleting":
				at := metav1.Now()
				cluster.SetDeletionTimestamp(&at)
				want = integration.ActionCodeBlocked
			case "store-deleting":
				at := metav1.Now()
				store.SetDeletionTimestamp(&at)
				want = integration.ActionCodeBlocked
			}
			for _, object := range []struct {
				gvr schema.GroupVersionResource
				obj *unstructured.Unstructured
			}{{ClusterGVR, cluster}, {ScheduleGVR, schedule}, {cnpgObjectStoreGVR, store}} {
				if err := env.dyn.Tracker().Update(object.gvr, object.obj, "db"); err != nil {
					t.Fatal(err)
				}
			}
			_, err = RunCNPGScheduleAction(ctx, env.clients(), "db", "nightly", "repairMethod", integration.ActionRequest{ReviewedContext: preview.Context, UID: preview.UID, Facts: facts})
			if ae, ok := cnpgActionStatus(t, err); !ok || ae.Code != want || len(env.patches) != 0 {
				t.Fatalf("err=%v writes=%d", err, len(env.patches))
			}
		})
	}
}
