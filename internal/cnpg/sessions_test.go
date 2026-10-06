package cnpg

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/auth"
	integration "github.com/skyhook-io/radar/internal/integration"
)

type cnpgExecCall struct {
	pod, container string
	argv           []string
	stdin          string
}

func cnpgFakeExec(calls *[]cnpgExecCall, out string, err error) ExecFunc {
	return func(_ context.Context, _ string, pod, container string, argv []string, stdin string) ([]byte, error) {
		*calls = append(*calls, cnpgExecCall{pod: pod, container: container, argv: argv, stdin: stdin})
		return []byte(out), err
	}
}

const cnpgSessionsSample = `{"serverTime" : "2026-09-29T21:28:14.277052+00:00", "maxConnections" : 100, "superuserReservedConnections" : 3, "clientBackends" : 2, "involvedTotal" : 3, "sessions" : [{"pid":606,"blockedBy":[],"backendStart":"2026-09-29T16:26:53.564513+00:00","state":"idle in transaction","waitEventType":"Client","waitEvent":"ClientRead","user":"app","database":"app","application":"holder","clientAddr":"10.244.0.57","backendType":"client backend","backendAgeSeconds":18080.7,"xactAgeSeconds":18080.6,"queryAgeSeconds":80.3,"stateAgeSeconds":80.3,"query":"SELECT 1;","queryTruncated":false}, {"pid":607,"blockedBy":[606],"backendStart":"2026-09-29T16:26:53.656271+00:00","state":"active","waitEventType":"Lock","waitEvent":"transactionid","user":"app","database":"app","application":"waiter","clientAddr":null,"backendType":"client backend","backendAgeSeconds":18080.6,"xactAgeSeconds":18080.6,"queryAgeSeconds":18080.6,"stateAgeSeconds":18080.6,"query":"UPDATE t SET v = v + 1","queryTruncated":true}]}
`

func TestParseCNPGSessions(t *testing.T) {
	f, err := parseCNPGSessions([]byte(cnpgSessionsSample))
	if err != nil {
		t.Fatal(err)
	}
	if f.MaxConnections != 100 || f.ClientBackends != 2 || len(f.Sessions) != 2 {
		t.Fatalf("facts = %+v", f)
	}
	if !f.Truncated {
		t.Error("involvedTotal 3 with 2 rows must read as truncated")
	}
	waiter := f.Sessions[1]
	if waiter.PID != 607 || len(waiter.BlockedBy) != 1 || waiter.BlockedBy[0] != 606 || waiter.WaitEventType != "Lock" || !waiter.QueryTruncated {
		t.Errorf("waiter = %+v", waiter)
	}
	if f.Sessions[0].BlockedBy == nil {
		t.Error("blockedBy must be an empty list, not null")
	}
	if _, err := parseCNPGSessions([]byte("ERROR: boom")); err == nil {
		t.Error("non-JSON output must be an error")
	}
}

func TestReadCNPGSessionsRunsFixedSQLAndClassifiesFailures(t *testing.T) {
	var calls []cnpgExecCall
	src, facts := readCNPGSessions(context.Background(), cnpgFakeExec(&calls, cnpgSessionsSample, nil), "db", "pg-1")
	if src.State != runtimeStateOK || facts == nil {
		t.Fatalf("state = %+v", src)
	}
	c := calls[0]
	if c.container != "postgres" || c.stdin != cnpgBlockingSQL || strings.Join(c.argv, " ") != "psql -XAtq -v ON_ERROR_STOP=1 -d postgres -f -" {
		t.Errorf("exec = %+v", c)
	}
	if !strings.Contains(cnpgBlockingSQL, "pg_blocking_pids") || !strings.Contains(cnpgBlockingSQL, "pg_backend_pid()") || !strings.Contains(cnpgBlockingSQL, "statement_timeout") {
		t.Error("blocking SQL must use pg_blocking_pids, exclude its own backend and bound its runtime")
	}

	src, facts = readCNPGSessions(context.Background(), cnpgFakeExec(&calls, "", errors.New(`pods "pg-1" is forbidden: User "bob" cannot create resource "pods/exec"`)), "db", "pg-1")
	if src.State != runtimeStateDenied || facts != nil {
		t.Errorf("forbidden exec = %+v", src)
	}
	src, _ = readCNPGSessions(context.Background(), cnpgFakeExec(&calls, "", context.DeadlineExceeded), "db", "pg-1")
	if src.State != runtimeStateUnreachable {
		t.Errorf("timeout = %+v", src)
	}
}

func TestCNPGCappedBuffer(t *testing.T) {
	b := &cappedBuffer{limit: 4}
	if n, err := b.Write([]byte("abcdef")); n != 6 || err != nil {
		t.Fatalf("write = %d, %v", n, err)
	}
	if string(b.buf) != "abcd" || !b.overflow {
		t.Errorf("buf = %q overflow = %v", b.buf, b.overflow)
	}
}

func cnpgSignalParamsFor(podUID string) map[string]any {
	return map[string]any{"pod": "pg-1", "podUID": podUID, "pid": 607, "backendStart": "2026-09-29T16:26:53.656271+00:00"}
}

func TestCNPGActionCancelBackendBindsPodPidAndStart(t *testing.T) {
	env := newCNPGActionEnv(t, []runtime.Object{cnpgActionCluster(nil)}, cnpgActionPod("pg-1", "u1", true), cnpgActionPod("pg-2", "u2", true))
	var calls []cnpgExecCall
	c := env.clients()
	c.Exec = cnpgFakeExec(&calls, `{"found" : 1, "signalled" : true}`, nil)

	res, err := RunCNPGClusterAction(context.Background(), c, "db", "pg", "cancelBackend", cnpgActionReq(t, nil, cnpgSignalParamsFor("u1")))
	if err != nil {
		t.Fatal(err)
	}
	if res.Target == nil || res.Target.PID != 607 || res.Target.Pod != "pg-1" || res.Target.BackendStart == "" {
		t.Errorf("target = %+v", res.Target)
	}
	call := calls[0]
	if call.pod != "pg-1" || call.container != "postgres" {
		t.Errorf("exec target = %+v", call)
	}
	// The user's values travel only as psql variables; the SQL text is fixed.
	if call.stdin != cnpgSignalSQL("pg_cancel_backend") || strings.Contains(call.stdin, "607") {
		t.Errorf("stdin = %q", call.stdin)
	}
	argv := strings.Join(call.argv, " ")
	if !strings.Contains(argv, "-v pid=607") || !strings.Contains(argv, "-v backend_start=2026-09-29T16:26:53.656271+00:00") {
		t.Errorf("argv = %q", argv)
	}
	for _, want := range []string{":'pid'::int", ":'backend_start'::timestamptz", "backend_type = 'client backend'"} {
		if !strings.Contains(call.stdin, want) {
			t.Errorf("signal SQL lacks %s", want)
		}
	}

	calls = nil
	c.Exec = cnpgFakeExec(&calls, `{"found" : 1, "signalled" : true}`, nil)
	if _, err := RunCNPGClusterAction(context.Background(), c, "db", "pg", "terminateBackend", cnpgActionReq(t, nil, cnpgSignalParamsFor("u1"))); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(calls[0].stdin, "pg_terminate_backend(pid)") {
		t.Errorf("terminate SQL = %q", calls[0].stdin)
	}
}

func TestCNPGActionCancelBackendRefusals(t *testing.T) {
	env := newCNPGActionEnv(t, []runtime.Object{cnpgActionCluster(nil)}, cnpgActionPod("pg-1", "u1", true))
	var calls []cnpgExecCall
	c := env.clients()

	c.Exec = cnpgFakeExec(&calls, `{"found" : 0, "signalled" : false}`, nil)
	_, err := RunCNPGClusterAction(context.Background(), c, "db", "pg", "cancelBackend", cnpgActionReq(t, nil, cnpgSignalParamsFor("u1")))
	if ae, ok := cnpgActionStatus(t, err); !ok || ae.Status != http.StatusConflict || ae.Code != integration.ActionCodeChanged {
		t.Errorf("gone backend = %v, want 409 changed", err)
	}

	calls = nil
	_, err = RunCNPGClusterAction(context.Background(), c, "db", "pg", "cancelBackend", cnpgActionReq(t, nil, cnpgSignalParamsFor("old-uid")))
	if ae, ok := cnpgActionStatus(t, err); !ok || ae.Status != http.StatusConflict || len(calls) != 0 {
		t.Errorf("recreated Pod = %v (calls %d), want 409 before any exec", err, len(calls))
	}

	bad := cnpgSignalParamsFor("u1")
	bad["backendStart"] = "yesterday'; DROP TABLE x; --"
	_, err = RunCNPGClusterAction(context.Background(), c, "db", "pg", "cancelBackend", cnpgActionReq(t, nil, bad))
	if ae, ok := cnpgActionStatus(t, err); !ok || ae.Status != http.StatusBadRequest || len(calls) != 0 {
		t.Errorf("malformed backendStart = %v, want 400 before any exec", err)
	}
}

func cnpgDestroyTestPVC(name, uid, instance string, owned bool, mut func(*corev1.PersistentVolumeClaim)) *corev1.PersistentVolumeClaim {
	p := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "db", UID: types.UID(uid), ResourceVersion: "7",
			Labels:      map[string]string{instanceNameLabel: instance, cnpgPVCRoleLabel: "PG_DATA", "cnpg.io/cluster": "pg"},
			Annotations: map[string]string{cnpgPVCStatusAnnotation: "ready"},
		},
		Status: corev1.PersistentVolumeClaimStatus{Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}},
	}
	if owned {
		controller := true
		p.OwnerReferences = []metav1.OwnerReference{{APIVersion: "postgresql.cnpg.io/v1", Kind: "Cluster", Name: "pg", UID: cnpgActionTestUID, Controller: &controller}}
	}
	if mut != nil {
		mut(p)
	}
	return p
}

// cnpgDestroyCluster is the default fixture with pg-2 fenced, as destroy requires.
func cnpgDestroyCluster(mut func(obj map[string]any)) *unstructured.Unstructured {
	return cnpgActionCluster(func(o map[string]any) {
		o["metadata"].(map[string]any)["annotations"] = map[string]any{cnpgFencedAnnotation: `["pg-2"]`}
		if mut != nil {
			mut(o)
		}
	})
}

func cnpgDestroyFacts() map[string]any {
	f := cnpgActionFacts()
	f["fencedInstances"] = map[string]any{"raw": `["pg-2"]`, "all": false, "instances": []any{"pg-2"}}
	return f
}

func cnpgDestroyEnv(t *testing.T) *cnpgActionEnv {
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "pg-2-join", Namespace: "db", Labels: map[string]string{instanceNameLabel: "pg-2"}}}
	return newCNPGActionEnv(t, []runtime.Object{cnpgDestroyCluster(nil)},
		cnpgActionPod("pg-1", "u1", true), cnpgActionPod("pg-2", "u2", true),
		cnpgDestroyTestPVC("pg-2", "pvc-2", "pg-2", true, nil),
		cnpgDestroyTestPVC("pg-2-wal", "pvc-2w", "pg-2", true, func(p *corev1.PersistentVolumeClaim) { p.Labels[cnpgPVCRoleLabel] = "PG_WAL" }),
		cnpgDestroyTestPVC("pg-2-foreign", "pvc-x", "pg-2", false, nil),
		cnpgDestroyTestPVC("pg-1", "pvc-1", "pg-1", true, nil),
		job)
}

func cnpgDestroyParamsFor(keep bool, pvcs ...string) map[string]any {
	list := []any{}
	for _, p := range pvcs {
		name, uid, _ := strings.Cut(p, "=")
		list = append(list, map[string]any{"name": name, "uid": uid})
	}
	return map[string]any{"pod": "pg-2", "podUID": "u2", "keepPVC": keep, "pvcs": list}
}

func TestCNPGActionDestroyInstanceDeletesLikeKubectlCNPG(t *testing.T) {
	env := cnpgDestroyEnv(t)
	var order []string
	env.typed.PrependReactor("delete", "*", func(a k8stesting.Action) (bool, runtime.Object, error) {
		d := a.(k8stesting.DeleteAction)
		order = append(order, a.GetResource().Resource+"/"+d.GetName())
		return false, nil, nil
	})
	res, err := RunCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "destroyInstance",
		cnpgActionReq(t, cnpgDestroyFacts(), cnpgDestroyParamsFor(false, "pg-2=pvc-2", "pg-2-wal=pvc-2w")))
	if err != nil {
		t.Fatal(err)
	}
	// PVCs first so the operator never sees a dangling PVC, then the Pod, then Jobs.
	want := "persistentvolumeclaims/pg-2,persistentvolumeclaims/pg-2-wal,pods/pg-2,jobs/pg-2-join"
	if strings.Join(order, ",") != want {
		t.Errorf("order = %v, want %s", order, want)
	}
	if res.Target == nil || res.Target.Pod != "pg-2" || len(res.Target.PVCs) != 2 || len(res.Target.Jobs) != 1 || *res.Target.KeepPVC {
		t.Errorf("target = %+v", res.Target)
	}
	for _, d := range env.deletes {
		if d.GetName() == "pg-2" && (d.GetDeleteOptions().Preconditions == nil || *d.GetDeleteOptions().Preconditions.UID != "u2") {
			t.Errorf("pod delete lacks the UID precondition: %+v", d.GetDeleteOptions())
		}
	}
}

func TestCNPGActionDestroyInstanceKeepPVCDetaches(t *testing.T) {
	env := cnpgDestroyEnv(t)
	_, err := RunCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "destroyInstance",
		cnpgActionReq(t, cnpgDestroyFacts(), cnpgDestroyParamsFor(true, "pg-2=pvc-2", "pg-2-wal=pvc-2w")))
	if err != nil {
		t.Fatal(err)
	}
	pvc, err := env.typed.CoreV1().PersistentVolumeClaims("db").Get(context.Background(), "pg-2", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("kept PVC was deleted: %v", err)
	}
	if len(pvc.OwnerReferences) != 0 || pvc.Annotations[cnpgPVCStatusAnnotation] != "detached" || pvc.Labels[instanceNameLabel] != "pg-2" {
		t.Errorf("kept PVC = owners %v annotations %v labels %v", pvc.OwnerReferences, pvc.Annotations, pvc.Labels)
	}
	if len(env.deletes) != 1 || env.deletes[0].GetName() != "pg-2" {
		t.Errorf("deletes = %v, want only the Pod", env.deletes)
	}
}

func TestCNPGActionDestroyInstanceRefusals(t *testing.T) {
	env := cnpgDestroyEnv(t)
	primary := cnpgDestroyParamsFor(false, "pg-1=pvc-1")
	primary["pod"], primary["podUID"] = "pg-1", "u1"
	_, err := RunCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "destroyInstance", cnpgActionReq(t, cnpgDestroyFacts(), primary))
	if ae, ok := cnpgActionStatus(t, err); !ok || ae.Code != integration.ActionCodeBlocked || !strings.Contains(ae.Message, "primary") {
		t.Errorf("primary = %v, want blocked", err)
	}

	_, err = RunCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "destroyInstance",
		cnpgActionReq(t, cnpgDestroyFacts(), cnpgDestroyParamsFor(false, "pg-2=pvc-2")))
	if ae, ok := cnpgActionStatus(t, err); !ok || ae.Code != integration.ActionCodeChanged {
		t.Errorf("unreviewed WAL volume = %v, want 409 changed", err)
	}

	noPVCs := cnpgDestroyParamsFor(false)
	delete(noPVCs, "pvcs")
	_, err = RunCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "destroyInstance", cnpgActionReq(t, cnpgDestroyFacts(), noPVCs))
	if ae, ok := cnpgActionStatus(t, err); !ok || ae.Status != http.StatusBadRequest {
		t.Errorf("missing pvcs = %v, want 400", err)
	}
	if len(env.deletes) != 0 {
		t.Errorf("a refusal deleted %v", env.deletes)
	}
}

func TestCNPGActionDestroyInstanceStopsWhenPromotedMidway(t *testing.T) {
	env := cnpgDestroyEnv(t)
	promoted := false
	env.typed.PrependReactor("delete", "persistentvolumeclaims", func(k8stesting.Action) (bool, runtime.Object, error) {
		if !promoted {
			promoted = true
			obj := cnpgDestroyCluster(func(o map[string]any) {
				st := o["status"].(map[string]any)
				st["targetPrimary"], st["phase"] = "pg-2", cnpgPhaseFailover
			})
			if err := env.dyn.Tracker().Update(ClusterGVR, obj, "db"); err != nil {
				t.Fatal(err)
			}
		}
		return false, nil, nil
	})
	_, err := RunCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "destroyInstance",
		cnpgActionReq(t, cnpgDestroyFacts(), cnpgDestroyParamsFor(false, "pg-2=pvc-2", "pg-2-wal=pvc-2w")))
	ae, ok := cnpgActionStatus(t, err)
	if !ok || ae.Code != integration.ActionCodePartial || ae.Status != http.StatusConflict || strings.Join(ae.Completed, ",") != "deleted PVC pg-2" {
		t.Fatalf("promotion mid-destroy = %+v, want 409 partial after the first PVC only", err)
	}
	if _, err := env.typed.CoreV1().PersistentVolumeClaims("db").Get(context.Background(), "pg-2-wal", metav1.GetOptions{}); err != nil {
		t.Errorf("the WAL volume of a now-target instance was deleted: %v", err)
	}
	if len(env.deletes) != 0 {
		t.Errorf("the Pod of a now-target instance was deleted: %v", env.deletes)
	}
}

func TestCNPGActionDestroyInstanceRefusesPodLabelledPrimary(t *testing.T) {
	env := cnpgDestroyEnv(t)
	pod := cnpgActionPod("pg-2", "u2", true)
	pod.Labels["cnpg.io/instanceRole"] = "primary"
	if _, err := env.typed.CoreV1().Pods("db").Update(context.Background(), pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	_, err := RunCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "destroyInstance",
		cnpgActionReq(t, cnpgDestroyFacts(), cnpgDestroyParamsFor(false, "pg-2=pvc-2", "pg-2-wal=pvc-2w")))
	if ae, ok := cnpgActionStatus(t, err); !ok || ae.Code != integration.ActionCodeChanged || len(ae.Completed) != 0 {
		t.Fatalf("pod labelled primary = %v, want 409 changed before any write", err)
	}
	if _, err := env.typed.CoreV1().PersistentVolumeClaims("db").Get(context.Background(), "pg-2", metav1.GetOptions{}); err != nil {
		t.Errorf("PVC deleted despite the refusal: %v", err)
	}
}

func TestCNPGActionDestroyInstanceRequiresFence(t *testing.T) {
	env := cnpgDestroyEnv(t)
	if err := env.dyn.Tracker().Update(ClusterGVR, cnpgActionCluster(nil), "db"); err != nil {
		t.Fatal(err)
	}
	_, err := RunCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "destroyInstance",
		cnpgActionReq(t, cnpgActionFacts(), cnpgDestroyParamsFor(false, "pg-2=pvc-2", "pg-2-wal=pvc-2w")))
	if ae, ok := cnpgActionStatus(t, err); !ok || ae.Code != integration.ActionCodeBlocked || !strings.HasPrefix(ae.Message, "Fence pg-2 first") {
		t.Fatalf("unfenced destroy = %v, want blocked asking to fence first", err)
	}
	if _, err := env.typed.CoreV1().PersistentVolumeClaims("db").Get(context.Background(), "pg-2", metav1.GetOptions{}); err != nil || len(env.deletes) != 0 {
		t.Errorf("an unfenced destroy wrote: PVC %v, deletes %v", err, env.deletes)
	}
}

// A failover can only make pg-2 primary once its fence is gone, so the fence
// is re-checked before every step like the primary itself.
func TestCNPGActionDestroyInstanceStopsWhenUnfencedMidway(t *testing.T) {
	env := cnpgDestroyEnv(t)
	promoted := false
	env.typed.PrependReactor("delete", "persistentvolumeclaims", func(k8stesting.Action) (bool, runtime.Object, error) {
		if !promoted {
			promoted = true
			if err := env.dyn.Tracker().Update(ClusterGVR, cnpgActionCluster(nil), "db"); err != nil {
				t.Fatal(err)
			}
		}
		return false, nil, nil
	})
	_, err := RunCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "destroyInstance",
		cnpgActionReq(t, cnpgDestroyFacts(), cnpgDestroyParamsFor(false, "pg-2=pvc-2", "pg-2-wal=pvc-2w")))
	if ae, ok := cnpgActionStatus(t, err); !ok || ae.Code != integration.ActionCodePartial || strings.Join(ae.Completed, ",") != "deleted PVC pg-2" {
		t.Fatalf("fence lifted mid-destroy = %v, want partial after the first PVC", err)
	}
	if len(env.deletes) != 0 {
		t.Errorf("the Pod of an unfenced instance was deleted: %v", env.deletes)
	}
}

func TestCNPGActionDestroyInstanceLiftsItsFenceLast(t *testing.T) {
	env := cnpgDestroyEnv(t)
	res, err := RunCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "destroyInstance",
		cnpgActionReq(t, cnpgDestroyFacts(), cnpgDestroyParamsFor(false, "pg-2=pvc-2", "pg-2-wal=pvc-2w")))
	if err != nil {
		t.Fatal(err)
	}
	if len(env.patches) != 1 {
		t.Fatalf("patches = %d, want the fence lift only", len(env.patches))
	}
	body := cnpgActionPatchBody(t, env.patches[0])
	if v, ok := cnpgActionAnnotations(t, body)[cnpgFencedAnnotation]; !ok || v != nil {
		t.Errorf("fence after destroy = %v (present %v), want removed", v, ok)
	}
	if body["metadata"].(map[string]any)["resourceVersion"] == nil {
		t.Error("fence lift lacks resourceVersion")
	}
	if strings.Contains(res.Message, `["*"]`) {
		t.Errorf("message = %q", res.Message)
	}

	all := cnpgDestroyEnv(t)
	star := cnpgDestroyCluster(func(o map[string]any) {
		o["metadata"].(map[string]any)["annotations"] = map[string]any{cnpgFencedAnnotation: `["*"]`}
	})
	if err := all.dyn.Tracker().Update(ClusterGVR, star, "db"); err != nil {
		t.Fatal(err)
	}
	facts := cnpgDestroyFacts()
	facts["fencedInstances"] = map[string]any{"raw": `["*"]`, "all": true, "instances": []any{"*"}}
	res, err = RunCNPGClusterAction(context.Background(), all.clients(), "db", "pg", "destroyInstance",
		cnpgActionReq(t, facts, cnpgDestroyParamsFor(false, "pg-2=pvc-2", "pg-2-wal=pvc-2w")))
	if err != nil || len(all.patches) != 0 || !strings.Contains(res.Message, `["*"]`) {
		t.Fatalf(`["*"] fence: err %v, patches %d, message %v`, err, len(all.patches), res)
	}
}

func TestCNPGActionDestroyInstanceFailedFenceLiftIsPartial(t *testing.T) {
	env := cnpgDestroyEnv(t)
	env.dyn.PrependReactor("patch", "clusters", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(schema.GroupResource{Group: Group, Resource: "clusters"}, "pg", errors.New("modified"))
	})
	_, err := RunCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "destroyInstance",
		cnpgActionReq(t, cnpgDestroyFacts(), cnpgDestroyParamsFor(false, "pg-2=pvc-2", "pg-2-wal=pvc-2w")))
	ae, ok := cnpgActionStatus(t, err)
	if !ok || ae.Code != integration.ActionCodePartial || !strings.Contains(ae.Message, "Unfence") || !slices.Contains(ae.Completed, "deleted Pod pg-2") {
		t.Fatalf("failed fence lift = %v, want partial naming Unfence", err)
	}
}

func TestCNPGDestroyPlanAndCapabilities(t *testing.T) {
	env := cnpgDestroyEnv(t)
	srv := newTestReader(nil)
	ctx := context.Background()
	plan, err := srv.DestroyPlan(ctx, env.clients(), "kind-test", "db", "pg", "pg-2")
	if err != nil {
		t.Fatal(err)
	}
	if !plan.PVCsReadable || len(plan.PVCs) != 2 || plan.PVCs[0].Name != "pg-2" || plan.PVCs[1].Role != "PG_WAL" || plan.PVCs[0].Capacity != "1Gi" {
		t.Errorf("pvcs = %+v", plan.PVCs)
	}
	if len(plan.Jobs) != 1 || plan.PodUID != "u2" || !plan.Actions.Delete.Allowed || !plan.Actions.Keep.Allowed {
		t.Errorf("plan = %+v", plan)
	}

	caps, err := srv.ClusterCapabilities(ctx, env.clients(), "kind-test", "db", "pg")
	if err != nil {
		t.Fatal(err)
	}
	if caps.InstanceActions["pg-1"].Destroy.Allowed || !caps.InstanceActions["pg-2"].Destroy.Allowed || !caps.Actions.DestroyInstance.Allowed {
		t.Errorf("destroy verdicts = %+v / %+v", caps.InstanceActions, caps.Actions.DestroyInstance)
	}
	if got := caps.InstanceActions["pg-3"].Destroy; got.Allowed || got.ReasonCode != "fence_required" || got.Reason != "Fence pg-3 first: a fenced instance cannot be promoted while it is destroyed" {
		t.Errorf("unfenced destroy verdict = %+v", got)
	}
	unfenced, err := srv.DestroyPlan(ctx, env.clients(), "kind-test", "db", "pg", "pg-3")
	if err != nil {
		t.Fatal(err)
	}
	if unfenced.Actions.Delete.Allowed || unfenced.Actions.Delete.ReasonCode != "fence_required" || unfenced.Actions.Keep.ReasonCode != "fence_required" {
		t.Errorf("unfenced destroy plan = %+v", unfenced.Actions)
	}
	// pg-2 is fenced here, so PostgreSQL is stopped on it.
	if !caps.Actions.Psql.Allowed || caps.InstanceActions["pg-2"].Psql.Allowed {
		t.Errorf("psql = %+v / %+v", caps.Actions.Psql, caps.InstanceActions["pg-2"].Psql)
	}
}

func TestCNPGCapabilitiesPsqlNamesExecGrant(t *testing.T) {
	env := newCNPGActionEnv(t, []runtime.Object{cnpgActionCluster(nil)}, cnpgActionPod("pg-1", "u1", true))
	srv := newTestReader(nil)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"db"}}
	perms.SetCanI("create", "", "pods/exec", "db", false)
	srv = newTestReader(perms)
	ctx := context.Background()
	caps, err := srv.ClusterCapabilities(ctx, env.clients(), "kind-test", "db", "pg")
	if err != nil {
		t.Fatal(err)
	}
	if p := caps.Actions.Psql; p.Allowed || !strings.Contains(p.Reason, "create pods/exec") {
		t.Errorf("psql = %+v, want denied naming create pods/exec", p)
	}
}

func cnpgTestPooler(paused bool) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1", "kind": "Pooler",
		"metadata": map[string]any{"name": "pool", "namespace": "db", "uid": "pooler-uid", "resourceVersion": "9", "generation": int64(2)},
		"spec": map[string]any{
			"cluster": map[string]any{"name": "pg"}, "type": "rw", "instances": int64(2),
			"pgbouncer": map[string]any{"poolMode": "transaction", "paused": paused, "parameters": map[string]any{"default_pool_size": "10"}},
		},
	}}
}

func TestCNPGPoolerPauseBindsReviewedState(t *testing.T) {
	controller := true
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "pool", Namespace: "db", OwnerReferences: []metav1.OwnerReference{{APIVersion: "postgresql.cnpg.io/v1", Kind: "Pooler", Name: "pool", UID: "pooler-uid", Controller: &controller}}},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 1, UpdatedReplicas: 2, AvailableReplicas: 1},
	}
	env := newCNPGActionEnv(t, []runtime.Object{cnpgTestPooler(false)}, deploy)
	req := integration.ActionRequest{ReviewedContext: "kind-test", UID: "pooler-uid", Facts: []byte(`{"paused":false}`)}

	res, err := RunCNPGPoolerAction(context.Background(), env.clients(), "db", "pool", "pause", req)
	if err != nil {
		t.Fatal(err)
	}
	body := cnpgActionPatchBody(t, env.patches[0])
	spec := body["spec"].(map[string]any)["pgbouncer"].(map[string]any)
	if spec["paused"] != true || body["metadata"].(map[string]any)["resourceVersion"] != "9" {
		t.Errorf("patch = %v", body)
	}
	if res.Target == nil || res.Target.Paused == nil || !*res.Target.Paused {
		t.Errorf("target = %+v", res.Target)
	}

	_, err = RunCNPGPoolerAction(context.Background(), env.clients(), "db", "pool", "resume", integration.ActionRequest{ReviewedContext: "kind-test", UID: "pooler-uid", Facts: []byte(`{"paused":true}`)})
	if ae, ok := cnpgActionStatus(t, err); !ok || ae.Code != integration.ActionCodeChanged {
		t.Errorf("stale paused = %v, want 409 changed", err)
	}
	_, err = RunCNPGPoolerAction(context.Background(), env.clients(), "db", "pool", "pause", integration.ActionRequest{ReviewedContext: "kind-test", UID: "pooler-uid"})
	if ae, ok := cnpgActionStatus(t, err); !ok || ae.Status != http.StatusBadRequest {
		t.Errorf("missing facts = %v, want 400", err)
	}

	srv := newTestReader(nil)
	caps, err := srv.PoolerCapabilities(context.Background(), env.clients(), "kind-test", "db", "pool")
	if err != nil {
		t.Fatal(err)
	}
	d := caps.Facts.Deployment
	if d.State != "ok" || *d.ReadyReplicas != 1 || caps.Facts.Service.State != "missing" || caps.Facts.Parameters["default_pool_size"] != "10" {
		t.Errorf("facts = %+v", caps.Facts)
	}
	if !caps.Actions.Pause.Allowed || caps.Actions.Resume.Allowed {
		t.Errorf("actions = %+v", caps.Actions)
	}
}

func TestCNPGPoolerFactsReportPoolMode(t *testing.T) {
	samples := map[string][]cnpgSample{
		"cnpg_pgbouncer_pools_pool_mode": {
			{labels: map[string]string{"database": "app", "user": "app"}, value: 2},
			{labels: map[string]string{"database": "pgbouncer", "user": "pgbouncer"}, value: 3},
		},
	}
	facts, _ := cnpgPoolerFacts(samples)
	if len(facts.Pools) != 1 || facts.Pools[0].PoolMode != "transaction" {
		t.Errorf("pools = %+v, want app/app in transaction mode and the admin pool excluded", facts.Pools)
	}
}

func TestParseCNPGShowState(t *testing.T) {
	st, err := parseCNPGShowState([]byte("active|yes\npaused|no\nsuspended|no\n"))
	if err != nil || st.Paused == nil || *st.Paused || st.Active == nil || !*st.Active {
		t.Fatalf("state = %+v, %v", st, err)
	}
	if _, err := parseCNPGShowState([]byte("ERROR: invalid command")); err == nil {
		t.Error("output without paused must be an error")
	}
}

func TestCNPGActionDestroyInstanceRefusesWithoutEveryGrantBeforeAnyWrite(t *testing.T) {
	env := cnpgDestroyEnv(t)
	env.denied = map[string]bool{"patch clusters": true}
	_, err := RunCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "destroyInstance",
		cnpgActionReq(t, cnpgDestroyFacts(), cnpgDestroyParamsFor(false, "pg-2=pvc-2", "pg-2-wal=pvc-2w")))
	var ae *integration.ActionError
	if !errors.As(err, &ae) || ae.Status != http.StatusForbidden || ae.Code == integration.ActionCodePartial {
		t.Fatalf("err = %v, want a plain 403 naming patch clusters", err)
	}
	if !strings.Contains(ae.Message, "patch clusters") {
		t.Errorf("message = %q", ae.Message)
	}
	if len(env.deletes) != 0 || len(env.patches) != 0 {
		t.Errorf("wrote before the preflight refused: %d deletes, %d patches", len(env.deletes), len(env.patches))
	}
}

func TestCNPGActionDestroyInstanceLeavesARecreatedClustersFences(t *testing.T) {
	env := cnpgDestroyEnv(t)
	env.typed.PrependReactor("delete", "jobs", func(a k8stesting.Action) (bool, runtime.Object, error) {
		fresh := cnpgDestroyCluster(nil)
		fresh.SetUID("recreated")
		fresh.SetResourceVersion("100")
		if err := env.dyn.Tracker().Update(ClusterGVR, fresh, "db"); err != nil {
			t.Fatal(err)
		}
		return false, nil, nil
	})
	_, err := RunCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "destroyInstance",
		cnpgActionReq(t, cnpgDestroyFacts(), cnpgDestroyParamsFor(false, "pg-2=pvc-2", "pg-2-wal=pvc-2w")))
	var ae *integration.ActionError
	if !errors.As(err, &ae) || ae.Code != integration.ActionCodePartial {
		t.Fatalf("err = %v, want partial", err)
	}
	for _, p := range env.patches {
		if p.GetResource().Resource == "clusters" {
			t.Errorf("patched the recreated Cluster: %s", p.GetPatch())
		}
	}
}

func TestCNPGActionDestroyInstanceReportsPartialOutcome(t *testing.T) {
	env := cnpgDestroyEnv(t)
	env.typed.PrependReactor("delete", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "pg-2", errors.New("denied by admission policy"))
	})
	_, err := RunCNPGClusterAction(context.Background(), env.clients(), "db", "pg", "destroyInstance",
		cnpgActionReq(t, cnpgDestroyFacts(), cnpgDestroyParamsFor(false, "pg-2=pvc-2", "pg-2-wal=pvc-2w")))
	ae, ok := cnpgActionStatus(t, err)
	if !ok || ae.Code != integration.ActionCodePartial || ae.Status != http.StatusForbidden ||
		strings.Join(ae.Completed, ",") != "deleted PVC pg-2,deleted PVC pg-2-wal" {
		t.Fatalf("pod delete refused after PVCs = %+v, want 403 partial listing both PVCs", err)
	}

}

func TestCNPGPoolerStatePending(t *testing.T) {
	pending := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pooler-1"}, Status: corev1.PodStatus{Phase: corev1.PodPending, Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse}}}}
	for _, tc := range []struct {
		name, output  string
		err           error
		state, reason string
	}{
		{"pending transport", "", errors.New("address not allowed"), runtimeStateUnreachable, "PgBouncer has not started (Pod cannot be scheduled)"},
		{"permission wins", "", apierrors.NewForbidden(schema.GroupResource{Resource: "pods/exec"}, "pooler-1", errors.New("denied")), execStateDenied, "denied"},
		{"successful read wins", "paused|no\n", nil, runtimeStateOK, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []cnpgExecCall
			got := readCNPGPgBouncerState(context.Background(), cnpgFakeExec(&calls, tc.output, tc.err), "db", pending)
			if got.State != tc.state || !strings.Contains(got.Error, tc.reason) || got.Pod != "pooler-1" {
				t.Fatalf("%+v", got)
			}
			if tc.state == runtimeStateOK && (got.Paused == nil || *got.Paused) {
				t.Fatalf("lost SHOW STATE: %+v", got)
			}
		})
	}
	running := pending.DeepCopy()
	running.Status.Phase = corev1.PodRunning
	var calls []cnpgExecCall
	got := readCNPGPgBouncerState(context.Background(), cnpgFakeExec(&calls, "", errors.New("address not allowed")), "db", running)
	if !strings.Contains(got.Error, "address not allowed") {
		t.Fatalf("lost running Pod transport details: %+v", got)
	}
}
