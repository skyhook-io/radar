package topology

import (
	"fmt"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// clusterShape is a per-kind object census. Feeding it to newSyntheticProvider
// produces a ResourceProvider whose cardinality and reference structure match a
// real cluster, so Builder.Build costs here track what the field pays.
//
// fieldShape's numbers are the informer item counts reported by a production
// EKS cluster: 432 namespaces, 248 nodes, ~134k cached objects, on which the
// all-namespace relationship-cache build was measured at 12.9s p95 with 145,668
// graph nodes and 127,637 edges. detectLargeClusterAndOptimize's estimate over
// this census lands at ~95k, matching the estimatedNodes that cluster reported —
// TestSyntheticShapeMatchesField pins that correspondence, so keep the ratios
// intact when rescaling or the fixture stops predicting field behaviour.
//
// The same census drives internal/loadtest.LargeMultiTenantEKS, which seeds a
// fake clientset rather than a ResourceProvider. pkg is its own module and
// cannot import internal, so the counts are restated here; change one and
// change the other, or the two harnesses stop describing the same cluster.
type clusterShape struct {
	Namespaces      int
	Nodes           int
	Deployments     int
	ReplicaSets     int
	Pods            int
	StatefulSets    int
	DaemonSets      int
	Services        int
	Ingresses       int
	Jobs            int
	CronJobs        int
	ConfigMaps      int
	Secrets         int
	PVCs            int
	HPAs            int
	PDBs            int
	NetworkPolicies int
	ServiceAccounts int
}

func fieldShape() clusterShape {
	return clusterShape{
		Namespaces:      432,
		Nodes:           248,
		Deployments:     19924,
		ReplicaSets:     43033,
		Pods:            29455,
		StatefulSets:    388,
		DaemonSets:      9,
		Services:        23547,
		Ingresses:       16977,
		Jobs:            19861,
		CronJobs:        4291,
		ConfigMaps:      3894,
		Secrets:         26668,
		PVCs:            787,
		HPAs:            385,
		PDBs:            12,
		NetworkPolicies: 1480,
		ServiceAccounts: 545,
	}
}

// fraction scales every kind by num/den, holding per-namespace density constant
// (namespaces scale too) so the curve across fractions isolates growth in total
// object count rather than in namespace width.
func (s clusterShape) fraction(num, den int) clusterShape {
	scale := func(v int) int {
		scaled := v * num / den
		if v > 0 && scaled == 0 {
			return 1
		}
		return scaled
	}
	return clusterShape{
		Namespaces:      scale(s.Namespaces),
		Nodes:           scale(s.Nodes),
		Deployments:     scale(s.Deployments),
		ReplicaSets:     scale(s.ReplicaSets),
		Pods:            scale(s.Pods),
		StatefulSets:    scale(s.StatefulSets),
		DaemonSets:      scale(s.DaemonSets),
		Services:        scale(s.Services),
		Ingresses:       scale(s.Ingresses),
		Jobs:            scale(s.Jobs),
		CronJobs:        scale(s.CronJobs),
		ConfigMaps:      scale(s.ConfigMaps),
		Secrets:         scale(s.Secrets),
		PVCs:            scale(s.PVCs),
		HPAs:            scale(s.HPAs),
		PDBs:            scale(s.PDBs),
		NetworkPolicies: scale(s.NetworkPolicies),
		ServiceAccounts: scale(s.ServiceAccounts),
	}
}

func (s clusterShape) totalObjects() int {
	return s.Nodes + s.Deployments + s.ReplicaSets + s.Pods + s.StatefulSets +
		s.DaemonSets + s.Services + s.Ingresses + s.Jobs + s.CronJobs +
		s.ConfigMaps + s.Secrets + s.PVCs + s.HPAs + s.PDBs +
		s.NetworkPolicies + s.ServiceAccounts
}

// synthNames threads the naming scheme through every generator. Objects only
// wire together when their namespace, labels and selectors agree, so the
// builder's selector-matching and owner-ref paths stay on their real hot loops
// instead of short-circuiting on a graph of orphans.
type synthNames struct{ shape clusterShape }

func (n synthNames) namespace(i int) string {
	return fmt.Sprintf("ns-%04d", i%n.shape.Namespaces)
}

func (n synthNames) app(i int) string {
	return fmt.Sprintf("app-%04d", i/n.shape.Namespaces)
}

func (n synthNames) appLabels(i int) map[string]string {
	return map[string]string{
		"app":                          n.app(i),
		"app.kubernetes.io/managed-by": "Helm",
		"team":                         fmt.Sprintf("team-%d", i%8),
	}
}

// podSpec carries the ConfigMap/Secret/PVC/ServiceAccount references that
// extractWorkloadReferences walks. Reference names are drawn from the same
// pools the config generators use, so a realistic fraction resolve to a real
// node and the rest exercise the dangling-reference path.
func (n synthNames) podSpec(i int) corev1.PodSpec {
	cm := fmt.Sprintf("cfg-%04d", i%max(n.shape.ConfigMaps, 1))
	secret := fmt.Sprintf("sec-%04d", i%max(n.shape.Secrets, 1))
	pvc := fmt.Sprintf("data-%04d", i%max(n.shape.PVCs, 1))
	return corev1.PodSpec{
		ServiceAccountName: fmt.Sprintf("sa-%04d", i%max(n.shape.ServiceAccounts, 1)),
		NodeName:           fmt.Sprintf("node-%04d", i%max(n.shape.Nodes, 1)),
		Containers: []corev1.Container{{
			Name:  "app",
			Image: "registry.k8s.io/pause:3.9",
			EnvFrom: []corev1.EnvFromSource{
				{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: cm}}},
				{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: secret}}},
			},
			Env: []corev1.EnvVar{{
				Name: "DB_PASSWORD",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: secret},
						Key:                  "password",
					},
				},
			}},
		}},
		Volumes: []corev1.Volume{{
			Name: "data",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvc},
			},
		}},
	}
}

func newSyntheticProvider(s clusterShape) *mockProvider {
	n := synthNames{shape: s}
	p := &mockProvider{}
	controller := true

	for i := range s.Nodes {
		p.nodes = append(p.nodes, &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("node-%04d", i)},
			Status: corev1.NodeStatus{
				Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
			},
		})
	}

	for i := range s.ServiceAccounts {
		p.serviceAccounts = append(p.serviceAccounts, &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("sa-%04d", i), Namespace: n.namespace(i)},
		})
	}

	for i := range s.ConfigMaps {
		p.configMaps = append(p.configMaps, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("cfg-%04d", i), Namespace: n.namespace(i)},
			Data:       map[string]string{"LOG_LEVEL": "info"},
		})
	}

	for i := range s.Secrets {
		p.secrets = append(p.secrets, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("sec-%04d", i), Namespace: n.namespace(i)},
			Type:       corev1.SecretTypeOpaque,
		})
	}

	for i := range s.PVCs {
		p.pvcs = append(p.pvcs, &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("data-%04d", i), Namespace: n.namespace(i)},
			Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
		})
	}

	replicas := int32(3)
	for i := range s.Deployments {
		p.deployments = append(p.deployments, &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      n.app(i),
				Namespace: n.namespace(i),
				Labels:    n.appLabels(i),
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": n.app(i)}},
				Strategy: appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: n.appLabels(i)},
					Spec:       n.podSpec(i),
				},
			},
			Status: appsv1.DeploymentStatus{ReadyReplicas: replicas, Replicas: replicas},
		})
	}

	for i := range s.StatefulSets {
		p.statefulSets = append(p.statefulSets, &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("sts-%04d", i/max(s.Namespaces, 1)),
				Namespace: n.namespace(i),
				Labels:    n.appLabels(i),
			},
			Spec: appsv1.StatefulSetSpec{
				Replicas: &replicas,
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": n.app(i)}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: n.appLabels(i)},
					Spec:       n.podSpec(i),
				},
			},
			Status: appsv1.StatefulSetStatus{ReadyReplicas: replicas, Replicas: replicas},
		})
	}

	for i := range s.DaemonSets {
		p.daemonSets = append(p.daemonSets, &appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("ds-%04d", i/max(s.Namespaces, 1)),
				Namespace: n.namespace(i),
				Labels:    n.appLabels(i),
			},
			Spec: appsv1.DaemonSetSpec{
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": n.app(i)}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: n.appLabels(i)},
					Spec:       n.podSpec(i),
				},
			},
		})
	}

	// Each ReplicaSet is a revision of the Deployment it wraps; the field census
	// carries ~2.2 per Deployment, which is what keeps stale revisions in the
	// graph long after a rollout finished.
	rsOwner := make([]int, s.ReplicaSets)
	for i := range s.ReplicaSets {
		owner := i % max(s.Deployments, 1)
		rsOwner[i] = owner
		revision := i / max(s.Deployments, 1)
		hash := fmt.Sprintf("%06d", i)
		rsLabels := n.appLabels(owner)
		rsLabels["pod-template-hash"] = hash
		p.replicaSets = append(p.replicaSets, &appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("%s-%s", n.app(owner), hash),
				Namespace: n.namespace(owner),
				Labels:    rsLabels,
				Annotations: map[string]string{
					"deployment.kubernetes.io/revision": fmt.Sprintf("%d", revision+1),
				},
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Name:       n.app(owner),
					Controller: &controller,
				}},
			},
			Spec: appsv1.ReplicaSetSpec{
				Replicas: &replicas,
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": n.app(owner), "pod-template-hash": hash}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: rsLabels},
					Spec:       n.podSpec(owner),
				},
			},
			Status: appsv1.ReplicaSetStatus{ReadyReplicas: replicas, Replicas: replicas},
		})
	}

	for i := range s.Pods {
		rsIdx := i % max(s.ReplicaSets, 1)
		rs := p.replicaSets[rsIdx]
		podLabels := map[string]string{}
		for k, v := range rs.Labels {
			podLabels[k] = v
		}
		p.pods = append(p.pods, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("%s-%05d", rs.Name, i),
				Namespace: rs.Namespace,
				Labels:    podLabels,
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "apps/v1",
					Kind:       "ReplicaSet",
					Name:       rs.Name,
					Controller: &controller,
				}},
			},
			Spec: n.podSpec(rsOwner[rsIdx]),
			Status: corev1.PodStatus{
				Phase:      corev1.PodRunning,
				Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
				ContainerStatuses: []corev1.ContainerStatus{{
					Name:  "app",
					Ready: true,
					State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
				}},
			},
		})
	}

	for i := range s.Services {
		p.services = append(p.services, &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("svc-%04d", i/max(s.Namespaces, 1)),
				Namespace: n.namespace(i),
				Labels:    n.appLabels(i),
			},
			Spec: corev1.ServiceSpec{
				Type:     corev1.ServiceTypeClusterIP,
				Selector: map[string]string{"app": n.app(i)},
				Ports:    []corev1.ServicePort{{Port: 8080, Protocol: corev1.ProtocolTCP}},
			},
		})
	}

	for i := range s.Ingresses {
		svcName := fmt.Sprintf("svc-%04d", (i/max(s.Namespaces, 1))%max(s.Services/max(s.Namespaces, 1), 1))
		pathType := networkingv1.PathTypePrefix
		p.ingresses = append(p.ingresses, &networkingv1.Ingress{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("ing-%04d", i/max(s.Namespaces, 1)),
				Namespace: n.namespace(i),
			},
			Spec: networkingv1.IngressSpec{
				Rules: []networkingv1.IngressRule{{
					Host: fmt.Sprintf("%s.%s.example.com", svcName, n.namespace(i)),
					IngressRuleValue: networkingv1.IngressRuleValue{
						HTTP: &networkingv1.HTTPIngressRuleValue{
							Paths: []networkingv1.HTTPIngressPath{{
								Path:     "/",
								PathType: &pathType,
								Backend: networkingv1.IngressBackend{
									Service: &networkingv1.IngressServiceBackend{
										Name: svcName,
										Port: networkingv1.ServiceBackendPort{Number: 8080},
									},
								},
							}},
						},
					},
				}},
			},
		})
	}

	for i := range s.CronJobs {
		p.cronJobs = append(p.cronJobs, &batchv1.CronJob{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("cron-%04d", i/max(s.Namespaces, 1)),
				Namespace: n.namespace(i),
			},
			Spec: batchv1.CronJobSpec{
				Schedule: "*/5 * * * *",
				JobTemplate: batchv1.JobTemplateSpec{
					Spec: batchv1.JobSpec{
						Template: corev1.PodTemplateSpec{Spec: n.podSpec(i)},
					},
				},
			},
		})
	}

	// Most Jobs in the field census are CronJob runs; the remainder are one-off
	// Jobs with no owner. Both shapes reach different owner-edge branches.
	for i := range s.Jobs {
		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("job-%06d", i),
				Namespace: n.namespace(i),
			},
			Spec: batchv1.JobSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: n.appLabels(i)},
					Spec:       n.podSpec(i),
				},
			},
			Status: batchv1.JobStatus{Succeeded: 1},
		}
		if s.CronJobs > 0 && i%4 != 0 {
			cronName := fmt.Sprintf("cron-%04d", (i/max(s.Namespaces, 1))%max(s.CronJobs/max(s.Namespaces, 1), 1))
			job.OwnerReferences = []metav1.OwnerReference{{
				APIVersion: "batch/v1",
				Kind:       "CronJob",
				Name:       cronName,
				Controller: &controller,
			}}
		}
		p.jobs = append(p.jobs, job)
	}

	for i := range s.HPAs {
		minReplicas := int32(1)
		p.hpas = append(p.hpas, &autoscalingv2.HorizontalPodAutoscaler{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("hpa-%04d", i/max(s.Namespaces, 1)),
				Namespace: n.namespace(i),
			},
			Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
				ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Name:       n.app(i),
				},
				MinReplicas: &minReplicas,
				MaxReplicas: 20,
			},
		})
	}

	for i := range s.PDBs {
		p.pdbs = append(p.pdbs, &policyv1.PodDisruptionBudget{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("pdb-%04d", i/max(s.Namespaces, 1)),
				Namespace: n.namespace(i),
			},
			Spec: policyv1.PodDisruptionBudgetSpec{
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": n.app(i)}},
			},
		})
	}

	for i := range s.NetworkPolicies {
		p.networkPolicies = append(p.networkPolicies, &networkingv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("np-%04d", i/max(s.Namespaces, 1)),
				Namespace: n.namespace(i),
			},
			Spec: networkingv1.NetworkPolicySpec{
				PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": n.app(i)}},
				PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			},
		})
	}

	return p
}

var scaleFractions = []struct {
	name     string
	num, den int
}{
	{"sixteenth", 1, 16},
	{"eighth", 1, 8},
	{"quarter", 1, 4},
	{"half", 1, 2},
	{"full", 1, 1},
}

// BenchmarkRelationshipCacheBuild measures the all-namespace build the SSE
// broadcaster runs on every debounce fire once warmup ends. The guard that
// spares the user-facing topology does not apply here, so the cost is whatever
// the full object census makes it — walk the fractions to see how that scales.
func BenchmarkRelationshipCacheBuild(b *testing.B) {
	for _, f := range scaleFractions {
		shape := fieldShape().fraction(f.num, f.den)
		b.Run(f.name, func(b *testing.B) {
			provider := newSyntheticProvider(shape)
			builder := NewBuilder(provider)
			opts := relationshipCacheOptions()

			topo, err := builder.Build(opts)
			if err != nil {
				b.Fatalf("build: %v", err)
			}

			b.ReportAllocs()
			for b.Loop() {
				if _, err := builder.Build(opts); err != nil {
					b.Fatalf("build: %v", err)
				}
			}

			b.ReportMetric(float64(shape.totalObjects()), "objects")
			b.ReportMetric(float64(topo.EstimatedNodes), "estimate")
			b.ReportMetric(float64(len(topo.Nodes)), "graphnodes")
			b.ReportMetric(float64(len(topo.Edges)), "graphedges")
		})
	}
}

// BenchmarkScopedRelationshipBuild measures one namespace of the field census
// through the relationship-build options. This is what a request pays when
// relationship lookups stop reading a prebuilt cluster-wide graph and build
// their own scoped one instead, so it is the number that decides whether that
// trade is affordable on a request path.
//
// Note what does NOT get cheaper: the builder filters inside its walk rather
// than indexing by namespace up front, so every informer list is still visited
// in full. Only node and edge production shrink.
func BenchmarkScopedRelationshipBuild(b *testing.B) {
	shape := fieldShape()
	provider := newSyntheticProvider(shape)
	builder := NewBuilder(provider)
	opts := relationshipCacheOptions()
	opts.Namespaces = []string{synthNames{shape: shape}.namespace(0)}

	topo, err := builder.Build(opts)
	if err != nil {
		b.Fatalf("build: %v", err)
	}

	b.ReportAllocs()
	for b.Loop() {
		if _, err := builder.Build(opts); err != nil {
			b.Fatalf("build: %v", err)
		}
	}

	b.ReportMetric(float64(len(topo.Nodes)), "graphnodes")
	b.ReportMetric(float64(len(topo.Edges)), "graphedges")
}

// BenchmarkGuardedTopologyBuild measures the same census through the path a
// browser actually takes. The large-cluster guard short-circuits before any
// graph work, so this is the budget the relationship-cache build is missing.
func BenchmarkGuardedTopologyBuild(b *testing.B) {
	shape := fieldShape()
	provider := newSyntheticProvider(shape)
	builder := NewBuilder(provider)
	opts := DefaultBuildOptions()
	opts.ViewMode = ViewModeResources

	topo, err := builder.Build(opts)
	if err != nil {
		b.Fatalf("build: %v", err)
	}
	if !topo.RequiresNamespaceFilter {
		b.Fatalf("expected the guard to fire on the field census, got a full build")
	}

	b.ReportAllocs()
	for b.Loop() {
		if _, err := builder.Build(opts); err != nil {
			b.Fatalf("build: %v", err)
		}
	}

	b.ReportMetric(float64(topo.EstimatedNodes), "estimate")
	b.ReportMetric(float64(len(topo.Nodes)), "graphnodes")
}

// TestSyntheticShapeMatchesField keeps the fixture honest: the estimate the
// builder derives from this census has to stay near what the real cluster
// reported, otherwise the benchmarks above stop predicting anything. The field
// snapshot reported ~95.6k estimated nodes; allow 10% drift for generator
// rounding, and fail loudly on anything larger.
func TestSyntheticShapeMatchesField(t *testing.T) {
	const fieldEstimate = 95594

	provider := newSyntheticProvider(fieldShape())
	builder := NewBuilder(provider)

	topo, err := builder.Build(DefaultBuildOptions())
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	if !topo.RequiresNamespaceFilter {
		t.Errorf("field census did not trip the large-cluster guard")
	}

	drift := topo.EstimatedNodes - fieldEstimate
	if drift < 0 {
		drift = -drift
	}
	if drift*10 > fieldEstimate {
		t.Errorf("estimated nodes = %d, field reported %d — more than 10%% apart. Either the "+
			"census above drifted, or estimateNodeCount changed what it counts; if the latter, "+
			"re-derive fieldEstimate from a current cluster rather than nudging it to pass",
			topo.EstimatedNodes, fieldEstimate)
	}
}

// TestRelationshipBuildScopeBoundsTheGraph pins what makes relationship lookups
// affordable on a large cluster: the namespace argument. ForRelationshipCache
// waives the large-cluster guard, so an unscoped build walks the cluster no
// matter how big it is — that is correct for the callers that need cluster-wide
// reach (cascade-delete preview, whole-graph counts) and ruinous for the ones
// that don't. A scoped build has to stay proportional to its namespace, not to
// the cluster.
//
// Quarter scale: the relationship is structural, not scale-dependent, and the
// full census costs ~25x more for the same signal. Benchmark at full scale.
func TestRelationshipBuildScopeBoundsTheGraph(t *testing.T) {
	shape := fieldShape().fraction(1, 4)
	provider := newSyntheticProvider(shape)
	builder := NewBuilder(provider)

	guarded, err := builder.Build(DefaultBuildOptions())
	if err != nil {
		t.Fatalf("guarded build: %v", err)
	}
	if len(guarded.Nodes) != 0 || !guarded.RequiresNamespaceFilter {
		t.Fatalf("guarded build produced %d nodes, expected the stub", len(guarded.Nodes))
	}

	clusterWide, err := builder.Build(relationshipCacheOptions())
	if err != nil {
		t.Fatalf("cluster-wide relationship build: %v", err)
	}

	scopedOpts := relationshipCacheOptions()
	scopedOpts.Namespaces = []string{synthNames{shape: shape}.namespace(0)}
	scoped, err := builder.Build(scopedOpts)
	if err != nil {
		t.Fatalf("scoped relationship build: %v", err)
	}

	// Every node the scoped build emits has to belong to the namespace asked
	// for, or a "related resources" answer would leak across tenants.
	wantNS := scopedOpts.Namespaces[0]
	for _, n := range scoped.Nodes {
		if ns, _ := n.Data["namespace"].(string); ns != "" && ns != wantNS {
			t.Fatalf("scoped build emitted node %q from namespace %q, want only %q", n.ID, ns, wantNS)
		}
	}

	// The cluster has shape.Namespaces namespaces of roughly equal size, so a
	// single-namespace build must land near 1/N of the whole graph. Allow 4x
	// headroom for cluster-scoped nodes and uneven distribution; the point is
	// to catch a scoped build that silently walks everything, not to pin a ratio.
	maxScoped := 4 * len(clusterWide.Nodes) / shape.Namespaces
	if len(scoped.Nodes) > maxScoped {
		t.Errorf("scoped build produced %d nodes against %d cluster-wide across %d namespaces "+
			"(budget %d) — the namespace scope is not bounding the walk",
			len(scoped.Nodes), len(clusterWide.Nodes), shape.Namespaces, maxScoped)
	}

	t.Logf("guarded: %d nodes — cluster-wide: %d nodes / %d edges — scoped to %s: %d nodes / %d edges",
		len(guarded.Nodes), len(clusterWide.Nodes), len(clusterWide.Edges), wantNS, len(scoped.Nodes), len(scoped.Edges))
}

// BenchmarkRelationshipCacheHighReplicaPods is the replica-dense shape the
// field census lacks: 2,000 Deployments of 25 Pods each, 50,000 Pods in all.
// Every Pod stays an individual node in the relationship cache.
func BenchmarkRelationshipCacheHighReplicaPods(b *testing.B) {
	ctrl := true
	provider := &mockProvider{}
	for d := range 2000 {
		ns := fmt.Sprintf("team-%d", d%50)
		dep, rs := fmt.Sprintf("app-%d", d), fmt.Sprintf("app-%d-abc", d)
		provider.deployments = append(provider.deployments, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: dep, Namespace: ns, UID: types.UID(dep)}})
		replicas := int32(25)
		provider.replicaSets = append(provider.replicaSets, &appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{Name: rs, Namespace: ns, UID: types.UID(rs), OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: dep, UID: types.UID(dep), Controller: &ctrl}}},
			Spec:       appsv1.ReplicaSetSpec{Replicas: &replicas},
		})
		for p := range 25 {
			name := fmt.Sprintf("%s-%d", rs, p)
			provider.pods = append(provider.pods, &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID(name), Labels: map[string]string{"app": dep},
					OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: rs, UID: types.UID(rs), Controller: &ctrl}}},
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			})
		}
	}
	builder := NewBuilder(provider)
	opts := relationshipCacheOptions()
	topo, err := builder.Build(opts)
	if err != nil {
		b.Fatalf("build: %v", err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := builder.Build(opts); err != nil {
			b.Fatalf("build: %v", err)
		}
	}
	b.ReportMetric(float64(len(topo.Nodes)), "graphnodes")
	b.ReportMetric(float64(len(topo.Edges)), "graphedges")
}
