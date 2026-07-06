package topology

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// stubProvider supplies a typed K8s resource for the kinds GetRelationships
// inspects for Pod hygiene fields and ManagedBy synthesis. Other methods
// return empty slices so calls fall through cleanly.
type stubProvider struct {
	pods     []*corev1.Pod
	services []*corev1.Service
	pdbs     []*policyv1.PodDisruptionBudget
}

func (s *stubProvider) Pods() ([]*corev1.Pod, error)         { return s.pods, nil }
func (s *stubProvider) Services() ([]*corev1.Service, error) { return s.services, nil }
func (s *stubProvider) Deployments() ([]*appsv1.Deployment, error) {
	return nil, nil
}
func (s *stubProvider) DaemonSets() ([]*appsv1.DaemonSet, error)     { return nil, nil }
func (s *stubProvider) StatefulSets() ([]*appsv1.StatefulSet, error) { return nil, nil }
func (s *stubProvider) ReplicaSets() ([]*appsv1.ReplicaSet, error)   { return nil, nil }
func (s *stubProvider) Jobs() ([]*batchv1.Job, error)                { return nil, nil }
func (s *stubProvider) CronJobs() ([]*batchv1.CronJob, error)        { return nil, nil }
func (s *stubProvider) Ingresses() ([]*networkingv1.Ingress, error)  { return nil, nil }
func (s *stubProvider) ConfigMaps() ([]*corev1.ConfigMap, error)     { return nil, nil }
func (s *stubProvider) Secrets() ([]*corev1.Secret, error)           { return nil, nil }
func (s *stubProvider) PersistentVolumeClaims() ([]*corev1.PersistentVolumeClaim, error) {
	return nil, nil
}
func (s *stubProvider) PersistentVolumes() ([]*corev1.PersistentVolume, error) { return nil, nil }
func (s *stubProvider) HorizontalPodAutoscalers() ([]*autoscalingv2.HorizontalPodAutoscaler, error) {
	return nil, nil
}
func (s *stubProvider) PodDisruptionBudgets() ([]*policyv1.PodDisruptionBudget, error) {
	return s.pdbs, nil
}
func (s *stubProvider) NetworkPolicies() ([]*networkingv1.NetworkPolicy, error) { return nil, nil }
func (s *stubProvider) Nodes() ([]*corev1.Node, error)                          { return nil, nil }
func (s *stubProvider) GetResourceStatus(kind, namespace, name string) *ResourceStatus {
	return nil
}

// TestGetRelationships_PodHygieneFields covers T2: pods carry
// ServiceAccount, Node, and ManagedBy refs derived from spec + labels.
func TestGetRelationships_PodHygieneFields(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "demo",
			Name:      "web-abc-xyz",
			Annotations: map[string]string{
				argoTrackingIDAnnotation: "argocd_guestbook:apps/Deployment:demo/web",
			},
		},
		Spec: corev1.PodSpec{
			ServiceAccountName: "web-sa",
			NodeName:           "node-1",
		},
	}
	provider := &stubProvider{pods: []*corev1.Pod{pod}}

	topo := &Topology{
		Nodes: []Node{{ID: "pod/demo/web-abc-xyz", Kind: KindPod, Name: "web-abc-xyz"}},
		Edges: []Edge{},
	}

	rel := GetRelationships("Pod", "demo", "web-abc-xyz", topo, provider, nil)
	if rel == nil {
		t.Fatal("expected non-nil Relationships for pod with hygiene fields")
	}
	if rel.ServiceAccount == nil || rel.ServiceAccount.Kind != "ServiceAccount" || rel.ServiceAccount.Name != "web-sa" || rel.ServiceAccount.Namespace != "demo" {
		t.Errorf("ServiceAccount: want {Kind:ServiceAccount NS:demo Name:web-sa}, got %+v", rel.ServiceAccount)
	}
	if rel.Node == nil || rel.Node.Kind != "Node" || rel.Node.Name != "node-1" {
		t.Errorf("Node: want {Kind:Node Name:node-1}, got %+v", rel.Node)
	}
	if len(rel.ManagedBy) != 1 || rel.ManagedBy[0].Kind != "Application" || rel.ManagedBy[0].Name != "guestbook" {
		t.Errorf("ManagedBy: want [{Application/argocd/guestbook}], got %+v", rel.ManagedBy)
	}
}

// TestGetRelationships_PodHygieneFields_EmptySAandUnscheduled verifies that
// optional fields are properly omitted when the source data is empty. The
// nil-result short-circuit must also still kick in.
func TestGetRelationships_PodHygieneFields_EmptySAandUnscheduled(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "demo", Name: "lone"},
		Spec:       corev1.PodSpec{ /* SA empty, NodeName empty */ },
	}
	provider := &stubProvider{pods: []*corev1.Pod{pod}}
	topo := &Topology{
		Nodes: []Node{{ID: "pod/demo/lone", Kind: KindPod, Name: "lone"}},
	}

	rel := GetRelationships("Pod", "demo", "lone", topo, provider, nil)
	if rel != nil {
		t.Errorf("expected nil for pod with no edges and no hygiene data, got %+v", rel)
	}
}

// TestGetRelationships_IncomingEdgeProtects_DispatchesByKind verifies that
// incoming "protects" edges split into rel.PDBs vs rel.NetworkPolicies based
// on the source kind.
//
// (Cluster-scoped NetworkPolicy variants like ClusterNetworkPolicy and
// CiliumClusterwideNetworkPolicy use a 2-segment node ID — parseNodeID
// rejects those today, so they never reach this dispatch. Pre-existing
// behavior, out of scope here.)
func TestGetRelationships_IncomingEdgeProtects_DispatchesByKind(t *testing.T) {
	topo := &Topology{
		Nodes: []Node{
			{ID: "deployment/demo/web", Kind: KindDeployment, Name: "web"},
			{ID: "poddisruptionbudget/demo/web-pdb", Kind: KindPDB, Name: "web-pdb"},
			{ID: "networkpolicy/demo/web-np", Kind: KindNetworkPolicy, Name: "web-np"},
			{ID: "ciliumnetworkpolicy/demo/web-cnp", Kind: KindCiliumNetworkPolicy, Name: "web-cnp"},
		},
		Edges: []Edge{
			{ID: "pdb-to-web", Source: "poddisruptionbudget/demo/web-pdb", Target: "deployment/demo/web", Type: EdgeProtects},
			{ID: "np-to-web", Source: "networkpolicy/demo/web-np", Target: "deployment/demo/web", Type: EdgeProtects},
			{ID: "cnp-to-web", Source: "ciliumnetworkpolicy/demo/web-cnp", Target: "deployment/demo/web", Type: EdgeProtects},
		},
	}

	rel := GetRelationships("Deployment", "demo", "web", topo, nil, nil)
	if rel == nil {
		t.Fatal("GetRelationships returned nil for deployment with 3 incoming protects edges")
	}

	if len(rel.PDBs) != 1 || rel.PDBs[0].Kind != "PodDisruptionBudget" || rel.PDBs[0].Name != "web-pdb" {
		t.Errorf("rel.PDBs: want [PodDisruptionBudget/web-pdb], got %+v", rel.PDBs)
	}

	if len(rel.NetworkPolicies) != 2 {
		t.Fatalf("rel.NetworkPolicies: want 2 entries (NetworkPolicy + CiliumNetworkPolicy), got %d (%+v)", len(rel.NetworkPolicies), rel.NetworkPolicies)
	}
	gotKinds := make(map[string]bool, 2)
	for _, ref := range rel.NetworkPolicies {
		gotKinds[ref.Kind] = true
	}
	for _, expected := range []string{"NetworkPolicy", "CiliumNetworkPolicy"} {
		if !gotKinds[expected] {
			t.Errorf("rel.NetworkPolicies missing %s; got kinds=%v", expected, gotKinds)
		}
	}
}

func TestGetRelationships_IncomingEdgeUsesSplitsScalersFromStorage(t *testing.T) {
	topo := &Topology{
		Nodes: []Node{
			{ID: "deployment/demo/web", Kind: KindDeployment, Name: "web"},
			{ID: "horizontalpodautoscaler/demo/web-hpa", Kind: KindHPA, Name: "web-hpa"},
			{ID: "persistentvolumeclaim/demo/web-data", Kind: KindPVC, Name: "web-data"},
		},
		Edges: []Edge{
			{ID: "hpa-to-web", Source: "horizontalpodautoscaler/demo/web-hpa", Target: "deployment/demo/web", Type: EdgeUses},
			{ID: "pvc-to-web", Source: "persistentvolumeclaim/demo/web-data", Target: "deployment/demo/web", Type: EdgeUses},
		},
	}

	rel := GetRelationships("Deployment", "demo", "web", topo, nil, nil)
	if rel == nil {
		t.Fatal("GetRelationships returned nil for deployment with incoming uses edges")
	}
	if len(rel.Scalers) != 1 || rel.Scalers[0].Kind != "HorizontalPodAutoscaler" || rel.Scalers[0].Name != "web-hpa" {
		t.Errorf("rel.Scalers: want [HorizontalPodAutoscaler/web-hpa], got %+v", rel.Scalers)
	}
	if len(rel.StorageRefs) != 1 || rel.StorageRefs[0].Kind != "PersistentVolumeClaim" || rel.StorageRefs[0].Name != "web-data" {
		t.Errorf("rel.StorageRefs: want [PersistentVolumeClaim/web-data], got %+v", rel.StorageRefs)
	}

	pvcRel := GetRelationships("PersistentVolumeClaim", "demo", "web-data", topo, nil, nil)
	if pvcRel == nil {
		t.Fatal("GetRelationships returned nil for PVC with outgoing uses edge")
	}
	if pvcRel.ScaleTarget != nil {
		t.Errorf("PVC relationship should not expose a scale target, got %+v", pvcRel.ScaleTarget)
	}
	if len(pvcRel.Consumers) != 1 || pvcRel.Consumers[0].Kind != "Deployment" || pvcRel.Consumers[0].Name != "web" {
		t.Errorf("PVC consumers: want [Deployment/web], got %+v", pvcRel.Consumers)
	}
}

// TestGetRelationships_OutgoingEdgeProtects_NotSurfaced verifies that outgoing
// EdgeProtects edges (a PDB / NetworkPolicy / CiliumNetworkPolicy / etc. pointing
// at the workloads it protects) are intentionally NOT projected into the
// Relationships of the source resource. The PDBs / NetworkPolicies fields are
// reserved for the INCOMING-direction semantic ("things that act on me").
//
// Surfacing the outgoing direction requires a new Protects/SelectedWorkloads
// field, which is out of scope here. Until that field lands, querying a PDB
// or NetworkPolicy that has only outgoing protects edges returns nil.
//
// This also guards B1 (the old bug that wrote outgoing protects into
// rel.ScaleTarget) and the post-B1 over-fix (writing them into rel.PDBs,
// which conflated PDB-side and NP-side outgoing edges).
func TestGetRelationships_OutgoingEdgeProtects_NotSurfaced(t *testing.T) {
	cases := []struct {
		name       string
		queryKind  string
		queryName  string // must match the name component of sourceID below
		sourceID   string
		sourceKind NodeKind
	}{
		{"PDB outgoing", "PodDisruptionBudget", "web-pdb", "poddisruptionbudget/demo/web-pdb", KindPDB},
		{"NetworkPolicy outgoing", "NetworkPolicy", "deny-egress", "networkpolicy/demo/deny-egress", KindNetworkPolicy},
		{"CiliumNetworkPolicy outgoing", "CiliumNetworkPolicy", "cnp-1", "ciliumnetworkpolicy/demo/cnp-1", KindCiliumNetworkPolicy},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			topo := &Topology{
				Nodes: []Node{
					{ID: c.sourceID, Kind: c.sourceKind, Name: c.queryName},
					{ID: "deployment/demo/web", Kind: KindDeployment, Name: "web"},
					{ID: "deployment/demo/api", Kind: KindDeployment, Name: "api"},
				},
				Edges: []Edge{
					{ID: "src-to-web", Source: c.sourceID, Target: "deployment/demo/web", Type: EdgeProtects},
					{ID: "src-to-api", Source: c.sourceID, Target: "deployment/demo/api", Type: EdgeProtects},
				},
			}

			// Control: the SAME topology, queried from the workload side, MUST
			// surface the policy via incoming-EdgeProtects dispatch. If this
			// fails, the test below would pass for the wrong reason — the
			// edges or node IDs aren't matching at all. Catches the
			// vacuous-pass class of mistakes.
			incoming := GetRelationships("Deployment", "demo", "web", topo, nil, nil)
			if incoming == nil {
				t.Fatalf("control assertion failed: querying the target Deployment should surface the policy via incoming EdgeProtects, got nil relationships")
			}
			switch c.sourceKind {
			case KindPDB:
				if len(incoming.PDBs) == 0 {
					t.Fatalf("control: expected workload to see incoming PDB, got %+v", incoming)
				}
			case KindNetworkPolicy, KindCiliumNetworkPolicy:
				if len(incoming.NetworkPolicies) == 0 {
					t.Fatalf("control: expected workload to see incoming NetworkPolicy, got %+v", incoming)
				}
			}

			// Actual assertion: querying from the source policy side should
			// NOT surface its targets (outgoing direction intentionally
			// unsurfaced until a Protects[] field exists).
			rel := GetRelationships(c.queryKind, "demo", c.queryName, topo, nil, nil)
			if rel != nil {
				t.Errorf("want nil (outgoing protects intentionally not surfaced), got %+v", rel)
			}
		})
	}
}

// TestGetRelationships_NoProtects_FieldsOmitted ensures the new split fields
// stay nil when no protects edges exist, so JSON omitempty keeps the wire
// format identical for unrelated resources.
func TestGetRelationships_NoProtects_FieldsOmitted(t *testing.T) {
	topo := &Topology{
		Nodes: []Node{
			{ID: "deployment/demo/lone", Kind: KindDeployment, Name: "lone"},
			{ID: "replicaset/demo/lone-abc", Kind: KindReplicaSet, Name: "lone-abc"},
		},
		Edges: []Edge{
			{ID: "lone-rs", Source: "deployment/demo/lone", Target: "replicaset/demo/lone-abc", Type: EdgeManages},
		},
	}

	rel := GetRelationships("Deployment", "demo", "lone", topo, nil, nil)
	if rel == nil {
		t.Fatal("GetRelationships returned nil for deployment with a child")
	}
	if len(rel.PDBs) != 0 {
		t.Errorf("rel.PDBs: want empty, got %+v", rel.PDBs)
	}
	if len(rel.NetworkPolicies) != 0 {
		t.Errorf("rel.NetworkPolicies: want empty, got %+v", rel.NetworkPolicies)
	}
}
