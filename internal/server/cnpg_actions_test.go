package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/auth"
)

var cnpgActionTestNow = time.Date(2026, 9, 29, 10, 11, 12, 345678000, time.UTC)

const cnpgActionTestUID = "cluster-uid-1"

func cnpgActionCluster(mut func(obj map[string]any)) *unstructured.Unstructured {
	obj := map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "Cluster",
		"metadata": map[string]any{
			"name": "pg", "namespace": "db", "uid": cnpgActionTestUID, "resourceVersion": "42",
			"annotations": map[string]any{},
		},
		"spec": map[string]any{
			"instances": int64(3),
			"plugins":   []any{map[string]any{"name": "barman-cloud.cloudnative-pg.io", "isWALArchiver": true, "parameters": map[string]any{"barmanObjectName": "store"}}},
			"backup":    map[string]any{"volumeSnapshot": map[string]any{"className": "csi"}},
		},
		"status": map[string]any{
			"currentPrimary": "pg-1",
			"targetPrimary":  "pg-1",
			"phase":          cnpgPhaseHealthy,
			"instanceNames":  []any{"pg-1", "pg-2", "pg-3"},
			"instancesStatus": map[string]any{
				"healthy": []any{"pg-1", "pg-2", "pg-3"},
			},
			"pluginStatus": []any{map[string]any{
				"name": "barman-cloud.cloudnative-pg.io", "backupCapabilities": []any{"TYPE_BACKUP"},
			}},
			"conditions": []any{
				map[string]any{"type": "ContinuousArchiving", "status": "True", "reason": "ContinuousArchivingSuccess", "message": "", "lastTransitionTime": "2026-09-01T00:00:00Z"},
				map[string]any{"type": "Ready", "status": "True", "reason": "ClusterIsReady", "message": "Cluster is Ready", "lastTransitionTime": "2026-09-01T00:00:00Z"},
			},
		},
	}
	if mut != nil {
		mut(obj)
	}
	return &unstructured.Unstructured{Object: obj}
}

func cnpgActionPod(name, uid string, ready bool) *corev1.Pod {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	controller := true
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "db", UID: types.UID(uid),
			Labels: map[string]string{"cnpg.io/cluster": "pg", "cnpg.io/podRole": "instance"},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "postgresql.cnpg.io/v1", Kind: "Cluster", Name: "pg", UID: cnpgActionTestUID, Controller: &controller,
			}},
		},
		Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: status}}},
	}
}

type cnpgActionEnv struct {
	dyn     *dynamicfake.FakeDynamicClient
	typed   *k8sfake.Clientset
	patches []k8stesting.PatchAction
	creates []*unstructured.Unstructured
	deletes []k8stesting.DeleteAction
	// denied lists "verb resource[/subresource]" the caller's access review refuses.
	denied map[string]bool
}

func (e *cnpgActionEnv) clients() cnpgActionClients {
	return cnpgActionClients{dyn: e.dyn, typed: e.typed, now: func() time.Time { return cnpgActionTestNow }}
}

func newCNPGActionEnv(t *testing.T, objs []runtime.Object, pods ...runtime.Object) *cnpgActionEnv {
	t.Helper()
	listKinds := map[schema.GroupVersionResource]string{
		cnpgClusterGVR: "ClusterList", cnpgBackupGVR: "BackupList", cnpgScheduleGVR: "ScheduledBackupList",
		cnpgPoolerGVR: "PoolerList", cnpgDatabaseGVR: "DatabaseList", cnpgPublGVR: "PublicationList", cnpgSubscrGVR: "SubscriptionList",
	}
	env := &cnpgActionEnv{
		dyn:   dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, objs...),
		typed: k8sfake.NewSimpleClientset(pods...),
	}
	env.dyn.PrependReactor("patch", "*", func(a k8stesting.Action) (bool, runtime.Object, error) {
		env.patches = append(env.patches, a.(k8stesting.PatchAction))
		return true, &unstructured.Unstructured{Object: map[string]any{}}, nil
	})
	env.dyn.PrependReactor("create", "backups", func(a k8stesting.Action) (bool, runtime.Object, error) {
		env.creates = append(env.creates, a.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured))
		return false, nil, nil
	})
	env.typed.PrependReactor("create", "selfsubjectaccessreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		review := a.(k8stesting.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
		attrs := review.Spec.ResourceAttributes
		res := attrs.Resource
		if attrs.Subresource != "" {
			res += "/" + attrs.Subresource
		}
		review.Status.Allowed = !env.denied[attrs.Verb+" "+res]
		return true, review, nil
	})
	env.typed.PrependReactor("delete", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		env.deletes = append(env.deletes, a.(k8stesting.DeleteAction))
		return true, nil, nil
	})
	return env
}

func cnpgActionReq(t *testing.T, facts map[string]any, params any) ActionRequest {
	t.Helper()
	req := ActionRequest{ReviewedContext: "kind-test", UID: cnpgActionTestUID}
	if facts != nil {
		b, _ := json.Marshal(facts)
		req.Facts = b
	}
	if params != nil {
		b, _ := json.Marshal(params)
		req.Params = b
	}
	return req
}

// The facts a dialog would echo back for the default fixture.
func cnpgActionFacts() map[string]any {
	return map[string]any{
		"currentPrimary":  "pg-1",
		"targetPrimary":   "pg-1",
		"hibernation":     "",
		"fencedInstances": map[string]any{"raw": "", "all": false, "instances": []any{}},
		"instances":       []any{map[string]any{"pod": "pg-1"}},
	}
}

func cnpgActionPatchBody(t *testing.T, p k8stesting.PatchAction) map[string]any {
	t.Helper()
	if p.GetPatchType() != types.MergePatchType {
		t.Fatalf("patch type = %s, want merge", p.GetPatchType())
	}
	var body map[string]any
	if err := json.Unmarshal(p.GetPatch(), &body); err != nil {
		t.Fatalf("patch body: %v", err)
	}
	return body
}

func cnpgActionAnnotations(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	md, _ := body["metadata"].(map[string]any)
	if md["resourceVersion"] != "42" {
		t.Errorf("patch is not locked on resourceVersion 42: %v", md)
	}
	a, _ := md["annotations"].(map[string]any)
	return a
}

func cnpgActionStatus(t *testing.T, err error) (*actionError, bool) {
	t.Helper()
	var ae *actionError
	ok := errors.As(err, &ae)
	return ae, ok
}

func TestCNPGActionBackupCreatesExactBody(t *testing.T) {
	env := newCNPGActionEnv(t, []runtime.Object{cnpgActionCluster(nil)})
	online := false
	res, err := runCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "backup", cnpgActionReq(t, cnpgActionFacts(), map[string]any{
		"method": "plugin", "pluginName": "barman-cloud.cloudnative-pg.io",
		"pluginParameters": map[string]string{"barmanObjectName": "store"}, "target": "primary", "online": online,
	}))
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if res.Backup != "pg-20260929101112" {
		t.Errorf("backup name = %q, want pg-20260929101112", res.Backup)
	}
	if len(env.creates) != 1 {
		t.Fatalf("creates = %d, want 1", len(env.creates))
	}
	got := env.creates[0].Object
	want := map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "Backup",
		"metadata": map[string]any{
			"name": "pg-20260929101112", "namespace": "db",
			"labels": map[string]any{"cnpg.io/cluster": "pg"},
		},
		"spec": map[string]any{
			"cluster":             map[string]any{"name": "pg"},
			"method":              "plugin",
			"pluginConfiguration": map[string]any{"name": "barman-cloud.cloudnative-pg.io", "parameters": map[string]any{"barmanObjectName": "store"}},
			"target":              "primary",
			"online":              false,
		},
	}
	gb, _ := json.Marshal(got)
	wb, _ := json.Marshal(want)
	if string(gb) != string(wb) {
		t.Errorf("backup body\n got %s\nwant %s", gb, wb)
	}
}

// An omitted target must stay omitted so the Backup inherits the cluster's.
func TestCNPGActionBackupOmittedTargetInherits(t *testing.T) {
	env := newCNPGActionEnv(t, []runtime.Object{cnpgActionCluster(nil)})
	if _, err := runCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "backup",
		cnpgActionReq(t, cnpgActionFacts(), map[string]any{"method": "volumeSnapshot", "name": "manual-one"})); err != nil {
		t.Fatalf("backup: %v", err)
	}
	spec := env.creates[0].Object["spec"].(map[string]any)
	if _, ok := spec["target"]; ok {
		t.Errorf("target was invented: %v", spec)
	}
	if _, ok := spec["pluginConfiguration"]; ok {
		t.Errorf("volumeSnapshot backup carries a pluginConfiguration: %v", spec)
	}
}

// The confirmation showed the cluster's backup target; a Backup that inherits
// it must not run against a target changed since.
func TestCNPGActionBackupBindsInheritedTarget(t *testing.T) {
	env := newCNPGActionEnv(t, []runtime.Object{cnpgActionCluster(func(obj map[string]any) {
		obj["spec"].(map[string]any)["backup"].(map[string]any)["target"] = "primary"
	})})
	_, err := runCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "backup",
		cnpgActionReq(t, cnpgActionFacts(), map[string]any{"method": "volumeSnapshot", "name": "manual-one"}))
	ae, ok := cnpgActionStatus(t, err)
	if !ok || ae.Status != http.StatusConflict || ae.Code != actionCodeChanged {
		t.Fatalf("err = %v, want 409 changed", err)
	}
	if len(env.creates) != 0 {
		t.Error("a Backup was created against an unreviewed target")
	}
	facts := cnpgActionFacts()
	facts["backupTarget"] = "primary"
	if _, err := runCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "backup",
		cnpgActionReq(t, facts, map[string]any{"method": "volumeSnapshot", "name": "manual-one"})); err != nil {
		t.Fatalf("backup with the reviewed target: %v", err)
	}
	// An explicit target doesn't read the cluster's, so its change doesn't matter.
	if _, err := runCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "backup",
		cnpgActionReq(t, cnpgActionFacts(), map[string]any{"method": "volumeSnapshot", "name": "manual-two", "target": "prefer-standby"})); err != nil {
		t.Fatalf("backup with an explicit target: %v", err)
	}
}

func TestCNPGActionBackupRejectsScheduleRunName(t *testing.T) {
	sched := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1", "kind": "ScheduledBackup",
		"metadata": map[string]any{"name": "nightly", "namespace": "db"},
		"spec":     map[string]any{"cluster": map[string]any{"name": "pg"}},
	}}
	env := newCNPGActionEnv(t, []runtime.Object{cnpgActionCluster(nil), sched})
	_, err := runCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "backup",
		cnpgActionReq(t, cnpgActionFacts(), map[string]any{"method": "volumeSnapshot", "name": "nightly-20260929000000"}))
	ae, ok := cnpgActionStatus(t, err)
	if !ok || ae.Status != http.StatusBadRequest {
		t.Fatalf("err = %v, want 400", err)
	}
	if len(env.creates) != 0 {
		t.Error("a Backup was created under a scheduled run's name")
	}
}

func TestCNPGActionBackupRejectsUnknownMethodAndBadName(t *testing.T) {
	env := newCNPGActionEnv(t, []runtime.Object{cnpgActionCluster(nil)})
	for _, params := range []map[string]any{
		{"method": "barmanObjectStore"},
		{"method": "plugin", "pluginName": "other"},
		{"method": "volumeSnapshot", "name": "Not_A_Name"},
	} {
		_, err := runCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "backup", cnpgActionReq(t, cnpgActionFacts(), params))
		if ae, ok := cnpgActionStatus(t, err); !ok || ae.Status != http.StatusBadRequest {
			t.Errorf("%v: err = %v, want 400", params, err)
		}
	}
}

// A create whose answer was lost is resolved by reading the same name back.
func TestCNPGActionBackupTimeoutResolvesByReadingBack(t *testing.T) {
	env := newCNPGActionEnv(t, []runtime.Object{cnpgActionCluster(nil)})
	attempts := 0
	env.dyn.PrependReactor("create", "backups", func(a k8stesting.Action) (bool, runtime.Object, error) {
		attempts++
		obj := a.(k8stesting.CreateAction).GetObject()
		if err := env.dyn.Tracker().Create(cnpgBackupGVR, obj, "db"); err != nil {
			t.Fatalf("tracker: %v", err)
		}
		return true, nil, apierrors.NewTimeoutError("request timed out", 1)
	})
	res, err := runCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "backup",
		cnpgActionReq(t, cnpgActionFacts(), map[string]any{"method": "volumeSnapshot"}))
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if !res.ResolvedAfterTimeout || attempts != 1 {
		t.Errorf("resolved=%v attempts=%d, want resolved after exactly one create", res.ResolvedAfterTimeout, attempts)
	}
}

func TestCNPGActionSwitchoverPatchesStatusLikeKubectlCNPG(t *testing.T) {
	env := newCNPGActionEnv(t, []runtime.Object{cnpgActionCluster(nil)},
		cnpgActionPod("pg-1", "u1", true), cnpgActionPod("pg-2", "u2", true), cnpgActionPod("pg-3", "u3", true))
	_, err := runCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "switchover",
		cnpgActionReq(t, cnpgActionFacts(), map[string]any{"target": "pg-2", "targetPodUID": "u2"}))
	if err != nil {
		t.Fatalf("switchover: %v", err)
	}
	if len(env.patches) != 1 {
		t.Fatalf("patches = %d, want 1", len(env.patches))
	}
	p := env.patches[0]
	if p.GetSubresource() != "status" {
		t.Errorf("subresource = %q, want status", p.GetSubresource())
	}
	body := cnpgActionPatchBody(t, p)
	if body["metadata"].(map[string]any)["resourceVersion"] != "42" {
		t.Error("switchover is not locked on resourceVersion")
	}
	status := body["status"].(map[string]any)
	if status["targetPrimary"] != "pg-2" || status["phase"] != "Switchover in progress" || status["phaseReason"] != "Switching over to pg-2" {
		t.Errorf("status = %v", status)
	}
	if status["targetPrimaryTimestamp"] != "2026-09-29T10:11:12.345678Z" {
		t.Errorf("targetPrimaryTimestamp = %v, want RFC3339Micro", status["targetPrimaryTimestamp"])
	}
	conds := status["conditions"].([]any)
	if len(conds) != 2 {
		t.Fatalf("conditions = %v, want the full list (merge patch replaces it)", conds)
	}
	ready := conds[1].(map[string]any)
	if ready["type"] != "Ready" || ready["status"] != "False" || ready["reason"] != "ClusterIsNotReady" || ready["message"] != "Cluster Is Not Ready" {
		t.Errorf("Ready = %v", ready)
	}
	if ready["lastTransitionTime"] != "2026-09-29T10:11:12Z" {
		t.Errorf("Ready lastTransitionTime = %v, want the switch time", ready["lastTransitionTime"])
	}
	if conds[0].(map[string]any)["lastTransitionTime"] != "2026-09-01T00:00:00Z" {
		t.Error("an unrelated condition was rewritten")
	}
}

func TestCNPGActionSwitchoverTargetChecks(t *testing.T) {
	fencedPg2 := cnpgActionCluster(func(o map[string]any) {
		o["metadata"].(map[string]any)["annotations"] = map[string]any{cnpgFencedAnnotation: `["pg-2"]`}
	})
	facts := cnpgActionFacts()
	facts["fencedInstances"] = map[string]any{"raw": `["pg-2"]`}
	t.Run("fenced target", func(t *testing.T) {
		env := newCNPGActionEnv(t, []runtime.Object{fencedPg2}, cnpgActionPod("pg-2", "u2", true))
		_, err := runCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "switchover",
			cnpgActionReq(t, facts, map[string]any{"target": "pg-2", "targetPodUID": "u2"}))
		if ae, ok := cnpgActionStatus(t, err); !ok || ae.Status != http.StatusConflict || ae.Code != actionCodeBlocked {
			t.Fatalf("err = %v, want 409 blocked", err)
		}
	})
	t.Run("recreated pod", func(t *testing.T) {
		env := newCNPGActionEnv(t, []runtime.Object{cnpgActionCluster(nil)}, cnpgActionPod("pg-2", "u2-new", true))
		_, err := runCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "switchover",
			cnpgActionReq(t, cnpgActionFacts(), map[string]any{"target": "pg-2", "targetPodUID": "u2"}))
		if ae, ok := cnpgActionStatus(t, err); !ok || ae.Code != actionCodeChanged {
			t.Fatalf("err = %v, want 409 changed", err)
		}
	})
	t.Run("not ready", func(t *testing.T) {
		env := newCNPGActionEnv(t, []runtime.Object{cnpgActionCluster(nil)}, cnpgActionPod("pg-2", "u2", false))
		_, err := runCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "switchover",
			cnpgActionReq(t, cnpgActionFacts(), map[string]any{"target": "pg-2", "targetPodUID": "u2"}))
		if ae, ok := cnpgActionStatus(t, err); !ok || ae.Code != actionCodeBlocked {
			t.Fatalf("err = %v, want 409 blocked", err)
		}
	})
	t.Run("pod of another cluster", func(t *testing.T) {
		foreign := cnpgActionPod("pg-2", "u2", true)
		foreign.OwnerReferences[0].UID = "other-uid"
		env := newCNPGActionEnv(t, []runtime.Object{cnpgActionCluster(nil)}, foreign)
		_, err := runCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "switchover",
			cnpgActionReq(t, cnpgActionFacts(), map[string]any{"target": "pg-2", "targetPodUID": "u2"}))
		if _, ok := cnpgActionStatus(t, err); !ok || len(env.patches) != 0 {
			t.Fatalf("err = %v patches=%d, want a refusal and no write", err, len(env.patches))
		}
	})
}

func TestCNPGActionFactMismatchReturnsCurrentFacts(t *testing.T) {
	env := newCNPGActionEnv(t, []runtime.Object{cnpgActionCluster(nil)}, cnpgActionPod("pg-2", "u2", true))
	facts := cnpgActionFacts()
	facts["currentPrimary"] = "pg-3"
	_, err := runCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "switchover",
		cnpgActionReq(t, facts, map[string]any{"target": "pg-2", "targetPodUID": "u2"}))
	ae, ok := cnpgActionStatus(t, err)
	if !ok || ae.Status != http.StatusConflict || ae.Code != actionCodeChanged {
		t.Fatalf("err = %v, want 409 changed", err)
	}
	if cur, ok := ae.Current.(CNPGClusterFacts); !ok || cur.CurrentPrimary != "pg-1" {
		t.Errorf("current = %#v, want the facts as they are now", ae.Current)
	}
	if len(env.patches) != 0 {
		t.Error("a write went out after a fact mismatch")
	}
}

func TestCNPGActionUIDMismatchAndMissingFacts(t *testing.T) {
	env := newCNPGActionEnv(t, []runtime.Object{cnpgActionCluster(nil)})
	req := cnpgActionReq(t, cnpgActionFacts(), nil)
	req.UID = "old-uid"
	_, err := runCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "restart", req)
	if ae, ok := cnpgActionStatus(t, err); !ok || ae.Code != actionCodeChanged {
		t.Fatalf("err = %v, want 409 changed for a recreated Cluster", err)
	}
	_, err = runCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "restart", cnpgActionReq(t, map[string]any{}, nil))
	if ae, ok := cnpgActionStatus(t, err); !ok || ae.Status != http.StatusBadRequest {
		t.Fatalf("err = %v, want 400 when the bound facts are missing", err)
	}
}

func TestCNPGActionReviewedContext(t *testing.T) {
	if err := checkReviewedContext("kind-a", "kind-a"); err != nil {
		t.Errorf("matching context refused: %v", err)
	}
	ae, ok := cnpgActionStatus(t, checkReviewedContext("kind-a", "prod"))
	if !ok || ae.Status != http.StatusConflict || ae.Code != actionCodeContextChanged {
		t.Errorf("mismatch = %v, want 409 context_changed", ae)
	}
}

func TestCNPGActionAnnotationWrites(t *testing.T) {
	hibernated := cnpgActionCluster(func(o map[string]any) {
		o["metadata"].(map[string]any)["annotations"] = map[string]any{cnpgHibernateAnnotation: "on"}
	})
	hibFacts := cnpgActionFacts()
	hibFacts["hibernation"] = "on"
	for _, tc := range []struct {
		action  string
		cluster *unstructured.Unstructured
		facts   map[string]any
		key     string
		want    any
	}{
		{"restart", cnpgActionCluster(nil), cnpgActionFacts(), cnpgRestartAnnotation, "2026-09-29T10:11:12Z"},
		{"reload", cnpgActionCluster(nil), cnpgActionFacts(), cnpgReloadAnnotation, "2026-09-29T10:11:12.345678Z"},
		{"hibernate", cnpgActionCluster(nil), cnpgActionFacts(), cnpgHibernateAnnotation, "on"},
		{"rehydrate", hibernated, hibFacts, cnpgHibernateAnnotation, "off"},
	} {
		t.Run(tc.action, func(t *testing.T) {
			env := newCNPGActionEnv(t, []runtime.Object{tc.cluster})
			if _, err := runCNPGClusterAction(context.Background(), env.clients(), "db", "pg", tc.action, cnpgActionReq(t, tc.facts, nil)); err != nil {
				t.Fatalf("%s: %v", tc.action, err)
			}
			if len(env.patches) != 1 || env.patches[0].GetSubresource() != "" {
				t.Fatalf("patches = %v, want one main-resource patch", env.patches)
			}
			body := cnpgActionPatchBody(t, env.patches[0])
			annos := cnpgActionAnnotations(t, body)
			if len(annos) != 1 || annos[tc.key] != tc.want {
				t.Errorf("annotations = %v, want only %s=%v", annos, tc.key, tc.want)
			}
			if _, ok := body["spec"]; ok {
				t.Error("an annotation action wrote spec")
			}
		})
	}
}

// Hibernation blocks the disruptive actions but never rehydration.
func TestCNPGActionHibernatedGuards(t *testing.T) {
	f := CNPGClusterFacts{Hibernated: true, Hibernation: "on", CurrentPrimary: "pg-1", BackupMethods: []CNPGBackupMethodFact{{Method: "volumeSnapshot", Capability: "backup"}}}
	for name, reason := range map[string]string{
		"backup": cnpgGuardBackup(f), "switchover": cnpgGuardSwitchover(f), "restart": cnpgGuardRestart(f), "fence": cnpgGuardFence(f),
	} {
		if reason == "" {
			t.Errorf("%s allowed on a hibernated cluster", name)
		}
	}
	if r := cnpgGuardRehydrate(f); r != "" {
		t.Errorf("rehydrate blocked: %s", r)
	}
	f.Terminating = true
	if cnpgGuardRehydrate(f) == "" || cnpgGuardUnfence(CNPGClusterFacts{Terminating: true}) == "" {
		t.Error("a terminating cluster still offers actions")
	}
	replica := CNPGClusterFacts{IsReplicaCluster: true, CurrentPrimary: "pg-1"}
	if cnpgGuardSwitchover(replica) == "" {
		t.Error("switchover offered on a replica cluster")
	}
	inFlight := CNPGClusterFacts{CurrentPrimary: "pg-1", TargetPrimary: "pg-2"}
	if cnpgGuardSwitchover(inFlight) == "" || cnpgGuardRestart(inFlight) == "" {
		t.Error("switchover/restart offered while a switchover is in flight")
	}
}

func TestCNPGActionRestartInstance(t *testing.T) {
	t.Run("standby delete carries the UID precondition", func(t *testing.T) {
		env := newCNPGActionEnv(t, []runtime.Object{cnpgActionCluster(nil)}, cnpgActionPod("pg-2", "u2", false))
		if _, err := runCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "restartInstance",
			cnpgActionReq(t, cnpgActionFacts(), map[string]any{"pod": "pg-2", "podUID": "u2"})); err != nil {
			t.Fatalf("restartInstance: %v", err)
		}
		if len(env.deletes) != 1 {
			t.Fatalf("deletes = %d, want 1", len(env.deletes))
		}
		d := env.deletes[0]
		opts := d.GetDeleteOptions()
		if d.GetName() != "pg-2" || opts.Preconditions == nil || opts.Preconditions.UID == nil || *opts.Preconditions.UID != "u2" {
			t.Errorf("delete %s with options %+v, want UID precondition u2", d.GetName(), opts)
		}
	})
	t.Run("primary restarts in place through status", func(t *testing.T) {
		env := newCNPGActionEnv(t, []runtime.Object{cnpgActionCluster(nil)}, cnpgActionPod("pg-1", "u1", true))
		if _, err := runCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "restartInstance",
			cnpgActionReq(t, cnpgActionFacts(), map[string]any{"pod": "pg-1", "podUID": "u1"})); err != nil {
			t.Fatalf("restartInstance: %v", err)
		}
		if len(env.deletes) != 0 || len(env.patches) != 1 || env.patches[0].GetSubresource() != "status" {
			t.Fatalf("deletes=%d patches=%v, want one status patch", len(env.deletes), env.patches)
		}
		status := cnpgActionPatchBody(t, env.patches[0])["status"].(map[string]any)
		if status["phase"] != "Primary instance is being restarted in-place" || status["phaseReason"] != "Requested by the user" {
			t.Errorf("status = %v", status)
		}
		if _, ok := status["targetPrimary"]; ok {
			t.Error("in-place restart touched targetPrimary")
		}
	})
	t.Run("primary allowed while waiting for user action", func(t *testing.T) {
		waiting := cnpgActionCluster(func(o map[string]any) { o["status"].(map[string]any)["phase"] = cnpgPhaseWaitingForUser })
		env := newCNPGActionEnv(t, []runtime.Object{waiting}, cnpgActionPod("pg-1", "u1", true))
		if _, err := runCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "restartInstance",
			cnpgActionReq(t, cnpgActionFacts(), map[string]any{"pod": "pg-1", "podUID": "u1"})); err != nil {
			t.Fatalf("restartInstance: %v", err)
		}
	})
	t.Run("primary refused mid-upgrade", func(t *testing.T) {
		upgrading := cnpgActionCluster(func(o map[string]any) { o["status"].(map[string]any)["phase"] = "Upgrading cluster" })
		env := newCNPGActionEnv(t, []runtime.Object{upgrading}, cnpgActionPod("pg-1", "u1", true))
		_, err := runCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "restartInstance",
			cnpgActionReq(t, cnpgActionFacts(), map[string]any{"pod": "pg-1", "podUID": "u1"}))
		if ae, ok := cnpgActionStatus(t, err); !ok || ae.Code != actionCodeBlocked {
			t.Fatalf("err = %v, want 409 blocked", err)
		}
	})
}

func TestCNPGActionFencing(t *testing.T) {
	withFence := func(raw string) (*unstructured.Unstructured, map[string]any) {
		c := cnpgActionCluster(func(o map[string]any) {
			o["metadata"].(map[string]any)["annotations"] = map[string]any{cnpgFencedAnnotation: raw}
		})
		f := cnpgActionFacts()
		f["fencedInstances"] = map[string]any{"raw": raw}
		return c, f
	}
	run := func(t *testing.T, raw, action string, params map[string]any) (*cnpgActionEnv, error) {
		t.Helper()
		c, f := withFence(raw)
		env := newCNPGActionEnv(t, []runtime.Object{c})
		_, err := runCNPGClusterAction(context.Background(), env.clients(), "db", "pg", action, cnpgActionReq(t, f, params))
		return env, err
	}
	fencedValue := func(t *testing.T, env *cnpgActionEnv) any {
		t.Helper()
		if len(env.patches) != 1 {
			t.Fatalf("patches = %d, want 1", len(env.patches))
		}
		annos := cnpgActionAnnotations(t, cnpgActionPatchBody(t, env.patches[0]))
		v, ok := annos[cnpgFencedAnnotation]
		if !ok {
			t.Fatal("patch does not set the fencing annotation")
		}
		return v
	}

	t.Run("fence adds to the JSON string", func(t *testing.T) {
		env, err := run(t, `["pg-3"]`, "fence", map[string]any{"instances": []string{"pg-2"}})
		if err != nil {
			t.Fatal(err)
		}
		if v := fencedValue(t, env); v != `["pg-2","pg-3"]` {
			t.Errorf("annotation = %v", v)
		}
	})
	t.Run("fence all", func(t *testing.T) {
		env, err := run(t, "", "fence", map[string]any{"instances": "*"})
		if err != nil {
			t.Fatal(err)
		}
		if v := fencedValue(t, env); v != `["*"]` {
			t.Errorf("annotation = %v", v)
		}
	})
	t.Run("lifting the last fence removes the annotation", func(t *testing.T) {
		env, err := run(t, `["pg-2"]`, "unfence", map[string]any{"instances": []string{"pg-2"}})
		if err != nil {
			t.Fatal(err)
		}
		if v := fencedValue(t, env); v != nil {
			t.Errorf("annotation = %v, want removed", v)
		}
	})
	t.Run("lifting one instance under wildcard is refused", func(t *testing.T) {
		env, err := run(t, `["*"]`, "unfence", map[string]any{"instances": []string{"pg-2"}})
		ae, ok := cnpgActionStatus(t, err)
		if !ok || ae.Status != http.StatusConflict || ae.Code != cnpgCodeAllFenced {
			t.Fatalf("err = %v, want 409 all_fenced", err)
		}
		if len(env.patches) != 0 {
			t.Error("a write went out")
		}
	})
	t.Run("explicit conversion from wildcard", func(t *testing.T) {
		env, err := run(t, `["*"]`, "unfence", map[string]any{"instances": []string{"pg-2"}, "convertFromAll": true, "remaining": []string{"pg-3", "pg-1"}})
		if err != nil {
			t.Fatal(err)
		}
		if v := fencedValue(t, env); v != `["pg-1","pg-3"]` {
			t.Errorf("annotation = %v", v)
		}
	})
	t.Run("conversion with a stale remaining list", func(t *testing.T) {
		_, err := run(t, `["*"]`, "unfence", map[string]any{"instances": []string{"pg-2"}, "convertFromAll": true, "remaining": []string{"pg-1"}})
		if ae, ok := cnpgActionStatus(t, err); !ok || ae.Code != actionCodeChanged {
			t.Fatalf("err = %v, want 409 changed", err)
		}
	})
	t.Run("malformed annotation blocks writes", func(t *testing.T) {
		for _, action := range []string{"fence", "unfence"} {
			_, err := run(t, `pg-2`, action, map[string]any{"instances": "*"})
			if ae, ok := cnpgActionStatus(t, err); !ok || ae.Code != actionCodeBlocked {
				t.Errorf("%s: err = %v, want 409 blocked", action, err)
			}
		}
	})
}

func cnpgSnapshotBackup(name, phase string, online any) *unstructured.Unstructured {
	spec := map[string]any{"cluster": map[string]any{"name": "pg"}, "method": "volumeSnapshot"}
	if online != nil {
		spec["online"] = online
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "Backup",
		"metadata":   map[string]any{"name": name, "namespace": "db"},
		"spec":       spec,
		"status":     map[string]any{"phase": phase},
	}}
}

// The operator fences the instance for a cold snapshot and lifts the fence
// itself; lifting it first would start PostgreSQL under the snapshot.
func TestCNPGActionUnfenceWaitsForColdSnapshot(t *testing.T) {
	fenced := func(o map[string]any) {
		o["metadata"].(map[string]any)["annotations"] = map[string]any{cnpgFencedAnnotation: `["pg-2"]`}
	}
	facts := func() map[string]any {
		f := cnpgActionFacts()
		f["fencedInstances"] = map[string]any{"raw": `["pg-2"]`}
		return f
	}
	cases := []struct {
		name    string
		backups []runtime.Object
		cluster func(o map[string]any)
		blocked bool
	}{
		{"running cold snapshot", []runtime.Object{cnpgSnapshotBackup("cold", "running", false)}, nil, true},
		{"cold by the cluster's default", []runtime.Object{cnpgSnapshotBackup("cold", "started", nil)}, func(o map[string]any) {
			o["spec"].(map[string]any)["backup"] = map[string]any{"volumeSnapshot": map[string]any{"className": "csi", "online": false}}
		}, true},
		{"finished cold snapshot", []runtime.Object{cnpgSnapshotBackup("cold", "completed", false)}, nil, false},
		{"running online snapshot", []runtime.Object{cnpgSnapshotBackup("hot", "running", true)}, nil, false},
		{"online by default", []runtime.Object{cnpgSnapshotBackup("hot", "running", nil)}, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := cnpgActionCluster(func(o map[string]any) {
				fenced(o)
				if tc.cluster != nil {
					tc.cluster(o)
				}
			})
			env := newCNPGActionEnv(t, append([]runtime.Object{c}, tc.backups...), cnpgActionPod("pg-1", "u1", true))
			resp, err := (&Server{}).cnpgClusterCapabilities(httptest.NewRequest(http.MethodGet, "/", nil), env.clients(), "kind-test", "db", "pg")
			if err != nil {
				t.Fatal(err)
			}
			if got := !resp.Actions.Unfence.Allowed; got != tc.blocked {
				t.Errorf("unfence blocked = %v (%q), want %v", got, resp.Actions.Unfence.Reason, tc.blocked)
			}
			if got := !resp.InstanceActions["pg-2"].Unfence.Allowed; got != tc.blocked {
				t.Errorf("pg-2 unfence blocked = %v, want %v", got, tc.blocked)
			}
			_, err = runCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "unfence", cnpgActionReq(t, facts(), map[string]any{"instances": []string{"pg-2"}}))
			ae, isBlocked := cnpgActionStatus(t, err)
			isBlocked = isBlocked && ae.Code == actionCodeBlocked
			if isBlocked != tc.blocked {
				t.Errorf("run: err = %v, want blocked %v", err, tc.blocked)
			}
			if tc.blocked && len(env.patches) != 0 {
				t.Error("a write went out")
			}
		})
	}
}

func TestCNPGActionMalformedFencingBlocksCapability(t *testing.T) {
	c := cnpgActionCluster(func(o map[string]any) {
		o["metadata"].(map[string]any)["annotations"] = map[string]any{cnpgFencedAnnotation: "{not json"}
	})
	env := newCNPGActionEnv(t, []runtime.Object{c}, cnpgActionPod("pg-1", "u1", true))
	srv := &Server{}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	resp, err := srv.cnpgClusterCapabilities(r, env.clients(), "kind-test", "db", "pg")
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Facts.FencedInstances.Malformed {
		t.Error("malformed annotation not reported")
	}
	if resp.Actions.Fence.Allowed || resp.Actions.Unfence.Allowed {
		t.Errorf("fence=%+v unfence=%+v, want both blocked", resp.Actions.Fence, resp.Actions.Unfence)
	}
	if !resp.Actions.Restart.Allowed || !resp.Actions.Hibernate.Allowed {
		t.Error("a malformed fence blocked unrelated actions")
	}
}

func TestCNPGActionCapabilitiesPermissionDenied(t *testing.T) {
	env := newCNPGActionEnv(t, []runtime.Object{cnpgActionCluster(nil)},
		cnpgActionPod("pg-1", "u1", true), cnpgActionPod("pg-2", "u2", true))
	srv := &Server{permCache: auth.NewPermissionCache()}
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"db"}}
	perms.SetCanI("patch", cnpgGroup, "clusters/status", "db", false)
	perms.SetCanI("get", "", "pods", "db", true)
	perms.SetCanI("patch", cnpgGroup, "clusters", "db", true)
	perms.SetCanI("create", cnpgGroup, "backups", "db", false)
	perms.SetCanI("delete", "", "pods", "db", true)
	srv.permCache.Set("alice", nil, perms)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r = r.WithContext(auth.ContextWithUser(r.Context(), &auth.User{Username: "alice"}))

	resp, err := srv.cnpgClusterCapabilities(r, env.clients(), "kind-test", "db", "pg")
	if err != nil {
		t.Fatal(err)
	}
	sw := resp.Actions.Switchover
	if sw.Allowed || sw.Permission != permissionDenied || sw.Grant == nil || *sw.Grant != cnpgGrantPatchStatus.In("db") || !strings.Contains(sw.Reason, "patch clusters/status") {
		t.Errorf("switchover = %+v, want denied naming patch clusters/status", sw)
	}
	if b := resp.Actions.Backup; b.Allowed || b.Grant == nil || *b.Grant != cnpgGrantCreateBackups.In("db") {
		t.Errorf("backup = %+v, want denied naming create backups", b)
	}
	if !resp.Actions.Restart.Allowed || resp.Actions.Restart.Permission != permissionAllowed {
		t.Errorf("restart = %+v, want allowed", resp.Actions.Restart)
	}
	// The primary's in-place restart needs the status grant; a standby's needs delete pods.
	if resp.InstanceActions["pg-1"].Restart.Allowed || !resp.InstanceActions["pg-2"].Restart.Allowed {
		t.Errorf("instance restarts = %+v", resp.InstanceActions)
	}
	if resp.UID != cnpgActionTestUID || resp.ResourceVersion != "42" || resp.Context != "kind-test" {
		t.Errorf("identity = %s/%s/%s", resp.UID, resp.ResourceVersion, resp.Context)
	}
}

func TestCNPGActionCapabilitiesPlansAndEffects(t *testing.T) {
	c := cnpgActionCluster(func(o map[string]any) {
		spec := o["spec"].(map[string]any)
		spec["primaryUpdateStrategy"] = "unsupervised"
		spec["primaryUpdateMethod"] = "switchover"
		o["metadata"].(map[string]any)["annotations"] = map[string]any{cnpgFencedAnnotation: `["pg-3"]`}
	})
	obj := func(kind, name string, spec map[string]any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "postgresql.cnpg.io/v1", "kind": kind,
			"metadata": map[string]any{"name": name, "namespace": "db"}, "spec": spec,
		}}
	}
	cl := map[string]any{"name": "pg"}
	env := newCNPGActionEnv(t, []runtime.Object{c,
		obj("Pooler", "pg-rw", map[string]any{"cluster": cl}),
		obj("Pooler", "other-rw", map[string]any{"cluster": map[string]any{"name": "other"}}),
		obj("ScheduledBackup", "nightly", map[string]any{"cluster": cl}),
		obj("ScheduledBackup", "paused", map[string]any{"cluster": cl, "suspend": true}),
		obj("Database", "app", map[string]any{"cluster": cl}),
	})
	resp, err := (&Server{}).cnpgClusterCapabilities(httptest.NewRequest(http.MethodGet, "/", nil), env.clients(), "kind-test", "db", "pg")
	if err != nil {
		t.Fatal(err)
	}
	effects := []string{}
	for _, s := range resp.RestartPlan.Steps {
		effects = append(effects, s.Instance+":"+s.Effect)
	}
	if got := strings.Join(effects, ","); got != "pg-2:recreate,pg-3:skipped_fenced,pg-1:switchover" {
		t.Errorf("restart plan = %s", got)
	}
	he := resp.HibernateEffects
	if strings.Join(he.Poolers.Names, ",") != "pg-rw" || strings.Join(he.UnsuspendedScheduledBackups.Names, ",") != "nightly" || len(he.Databases.Names) != 1 {
		t.Errorf("hibernate effects = %+v", he)
	}
	if !he.Volumes.Available {
		t.Errorf("volumes = %+v", he.Volumes)
	}
	methods := []string{}
	for _, m := range resp.Facts.BackupMethods {
		methods = append(methods, m.Method+"/"+m.Capability)
	}
	if strings.Join(methods, ",") != "plugin/backup,volumeSnapshot/backup" {
		t.Errorf("backup methods = %v", methods)
	}
}

func TestCNPGActionBackupMethodCapability(t *testing.T) {
	c := cnpgActionCluster(func(o map[string]any) {
		delete(o["spec"].(map[string]any), "backup")
		o["status"].(map[string]any)["pluginStatus"] = []any{map[string]any{"name": "barman-cloud.cloudnative-pg.io", "backupCapabilities": []any{}}}
	})
	m := cnpgBackupMethods(c)
	if len(m) != 1 || m[0].Capability != "none" {
		t.Fatalf("methods = %+v, want the plugin marked none", m)
	}
	if cnpgGuardBackup(CNPGClusterFacts{BackupMethods: m}) == "" {
		t.Error("backup offered with no backup-capable method")
	}
	c = cnpgActionCluster(func(o map[string]any) { delete(o["status"].(map[string]any), "pluginStatus") })
	if m := cnpgBackupMethods(c); m[0].Capability != "unknown" {
		t.Errorf("unreported plugin = %+v, want unknown", m[0])
	}
	// barman-cloud 0.14 reports status without a backupCapabilities field at all.
	c = cnpgActionCluster(func(o map[string]any) {
		o["status"].(map[string]any)["pluginStatus"] = []any{map[string]any{
			"name": "barman-cloud.cloudnative-pg.io", "version": "0.14.0",
			"capabilities": []any{"TYPE_RECONCILER_HOOKS", "TYPE_LIFECYCLE_SERVICE"},
		}}
	})
	if m := cnpgBackupMethods(c); m[0].Capability != "unknown" {
		t.Errorf("plugin without a backupCapabilities field = %+v, want unknown", m[0])
	}
}

func cnpgActionSchedule(mut func(o map[string]any)) *unstructured.Unstructured {
	o := map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1", "kind": "ScheduledBackup",
		"metadata": map[string]any{"name": "nightly", "namespace": "db", "uid": "sched-uid", "resourceVersion": "7", "generation": int64(3)},
		"spec": map[string]any{
			"cluster":              map[string]any{"name": "pg"},
			"schedule":             "0 0 0 * * *",
			"method":               "plugin",
			"pluginConfiguration":  map[string]any{"name": "barman-cloud.cloudnative-pg.io", "parameters": map[string]any{"barmanObjectName": "store"}},
			"online":               false,
			"onlineConfiguration":  map[string]any{"immediateCheckpoint": true},
			"backupOwnerReference": "self",
		},
		"status": map[string]any{"nextScheduleTime": "2026-09-28T00:00:00Z"},
	}
	if mut != nil {
		mut(o)
	}
	return &unstructured.Unstructured{Object: o}
}

func TestCNPGScheduleRunDestinationGuard(t *testing.T) {
	for _, tc := range []struct {
		name, method string
		configure    bool
	}{
		{"default without destination", "", false},
		{"in-tree without destination", "barmanObjectStore", false},
		{"in-tree with destination", "barmanObjectStore", true},
		{"snapshot without configuration", "volumeSnapshot", false},
		{"snapshot configured", "volumeSnapshot", true},
		{"plugin without destination", "plugin", false},
		{"plugin configured", "plugin", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cluster := cnpgActionCluster(func(o map[string]any) {
				spec := o["spec"].(map[string]any)
				delete(spec, "backup")
				delete(spec, "plugins")
				if tc.method == "plugin" {
					p := map[string]any{"name": "barman-cloud.cloudnative-pg.io"}
					if tc.configure {
						p["parameters"] = map[string]any{"barmanObjectName": "store"}
					}
					spec["plugins"] = []any{p}
				} else if tc.configure {
					if tc.method == "volumeSnapshot" {
						spec["backup"] = map[string]any{"volumeSnapshot": map[string]any{}}
					} else {
						spec["backup"] = map[string]any{"barmanObjectStore": map[string]any{"destinationPath": "s3://backups"}}
					}
				}
			})
			schedule := cnpgActionSchedule(func(o map[string]any) {
				spec := o["spec"].(map[string]any)
				spec["method"] = tc.method
				delete(spec, "pluginConfiguration")
				if tc.method == "plugin" {
					spec["pluginConfiguration"] = map[string]any{"name": "barman-cloud.cloudnative-pg.io"}
				}
			})
			env := newCNPGActionEnv(t, []runtime.Object{cluster, schedule})
			caps, err := (&Server{}).cnpgScheduleCapabilities(httptest.NewRequest(http.MethodGet, "/", nil), env.clients(), "kind-test", "db", "nightly")
			if err != nil {
				t.Fatal(err)
			}
			if caps.Actions.Run.Allowed != tc.configure {
				t.Fatalf("run capability = %+v", caps.Actions.Run)
			}
			if !tc.configure && caps.Actions.Run.Reason != "Configure a backup destination on pg first" {
				t.Fatalf("reason = %s", caps.Actions.Run.Reason)
			}
			_, err = runCNPGScheduleAction(context.Background(), env.clients(), "db", "nightly", "run", ActionRequest{UID: "sched-uid", Facts: json.RawMessage(`{"generation":3}`)})
			if tc.configure {
				if err != nil || len(env.creates) != 1 {
					t.Fatalf("configured run: %v, creates=%d", err, len(env.creates))
				}
			} else {
				var ae *actionError
				if !errors.As(err, &ae) || ae.Code != "blocked" || len(env.creates) != 0 {
					t.Fatalf("unconfigured run: %v, creates=%d", err, len(env.creates))
				}
			}
		})
	}
}

func TestCNPGScheduleRunMethodMismatch(t *testing.T) {
	cluster := cnpgActionCluster(nil)
	schedule := cnpgActionSchedule(func(o map[string]any) {
		spec := o["spec"].(map[string]any)
		delete(spec, "method")
		delete(spec, "pluginConfiguration")
	})
	env := newCNPGActionEnv(t, []runtime.Object{cluster, schedule})
	caps, err := (&Server{}).cnpgScheduleCapabilities(httptest.NewRequest(http.MethodGet, "/", nil), env.clients(), "kind-test", "db", "nightly")
	if err != nil {
		t.Fatal(err)
	}
	if caps.Actions.Run.Allowed || !strings.Contains(caps.Actions.Run.Reason, "No barmanObjectStore destination on pg") || !strings.Contains(caps.Actions.Run.Reason, "Use method plugin") {
		t.Fatalf("method mismatch should identify the existing plugin destination: %+v", caps.Actions.Run)
	}
	_, err = runCNPGScheduleAction(context.Background(), env.clients(), "db", "nightly", "run", ActionRequest{UID: "sched-uid", Facts: json.RawMessage(`{"generation":3}`)})
	var ae *actionError
	if !errors.As(err, &ae) || ae.Code != "blocked" || len(env.creates) != 0 {
		t.Fatalf("mismatched method must not create a Backup: %v, creates=%d", err, len(env.creates))
	}
}

func TestCNPGBackupDestinationGuard(t *testing.T) {
	cluster := cnpgActionCluster(func(o map[string]any) {
		spec := o["spec"].(map[string]any)
		delete(spec, "plugins")
		spec["backup"] = map[string]any{"barmanObjectStore": map[string]any{}}
	})
	env := newCNPGActionEnv(t, []runtime.Object{cluster})
	caps, err := (&Server{}).cnpgClusterCapabilities(httptest.NewRequest(http.MethodGet, "/", nil), env.clients(), "kind-test", "db", "pg")
	if err != nil {
		t.Fatal(err)
	}
	if caps.Actions.Backup.Allowed || caps.Actions.Backup.Reason != "Configure a backup destination on pg first" {
		t.Fatalf("backup capability = %+v", caps.Actions.Backup)
	}
	_, err = runCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "backup", cnpgActionReq(t, cnpgActionFacts(), map[string]any{"method": "barmanObjectStore"}))
	var ae *actionError
	if !errors.As(err, &ae) || ae.Code != "blocked" || len(env.creates) != 0 {
		t.Fatalf("backup: %v, creates=%d", err, len(env.creates))
	}
}

func TestCNPGActionScheduleRunCopiesSettings(t *testing.T) {
	env := newCNPGActionEnv(t, []runtime.Object{cnpgActionCluster(nil), cnpgActionSchedule(nil)})
	req := ActionRequest{ReviewedContext: "kind-test", UID: "sched-uid", Facts: json.RawMessage(`{"generation":3}`)}
	res, err := runCNPGScheduleAction(context.Background(), env.clients(), "db", "nightly", "run", req)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Backup != "nightly-manual-20260929101112" || len(env.creates) != 1 {
		t.Fatalf("backup = %q creates = %d", res.Backup, len(env.creates))
	}
	b := env.creates[0]
	if b.GetLabels()["cnpg.io/cluster"] != "pg" || b.GetAnnotations()[cnpgRequestedFromAnno] != "nightly" {
		t.Errorf("metadata = %v / %v", b.GetLabels(), b.GetAnnotations())
	}
	if len(b.GetOwnerReferences()) != 0 {
		t.Error("the manual run is owned by the schedule")
	}
	spec := b.Object["spec"].(map[string]any)
	got, _ := json.Marshal(spec)
	want := `{"cluster":{"name":"pg"},"method":"plugin","online":false,"onlineConfiguration":{"immediateCheckpoint":true},"pluginConfiguration":{"name":"barman-cloud.cloudnative-pg.io","parameters":{"barmanObjectName":"store"}}}`
	if string(got) != want {
		t.Errorf("spec\n got %s\nwant %s", got, want)
	}
}

func TestCNPGActionScheduleRunBindsReviewedSettings(t *testing.T) {
	env := newCNPGActionEnv(t, []runtime.Object{cnpgActionCluster(nil), cnpgActionSchedule(nil)})
	req := ActionRequest{ReviewedContext: "kind-test", UID: "sched-uid", Facts: json.RawMessage(`{"generation":2}`)}
	_, err := runCNPGScheduleAction(context.Background(), env.clients(), "db", "nightly", "run", req)
	var ae *actionError
	if !errors.As(err, &ae) || ae.Status != http.StatusConflict {
		t.Fatalf("stale generation: err = %v, want 409", err)
	}
	if len(env.creates) != 0 {
		t.Error("a Backup was created from settings the user did not review")
	}
	req.Facts = nil
	if _, err := runCNPGScheduleAction(context.Background(), env.clients(), "db", "nightly", "run", req); !errors.As(err, &ae) || ae.Status != http.StatusBadRequest {
		t.Fatalf("missing generation: err = %v, want 400", err)
	}
}

func TestCNPGActionScheduleSuspendResume(t *testing.T) {
	t.Run("resume reports catch-up", func(t *testing.T) {
		suspended := cnpgActionSchedule(func(o map[string]any) { o["spec"].(map[string]any)["suspend"] = true })
		env := newCNPGActionEnv(t, []runtime.Object{cnpgActionCluster(nil), suspended})
		res, err := runCNPGScheduleAction(context.Background(), env.clients(), "db", "nightly", "resume",
			ActionRequest{ReviewedContext: "kind-test", UID: "sched-uid", Facts: json.RawMessage(`{"suspended":true}`)})
		if err != nil {
			t.Fatal(err)
		}
		if !res.CatchUp {
			t.Error("catchUp not reported for a past nextScheduleTime")
		}
		body := cnpgActionPatchBody(t, env.patches[0])
		if body["spec"].(map[string]any)["suspend"] != false || body["metadata"].(map[string]any)["resourceVersion"] != "7" {
			t.Errorf("patch = %v", body)
		}
	})
	t.Run("suspend binds the reviewed state", func(t *testing.T) {
		env := newCNPGActionEnv(t, []runtime.Object{cnpgActionCluster(nil), cnpgActionSchedule(nil)})
		_, err := runCNPGScheduleAction(context.Background(), env.clients(), "db", "nightly", "suspend",
			ActionRequest{ReviewedContext: "kind-test", UID: "sched-uid", Facts: json.RawMessage(`{"suspended":true}`)})
		if ae, ok := cnpgActionStatus(t, err); !ok || ae.Code != actionCodeChanged {
			t.Fatalf("err = %v, want 409 changed", err)
		}
	})
}

func TestCNPGActionApiserverConflictIsNotRetried(t *testing.T) {
	env := newCNPGActionEnv(t, []runtime.Object{cnpgActionCluster(nil)})
	calls := 0
	env.dyn.PrependReactor("patch", "clusters", func(k8stesting.Action) (bool, runtime.Object, error) {
		calls++
		return true, nil, apierrors.NewConflict(schema.GroupResource{Group: cnpgGroup, Resource: "clusters"}, "pg", errors.New("the object has been modified"))
	})
	_, err := runCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "hibernate", cnpgActionReq(t, cnpgActionFacts(), nil))
	if ae, ok := cnpgActionStatus(t, err); !ok || ae.Code != actionCodeChanged || ae.Current == nil {
		t.Fatalf("err = %v, want 409 changed with current facts", err)
	}
	if calls != 1 {
		t.Errorf("patch attempts = %d, want 1", calls)
	}
}

func TestCNPGActionErrorMapping(t *testing.T) {
	srv := &Server{}
	gr := schema.GroupResource{Group: cnpgGroup, Resource: "clusters"}
	for _, tc := range []struct {
		name   string
		action string
		err    error
		status int
		code   string
		substr []string
	}{
		{"forbidden names the grant", "switchover", apierrors.NewForbidden(gr, "pg", errors.New("denied")), http.StatusForbidden, "",
			[]string{"patch clusters/status (postgresql.cnpg.io) in namespace db", "denied"}},
		{"webhook down", "backup", apierrors.NewInternalError(errors.New(`failed calling webhook "vbackup.cnpg.io": connection refused`)), http.StatusServiceUnavailable, cnpgCodeWebhook,
			[]string{"admission webhook did not answer", "connection refused"}},
		{"invalid", "backup", apierrors.NewInvalid(schema.GroupKind{Group: cnpgGroup, Kind: "Backup"}, "b", nil), http.StatusUnprocessableEntity, "", []string{"is invalid"}},
		{"not found", "restart", apierrors.NewNotFound(gr, "pg"), http.StatusNotFound, "", []string{"not found"}},
		{"refusal", "fence", blockedAction("It is fenced already"), http.StatusConflict, actionCodeBlocked, []string{"fenced already"}},
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
			for _, s := range tc.substr {
				if !strings.Contains(msg, s) {
					t.Errorf("error %q does not contain %q", msg, s)
				}
			}
		})
	}
}

func TestCNPGActionCapabilitiesRestoreNeedsCreateClusters(t *testing.T) {
	env := newCNPGActionEnv(t, []runtime.Object{cnpgActionCluster(nil)},
		cnpgActionPod("pg-1", "u1", true), cnpgActionPod("pg-2", "u2", true))
	for _, allowed := range []bool{false, true} {
		srv := &Server{permCache: auth.NewPermissionCache()}
		perms := &auth.UserPermissions{AllowedNamespaces: []string{"db"}}
		perms.SetCanI("create", cnpgGroup, "clusters", "db", allowed)
		srv.permCache.Set("alice", nil, perms)
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r = r.WithContext(auth.ContextWithUser(r.Context(), &auth.User{Username: "alice"}))
		resp, err := srv.cnpgClusterCapabilities(r, env.clients(), "kind-test", "db", "pg")
		if err != nil {
			t.Fatal(err)
		}
		got := resp.Actions.Restore
		if allowed {
			if !got.Allowed || got.Permission != permissionAllowed {
				t.Errorf("restore with create clusters = %+v, want allowed", got)
			}
			continue
		}
		if got.Allowed || got.Permission != permissionDenied || !strings.Contains(got.Reason, "create clusters (postgresql.cnpg.io) in namespace db") {
			t.Errorf("restore without create clusters = %+v, want denied naming the grant", got)
		}
	}
}

func TestParseCNPGFencedRejectsNonLists(t *testing.T) {
	for raw, malformed := range map[string]bool{"": false, `[]`: false, `["pg-1"]`: false, `null`: true, ` null `: true, `"pg-1"`: true, `{}`: true} {
		if got := parseCNPGFenced(raw).Malformed; got != malformed {
			t.Errorf("parseCNPGFenced(%q).Malformed = %v, want %v", raw, got, malformed)
		}
	}
}

func TestCNPGClusterFactsArchivingFailing(t *testing.T) {
	failing := cnpgActionCluster(func(obj map[string]any) {
		obj["status"].(map[string]any)["conditions"] = []any{map[string]any{"type": "ContinuousArchiving", "status": "False"}}
	})
	if f, _ := cnpgClusterFactsOf(context.Background(), nil, failing); !f.ArchivingFailing {
		t.Error("ContinuousArchiving=False must read as archiving failing")
	}
	if f, _ := cnpgClusterFactsOf(context.Background(), nil, cnpgActionCluster(nil)); f.ArchivingFailing {
		t.Error("ContinuousArchiving=True must not read as archiving failing")
	}
}

func TestCNPGIsReplicaClusterMatchesOperator(t *testing.T) {
	for _, c := range []struct {
		replica map[string]any
		want    bool
	}{
		{nil, false},
		{map[string]any{"enabled": true, "source": "east"}, true},
		{map[string]any{"enabled": false, "primary": "east"}, false},
		{map[string]any{"primary": "east", "source": "east"}, true},
		{map[string]any{"primary": "pg", "source": "east"}, false},
		{map[string]any{"self": "west", "primary": "west"}, false},
		{map[string]any{"source": "east"}, true},
	} {
		cluster := cnpgActionCluster(func(obj map[string]any) {
			if c.replica != nil {
				obj["spec"].(map[string]any)["replica"] = c.replica
			}
		})
		if got := cnpgIsReplicaCluster(cluster); got != c.want {
			t.Errorf("replica %v: got %v, want %v", c.replica, got, c.want)
		}
	}
}
