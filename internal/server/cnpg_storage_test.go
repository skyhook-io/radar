package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
)

func cnpgTestPVC(ns, name, cluster, instance, role string, owner *metav1.OwnerReference, requested, capacity string) *corev1.PersistentVolumeClaim {
	sc := "fast"
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{cnpgClusterLabel: cluster, cnpgPVCRoleLabel: role}},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: &sc,
			Resources:        corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(requested)}},
		},
		Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound, Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(capacity)}},
	}
	if instance != "" {
		pvc.Labels[cnpgInstanceNameLabel] = instance
	}
	if owner != nil {
		pvc.OwnerReferences = []metav1.OwnerReference{*owner}
	}
	return pvc
}

func seedCNPGClaims(t *testing.T, pvcs ...*corev1.PersistentVolumeClaim) {
	t.Helper()
	ctx := context.Background()
	for _, p := range pvcs {
		if _, err := testFakeClient.CoreV1().PersistentVolumeClaims(p.Namespace).Create(ctx, p, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create pvc %s: %v", p.Name, err)
		}
		t.Cleanup(func() {
			_ = testFakeClient.CoreV1().PersistentVolumeClaims(p.Namespace).Delete(context.Background(), p.Name, metav1.DeleteOptions{})
		})
	}
	lister := k8s.GetResourceCache().PersistentVolumeClaims()
	deadline := time.Now().Add(5 * time.Second)
	for {
		seen := 0
		for _, p := range pvcs {
			if _, err := lister.PersistentVolumeClaims(p.Namespace).Get(p.Name); err == nil {
				seen++
			}
		}
		if seen == len(pvcs) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("claims did not reach the cache")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func seedCNPGStorageClass(t *testing.T, name string, allow bool) {
	t.Helper()
	sc := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: name}, Provisioner: "example.com/csi", AllowVolumeExpansion: &allow}
	if _, err := testFakeClient.StorageV1().StorageClasses().Create(context.Background(), sc, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create storageclass: %v", err)
	}
	t.Cleanup(func() {
		_ = testFakeClient.StorageV1().StorageClasses().Delete(context.Background(), name, metav1.DeleteOptions{})
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := k8s.GetResourceCache().StorageClasses().Get(name); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("storageclass did not reach the cache")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// seedCNPGStorageCluster: two instances, instance 1 with separate WAL storage
// mid-resize, and a claim that carries the cluster label without being owned
// by it.
func seedCNPGStorageCluster(t *testing.T, ns string) {
	t.Helper()
	uid := types.UID(ns + "-uid")
	cluster := cnpgObj("postgresql.cnpg.io/v1", "Cluster", ns, "pg-orders",
		map[string]any{
			"instances":   int64(2),
			"storage":     map[string]any{"size": "2Gi", "storageClass": "fast"},
			"walStorage":  map[string]any{"size": "1Gi"},
			"tablespaces": []any{map[string]any{"name": "archive", "storage": map[string]any{"pvcTemplate": map[string]any{"resources": map[string]any{"requests": map[string]any{"storage": "5Gi"}}}}}},
		},
		map[string]any{
			"currentPrimary": "pg-orders-1",
			"instanceNames":  []any{"pg-orders-1", "pg-orders-2"},
			"healthyPVC":     []any{"pg-orders-1", "pg-orders-2"},
			"resizingPVC":    []any{"pg-orders-1-wal"},
		})
	seedCNPGWorkspace(t, cnpgWorkspaceTestKinds, cnpgRuntimeWithUID(cluster, string(uid)))
	owner := &metav1.OwnerReference{APIVersion: "postgresql.cnpg.io/v1", Kind: "Cluster", Name: "pg-orders", UID: uid, Controller: boolPtr(true)}
	foreign := &metav1.OwnerReference{APIVersion: "postgresql.cnpg.io/v1", Kind: "Cluster", Name: "pg-orders", UID: "someone-else"}
	wal := cnpgTestPVC(ns, "pg-orders-1-wal", "pg-orders", "pg-orders-1", cnpgPVCRoleWAL, owner, "2Gi", "1Gi")
	wal.Status.Conditions = []corev1.PersistentVolumeClaimCondition{{Type: corev1.PersistentVolumeClaimFileSystemResizePending, Status: corev1.ConditionTrue, Message: "waiting for pod restart"}}
	seedCNPGClaims(t,
		cnpgTestPVC(ns, "pg-orders-1", "pg-orders", "pg-orders-1", cnpgPVCRoleData, owner, "2Gi", "2Gi"),
		wal,
		cnpgTestPVC(ns, "pg-orders-2", "pg-orders", "pg-orders-2", cnpgPVCRoleData, owner, "2Gi", "2Gi"),
		cnpgTestPVC(ns, "pg-orders-9", "pg-orders", "pg-orders-9", cnpgPVCRoleData, foreign, "2Gi", "2Gi"),
	)
	ownerPod := metav1.OwnerReference{APIVersion: owner.APIVersion, Kind: owner.Kind, Name: owner.Name, UID: uid, Controller: boolPtr(true)}
	primary := cnpgPod(ns, "pg-orders-1", "pg-orders", ownerPod)
	primary.UID = types.UID(ns + "-p1")
	primary.Spec.Containers[0].Command = []string{"/controller/manager", "instance", "run", "--status-port-tls"}
	replica := cnpgPod(ns, "pg-orders-2", "pg-orders", ownerPod)
	replica.UID = types.UID(ns + "-p2")
	replica.Labels["cnpg.io/instanceRole"] = "replica"
	seedCNPGPods(t, primary, replica)
}

func getCNPGStorage(t *testing.T, path string) (int, CNPGClusterStorageResponse, string) {
	t.Helper()
	resp, err := http.Get(testServer.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var out CNPGClusterStorageResponse
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("decode: %v (%s)", err, body)
		}
	}
	return resp.StatusCode, out, string(body)
}

// usePrometheusVolumeStats serves kubelet volume stats for the named claims.
func usePrometheusVolumeStats(t *testing.T, used, capacity map[string]float64) {
	t.Helper()
	usePrometheusVolumeStatsFrom(t, used, capacity, false)
}

// usePrometheusVolumeStatsFrom with twoClusters answers identity checks with a
// second cluster holding claims of the same names.
func usePrometheusVolumeStatsFrom(t *testing.T, used, capacity map[string]float64, twoClusters bool) {
	t.Helper()
	series := func(values map[string]float64, query string) string {
		var rows []string
		for claim, v := range values {
			if !strings.Contains(query, claim) {
				continue
			}
			b, _ := json.Marshal(map[string]any{"metric": map[string]string{"persistentvolumeclaim": claim}, "value": []any{1700000000, strconv.FormatFloat(v, 'f', -1, 64)}})
			rows = append(rows, string(b))
		}
		return `{"status":"success","data":{"resultType":"vector","result":[` + strings.Join(rows, ",") + `]}}`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		q := r.URL.Query().Get("query")
		if q == "" {
			_ = r.ParseForm()
			q = r.Form.Get("query")
		}
		switch {
		case q == "up":
			_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"job":"prometheus"},"value":[1700000000,"1"]}]}}`)
		case strings.HasPrefix(q, "max(count by (persistentvolumeclaim)"):
			identities := "1"
			if twoClusters {
				identities = "2"
			}
			_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000,"`+identities+`"]}]}}`)
		case strings.Contains(q, "kubelet_volume_stats_used_bytes"):
			_, _ = io.WriteString(w, series(used, q))
		case strings.Contains(q, "kubelet_volume_stats_capacity_bytes"):
			_, _ = io.WriteString(w, series(capacity, q))
		default:
			_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
		}
	}))
	prometheuspkg.Initialize(nil, nil, "test")
	prometheuspkg.SetManualURL(srv.URL)
	t.Cleanup(func() {
		srv.Close()
		prometheuspkg.Reset()
		prometheuspkg.Initialize(nil, nil, "")
	})
}

func cnpgStorageInstances(t *testing.T, got CNPGClusterStorageResponse) map[string]CNPGStorageInstance {
	t.Helper()
	out := map[string]CNPGStorageInstance{}
	for _, in := range got.Instances {
		out[in.Name] = in
	}
	return out
}

func TestCNPGClusterStorage_VolumesWALAndUsage(t *testing.T) {
	seedCNPGStorageCluster(t, "pgst")
	seedCNPGStorageClass(t, "fast", true)
	useCNPGProxyAPIServer(t, func(w http.ResponseWriter, c cnpgProxyCall) {
		if c.port == "9187" {
			_, _ = io.WriteString(w, cnpgMetricsFixture+"cnpg_collector_pg_wal{value=\"count\"} 8\n")
			return
		}
		cnpgHealthyInstances(w, c)
	})
	usePrometheusVolumeStats(t,
		map[string]float64{"pg-orders-1": 1.9e9, "pg-orders-2": 1e9},
		map[string]float64{"pg-orders-1": 2e9, "pg-orders-2": 2e9})

	status, got, body := getCNPGStorage(t, "/api/cnpg/clusters/pgst/pg-orders/storage")
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	if got.Volumes.State != cnpgStorageStateOK || got.WAL.State != cnpgStorageStateOK {
		t.Fatalf("coverage volumes=%+v wal=%+v", got.Volumes, got.WAL)
	}
	if len(got.Excluded) != 1 || got.Excluded[0].Claim != "pg-orders-9" {
		t.Errorf("excluded = %+v, want the claim owned by another UID", got.Excluded)
	}
	insts := cnpgStorageInstances(t, got)
	if len(insts) != 2 {
		t.Fatalf("instances = %+v", got.Instances)
	}
	p := insts["pg-orders-1"]
	if p.Role != "primary" || insts["pg-orders-2"].Role != "replica" {
		t.Errorf("roles = %s %s", p.Role, insts["pg-orders-2"].Role)
	}
	if len(p.Volumes) != 2 || p.Volumes[0].Role != cnpgPVCRoleData || p.Volumes[1].Role != cnpgPVCRoleWAL {
		t.Fatalf("primary volumes = %+v, want data then WAL", p.Volumes)
	}
	data, wal := p.Volumes[0], p.Volumes[1]
	if data.StorageClass.AllowVolumeExpansion == nil || !*data.StorageClass.AllowVolumeExpansion || data.ClusterState != "healthy" {
		t.Errorf("data volume = %+v", data)
	}
	if !wal.Resize.Pending || len(wal.Resize.Conditions) != 1 || wal.Resize.Conditions[0].Type != "FileSystemResizePending" || wal.ClusterState != "resizing" {
		t.Errorf("wal resize = %+v state %q", wal.Resize, wal.ClusterState)
	}
	if data.Usage.State != cnpgUsageStateOK || data.Usage.Ratio == nil || *data.Usage.Ratio < 0.94 {
		t.Errorf("data usage = %+v", data.Usage)
	}
	// Prometheus answered for the data claims only: the WAL claim is unmeasured,
	// never zero.
	if wal.Usage.State != cnpgUsageStateNoSeries || wal.Usage.Ratio != nil || wal.Usage.UsedBytes != nil {
		t.Errorf("wal usage = %+v, want noSeries without figures", wal.Usage)
	}
	if got.Usage.State != cnpgStorageStatePartial {
		t.Errorf("usage coverage = %+v, want partial", got.Usage)
	}
	if len(got.Findings) != 1 || got.Findings[0].Severity != "critical" || got.Findings[0].Claim != "pg-orders-1" {
		t.Errorf("findings = %+v, want one critical on pg-orders-1", got.Findings)
	}

	pw := p.WAL
	if pw == nil || pw.Volume != "pg-orders-1-wal" || !cnpgEqF(pw.SizeBytes, 1.34217728e+08) || !cnpgEqF(pw.Segments, 8) {
		t.Fatalf("primary WAL = %+v", pw)
	}
	if pw.ReadyToArchive == nil || *pw.ReadyToArchive != 3 || pw.ArchivingFailed {
		t.Errorf("archive backlog = %+v", pw)
	}
	if len(pw.Slots) != 1 || pw.Slots[0].Bytes != 16384 {
		t.Errorf("slots = %+v", pw.Slots)
	}
	if rw := insts["pg-orders-2"].WAL; rw == nil || rw.Volume != "pg-orders-2" {
		t.Errorf("replica WAL volume = %+v, want the data claim when there is no WAL claim", rw)
	}

	targets := got.Expansion.Targets
	if len(targets) != 3 || targets[0].Field != "spec.storage.size" || targets[0].Declared != "2Gi" ||
		targets[1].Field != "spec.walStorage.size" || targets[2].Field != "spec.tablespaces[name=archive].storage.pvcTemplate.resources.requests.storage" || targets[2].Declared != "5Gi" {
		t.Errorf("expansion targets = %+v", targets)
	}
}

func TestCNPGClusterStorage_NoPrometheusIsNeverZero(t *testing.T) {
	seedCNPGStorageCluster(t, "pgst2")
	useCNPGProxyAPIServer(t, cnpgHealthyInstances)
	prometheuspkg.Reset()

	status, got, body := getCNPGStorage(t, "/api/cnpg/clusters/pgst2/pg-orders/storage")
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	if got.Usage.State != cnpgUsageStateNoPrometheus {
		t.Fatalf("usage = %+v, want noPrometheus", got.Usage)
	}
	for _, in := range got.Instances {
		for _, v := range in.Volumes {
			if v.Usage.State != cnpgUsageStateNoPrometheus || v.Usage.Ratio != nil || v.Usage.UsedBytes != nil {
				t.Errorf("%s usage = %+v", v.Claim, v.Usage)
			}
		}
	}
	if len(got.Findings) != 0 {
		t.Errorf("findings without a measurement: %+v", got.Findings)
	}
	if !strings.Contains(body, `"storageClass":{"name":"fast","reason":"StorageClass fast not found"}`) {
		t.Errorf("an unreadable class must not read as not expandable: %s", body)
	}
}

func TestCNPGClusterStorage_ReadingTheClusterDoesNotImplyItsClaims(t *testing.T) {
	seedCNPGStorageCluster(t, "pgst3")
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"pgst3"}}
	perms.SetCanI("get", cnpgGroup, "clusters", "pgst3", true)
	allow(perms, "", "persistentvolumeclaims", "pgst3", false)
	allow(perms, "", "pods", "pgst3", false)
	env.srv.permCache.Set("dba", nil, perms)

	resp := env.authGet(t, "/api/cnpg/clusters/pgst3/pg-orders/storage", "dba", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d: %s", resp.StatusCode, b)
	}
	var got CNPGClusterStorageResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Volumes.State != cnpgStorageStateDenied || got.Volumes.Grant != "list persistentvolumeclaims in pgst3" {
		t.Errorf("volumes = %+v", got.Volumes)
	}
	if got.WAL.State != cnpgStorageStateDenied || got.WAL.Grant != "list pods in pgst3" {
		t.Errorf("wal = %+v", got.WAL)
	}
	for _, in := range got.Instances {
		if len(in.Volumes) != 0 || in.WAL != nil {
			t.Errorf("instance %s leaked facts: %+v", in.Name, in)
		}
	}

	perms2 := &auth.UserPermissions{AllowedNamespaces: []string{"pgst3"}}
	perms2.SetCanI("get", cnpgGroup, "clusters", "pgst3", false)
	env.srv.permCache.Set("nobody", nil, perms2)
	denied := env.authGet(t, "/api/cnpg/clusters/pgst3/pg-orders/storage", "nobody", "")
	denied.Body.Close()
	if denied.StatusCode != http.StatusForbidden {
		t.Errorf("without get clusters: %d, want 403", denied.StatusCode)
	}
}

func TestCNPGFleetDisk(t *testing.T) {
	seedCNPGStorageCluster(t, "pgfd")
	usePrometheusVolumeStats(t,
		map[string]float64{"pg-orders-1": 1.7e9, "pg-orders-2": 1e9, "pg-orders-9": 1.99e9},
		map[string]float64{"pg-orders-1": 2e9, "pg-orders-2": 2e9, "pg-orders-9": 2e9})

	resp, err := http.Get(testServer.URL + "/api/cnpg/disk?namespaces=pgfd")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got CNPGFleetDiskResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Clusters) != 1 {
		t.Fatalf("clusters = %+v", got.Clusters)
	}
	d := got.Clusters[0]
	// The foreign claim is fuller but is not this cluster's; the WAL claim has
	// no series, so the answer is partial.
	if d.State != cnpgStorageStatePartial || d.Claims != 3 || d.Measured != 2 || d.Max == nil || d.Max.Claim != "pg-orders-1" || d.Max.Instance != "pg-orders-1" {
		t.Errorf("disk = %+v max=%+v", d, d.Max)
	}
}

// Same-named claims in another cluster sharing this Prometheus: no value is
// reported, since max() over both could hide this cluster's fullness.
func TestCNPGFleetDiskRefusesAmbiguousClusterIdentity(t *testing.T) {
	seedCNPGStorageCluster(t, "pgfa")
	useCNPGProxyAPIServer(t, cnpgHealthyInstances)
	usePrometheusVolumeStatsFrom(t,
		map[string]float64{"pg-orders-1": 1.7e9, "pg-orders-2": 1e9},
		map[string]float64{"pg-orders-1": 2e9, "pg-orders-2": 2e9}, true)

	resp, err := http.Get(testServer.URL + "/api/cnpg/disk?namespaces=pgfa")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got CNPGFleetDiskResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Clusters) != 1 || got.Clusters[0].State != cnpgHistoryStateAmbiguous || got.Clusters[0].Max != nil || got.Clusters[0].Reason == "" {
		t.Fatalf("disk = %+v", got.Clusters)
	}

	status, storage, body := getCNPGStorage(t, "/api/cnpg/clusters/pgfa/pg-orders/storage")
	if status != http.StatusOK || storage.Usage.State != cnpgHistoryStateAmbiguous {
		t.Fatalf("storage = %d %s", status, body)
	}
	for _, in := range storage.Instances {
		for _, v := range in.Volumes {
			if v.Usage.Ratio != nil || v.Usage.UsedBytes != nil {
				t.Errorf("volume %s carries a value from an ambiguous scope: %+v", v.Claim, v.Usage)
			}
		}
	}
}

func TestCNPGDiskFindingsThresholds(t *testing.T) {
	r := func(v float64) *float64 { return &v }
	vols := []CNPGStorageVolume{
		{Claim: "a", Role: cnpgPVCRoleData, Usage: CNPGStorageVolumeUsage{State: cnpgUsageStateOK, Ratio: r(0.79)}},
		{Claim: "b", Role: cnpgPVCRoleWAL, Usage: CNPGStorageVolumeUsage{State: cnpgUsageStateOK, Ratio: r(0.80)}},
		{Claim: "c", Role: cnpgPVCRoleTablespace, Tablespace: "archive", Usage: CNPGStorageVolumeUsage{State: cnpgUsageStateOK, Ratio: r(0.90)}},
		{Claim: "d", Role: cnpgPVCRoleData, Usage: CNPGStorageVolumeUsage{State: cnpgUsageStateNoSeries}},
	}
	got := cnpgDiskFindings("pg-1", vols, nil)
	if len(got) != 2 || got[0].Severity != "warning" || got[1].Severity != "critical" {
		t.Fatalf("findings = %+v", got)
	}
	if got[1].Message != "The tablespace archive volume of pg-1 is 90% full" {
		t.Errorf("message = %q", got[1].Message)
	}
	unverified := cnpgDiskFindings("pg-1", vols, &prometheuspkg.CNPGIsolation{Mode: prometheuspkg.CNPGIsolationUnverified, Note: "Radar couldn't confirm these volume stats belong to this exact cluster"})
	if !strings.Contains(unverified[1].Message, "couldn't confirm these volume stats belong to this exact cluster") {
		t.Errorf("an unverified match must say so: %q", unverified[1].Message)
	}
}

func TestCNPGStorageExpansionDefaultsToStorageSize(t *testing.T) {
	c := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"storage": map[string]any{"resizeInUseVolumes": false}}}}
	got := cnpgStorageExpansionOf(c)
	if len(got.Targets) != 1 || got.Targets[0].Field != "spec.storage.size" || got.Targets[0].Declared != "" {
		t.Errorf("targets = %+v", got.Targets)
	}
	if got.ResizeInUseVolumes == nil || *got.ResizeInUseVolumes {
		t.Errorf("resizeInUseVolumes = %v, want the declared false", got.ResizeInUseVolumes)
	}
}

func TestCNPGWALCoverageReason(t *testing.T) {
	cases := []struct {
		failed, partial, total int
		want                   string
	}{
		{3, 0, 3, "3 of 3 instances could not be read"},
		{1, 2, 3, "1 of 3 instances could not be read; 2 of 3 were read only in part"},
		{0, 1, 3, "1 of 3 was read only in part"},
	}
	for _, c := range cases {
		if got := cnpgWALCoverageReason(c.failed, c.partial, c.total); got != c.want {
			t.Errorf("(%d, %d, %d) = %q, want %q", c.failed, c.partial, c.total, got, c.want)
		}
	}
}

// An exporter whose queries fail still answers, without the WAL collector:
// that instance was read only in part, and its slot list is not "no slots".
func TestCNPGClusterStorage_WALWithoutCollectorIsPartial(t *testing.T) {
	seedCNPGStorageCluster(t, "pgst3")
	useCNPGProxyAPIServer(t, func(w http.ResponseWriter, c cnpgProxyCall) {
		if c.port == "9187" {
			_, _ = io.WriteString(w, "# TYPE cnpg_pg_postmaster_start_time gauge\ncnpg_pg_postmaster_start_time 1.7e9\n")
			return
		}
		cnpgHealthyInstances(w, c)
	})
	status, got, body := getCNPGStorage(t, "/api/cnpg/clusters/pgst3/pg-orders/storage")
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	if got.WAL.State != cnpgStorageStatePartial || got.WAL.Reason != "2 of 2 were read only in part" {
		t.Errorf("wal coverage = %+v", got.WAL)
	}
	for name, in := range cnpgStorageInstances(t, got) {
		if in.WAL == nil || in.WAL.Metrics.State != cnpgRuntimeStatePartial || in.WAL.Metrics.Reason == "" || in.WAL.Status.State != cnpgRuntimeStateOK {
			t.Errorf("%s WAL = %+v", name, in.WAL)
		}
	}
}
