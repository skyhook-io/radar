package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	metadatafake "k8s.io/client-go/metadata/fake"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
)

const cnpgHATestNS = "pgha"

func cnpgHAOwner(uid string) metav1.OwnerReference {
	return metav1.OwnerReference{APIVersion: "postgresql.cnpg.io/v1", Kind: "Cluster", Name: "pg-ha", UID: types.UID(uid), Controller: boolPtr(true)}
}

func cnpgHACluster(now time.Time) *unstructured.Unstructured {
	c := withUID(cnpgObj("postgresql.cnpg.io/v1", "Cluster", cnpgHATestNS, "pg-ha", map[string]any{
		"instances": int64(3),
		"postgresql": map[string]any{"synchronous": map[string]any{
			"method": "any", "number": int64(1), "failoverQuorum": true,
		}},
		"certificates":          map[string]any{"serverTLSSecret": "pg-ha-user-tls", "serverCASecret": "pg-ha-user-ca"},
		"nodeMaintenanceWindow": map[string]any{"inProgress": true},
	}, map[string]any{
		"image":          "pg:17.2",
		"currentPrimary": "pg-ha-1",
		"certificates": map[string]any{
			"serverTLSSecret":      "pg-ha-user-tls",
			"serverCASecret":       "pg-ha-user-ca",
			"replicationTLSSecret": "pg-ha-replication",
			"clientCASecret":       "pg-ha-ca",
			"expirations": map[string]any{
				"pg-ha-user-tls":    now.Add(20 * 24 * time.Hour).Format(issues.CNPGCertExpiryLayout),
				"pg-ha-user-ca":     now.Add(300 * 24 * time.Hour).Format(issues.CNPGCertExpiryLayout),
				"pg-ha-replication": "garbage",
				"pg-ha-ca":          now.Add(60 * 24 * time.Hour).Format(issues.CNPGCertExpiryLayout),
			},
		},
	}), "ha-uid")
	return c
}

func seedCNPGHAFixture(t *testing.T, now time.Time) *unstructured.Unstructured {
	t.Helper()
	kinds := append(append([]k8s.APIResource(nil), cnpgWorkspaceTestKinds...), cnpgTestResource(cnpgGroup, "FailoverQuorum", "failoverquorums", true))
	cluster := cnpgHACluster(now)
	seedCNPGWorkspace(t, kinds, cluster)

	owner := cnpgHAOwner("ha-uid")
	p1 := cnpgPod(cnpgHATestNS, "pg-ha-1", "pg-ha", owner)
	p1.Spec.NodeName = "ha-node-a"
	p1.Spec.Containers[0].Image = "pg:17.2"
	p2 := cnpgPod(cnpgHATestNS, "pg-ha-2", "pg-ha", owner)
	p2.Labels["cnpg.io/instanceRole"] = "replica"
	p2.Spec.NodeName = "ha-node-b"
	p2.Spec.Containers[0].Image = "pg:17.1"
	p1.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	p2.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	p3 := cnpgPod(cnpgHATestNS, "pg-ha-3", "pg-ha", owner)
	p3.Labels["cnpg.io/instanceRole"] = "replica"
	p3.Spec.NodeName = "ha-node-b"
	p3.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}}
	seedCNPGPods(t, p1, p2, p3)

	ctx := context.Background()
	create := func(name string, fn func() error) {
		if err := fn(); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}
	for _, n := range []struct{ name, zone string }{{"ha-node-a", "zone-a"}, {"ha-node-b", "zone-b"}} {
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: n.name, Labels: map[string]string{cnpgZoneLabel: n.zone}}}
		create(n.name, func() error {
			_, err := testFakeClient.CoreV1().Nodes().Create(ctx, node, metav1.CreateOptions{})
			return err
		})
		t.Cleanup(func() {
			_ = testFakeClient.CoreV1().Nodes().Delete(context.Background(), node.Name, metav1.DeleteOptions{})
		})
	}
	one := intstr.FromInt32(1)
	pdbs := []*policyv1.PodDisruptionBudget{
		{ObjectMeta: metav1.ObjectMeta{Name: "pg-ha", Namespace: cnpgHATestNS, Generation: 2, Labels: map[string]string{cnpgClusterLabel: "pg-ha"}, OwnerReferences: []metav1.OwnerReference{owner}},
			Spec:   policyv1.PodDisruptionBudgetSpec{MinAvailable: &one},
			Status: policyv1.PodDisruptionBudgetStatus{ObservedGeneration: 2, ExpectedPods: 2, CurrentHealthy: 1, DesiredHealthy: 1, DisruptionsAllowed: 0}},
		{ObjectMeta: metav1.ObjectMeta{Name: "pg-ha-primary", Namespace: cnpgHATestNS, Generation: 1, Labels: map[string]string{cnpgClusterLabel: "pg-ha"}, OwnerReferences: []metav1.OwnerReference{owner}},
			Spec:   policyv1.PodDisruptionBudgetSpec{MinAvailable: &one},
			Status: policyv1.PodDisruptionBudgetStatus{ObservedGeneration: 1, ExpectedPods: 1, CurrentHealthy: 1, DesiredHealthy: 1}},
		{ObjectMeta: metav1.ObjectMeta{Name: "pg-ha-foreign", Namespace: cnpgHATestNS, Labels: map[string]string{cnpgClusterLabel: "pg-ha"}}},
	}
	for _, p := range pdbs {
		create(p.Name, func() error {
			_, err := testFakeClient.PolicyV1().PodDisruptionBudgets(cnpgHATestNS).Create(ctx, p, metav1.CreateOptions{})
			return err
		})
		t.Cleanup(func() {
			_ = testFakeClient.PolicyV1().PodDisruptionBudgets(cnpgHATestNS).Delete(context.Background(), p.Name, metav1.DeleteOptions{})
		})
	}
	started := metav1.NewTime(now.Add(-time.Hour))
	jobs := []*batchv1.Job{
		{ObjectMeta: metav1.ObjectMeta{Name: "pg-ha-1-initdb", Namespace: cnpgHATestNS, Labels: map[string]string{cnpgClusterLabel: "pg-ha", cnpgJobRoleLabel: "initdb", "cnpg.io/instanceName": "pg-ha-1"}, OwnerReferences: []metav1.OwnerReference{owner}},
			Status: batchv1.JobStatus{StartTime: &started, Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "pg-ha-3-join", Namespace: cnpgHATestNS, Labels: map[string]string{cnpgClusterLabel: "pg-ha", cnpgJobRoleLabel: "join"}, OwnerReferences: []metav1.OwnerReference{owner}},
			Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded", Message: "Job has reached the specified backoff limit"}}}},
	}
	for _, j := range jobs {
		create(j.Name, func() error {
			_, err := testFakeClient.BatchV1().Jobs(cnpgHATestNS).Create(ctx, j, metav1.CreateOptions{})
			return err
		})
		t.Cleanup(func() {
			_ = testFakeClient.BatchV1().Jobs(cnpgHATestNS).Delete(context.Background(), j.Name, metav1.DeleteOptions{})
		})
	}
	cache := k8s.GetResourceCache()
	waitFor(t, 5*time.Second, func() bool {
		n, _ := cache.Nodes().Get("ha-node-b")
		pd, _ := cache.PodDisruptionBudgets().PodDisruptionBudgets(cnpgHATestNS).List(labels.Everything())
		js, _ := cache.Jobs().Jobs(cnpgHATestNS).List(labels.Everything())
		return n != nil && len(pd) == 3 && len(js) == 2
	})
	return cluster
}

func cnpgHAClientsFor(t *testing.T, now time.Time) cnpgHAClients {
	t.Helper()
	renew := metav1.NewMicroTime(now.Add(-3 * time.Second))
	stale := metav1.NewMicroTime(now.Add(-time.Minute))
	fifteen := int32(15)
	holder, opHolder := "pg-ha-1", "cnpg-controller-manager-abc_123"
	owner := cnpgHAOwner("ha-uid")
	typed := fake.NewClientset(
		&coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: "pg-ha", Namespace: cnpgHATestNS, OwnerReferences: []metav1.OwnerReference{owner}},
			Spec: coordinationv1.LeaseSpec{HolderIdentity: &holder, RenewTime: &renew, LeaseDurationSeconds: &fifteen}},
		&coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: cnpgOperatorLeaseName, Namespace: "cnpg-system"},
			Spec: coordinationv1.LeaseSpec{HolderIdentity: &opHolder, RenewTime: &stale, LeaseDurationSeconds: &fifteen}},
		&discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "pg-ha-rw-x", Namespace: cnpgHATestNS, Labels: map[string]string{discoveryv1.LabelServiceName: "pg-ha-rw"}},
			Endpoints: []discoveryv1.Endpoint{
				{TargetRef: &corev1.ObjectReference{Kind: "Pod", Name: "pg-ha-1"}, Conditions: discoveryv1.EndpointConditions{Ready: boolPtr(true)}},
				{TargetRef: &corev1.ObjectReference{Kind: "Pod", Name: "pg-ha-2"}, Conditions: discoveryv1.EndpointConditions{Ready: boolPtr(false)}},
			}},
	)
	fq := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1", "kind": "FailoverQuorum",
		"metadata": map[string]any{"name": "pg-ha", "namespace": cnpgHATestNS, "ownerReferences": []any{map[string]any{
			"apiVersion": "postgresql.cnpg.io/v1", "kind": "Cluster", "name": "pg-ha", "uid": "ha-uid", "controller": true,
		}}},
		"status": map[string]any{"method": "ANY", "standbyNames": []any{"pg-ha-2", "pg-ha-3"}, "standbyNumber": int64(1), "primary": "pg-ha-1"},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{cnpgFailoverQuorumGVR: "FailoverQuorumList"}, fq)
	metaScheme := metadatafake.NewTestScheme()
	metaScheme.AddKnownTypeWithName(corev1.SchemeGroupVersion.WithKind("Secret"), &metav1.PartialObjectMetadata{})
	meta := metadatafake.NewSimpleMetadataClient(metaScheme,
		&metav1.PartialObjectMetadata{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
			ObjectMeta: metav1.ObjectMeta{Name: "pg-ha-user-tls", Namespace: cnpgHATestNS, Annotations: map[string]string{certManagerCertificateAnno: "pg-ha-server", certManagerIssuerAnno: "internal-ca", certManagerIssuerKindAnno: "ClusterIssuer"}},
		},
		&metav1.PartialObjectMetadata{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
			ObjectMeta: metav1.ObjectMeta{Name: "pg-ha-user-ca", Namespace: cnpgHATestNS},
		},
	)
	return cnpgHAClients{typed: typed, dyn: dyn, meta: meta}
}

func TestCNPGClusterHA_ReadsEveryFact(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	cluster := seedCNPGHAFixture(t, now)
	srv := &Server{}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	got := srv.cnpgClusterHA(r, cnpgHAClientsFor(t, now), k8s.GetResourceCache(), cluster, now)

	if got.DesiredImage != "pg:17.2" || len(got.Instances) != 3 {
		t.Fatalf("instances = %+v", got.Instances)
	}
	inst := map[string]CNPGHAInstance{}
	for _, i := range got.Instances {
		inst[i.Pod] = i
	}
	if i := inst["pg-ha-2"]; i.Zone != "zone-b" || i.ImageMatches == nil || *i.ImageMatches || i.Role != "replica" {
		t.Errorf("pg-ha-2 = %+v, want zone-b with image drift", i)
	}
	if i := inst["pg-ha-1"]; i.Zone != "zone-a" || i.ImageMatches == nil || !*i.ImageMatches || i.QOSClass != "" && i.QOSClass != "BestEffort" {
		t.Errorf("pg-ha-1 = %+v", i)
	}

	q := got.Quorum
	if !q.Enabled || q.EnabledBy != "spec" || q.Object.State != cnpgHAStateOK || q.Status == nil {
		t.Fatalf("quorum = %+v", q)
	}
	// N=2 potentially synchronous, W=1, R=1 (pg-ha-3 is not ready): 1+1 > 2 is false.
	if q.N == nil || *q.N != 2 || *q.W != 1 || q.R == nil || *q.R != 1 || q.Holds == nil || *q.Holds {
		t.Errorf("quorum arithmetic = N%v W%v R%v holds %v", q.N, q.W, q.R, q.Holds)
	}

	if got.PDBs.State != cnpgHAStateOK || len(got.PDBs.Items) != 2 {
		t.Fatalf("pdbs = %+v (the unowned budget must be ignored)", got.PDBs)
	}
	if p := got.PDBs.Items[0]; p.Name != "pg-ha" || p.Role != "replicas" || p.DisruptionsAllowed != 0 || !p.Observed || p.MinAvailable != "1" {
		t.Errorf("replica pdb = %+v", p)
	}

	if l := got.PrimaryLease; l.State != cnpgHAStateOK || l.Holder != "pg-ha-1" || l.Expired == nil || *l.Expired || l.ControlledByCluster == nil || !*l.ControlledByCluster {
		t.Errorf("primary lease = %+v", l)
	}

	if len(got.Jobs.Items) != 2 {
		t.Fatalf("jobs = %+v", got.Jobs)
	}
	phases := map[string]string{}
	for _, j := range got.Jobs.Items {
		phases[j.Role] = j.Phase
	}
	if phases["initdb"] != "succeeded" || phases["join"] != "failed" {
		t.Errorf("job phases = %v", phases)
	}

	if e := got.RWEndpoints; e.State != cnpgHAStateOK || len(e.Pods) != 1 || e.Pods[0] != "pg-ha-1" {
		t.Errorf("rw endpoints = %+v", e)
	}

	certs := map[string]CNPGHACertificate{}
	for _, c := range got.Certificates {
		certs[c.Secret] = c
	}
	if c := certs["pg-ha-user-tls"]; c.Renewal != "user" || c.ExpiresAt == "" || c.CertManager == nil || c.CertManager.Certificate != "pg-ha-server" || c.Metadata == nil || c.Metadata.State != cnpgHAStateOK {
		t.Errorf("user tls = %+v", c)
	}
	if c := certs["pg-ha-user-ca"]; c.Renewal != "user" || c.CertManager != nil || c.Metadata.State != cnpgHAStateOK {
		t.Errorf("user ca without cert-manager = %+v", c)
	}
	if c := certs["pg-ha-replication"]; c.Renewal != "operator" || c.ExpiresAt != "" || c.Raw != "garbage" || c.Metadata != nil {
		t.Errorf("unparseable operator cert = %+v", c)
	}

	if !got.Maintenance.Declared || !got.Maintenance.InProgress || !got.Maintenance.ReusePVC {
		t.Errorf("maintenance = %+v, want in progress with reusePVC defaulting to true", got.Maintenance)
	}
}

func TestCNPGQuorumArithmetic(t *testing.T) {
	ready := func(name string, ok bool) *corev1.Pod {
		st := corev1.ConditionFalse
		if ok {
			st = corev1.ConditionTrue
		}
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name}, Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: st}}}}
	}
	pods := []*corev1.Pod{ready("a", true), ready("b", true), ready("c", false)}

	q := CNPGHAQuorum{Status: &CNPGHAQuorumStatus{StandbyNames: []string{"b", "c"}, StandbyNumber: 2, Primary: "a"}}
	cnpgQuorumArithmetic(&q, pods, true)
	if *q.R != 1 || !*q.Holds {
		t.Errorf("R=1 W=2 N=2 should hold: %+v", q)
	}

	reset := CNPGHAQuorum{Status: &CNPGHAQuorumStatus{StandbyNames: []string{}}}
	cnpgQuorumArithmetic(&reset, pods, true)
	if reset.N != nil || reset.Holds != nil {
		t.Errorf("a reset object records no configuration; nothing may be computed: %+v", reset)
	}

	unknownPods := CNPGHAQuorum{Status: &CNPGHAQuorumStatus{StandbyNames: []string{"b"}, StandbyNumber: 1}}
	cnpgQuorumArithmetic(&unknownPods, nil, false)
	if unknownPods.N == nil || unknownPods.R != nil || unknownPods.Holds != nil {
		t.Errorf("without Pods R and the verdict are unknown: %+v", unknownPods)
	}
}

// Reading the Cluster never implies the rest: a caller who can only get the
// Cluster and list Pods sees every other fact denied, named by its grant, and
// Radar never asks the apiserver for what the caller cannot read.
func TestCNPGClusterHA_EachReadIsAuthorizedOnItsOwn(t *testing.T) {
	now := time.Now().UTC()
	seedCNPGHAFixture(t, now)

	var mu sync.Mutex
	var calls []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		cnpgWriteAPIStatus(w, http.StatusForbidden, metav1.StatusReasonForbidden, "forbidden")
	}))
	t.Cleanup(api.Close)
	previous := k8s.SetTestConfig(&rest.Config{Host: api.URL})
	t.Cleanup(func() { k8s.SetTestConfig(previous) })

	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{cnpgHATestNS}}
	perms.SetCanI("get", cnpgGroup, "clusters", cnpgHATestNS, true)
	perms.SetCanI("list", "", "pods", cnpgHATestNS, true)
	for _, d := range []struct{ verb, group, resource, ns string }{
		{"get", "", "nodes", ""},
		{"get", cnpgGroup, "failoverquorums", cnpgHATestNS},
		{"list", "policy", "poddisruptionbudgets", cnpgHATestNS},
		{"get", "coordination.k8s.io", "leases", cnpgHATestNS},
		{"list", "batch", "jobs", cnpgHATestNS},
		{"list", "discovery.k8s.io", "endpointslices", cnpgHATestNS},
		{"get", "", "secrets", cnpgHATestNS},
		{"list", "apps", "deployments", ""},
	} {
		perms.SetCanI(d.verb, d.group, d.resource, d.ns, false)
	}
	env.srv.permCache.Set("narrow", nil, perms)

	resp := env.authGet(t, "/api/cnpg/clusters/"+cnpgHATestNS+"/pg-ha/ha", "narrow", "")
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	var got CNPGClusterHAResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	for name, src := range map[string]CNPGHASource{
		"nodes": got.Nodes, "quorum": got.Quorum.Object, "pdbs": got.PDBs.CNPGHASource,
		"primaryLease": got.PrimaryLease.CNPGHASource, "jobs": got.Jobs.CNPGHASource, "rwEndpoints": got.RWEndpoints.CNPGHASource,
	} {
		if src.State != cnpgHAStateDenied || src.Grant == nil {
			t.Errorf("%s = %+v, want denied naming the grant", name, src)
		}
	}
	if got.Pods.State != cnpgHAStateOK || len(got.Instances) != 3 || got.Instances[0].Zone != "" {
		t.Errorf("pods readable, zones not: %+v", got.Instances)
	}
	for _, c := range got.Certificates {
		if c.Renewal == "user" && (c.Metadata == nil || c.Metadata.State != cnpgHAStateDenied || c.CertManager != nil) {
			t.Errorf("%s: cert-manager link must be unknown without get secrets: %+v", c.Secret, c)
		}
	}
	if len(got.PDBs.Items) != 0 || len(got.Jobs.Items) != 0 {
		t.Errorf("denied lists must be empty: %+v %+v", got.PDBs.Items, got.Jobs.Items)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, c := range calls {
		if strings.Contains(c, "leases") || strings.Contains(c, "failoverquorums") || strings.Contains(c, "endpointslices") || strings.Contains(c, "secrets") {
			t.Errorf("Radar asked the apiserver for something the caller cannot read: %s", c)
		}
	}
}

func TestCNPGUncachedReasonSaysWhatIsUnknownAndWhy(t *testing.T) {
	if got := cnpgUncachedReason("Zones", "Nodes", "", true, false, false); got != "Zones unknown: Radar's own credentials could not list Nodes when it connected" {
		t.Errorf("uncached = %q", got)
	}
	if got := cnpgUncachedReason("Instance Jobs", "Jobs", "pg", false, true, true); got != "Instance Jobs unknown: Radar watches Jobs only in the namespaces it chose when it connected, and pg is not one of them" {
		t.Errorf("out of scope = %q", got)
	}
	if got := cnpgUncachedReason("Instance Jobs", "Jobs", "pg", false, false, false); got != "Instance Jobs unknown: Radar is still loading Jobs" {
		t.Errorf("syncing = %q", got)
	}
	if got := cnpgUncachedReason("Zones", "Nodes", "", false, false, true); got != "" {
		t.Errorf("cached = %q", got)
	}
}
