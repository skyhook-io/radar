package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func cnpgRestoredCluster() *unstructured.Unstructured {
	return cnpgActionCluster(func(obj map[string]any) {
		spec := obj["spec"].(map[string]any)
		delete(spec, "plugins")
		delete(spec, "backup")
		spec["instances"] = int64(1)
		spec["bootstrap"] = map[string]any{"recovery": map[string]any{
			"source":         "origin",
			"recoveryTarget": map[string]any{"targetTime": "2026-09-29T08:00:00Z"},
		}}
		spec["externalClusters"] = []any{map[string]any{"name": "origin", "plugin": map[string]any{
			"name": "barman-cloud.cloudnative-pg.io", "parameters": map[string]any{"barmanObjectName": "store", "serverName": "pg-src"},
		}}}
		status := obj["status"].(map[string]any)
		status["phase"] = "Setting up primary"
		status["instances"] = int64(1)
		status["readyInstances"] = int64(0)
	})
}

func controllerRef(kind, name, uid string) []metav1.OwnerReference {
	yes := true
	apiVersion := "postgresql.cnpg.io/v1"
	if kind == "Job" {
		apiVersion = "batch/v1"
	}
	return []metav1.OwnerReference{{APIVersion: apiVersion, Kind: kind, Name: name, UID: types.UID(uid), Controller: &yes}}
}

func TestCNPGRecoverySpecOf(t *testing.T) {
	r := cnpgRecoverySpecOf(cnpgRestoredCluster())
	if r == nil || r.SourceKind != "objectStore" || r.ObjectStore != "store" || r.ServerName != "pg-src" || r.Target["targetTime"] != "2026-09-29T08:00:00Z" {
		t.Fatalf("unexpected recovery spec: %+v", r)
	}
	backup := cnpgActionCluster(func(obj map[string]any) {
		obj["spec"].(map[string]any)["bootstrap"] = map[string]any{"recovery": map[string]any{"backup": map[string]any{"name": "b1"}}}
	})
	if r := cnpgRecoverySpecOf(backup); r == nil || r.SourceKind != "backup" || r.Backup != "b1" {
		t.Fatalf("backup recovery: %+v", r)
	}
	if cnpgRecoverySpecOf(cnpgActionCluster(nil)) != nil {
		t.Fatal("a Cluster without bootstrap.recovery is not a restore")
	}
}

func TestCNPGRecoverySnapshotClassifiesPodsByOwner(t *testing.T) {
	cluster := cnpgRestoredCluster()
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "pg-1-full-recovery", Namespace: "db", Labels: map[string]string{"cnpg.io/cluster": "pg"}, OwnerReferences: controllerRef("Cluster", "pg", cnpgActionTestUID)},
		Status:     batchv1.JobStatus{Active: 1},
	}
	foreignJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "pg-other", Namespace: "db", Labels: map[string]string{"cnpg.io/cluster": "pg"}, OwnerReferences: controllerRef("Cluster", "pg", "old-uid")},
	}
	recoveryPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pg-1-full-recovery-abcde", Namespace: "db", UID: "p1", Labels: map[string]string{"cnpg.io/cluster": "pg"}, OwnerReferences: controllerRef("Job", "pg-1-full-recovery", "job-uid")},
		Spec:       corev1.PodSpec{InitContainers: []corev1.Container{{Name: "bootstrap-controller"}}, Containers: []corev1.Container{{Name: "full-recovery"}}},
		Status: corev1.PodStatus{
			Phase:                 corev1.PodRunning,
			InitContainerStatuses: []corev1.ContainerStatus{{Name: "bootstrap-controller", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Reason: "Completed"}}}},
			ContainerStatuses:     []corev1.ContainerStatus{{Name: "full-recovery", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}},
		},
	}
	strayPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pg-other-xyz", Namespace: "db", Labels: map[string]string{"cnpg.io/cluster": "pg"}, OwnerReferences: controllerRef("Job", "pg-other", "j2")}}
	events := []runtime.Object{
		&corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "e1", Namespace: "db"}, InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "pg-1-full-recovery-abcde"}, Type: "Warning", Reason: "BackOff", Message: "restarting", LastTimestamp: metav1.NewTime(time.Now())},
		&corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "e2", Namespace: "db"}, InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "unrelated"}, Type: "Warning", Reason: "Other"},
	}
	typed := k8sfake.NewSimpleClientset(append([]runtime.Object{job, foreignJob, recoveryPod, strayPod}, events...)...)
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	snap := s.cnpgRecoverySnapshot(req, typed, cluster)
	if len(snap.Jobs) != 1 || snap.Jobs[0].Name != "pg-1-full-recovery" {
		t.Fatalf("only the Cluster-owned Job counts: %+v", snap.Jobs)
	}
	if len(snap.Pods) != 1 || snap.Pods[0].Kind != "job" || !snap.Pods[0].OwnerVerified {
		t.Fatalf("only the owned Job's Pod counts: %+v", snap.Pods)
	}
	if snap.Pods[0].InitContainers[0].State != "terminated" || snap.Pods[0].Containers[0].State != "running" {
		t.Fatalf("container states: %+v", snap.Pods[0])
	}
	if len(snap.Events) != 1 || snap.Events[0].Reason != "BackOff" {
		t.Fatalf("events should be limited to the recovery objects: %+v", snap.Events)
	}
	if snap.Coverage["pods"].State != cnpgReadOK || snap.Recovery == nil || snap.Cluster.ReadyInstances == nil || *snap.Cluster.ReadyInstances != 0 {
		t.Fatalf("snapshot: %+v", snap)
	}
}

func TestCNPGRecoverySnapshotReportsDeniedReads(t *testing.T) {
	typed := k8sfake.NewSimpleClientset()
	typed.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apiForbidden("pods")
	})
	snap := (&Server{}).cnpgRecoverySnapshot(httptest.NewRequest(http.MethodGet, "/", nil), typed, cnpgRestoredCluster())
	if snap.Coverage["pods"].State != cnpgReadDenied || !strings.Contains(snap.Coverage["pods"].Grant, "list pods") {
		t.Fatalf("a denied Pod list must be reported with its grant: %+v", snap.Coverage["pods"])
	}
	if len(snap.Pods) != 0 {
		t.Fatal("no Pods when they cannot be read")
	}
}

func TestRecordCNPGRestoreValidation(t *testing.T) {
	restored := cnpgRestoredCluster()
	source := cnpgActionCluster(func(obj map[string]any) {
		m := obj["metadata"].(map[string]any)
		m["name"], m["uid"] = "pg-src", "src-uid"
	})
	env := newCNPGActionEnv(t, []runtime.Object{restored, source})
	now := time.Date(2026, 9, 30, 9, 0, 0, 0, time.UTC)
	req := cnpgActionReq(t, nil, map[string]any{"checked": "  row counts match  ", "targetTime": "2026-09-29T08:00:00Z", "source": map[string]any{"name": "pg-src"}})

	note, err := recordCNPGRestoreValidation(context.Background(), env.dyn, "db", "pg", req, "alice", now)
	if err != nil {
		t.Fatal(err)
	}
	if note.Checked != "row counts match" || note.RecordedBy != "alice" || note.RecordedAt != "2026-09-30T09:00:00Z" {
		t.Fatalf("note: %+v", note)
	}
	if note.Source == nil || note.Source.UID != "src-uid" || !note.Source.Verified || note.Target.UID != cnpgActionTestUID {
		t.Fatalf("UIDs must come from the server's reads: %+v", note)
	}
	if len(env.patches) != 1 {
		t.Fatalf("want one patch, got %d", len(env.patches))
	}
	body := cnpgActionPatchBody(t, env.patches[0])
	if body["metadata"].(map[string]any)["resourceVersion"] != "42" {
		t.Fatal("the patch must be bound to the resourceVersion read")
	}
	raw := cnpgActionAnnotations(t, body)[cnpgRestoreValidationAnno].(string)
	var stored CNPGRestoreValidation
	if err := json.Unmarshal([]byte(raw), &stored); err != nil || stored.Checked != "row counts match" {
		t.Fatalf("stored annotation: %s (%v)", raw, err)
	}
}

func TestRecordCNPGRestoreValidationRefusals(t *testing.T) {
	notRestored := cnpgActionCluster(nil)
	env := newCNPGActionEnv(t, []runtime.Object{notRestored})
	now := time.Now()
	if _, err := recordCNPGRestoreValidation(context.Background(), env.dyn, "db", "pg", cnpgActionReq(t, nil, map[string]any{"checked": "x"}), "", now); err == nil {
		t.Fatal("a Cluster that was not restored cannot carry a validation note")
	} else if ae, ok := cnpgActionStatus(t, err); !ok || ae.Code != cnpgCodeBlocked {
		t.Fatalf("want blocked, got %v", err)
	}
	env = newCNPGActionEnv(t, []runtime.Object{cnpgRestoredCluster()})
	for name, params := range map[string]any{
		"empty":         map[string]any{"checked": "   "},
		"bad target":    map[string]any{"checked": "ok", "targetTime": "yesterday"},
		"unknown field": map[string]any{"checked": "ok", "recordedBy": "mallory"},
	} {
		if _, err := recordCNPGRestoreValidation(context.Background(), env.dyn, "db", "pg", cnpgActionReq(t, nil, params), "", now); err == nil {
			t.Fatalf("%s: want an error", name)
		}
	}
	stale := cnpgActionReq(t, nil, map[string]any{"checked": "ok"})
	stale.UID = "other"
	if _, err := recordCNPGRestoreValidation(context.Background(), env.dyn, "db", "pg", stale, "", now); err == nil {
		t.Fatal("a recreated Cluster must be refused")
	}
	if len(env.patches) != 0 {
		t.Fatal("refusals must not write")
	}
}

func TestParseCNPGRestoreValidation(t *testing.T) {
	if v, e := parseCNPGRestoreValidation(""); v != nil || e != "" {
		t.Fatal("absent is not an error")
	}
	if _, e := parseCNPGRestoreValidation("{"); e == "" {
		t.Fatal("malformed must be reported")
	}
	if _, e := parseCNPGRestoreValidation(`{"recordedAt":"2026-09-30T00:00:00Z"}`); e == "" {
		t.Fatal("a note without what was checked is not a note")
	}
}

func TestCNPGReportLogLineRedactsQueryText(t *testing.T) {
	line := `2026-09-30T00:00:00.000000000Z {"level":"info","logger":"postgres","msg":"record","record":{"message":"duration: 1.2 ms  statement: SELECT * FROM users WHERE email='a@b.c'","query":"SELECT 1","detail":"parameters: $1 = 'x'","error_severity":"LOG"}}`
	out := cnpgReportLogLine(line, false)
	if strings.Contains(out, "users") || strings.Contains(out, "SELECT 1") || strings.Contains(out, "$1") {
		t.Fatalf("query text leaked: %s", out)
	}
	if !strings.HasPrefix(out, "2026-09-30T00:00:00.000000000Z ") || !strings.Contains(out, "duration: 1.2 ms  statement: ") {
		t.Fatalf("timestamp and the non-query part must survive: %s", out)
	}
	if kept := cnpgReportLogLine(line, true); !strings.Contains(kept, "SELECT * FROM users") {
		t.Fatalf("opt-in keeps query text: %s", kept)
	}
	if plain := cnpgReportLogLine("connecting to postgresql://app:s3cretpw@db:5432/app", false); strings.Contains(plain, "s3cretpw") {
		t.Fatalf("secret patterns are redacted on every line: %s", plain)
	}
}

func TestCNPGReportSecretNames(t *testing.T) {
	obj := map[string]any{"spec": map[string]any{
		"superuserSecret": map[string]any{"name": "su"},
		"certificates":    map[string]any{"serverTLSSecret": "tls", "serverCASecret": "ca"},
		"bootstrap":       map[string]any{"initdb": map[string]any{"secret": map[string]any{"name": "app"}}},
		"configuration":   map[string]any{"s3Credentials": map[string]any{"accessKeyId": map[string]any{"name": "creds", "key": "ID"}}},
		"monitoring":      map[string]any{"customQueriesConfigMap": []any{map[string]any{"name": "queries", "key": "q.yaml"}}},
		"imageCatalogRef": map[string]any{"name": "catalog", "kind": "ImageCatalog"},
	}}
	got := map[string]bool{}
	cnpgReportSecretNamesIn(obj, "Cluster/pg", func(name, _ string) { got[name] = true })
	for _, want := range []string{"su", "tls", "ca", "app", "creds"} {
		if !got[want] {
			t.Errorf("missing Secret %q in %v", want, got)
		}
	}
	for _, not := range []string{"queries", "catalog", "[REDACTED]"} {
		if got[not] {
			t.Errorf("%q is not a Secret", not)
		}
	}
}

func TestCNPGReportBundle(t *testing.T) {
	cluster := cnpgActionCluster(nil)
	pod := cnpgActionPod("pg-1", "pod-1", true)
	pod.Spec = corev1.PodSpec{
		Containers: []corev1.Container{{Name: "postgres", Env: []corev1.EnvVar{{Name: "DB_PASSWORD", Value: "hunter2"}, {Name: "PGDATA", Value: "/var/lib"}}}},
		Volumes:    []corev1.Volume{{Name: "su", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "pg-superuser"}}}},
	}
	env := newCNPGActionEnv(t, []runtime.Object{cluster}, pod)
	var buf bytes.Buffer
	z := &cnpgReportZip{zw: zip.NewWriter(&buf), root: "r", limit: cnpgReportTotalCap}
	index := CNPGReportIndex{}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	b := &cnpgReportBuilder{s: &Server{}, r: r, ctx: r.Context(), dyn: env.dyn, typed: env.typed, cluster: cluster, z: z, index: &index, secrets: map[string]map[string]bool{}}
	b.build(cnpgReportOptions{})
	if err := z.zw.Close(); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		data, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = string(data)
	}
	for _, want := range []string{"r/manifests/cluster.yaml", "r/manifests/cluster-pods.yaml", "r/manifests/backups.yaml"} {
		if _, ok := files[want]; !ok {
			t.Errorf("missing %s (have %v)", want, keysOf(files))
		}
	}
	if strings.Contains(files["r/manifests/cluster-pods.yaml"], "hunter2") || !strings.Contains(files["r/manifests/cluster-pods.yaml"], "/var/lib") {
		t.Fatal("sensitive env values must be redacted, others kept")
	}
	secrets := b.secretRefs()
	if len(secrets) != 1 || secrets[0].Name != "pg-superuser" {
		t.Fatalf("secret names: %+v", secrets)
	}
	var logs *CNPGReportItem
	for i := range index.Contents {
		if index.Contents[i].Item == "Logs" {
			logs = &index.Contents[i]
		}
	}
	if logs == nil || logs.State != cnpgReadSkipped {
		t.Fatalf("logs are opt-in: %+v", index.Contents)
	}
}

func TestParseCNPGReportOptions(t *testing.T) {
	for q, ok := range map[string]bool{"": true, "logs=true&tailLines=500": true, "queryText=true": false, "logs=true&tailLines=0": false, "logs=true&tailLines=99999": false} {
		_, err := parseCNPGReportOptions(httptest.NewRequest(http.MethodGet, "/?"+q, nil))
		if (err == nil) != ok {
			t.Errorf("%q: err=%v", q, err)
		}
	}
}

func TestCNPGOperatorWatchAndMetricsPort(t *testing.T) {
	c := &corev1.Container{Env: []corev1.EnvVar{{Name: "WATCH_NAMESPACE", Value: "a, b"}}, Ports: []corev1.ContainerPort{{Name: "metrics", ContainerPort: 9999}}}
	w := cnpgOperatorWatchOf(c, "cnpg-system")
	if w.All || len(w.Namespaces) != 2 || w.Namespaces[1] != "b" {
		t.Fatalf("watch: %+v", w)
	}
	if cnpgOperatorMetricsPort(c) != 9999 {
		t.Fatal("the named metrics port wins")
	}
	field := &corev1.Container{Env: []corev1.EnvVar{{Name: "WATCH_NAMESPACE", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}}}}}
	if w := cnpgOperatorWatchOf(field, "cnpg-system"); w.All || w.Namespaces[0] != "cnpg-system" {
		t.Fatalf("fieldRef watch: %+v", w)
	}
	if w := cnpgOperatorWatchOf(&corev1.Container{}, "x"); !w.All {
		t.Fatal("unset watches all namespaces")
	}
	if cnpgOperatorMetricsPort(&corev1.Container{Args: []string{"--metrics-bind-address=:8443"}}) != 8443 {
		t.Fatal("the bind address flag is the fallback")
	}
	if cnpgLeaseHolderPod("cnpg-controller-manager-5fbdd6bb78-jx82z_8f70a311-0f38") != "cnpg-controller-manager-5fbdd6bb78-jx82z" {
		t.Fatal("holder identity is <pod>_<uuid>")
	}
}

func TestCNPGReconcileStatsAndEndpoints(t *testing.T) {
	samples, err := parseCNPGPromSamples([]byte(`# TYPE controller_runtime_reconcile_errors_total counter
controller_runtime_reconcile_errors_total{controller="cluster"} 3
# TYPE controller_runtime_reconcile_total counter
controller_runtime_reconcile_total{controller="cluster",result="success"} 10
controller_runtime_reconcile_total{controller="cluster",result="error"} 3
`))
	if err != nil {
		t.Fatal(err)
	}
	stats := cnpgReconcileStats(samples)
	if len(stats) != 1 || *stats[0].Errors != 3 || *stats[0].Total != 13 || stats[0].Results["error"] != 3 {
		t.Fatalf("stats: %+v", stats)
	}
	no := false
	ready, notReady := cnpgEndpointCounts([]discoveryv1.EndpointSlice{{Endpoints: []discoveryv1.Endpoint{{}, {Conditions: discoveryv1.EndpointConditions{Ready: &no}}}}})
	if ready != 1 || notReady != 1 {
		t.Fatalf("an unset ready condition counts as ready: %d/%d", ready, notReady)
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func apiForbidden(resource string) error {
	return apierrors.NewForbidden(schema.GroupResource{Resource: resource}, "", errors.New("denied"))
}

func TestCNPGReportCleanObjectRedactsDeclaredEnv(t *testing.T) {
	cluster := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"env": []any{
				map[string]any{"name": "AWS_SECRET_ACCESS_KEY", "value": "plaintext"},
				map[string]any{"name": "TZ", "value": "UTC"},
			},
			"backup": map[string]any{"barmanObjectStore": map[string]any{"s3Credentials": map[string]any{"secretAccessKey": map[string]any{"name": "creds", "key": "k"}}}},
		},
	}}
	pooler := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{
			map[string]any{"name": "pgbouncer", "env": []any{map[string]any{"name": "DB_PASSWORD", "value": "hunter2"}}},
		}}}},
	}}
	c := cnpgReportCleanObject(cluster).Object["spec"].(map[string]any)
	env := c["env"].([]any)
	if env[0].(map[string]any)["value"] != cnpgReportRedacted || env[1].(map[string]any)["value"] != "UTC" {
		t.Errorf("cluster env = %v", env)
	}
	if name := c["backup"].(map[string]any)["barmanObjectStore"].(map[string]any)["s3Credentials"].(map[string]any)["secretAccessKey"].(map[string]any)["name"]; name != "creds" {
		t.Errorf("Secret reference name was blanked: %v", name)
	}
	pc := cnpgReportCleanObject(pooler).Object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	if pc["env"].([]any)[0].(map[string]any)["value"] != cnpgReportRedacted {
		t.Errorf("pooler env = %v", pc["env"])
	}
	if cluster.Object["spec"].(map[string]any)["env"].([]any)[0].(map[string]any)["value"] != "plaintext" {
		t.Error("the source object was modified")
	}
}
