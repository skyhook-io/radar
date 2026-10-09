package cnpg

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"sort"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"

	auth "github.com/skyhook-io/radar/internal/auth"
	integration "github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
)

const (
	cnpgHAStateOK           = "ok"
	cnpgHAStateDenied       = "denied"
	cnpgHAStateNotFound     = "notFound"
	cnpgHAStateNotInstalled = "notInstalled"
	cnpgHAStateUnavailable  = "unavailable"
	cnpgHAStateError        = "error"

	jobRoleLabel           = "cnpg.io/jobRole"
	cnpgZoneLabel          = "topology.kubernetes.io/zone"
	cnpgOperatorLeaseName  = "db9c8771.cnpg.io"
	cnpgFailoverQuorumAnno = "alpha.cnpg.io/failoverQuorum"
	cnpgHAReadTimeout      = 5 * time.Second

	certManagerCertificateAnno = "cert-manager.io/certificate-name"
	certManagerIssuerAnno      = "cert-manager.io/issuer-name"
	certManagerIssuerKindAnno  = "cert-manager.io/issuer-kind"
)

var (
	cnpgFailoverQuorumGVR = schema.GroupVersionResource{Group: Group, Version: "v1", Resource: "failoverquorums"}
	cnpgSecretsGVR        = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
)

type CNPGClusterHAResponse struct {
	Cluster           CNPGRuntimeObjectRef   `json:"cluster"`
	SampledAt         string                 `json:"sampledAt"`
	DesiredImage      string                 `json:"desiredImage,omitempty"`
	DeclaredInstances *int64                 `json:"declaredInstances,omitempty"`
	ExpectedInstances []string               `json:"expectedInstances"`
	Instances         []CNPGHAInstance       `json:"instances"`
	Pods              integration.ReadSource `json:"pods"`
	Nodes             integration.ReadSource `json:"nodes"`
	Quorum            CNPGHAQuorum           `json:"quorum"`
	PDBs              CNPGHAPDBs             `json:"pdbs"`
	PrimaryLease      CNPGHALease            `json:"primaryLease"`
	OperatorLease     CNPGHALease            `json:"operatorLease"`
	Jobs              CNPGHAJobs             `json:"jobs"`
	RWEndpoints       CNPGHAEndpoints        `json:"rwEndpoints"`
	Certificates      []CNPGHACertificate    `json:"certificates"`
	Maintenance       CNPGMaintenanceFacts   `json:"maintenance"`
}

// CNPGHAInstance is one instance Pod. Zone is empty when the Node is not
// readable or carries no zone label (Nodes says which). PostgresStartedAt is
// the postgres container's current start: a Pod restart moves it, an in-place
// PostgreSQL restart does not.
type CNPGHAInstance struct {
	Pod               string `json:"pod"`
	PodUID            string `json:"podUID"`
	Role              string `json:"role"`
	Ready             bool   `json:"ready"`
	Node              string `json:"node,omitempty"`
	Zone              string `json:"zone,omitempty"`
	QOSClass          string `json:"qosClass,omitempty"`
	Image             string `json:"image,omitempty"`
	ImageMatches      *bool  `json:"imageMatches,omitempty"`
	PodCreatedAt      string `json:"podCreatedAt,omitempty"`
	PostgresStartedAt string `json:"postgresStartedAt,omitempty"`
	RestartCount      int32  `json:"restartCount"`
}

// CNPGHAQuorum is the recorded synchronous-replication configuration, not a
// verdict. N is the potentially synchronous standbys, W the standbys a commit
// waits for, R the promotable ones (in the cluster and ready now). Holds is
// R + W > N, and is absent when any term is unknown.
type CNPGHAQuorum struct {
	Enabled        bool                   `json:"enabled"`
	EnabledBy      string                 `json:"enabledBy,omitempty"`
	Method         string                 `json:"method,omitempty"`
	Number         *int64                 `json:"number,omitempty"`
	DataDurability string                 `json:"dataDurability,omitempty"`
	Object         integration.ReadSource `json:"object"`
	Status         *CNPGHAQuorumStatus    `json:"status,omitempty"`
	N              *int                   `json:"n,omitempty"`
	W              *int                   `json:"w,omitempty"`
	R              *int                   `json:"r,omitempty"`
	Promotable     []string               `json:"promotable,omitempty"`
	Holds          *bool                  `json:"holds,omitempty"`
}

type CNPGHAQuorumStatus struct {
	Method        string   `json:"method,omitempty"`
	StandbyNames  []string `json:"standbyNames"`
	StandbyNumber int      `json:"standbyNumber"`
	Primary       string   `json:"primary,omitempty"`
}

// CNPGHAPDBs are the budgets the operator owns for this Cluster. Enabled is
// spec.enablePDB (default true); an absent budget under enablePDB false is
// the declared state, not a fault.
type CNPGHAPDBs struct {
	integration.ReadSource
	Enabled bool        `json:"enabled"`
	Items   []CNPGHAPDB `json:"items"`
}

type CNPGHAPDB struct {
	Name               string `json:"name"`
	Role               string `json:"role"`
	MinAvailable       string `json:"minAvailable,omitempty"`
	MaxUnavailable     string `json:"maxUnavailable,omitempty"`
	ExpectedPods       int32  `json:"expectedPods"`
	CurrentHealthy     int32  `json:"currentHealthy"`
	DesiredHealthy     int32  `json:"desiredHealthy"`
	DisruptionsAllowed int32  `json:"disruptionsAllowed"`
	// Observed is false when the disruption controller has not caught up with
	// the budget's current generation; its numbers are then stale.
	Observed bool `json:"observed"`
}

// CNPGHALease is one Lease. Expired compares renewTime + duration with now.
type CNPGHALease struct {
	integration.ReadSource
	Namespace           string `json:"namespace,omitempty"`
	Name                string `json:"name,omitempty"`
	Holder              string `json:"holder,omitempty"`
	RenewTime           string `json:"renewTime,omitempty"`
	DurationSeconds     *int32 `json:"durationSeconds,omitempty"`
	Expired             *bool  `json:"expired,omitempty"`
	ControlledByCluster *bool  `json:"controlledByCluster,omitempty"`
}

type CNPGHAJobs struct {
	integration.ReadSource
	Items []CNPGHAJob `json:"items"`
}

// CNPGHAJob Phase: active | succeeded | failed | pending.
type CNPGHAJob struct {
	Name           string `json:"name"`
	Role           string `json:"role,omitempty"`
	Instance       string `json:"instance,omitempty"`
	Phase          string `json:"phase"`
	Reason         string `json:"reason,omitempty"`
	StartTime      string `json:"startTime,omitempty"`
	CompletionTime string `json:"completionTime,omitempty"`
}

// CNPGHAEndpoints are the ready endpoints behind the -rw Service, named by Pod.
type CNPGHAEndpoints struct {
	integration.ReadSource
	Service string   `json:"service"`
	Pods    []string `json:"pods"`
}

// CNPGHACertificate is one entry of status.certificates.expirations. ExpiresAt
// is RFC3339 when the operator's value parsed; Raw is always what it wrote.
// Renewal: operator (generated by CloudNativePG) | user (named in
// spec.certificates). CertManager is set only when the Secret's metadata was
// read and carries cert-manager's annotations; Metadata says whether it was.
type CNPGHACertificate struct {
	Secret      string                  `json:"secret"`
	Purposes    []string                `json:"purposes,omitempty"`
	Raw         string                  `json:"raw"`
	ExpiresAt   string                  `json:"expiresAt,omitempty"`
	Renewal     string                  `json:"renewal"`
	Metadata    *integration.ReadSource `json:"metadata,omitempty"`
	CertManager *CNPGCertManagerRef     `json:"certManager,omitempty"`
}

type CNPGCertManagerRef struct {
	Certificate string `json:"certificate"`
	Issuer      string `json:"issuer,omitempty"`
	IssuerKind  string `json:"issuerKind,omitempty"`
}

type HAClients struct {
	Typed    kubernetes.Interface
	Dynamic  dynamic.Interface
	Metadata metadata.Interface
}

func (s *Reader) ClusterHA(callerCtx context.Context, c HAClients, cache *k8s.ResourceCache, cluster *unstructured.Unstructured, now time.Time) CNPGClusterHAResponse {
	namespace, name := cluster.GetNamespace(), cluster.GetName()
	ctx, cancel := context.WithTimeout(callerCtx, cnpgHAReadTimeout)
	defer cancel()

	resp := CNPGClusterHAResponse{
		Cluster:      CNPGRuntimeObjectRef{Namespace: namespace, Name: name, UID: cluster.GetUID()},
		SampledAt:    now.Format(time.RFC3339),
		DesiredImage: cnpgDesiredImage(cluster),
		Instances:    []CNPGHAInstance{},
		Certificates: []CNPGHACertificate{},
		Maintenance:  cnpgMaintenanceFactsOf(cluster),
	}

	if n, found, _ := unstructured.NestedInt64(cluster.Object, "spec", "instances"); found {
		resp.DeclaredInstances = &n
	}
	resp.ExpectedInstances = append([]string{}, cnpgStatusStrings(cluster, "instanceNames")...)

	var pods []*corev1.Pod
	if s.Access.CanRead(callerCtx, "", "pods", namespace, "list") {
		var err error
		pods, err = clusterInstancePods(cache, cluster)
		if err != nil {
			resp.Pods = integration.ReadSource{State: cnpgHAStateUnavailable, Reason: err.Error()}
		} else {
			resp.Pods = integration.ReadSource{State: cnpgHAStateOK}
		}
	} else {
		resp.Pods = cnpgHADenied(auth.Grant{Verb: "list", Resource: "pods"}, namespace)
	}
	resp.Nodes = s.hANodesSource(callerCtx, cache)
	resp.Instances = cnpgHAInstances(cache, pods, resp.DesiredImage, resp.Nodes.State == cnpgHAStateOK)

	resp.Quorum = s.hAQuorum(ctx, callerCtx, c, cluster, pods, resp.Pods.State == cnpgHAStateOK)
	resp.PDBs = s.hAPDBs(callerCtx, cache, cluster)
	resp.PrimaryLease = s.hAPrimaryLease(ctx, callerCtx, c, cluster, now)
	resp.OperatorLease = s.hAOperatorLease(ctx, callerCtx, c, cache, now)
	resp.Jobs = s.hAJobs(callerCtx, cache, cluster)
	for _, job := range resp.Jobs.Items {
		if job.Instance != "" && (job.Role == "join" || job.Role == "initdb") && (job.Phase == "active" || job.Phase == "pending") {
			resp.ExpectedInstances = append(resp.ExpectedInstances, job.Instance)
		}
	}
	slices.Sort(resp.ExpectedInstances)
	resp.ExpectedInstances = slices.Compact(resp.ExpectedInstances)
	resp.RWEndpoints = s.hARWEndpoints(ctx, callerCtx, c, cluster)
	resp.Certificates = s.hACertificates(ctx, callerCtx, c, cluster)
	return resp
}

func cnpgHADenied(g auth.Grant, namespace string) integration.ReadSource {
	g = g.In(namespace)
	return integration.ReadSource{State: cnpgHAStateDenied, Grant: g.Ref(), Reason: "You are not allowed to " + g.String()}
}

func cnpgHAClusterDenied(g auth.Grant) integration.ReadSource {
	return integration.ReadSource{State: cnpgHAStateDenied, Grant: g.Ref(), Reason: "You are not allowed to " + g.String()}
}

// cnpgHAReadError classifies an impersonated read's failure. NotFound is left
// to the caller: its meaning differs per object.
func cnpgHAReadError(err error, g auth.Grant, namespace string) integration.ReadSource {
	switch {
	case apierrors.IsForbidden(err):
		return cnpgHADenied(g, namespace)
	case apierrors.IsNotFound(err):
		return integration.ReadSource{State: cnpgHAStateNotFound}
	case errors.Is(err, context.DeadlineExceeded) || apierrors.IsTimeout(err):
		return integration.ReadSource{State: cnpgHAStateUnavailable, Reason: "no answer within " + cnpgHAReadTimeout.String()}
	default:
		if plain, ok := cnpgTransportSentence(err, 0, cnpgHAReadTimeout); ok {
			log.Printf("[cnpg] Failed to read %s: %v", g.In(namespace).String(), err)
			return integration.ReadSource{State: cnpgHAStateUnavailable, Reason: plain}
		}
		return integration.ReadSource{State: cnpgHAStateError, Reason: truncateCNPGRuntimeError(err.Error())}
	}
}

func cnpgDesiredImage(cluster *unstructured.Unstructured) string {
	if img, _, _ := unstructured.NestedString(cluster.Object, "status", "image"); img != "" {
		return img
	}
	img, _, _ := unstructured.NestedString(cluster.Object, "spec", "imageName")
	return img
}

// cnpgUncachedReason says what the reader cannot see when Radar's cache holds
// no copy of a kind the caller may read: Radar lists with its own credentials
// at connect time, which can be narrower than the caller's. Empty when the
// kind is cached and synced. uncached: Radar holds no informer for the kind.
// outOfScope: it watches the kind only in other namespaces, either because its
// credentials could list it only there or because the namespace was beyond
// the set it probed.
func cnpgUncachedReason(fact, kind, namespace string, uncached, outOfScope, ready bool) string {
	switch {
	case uncached:
		return fmt.Sprintf("%s unknown: Radar's own credentials could not list %s when it connected", fact, kind)
	case outOfScope:
		return fmt.Sprintf("%s unknown: Radar watches %s only in the namespaces it chose when it connected, and %s is not one of them", fact, kind, namespace)
	case !ready:
		return fmt.Sprintf("%s unknown: Radar is still loading %s", fact, kind)
	}
	return ""
}

func (s *Reader) hANodesSource(ctx context.Context, cache *k8s.ResourceCache) integration.ReadSource {
	if !s.Access.CanRead(ctx, "", "nodes", "", "get") {
		return cnpgHAClusterDenied(auth.Grant{Verb: "get", Resource: "nodes"})
	}
	if reason := cnpgUncachedReason("Zones", "Nodes", "", cache.Nodes() == nil, false, cache.IsKindReady("nodes")); reason != "" {
		return integration.ReadSource{State: cnpgHAStateUnavailable, Reason: reason}
	}
	return integration.ReadSource{State: cnpgHAStateOK}
}

func cnpgHAInstances(cache *k8s.ResourceCache, pods []*corev1.Pod, desiredImage string, nodesReadable bool) []CNPGHAInstance {
	out := make([]CNPGHAInstance, 0, len(pods))
	for _, p := range pods {
		inst := CNPGHAInstance{
			Pod:          p.Name,
			PodUID:       string(p.UID),
			Role:         runtimeRole(p),
			Ready:        cnpgActionPodReady(p),
			Node:         p.Spec.NodeName,
			QOSClass:     string(p.Status.QOSClass),
			PodCreatedAt: p.CreationTimestamp.UTC().Format(time.RFC3339),
		}
		for _, c := range p.Spec.Containers {
			if c.Name == defaultLogContainer {
				inst.Image = c.Image
			}
		}
		for _, cs := range p.Status.ContainerStatuses {
			if cs.Name != defaultLogContainer {
				continue
			}
			inst.RestartCount = cs.RestartCount
			if cs.State.Running != nil {
				inst.PostgresStartedAt = cs.State.Running.StartedAt.UTC().Format(time.RFC3339)
			}
		}
		if desiredImage != "" && inst.Image != "" {
			match := inst.Image == desiredImage
			inst.ImageMatches = &match
		}
		if nodesReadable && inst.Node != "" {
			if n, err := cache.Nodes().Get(inst.Node); err == nil {
				inst.Zone = n.Labels[cnpgZoneLabel]
			}
		}
		out = append(out, inst)
	}
	return out
}

// cnpgHAQuorum reads the recorded synchronous configuration. The object exists
// only while quorum failover is enabled; an enabled cluster without one has a
// configuration the primary has not recorded yet, and failover waits.
func (s *Reader) hAQuorum(ctx context.Context, callerCtx context.Context, c HAClients, cluster *unstructured.Unstructured, pods []*corev1.Pod, podsKnown bool) CNPGHAQuorum {
	namespace, name := cluster.GetNamespace(), cluster.GetName()
	q := CNPGHAQuorum{}
	sync, hasSync, _ := unstructured.NestedMap(cluster.Object, "spec", "postgresql", "synchronous")
	if hasSync {
		q.Method, _ = sync["method"].(string)
		q.DataDurability, _ = sync["dataDurability"].(string)
		if n, ok, _ := unstructured.NestedInt64(sync, "number"); ok {
			q.Number = &n
		}
		if v, ok := sync["failoverQuorum"].(bool); ok && v {
			q.Enabled, q.EnabledBy = true, "spec"
		}
	}
	switch strings.ToLower(cluster.GetAnnotations()[cnpgFailoverQuorumAnno]) {
	case "true":
		if hasSync {
			q.Enabled, q.EnabledBy = true, "annotation"
		}
	case "false":
		q.Enabled, q.EnabledBy = false, "annotation"
	}

	if disc := s.Observations.Discovery; disc != nil {
		if _, ok := disc.GetGVRWithGroup("FailoverQuorum", Group); !ok {
			q.Object = integration.ReadSource{State: cnpgHAStateNotInstalled, Reason: "this CloudNativePG version has no FailoverQuorum resource"}
			return q
		}
	}
	grant := auth.Grant{Verb: "get", Group: Group, Resource: "failoverquorums"}
	if !s.Access.CanRead(callerCtx, Group, "failoverquorums", namespace, "get") {
		q.Object = cnpgHADenied(grant, namespace)
		return q
	}
	obj, err := c.Dynamic.Resource(cnpgFailoverQuorumGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		q.Object = cnpgHAReadError(err, grant, namespace)
		return q
	}
	if !controlledBy(obj.GetOwnerReferences(), Group, "Cluster", name, cluster.GetUID()) {
		q.Object = integration.ReadSource{State: cnpgHAStateError, Reason: "a FailoverQuorum of this name exists but this Cluster does not own it"}
		return q
	}
	q.Object = integration.ReadSource{State: cnpgHAStateOK}
	st := &CNPGHAQuorumStatus{StandbyNames: []string{}}
	st.Method, _, _ = unstructured.NestedString(obj.Object, "status", "method")
	st.Primary, _, _ = unstructured.NestedString(obj.Object, "status", "primary")
	if names, ok, _ := unstructured.NestedStringSlice(obj.Object, "status", "standbyNames"); ok {
		st.StandbyNames = names
	}
	if n, ok, _ := unstructured.NestedInt64(obj.Object, "status", "standbyNumber"); ok {
		st.StandbyNumber = int(n)
	}
	q.Status = st
	cnpgQuorumArithmetic(&q, pods, podsKnown)
	return q
}

// cnpgQuorumArithmetic fills N, W, R and whether R + W > N. A reset object
// (no standby names) records no configuration: nothing is computed. R counts
// the standby names that are instances of this Cluster with a ready Pod, other
// than the recorded primary — Pod readiness is Radar's view of "able to report
// its state", which the operator checks directly.
func cnpgQuorumArithmetic(q *CNPGHAQuorum, pods []*corev1.Pod, podsKnown bool) {
	st := q.Status
	if st == nil || len(st.StandbyNames) == 0 {
		return
	}
	n, w := len(st.StandbyNames), st.StandbyNumber
	q.N, q.W = &n, &w
	if !podsKnown {
		return
	}
	ready := map[string]bool{}
	for _, p := range pods {
		if cnpgActionPodReady(p) {
			ready[p.Name] = true
		}
	}
	promotable := []string{}
	for _, s := range st.StandbyNames {
		if s != st.Primary && ready[s] {
			promotable = append(promotable, s)
		}
	}
	rr := len(promotable)
	holds := rr+w > n
	q.R, q.Promotable, q.Holds = &rr, promotable, &holds
}

func (s *Reader) hAPDBs(ctx context.Context, cache *k8s.ResourceCache, cluster *unstructured.Unstructured) CNPGHAPDBs {
	namespace := cluster.GetNamespace()
	out := CNPGHAPDBs{Enabled: true, Items: []CNPGHAPDB{}}
	if v, ok, _ := unstructured.NestedBool(cluster.Object, "spec", "enablePDB"); ok {
		out.Enabled = v
	}
	grant := auth.Grant{Verb: "list", Group: "policy", Resource: "poddisruptionbudgets"}
	if !s.Access.CanRead(ctx, "policy", "poddisruptionbudgets", namespace, "list") {
		out.ReadSource = cnpgHADenied(grant, namespace)
		return out
	}
	lister := cache.PodDisruptionBudgets()
	within := integration.NamespacesWithinCache(cache, "poddisruptionbudgets", []string{namespace})
	if reason := cnpgUncachedReason("Disruption budgets", "PodDisruptionBudgets", namespace, lister == nil, within.Unavailable, cache.IsKindReady("poddisruptionbudgets")); reason != "" {
		out.ReadSource = integration.ReadSource{State: cnpgHAStateUnavailable, Reason: reason}
		return out
	}
	list, err := lister.PodDisruptionBudgets(namespace).List(labels.SelectorFromSet(labels.Set{clusterLabel: cluster.GetName()}))
	if err != nil {
		out.ReadSource = integration.ReadSource{State: cnpgHAStateError, Reason: err.Error()}
		return out
	}
	out.ReadSource = integration.ReadSource{State: cnpgHAStateOK}
	for _, p := range list {
		if !controlledBy(p.OwnerReferences, Group, "Cluster", cluster.GetName(), cluster.GetUID()) {
			continue
		}
		out.Items = append(out.Items, cnpgHAPDBOf(p, cluster.GetName()))
	}
	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].Name < out.Items[j].Name })
	return out
}

func cnpgHAPDBOf(p *policyv1.PodDisruptionBudget, cluster string) CNPGHAPDB {
	item := CNPGHAPDB{
		Name:               p.Name,
		Role:               "other",
		ExpectedPods:       p.Status.ExpectedPods,
		CurrentHealthy:     p.Status.CurrentHealthy,
		DesiredHealthy:     p.Status.DesiredHealthy,
		DisruptionsAllowed: p.Status.DisruptionsAllowed,
		Observed:           p.Status.ObservedGeneration >= p.Generation,
	}
	switch p.Name {
	case cluster + "-primary":
		item.Role = "primary"
	case cluster:
		item.Role = "replicas"
	}
	if p.Spec.MinAvailable != nil {
		item.MinAvailable = p.Spec.MinAvailable.String()
	}
	if p.Spec.MaxUnavailable != nil {
		item.MaxUnavailable = p.Spec.MaxUnavailable.String()
	}
	return item
}

func cnpgHALeaseFrom(l *leaseView, now time.Time) CNPGHALease {
	out := CNPGHALease{ReadSource: integration.ReadSource{State: cnpgHAStateOK}, Namespace: l.namespace, Name: l.name, Holder: l.holder, DurationSeconds: l.duration}
	if l.renew != nil {
		out.RenewTime = l.renew.UTC().Format(time.RFC3339)
		if l.duration != nil {
			expired := now.After(l.renew.Add(time.Duration(*l.duration) * time.Second))
			out.Expired = &expired
		}
	}
	return out
}

type leaseView struct {
	namespace, name, holder string
	renew                   *time.Time
	duration                *int32
}

func (s *Reader) hAPrimaryLease(ctx context.Context, callerCtx context.Context, c HAClients, cluster *unstructured.Unstructured, now time.Time) CNPGHALease {
	namespace, name := cluster.GetNamespace(), cluster.GetName()
	grant := auth.Grant{Verb: "get", Group: "coordination.k8s.io", Resource: "leases"}
	if !s.Access.CanRead(callerCtx, "coordination.k8s.io", "leases", namespace, "get") {
		return CNPGHALease{ReadSource: cnpgHADenied(grant, namespace), Namespace: namespace, Name: name}
	}
	l, err := c.Typed.CoordinationV1().Leases(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		src := cnpgHAReadError(err, grant, namespace)
		if src.State == cnpgHAStateNotFound {
			src.Reason = "No primary Lease: CloudNativePG creates one from 1.30"
		}
		return CNPGHALease{ReadSource: src, Namespace: namespace, Name: name}
	}
	v := &leaseView{namespace: namespace, name: name, duration: l.Spec.LeaseDurationSeconds}
	if l.Spec.HolderIdentity != nil {
		v.holder = *l.Spec.HolderIdentity
	}
	if l.Spec.RenewTime != nil {
		t := l.Spec.RenewTime.Time
		v.renew = &t
	}
	out := cnpgHALeaseFrom(v, now)
	controlled := controlledBy(l.OwnerReferences, Group, "Cluster", name, cluster.GetUID())
	out.ControlledByCluster = &controlled
	return out
}

// cnpgHAOperatorLease finds the operator's leader-election Lease in the
// namespace of a visible operator Deployment. Which operator replica leads is
// a different fact from which instance is primary.
func (s *Reader) hAOperatorLease(ctx context.Context, callerCtx context.Context, c HAClients, cache *k8s.ResourceCache, now time.Time) CNPGHALease {
	acc, deployments := s.operatorDeployments(callerCtx, cache, s.Observations.OperatorScope(callerCtx))
	var namespace string
	for _, d := range deployments {
		if d.Labels[cnpgOperatorNameLabel] == cnpgOperatorNameValue {
			namespace = d.Namespace
			break
		}
	}
	if namespace == "" {
		reason := "The operator Deployment is not visible to you, so its namespace is unknown"
		switch acc.State {
		case integration.KindCoverageFull:
			reason = "No operator Deployment labelled " + cnpgOperatorNameLabel + "=" + cnpgOperatorNameValue + " was found"
		case integration.KindCoverageUncached:
			reason = "Radar does not watch Deployments in the namespaces you can read, so the operator's namespace is unknown"
		}
		return CNPGHALease{ReadSource: integration.ReadSource{State: cnpgHAStateUnavailable, Reason: reason}, Name: cnpgOperatorLeaseName}
	}
	grant := auth.Grant{Verb: "get", Group: "coordination.k8s.io", Resource: "leases"}
	if !s.Access.CanRead(callerCtx, "coordination.k8s.io", "leases", namespace, "get") {
		return CNPGHALease{ReadSource: cnpgHADenied(grant, namespace), Namespace: namespace, Name: cnpgOperatorLeaseName}
	}
	l, err := c.Typed.CoordinationV1().Leases(namespace).Get(ctx, cnpgOperatorLeaseName, metav1.GetOptions{})
	if err != nil {
		src := cnpgHAReadError(err, grant, namespace)
		if src.State == cnpgHAStateNotFound {
			src.Reason = "No leader-election Lease: the operator may run with leader election off"
		}
		return CNPGHALease{ReadSource: src, Namespace: namespace, Name: cnpgOperatorLeaseName}
	}
	v := &leaseView{namespace: namespace, name: cnpgOperatorLeaseName, duration: l.Spec.LeaseDurationSeconds}
	if l.Spec.HolderIdentity != nil {
		v.holder = *l.Spec.HolderIdentity
	}
	if l.Spec.RenewTime != nil {
		t := l.Spec.RenewTime.Time
		v.renew = &t
	}
	return cnpgHALeaseFrom(v, now)
}

func (s *Reader) hAJobs(ctx context.Context, cache *k8s.ResourceCache, cluster *unstructured.Unstructured) CNPGHAJobs {
	namespace := cluster.GetNamespace()
	out := CNPGHAJobs{Items: []CNPGHAJob{}}
	grant := auth.Grant{Verb: "list", Group: "batch", Resource: "jobs"}
	if !s.Access.CanRead(ctx, "batch", "jobs", namespace, "list") {
		out.ReadSource = cnpgHADenied(grant, namespace)
		return out
	}
	lister := cache.Jobs()
	within := integration.NamespacesWithinCache(cache, "jobs", []string{namespace})
	if reason := cnpgUncachedReason("Instance Jobs", "Jobs", namespace, lister == nil, within.Unavailable, cache.IsKindReady("jobs")); reason != "" {
		out.ReadSource = integration.ReadSource{State: cnpgHAStateUnavailable, Reason: reason}
		return out
	}
	list, err := lister.Jobs(namespace).List(labels.SelectorFromSet(labels.Set{clusterLabel: cluster.GetName()}))
	if err != nil {
		out.ReadSource = integration.ReadSource{State: cnpgHAStateError, Reason: err.Error()}
		return out
	}
	out.ReadSource = integration.ReadSource{State: cnpgHAStateOK}
	var jobPods []*corev1.Pod
	if s.Access.CanRead(ctx, "", "pods", namespace, "list") && cache.Pods() != nil && cache.IsKindReady("pods") && !integration.NamespacesWithinCache(cache, "pods", []string{namespace}).Unavailable {
		jobPods, _ = cache.Pods().Pods(namespace).List(labels.Everything())
	}
	for _, j := range list {
		if !controlledBy(j.OwnerReferences, Group, "Cluster", cluster.GetName(), cluster.GetUID()) {
			continue
		}
		out.Items = append(out.Items, cnpgHAJobOf(j, jobPods...))
	}
	sort.Slice(out.Items, func(i, j int) bool {
		if out.Items[i].StartTime != out.Items[j].StartTime {
			return out.Items[i].StartTime > out.Items[j].StartTime
		}
		return out.Items[i].Name < out.Items[j].Name
	})
	return out
}

func cnpgHAJobOf(j *batchv1.Job, pods ...*corev1.Pod) CNPGHAJob {
	item := CNPGHAJob{Name: j.Name, Role: j.Labels[jobRoleLabel], Instance: j.Labels["cnpg.io/instanceName"], Phase: "pending"}
	if j.Status.StartTime != nil {
		item.StartTime = j.Status.StartTime.UTC().Format(time.RFC3339)
	}
	if j.Status.CompletionTime != nil {
		item.CompletionTime = j.Status.CompletionTime.UTC().Format(time.RFC3339)
	}
	for _, c := range j.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			item.Phase = "succeeded"
			return item
		case batchv1.JobFailed:
			item.Phase, item.Reason = "failed", strings.TrimSpace(c.Reason+": "+c.Message)
			item.Reason = strings.TrimPrefix(strings.TrimSuffix(item.Reason, ":"), ": ")
			return item
		}
	}
	if j.Status.Active > 0 {
		item.Phase = "active"
		var schedulerReason string
		for _, pod := range pods {
			if pod.DeletionTimestamp != nil || !controlledBy(pod.OwnerReferences, "batch", "Job", j.Name, j.UID) {
				continue
			}
			if pod.Status.Phase == corev1.PodRunning {
				return item
			}
			if pod.Status.Phase != corev1.PodPending {
				continue
			}
			for _, condition := range pod.Status.Conditions {
				if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionFalse && condition.Reason == corev1.PodReasonUnschedulable {
					schedulerReason = "Pod cannot be scheduled: " + condition.Reason + ": " + condition.Message
				}
			}
		}
		if schedulerReason != "" {
			item.Phase, item.Reason = "pending", schedulerReason
		}
	}
	return item
}

func (s *Reader) hARWEndpoints(ctx context.Context, callerCtx context.Context, c HAClients, cluster *unstructured.Unstructured) CNPGHAEndpoints {
	namespace := cluster.GetNamespace()
	svc := cluster.GetName() + "-rw"
	out := CNPGHAEndpoints{Service: svc, Pods: []string{}}
	grant := auth.Grant{Verb: "list", Group: "discovery.k8s.io", Resource: "endpointslices"}
	if !s.Access.CanRead(callerCtx, "discovery.k8s.io", "endpointslices", namespace, "list") {
		out.ReadSource = cnpgHADenied(grant, namespace)
		return out
	}
	list, err := c.Typed.DiscoveryV1().EndpointSlices(namespace).List(ctx, metav1.ListOptions{LabelSelector: discoveryv1.LabelServiceName + "=" + svc})
	if err != nil {
		out.ReadSource = cnpgHAReadError(err, grant, namespace)
		return out
	}
	out.ReadSource = integration.ReadSource{State: cnpgHAStateOK}
	out.Pods = cnpgReadyEndpointPods(list.Items)
	return out
}

func cnpgReadyEndpointPods(slices []discoveryv1.EndpointSlice) []string {
	seen := map[string]bool{}
	pods := []string{}
	for _, sl := range slices {
		for _, ep := range sl.Endpoints {
			if ep.Conditions.Ready != nil && !*ep.Conditions.Ready {
				continue
			}
			if ep.TargetRef == nil || ep.TargetRef.Kind != "Pod" || seen[ep.TargetRef.Name] {
				continue
			}
			seen[ep.TargetRef.Name] = true
			pods = append(pods, ep.TargetRef.Name)
		}
	}
	sort.Strings(pods)
	return pods
}

// cnpgHACertificates lists every certificate the operator reports an expiry
// for, with who renews it. A user-provided Secret's metadata is read (never
// its data) to name a cert-manager Certificate; without get secrets that link
// is unknown, not absent.
func (s *Reader) hACertificates(ctx context.Context, callerCtx context.Context, c HAClients, cluster *unstructured.Unstructured) []CNPGHACertificate {
	namespace := cluster.GetNamespace()
	exp, _, _ := unstructured.NestedStringMap(cluster.Object, "status", "certificates", "expirations")
	purposes := map[string][]string{}
	for _, f := range []string{"serverCASecret", "serverTLSSecret", "replicationTLSSecret", "clientCASecret"} {
		if n, _, _ := unstructured.NestedString(cluster.Object, "status", "certificates", f); n != "" {
			purposes[n] = append(purposes[n], f)
		}
	}
	user := issues.CNPGUserCertificateSecrets(cluster)
	canGetSecrets := s.Access.CanRead(callerCtx, "", "secrets", namespace, "get")
	grant := auth.Grant{Verb: "get", Resource: "secrets"}

	out := make([]CNPGHACertificate, 0, len(exp))
	for secret, raw := range exp {
		cert := CNPGHACertificate{Secret: secret, Raw: raw, Purposes: purposes[secret], Renewal: "operator"}
		if t, ok := issues.ParseCNPGCertExpiry(raw); ok {
			cert.ExpiresAt = t.UTC().Format(time.RFC3339)
		}
		if _, ok := user[secret]; ok {
			cert.Renewal = "user"
			src := integration.ReadSource{State: cnpgHAStateOK}
			switch {
			case !canGetSecrets:
				src = cnpgHADenied(grant, namespace)
			default:
				m, err := c.Metadata.Resource(cnpgSecretsGVR).Namespace(namespace).Get(ctx, secret, metav1.GetOptions{})
				if err != nil {
					src = cnpgHAReadError(err, grant, namespace)
				} else if name := m.GetAnnotations()[certManagerCertificateAnno]; name != "" {
					cert.CertManager = &CNPGCertManagerRef{
						Certificate: name,
						Issuer:      m.GetAnnotations()[certManagerIssuerAnno],
						IssuerKind:  m.GetAnnotations()[certManagerIssuerKindAnno],
					}
				}
			}
			cert.Metadata = &src
		}
		out = append(out, cert)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Secret < out[j].Secret })
	return out
}
