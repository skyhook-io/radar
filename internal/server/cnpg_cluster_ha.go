package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
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

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
)

// GET /api/cnpg/clusters/{namespace}/{name}/ha: the facts that decide whether
// a Cluster survives losing an instance, and what a planned switchover will
// meet. Reading the Cluster never implies reading anything else: Nodes, Jobs,
// PodDisruptionBudgets, Leases, EndpointSlices, Secret metadata and the
// FailoverQuorum are each authorized on their own and report their own state.

const (
	cnpgHAStateOK           = "ok"
	cnpgHAStateDenied       = "denied"
	cnpgHAStateNotFound     = "notFound"
	cnpgHAStateNotInstalled = "notInstalled"
	cnpgHAStateUnavailable  = "unavailable"
	cnpgHAStateError        = "error"

	cnpgJobRoleLabel       = "cnpg.io/jobRole"
	cnpgZoneLabel          = "topology.kubernetes.io/zone"
	cnpgOperatorLeaseName  = "db9c8771.cnpg.io"
	cnpgFailoverQuorumAnno = "alpha.cnpg.io/failoverQuorum"
	cnpgHAReadTimeout      = 5 * time.Second

	certManagerCertificateAnno = "cert-manager.io/certificate-name"
	certManagerIssuerAnno      = "cert-manager.io/issuer-name"
	certManagerIssuerKindAnno  = "cert-manager.io/issuer-kind"
)

var (
	cnpgFailoverQuorumGVR = schema.GroupVersionResource{Group: cnpgGroup, Version: "v1", Resource: "failoverquorums"}
	cnpgSecretsGVR        = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
)

// CNPGHASource is one sub-read's outcome. Grant names what a denied read
// needs; Reason explains anything other than ok.
type CNPGHASource struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
	Grant  string `json:"grant,omitempty"`
}

type CNPGClusterHAResponse struct {
	Cluster       CNPGRuntimeObjectRef `json:"cluster"`
	SampledAt     string               `json:"sampledAt"`
	DesiredImage  string               `json:"desiredImage,omitempty"`
	Instances     []CNPGHAInstance     `json:"instances"`
	Pods          CNPGHASource         `json:"pods"`
	Nodes         CNPGHASource         `json:"nodes"`
	Quorum        CNPGHAQuorum         `json:"quorum"`
	PDBs          CNPGHAPDBs           `json:"pdbs"`
	PrimaryLease  CNPGHALease          `json:"primaryLease"`
	OperatorLease CNPGHALease          `json:"operatorLease"`
	Jobs          CNPGHAJobs           `json:"jobs"`
	RWEndpoints   CNPGHAEndpoints      `json:"rwEndpoints"`
	Certificates  []CNPGHACertificate  `json:"certificates"`
	Maintenance   CNPGMaintenanceFacts `json:"maintenance"`
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
	Enabled        bool                `json:"enabled"`
	EnabledBy      string              `json:"enabledBy,omitempty"`
	Method         string              `json:"method,omitempty"`
	Number         *int64              `json:"number,omitempty"`
	DataDurability string              `json:"dataDurability,omitempty"`
	Object         CNPGHASource        `json:"object"`
	Status         *CNPGHAQuorumStatus `json:"status,omitempty"`
	N              *int                `json:"n,omitempty"`
	W              *int                `json:"w,omitempty"`
	R              *int                `json:"r,omitempty"`
	Promotable     []string            `json:"promotable,omitempty"`
	Holds          *bool               `json:"holds,omitempty"`
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
	CNPGHASource
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
	CNPGHASource
	Namespace           string `json:"namespace,omitempty"`
	Name                string `json:"name,omitempty"`
	Holder              string `json:"holder,omitempty"`
	RenewTime           string `json:"renewTime,omitempty"`
	DurationSeconds     *int32 `json:"durationSeconds,omitempty"`
	Expired             *bool  `json:"expired,omitempty"`
	ControlledByCluster *bool  `json:"controlledByCluster,omitempty"`
}

type CNPGHAJobs struct {
	CNPGHASource
	Items []CNPGHAJob `json:"items"`
}

// CNPGHAJob Phase: running | succeeded | failed | pending.
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
	CNPGHASource
	Service string   `json:"service"`
	Pods    []string `json:"pods"`
}

// CNPGHACertificate is one entry of status.certificates.expirations. ExpiresAt
// is RFC3339 when the operator's value parsed; Raw is always what it wrote.
// Renewal: operator (generated by CloudNativePG) | user (named in
// spec.certificates). CertManager is set only when the Secret's metadata was
// read and carries cert-manager's annotations; Metadata says whether it was.
type CNPGHACertificate struct {
	Secret      string              `json:"secret"`
	Purposes    []string            `json:"purposes,omitempty"`
	Raw         string              `json:"raw"`
	ExpiresAt   string              `json:"expiresAt,omitempty"`
	Renewal     string              `json:"renewal"`
	Metadata    *CNPGHASource       `json:"metadata,omitempty"`
	CertManager *CNPGCertManagerRef `json:"certManager,omitempty"`
}

type CNPGCertManagerRef struct {
	Certificate string `json:"certificate"`
	Issuer      string `json:"issuer,omitempty"`
	IssuerKind  string `json:"issuerKind,omitempty"`
}

type cnpgHAClients struct {
	typed kubernetes.Interface
	dyn   dynamic.Interface
	meta  metadata.Interface
}

func (s *Server) handleCNPGClusterHA(w http.ResponseWriter, r *http.Request) {
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if !s.requireConnected(w) {
		return
	}
	if noNamespaceAccess(s.getUserNamespaces(r, []string{namespace})) {
		s.writeError(w, http.StatusForbidden, "no access to namespace "+namespace)
		return
	}
	if !s.canRead(r, cnpgGroup, "clusters", namespace, "get") {
		s.writeError(w, http.StatusForbidden, "no access to clusters.postgresql.cnpg.io in namespace "+namespace)
		return
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "resource cache not available")
		return
	}
	cluster, ok := s.loadCNPGCluster(w, r, cache, namespace, name)
	if !ok {
		return
	}
	typed := s.getClientForRequest(r)
	dyn := s.getDynamicClientForRequest(r)
	cfg := s.getConfigForRequest(r)
	if typed == nil || dyn == nil || cfg == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	meta, err := metadata.NewForConfig(cfg)
	if err != nil {
		log.Printf("[cnpg] Failed to build metadata client for %s/%s: %v", namespace, name, err)
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available")
		return
	}
	s.writeJSON(w, s.cnpgClusterHA(r, cnpgHAClients{typed: typed, dyn: dyn, meta: meta}, cache, cluster, time.Now().UTC()))
}

func (s *Server) cnpgClusterHA(r *http.Request, c cnpgHAClients, cache *k8s.ResourceCache, cluster *unstructured.Unstructured, now time.Time) CNPGClusterHAResponse {
	namespace, name := cluster.GetNamespace(), cluster.GetName()
	ctx, cancel := context.WithTimeout(r.Context(), cnpgHAReadTimeout)
	defer cancel()

	resp := CNPGClusterHAResponse{
		Cluster:      CNPGRuntimeObjectRef{Namespace: namespace, Name: name, UID: cluster.GetUID()},
		SampledAt:    now.Format(time.RFC3339),
		DesiredImage: cnpgDesiredImage(cluster),
		Instances:    []CNPGHAInstance{},
		Certificates: []CNPGHACertificate{},
		Maintenance:  cnpgMaintenanceFactsOf(cluster),
	}

	var pods []*corev1.Pod
	if s.canRead(r, "", "pods", namespace, "list") {
		var err error
		pods, err = cnpgClusterInstancePods(cache, cluster)
		if err != nil {
			resp.Pods = CNPGHASource{State: cnpgHAStateUnavailable, Reason: err.Error()}
		} else {
			resp.Pods = CNPGHASource{State: cnpgHAStateOK}
		}
	} else {
		resp.Pods = cnpgHADenied(cnpgGrant{"list", "", "pods", ""}, namespace)
	}
	resp.Nodes = s.cnpgHANodesSource(r, cache)
	resp.Instances = cnpgHAInstances(cache, pods, resp.DesiredImage, resp.Nodes.State == cnpgHAStateOK)

	resp.Quorum = s.cnpgHAQuorum(ctx, r, c, cluster, pods, resp.Pods.State == cnpgHAStateOK)
	resp.PDBs = s.cnpgHAPDBs(r, cache, cluster)
	resp.PrimaryLease = s.cnpgHAPrimaryLease(ctx, r, c, cluster, now)
	resp.OperatorLease = s.cnpgHAOperatorLease(ctx, r, c, cache, now)
	resp.Jobs = s.cnpgHAJobs(r, cache, cluster)
	resp.RWEndpoints = s.cnpgHARWEndpoints(ctx, r, c, cluster)
	resp.Certificates = s.cnpgHACertificates(ctx, r, c, cluster)
	return resp
}

func cnpgHADenied(g cnpgGrant, namespace string) CNPGHASource {
	return CNPGHASource{State: cnpgHAStateDenied, Grant: g.String(namespace), Reason: "You are not allowed to " + g.String(namespace)}
}

func cnpgHAClusterDenied(g cnpgGrant) CNPGHASource {
	grant := g.ClusterString()
	return CNPGHASource{State: cnpgHAStateDenied, Grant: grant, Reason: "You are not allowed to " + grant}
}

// cnpgHAReadError classifies an impersonated read's failure. NotFound is left
// to the caller: its meaning differs per object.
func cnpgHAReadError(err error, g cnpgGrant, namespace string) CNPGHASource {
	switch {
	case apierrors.IsForbidden(err):
		return cnpgHADenied(g, namespace)
	case apierrors.IsNotFound(err):
		return CNPGHASource{State: cnpgHAStateNotFound}
	case errors.Is(err, context.DeadlineExceeded) || apierrors.IsTimeout(err):
		return CNPGHASource{State: cnpgHAStateUnavailable, Reason: "no answer within " + cnpgHAReadTimeout.String()}
	default:
		if plain, ok := cnpgTransportSentence(err, 0, cnpgHAReadTimeout); ok {
			log.Printf("[cnpg] Failed to read %s: %v", g.String(namespace), err)
			return CNPGHASource{State: cnpgHAStateUnavailable, Reason: plain}
		}
		return CNPGHASource{State: cnpgHAStateError, Reason: truncateCNPGRuntimeError(err.Error())}
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

func (s *Server) cnpgHANodesSource(r *http.Request, cache *k8s.ResourceCache) CNPGHASource {
	if !s.canRead(r, "", "nodes", "", "get") {
		return cnpgHAClusterDenied(cnpgGrant{"get", "", "nodes", ""})
	}
	if reason := cnpgUncachedReason("Zones", "Nodes", "", cache.Nodes() == nil, false, cache.IsKindReady("nodes")); reason != "" {
		return CNPGHASource{State: cnpgHAStateUnavailable, Reason: reason}
	}
	return CNPGHASource{State: cnpgHAStateOK}
}

func cnpgHAInstances(cache *k8s.ResourceCache, pods []*corev1.Pod, desiredImage string, nodesReadable bool) []CNPGHAInstance {
	out := make([]CNPGHAInstance, 0, len(pods))
	for _, p := range pods {
		inst := CNPGHAInstance{
			Pod:          p.Name,
			PodUID:       string(p.UID),
			Role:         cnpgRuntimeRole(p),
			Ready:        cnpgActionPodReady(p),
			Node:         p.Spec.NodeName,
			QOSClass:     string(p.Status.QOSClass),
			PodCreatedAt: p.CreationTimestamp.UTC().Format(time.RFC3339),
		}
		for _, c := range p.Spec.Containers {
			if c.Name == cnpgDefaultLogContainer {
				inst.Image = c.Image
			}
		}
		for _, cs := range p.Status.ContainerStatuses {
			if cs.Name != cnpgDefaultLogContainer {
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
func (s *Server) cnpgHAQuorum(ctx context.Context, r *http.Request, c cnpgHAClients, cluster *unstructured.Unstructured, pods []*corev1.Pod, podsKnown bool) CNPGHAQuorum {
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

	if disc := k8s.GetResourceDiscovery(); disc != nil {
		if _, ok := disc.GetGVRWithGroup("FailoverQuorum", cnpgGroup); !ok {
			q.Object = CNPGHASource{State: cnpgHAStateNotInstalled, Reason: "this CloudNativePG version has no FailoverQuorum resource"}
			return q
		}
	}
	grant := cnpgGrant{"get", cnpgGroup, "failoverquorums", ""}
	if !s.canRead(r, cnpgGroup, "failoverquorums", namespace, "get") {
		q.Object = cnpgHADenied(grant, namespace)
		return q
	}
	obj, err := c.dyn.Resource(cnpgFailoverQuorumGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		q.Object = cnpgHAReadError(err, grant, namespace)
		return q
	}
	if !cnpgControlledBy(obj.GetOwnerReferences(), cnpgGroup, "Cluster", name, cluster.GetUID()) {
		q.Object = CNPGHASource{State: cnpgHAStateError, Reason: "a FailoverQuorum of this name exists but this Cluster does not own it"}
		return q
	}
	q.Object = CNPGHASource{State: cnpgHAStateOK}
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

func (s *Server) cnpgHAPDBs(r *http.Request, cache *k8s.ResourceCache, cluster *unstructured.Unstructured) CNPGHAPDBs {
	namespace := cluster.GetNamespace()
	out := CNPGHAPDBs{Enabled: true, Items: []CNPGHAPDB{}}
	if v, ok, _ := unstructured.NestedBool(cluster.Object, "spec", "enablePDB"); ok {
		out.Enabled = v
	}
	grant := cnpgGrant{"list", "policy", "poddisruptionbudgets", ""}
	if !s.canRead(r, "policy", "poddisruptionbudgets", namespace, "list") {
		out.CNPGHASource = cnpgHADenied(grant, namespace)
		return out
	}
	lister := cache.PodDisruptionBudgets()
	within := capacityNamespacesWithinCache(cache, "poddisruptionbudgets", []string{namespace})
	if reason := cnpgUncachedReason("Disruption budgets", "PodDisruptionBudgets", namespace, lister == nil, within.unavailable, cache.IsKindReady("poddisruptionbudgets")); reason != "" {
		out.CNPGHASource = CNPGHASource{State: cnpgHAStateUnavailable, Reason: reason}
		return out
	}
	list, err := lister.PodDisruptionBudgets(namespace).List(labels.SelectorFromSet(labels.Set{cnpgClusterLabel: cluster.GetName()}))
	if err != nil {
		out.CNPGHASource = CNPGHASource{State: cnpgHAStateError, Reason: err.Error()}
		return out
	}
	out.CNPGHASource = CNPGHASource{State: cnpgHAStateOK}
	for _, p := range list {
		if !cnpgControlledBy(p.OwnerReferences, cnpgGroup, "Cluster", cluster.GetName(), cluster.GetUID()) {
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
	out := CNPGHALease{CNPGHASource: CNPGHASource{State: cnpgHAStateOK}, Namespace: l.namespace, Name: l.name, Holder: l.holder, DurationSeconds: l.duration}
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

func (s *Server) cnpgHAPrimaryLease(ctx context.Context, r *http.Request, c cnpgHAClients, cluster *unstructured.Unstructured, now time.Time) CNPGHALease {
	namespace, name := cluster.GetNamespace(), cluster.GetName()
	grant := cnpgGrant{"get", "coordination.k8s.io", "leases", ""}
	if !s.canRead(r, "coordination.k8s.io", "leases", namespace, "get") {
		return CNPGHALease{CNPGHASource: cnpgHADenied(grant, namespace), Namespace: namespace, Name: name}
	}
	l, err := c.typed.CoordinationV1().Leases(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		src := cnpgHAReadError(err, grant, namespace)
		if src.State == cnpgHAStateNotFound {
			src.Reason = "No primary Lease: CloudNativePG creates one from 1.30"
		}
		return CNPGHALease{CNPGHASource: src, Namespace: namespace, Name: name}
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
	controlled := cnpgControlledBy(l.OwnerReferences, cnpgGroup, "Cluster", name, cluster.GetUID())
	out.ControlledByCluster = &controlled
	return out
}

// cnpgHAOperatorLease finds the operator's leader-election Lease in the
// namespace of a visible operator Deployment. Which operator replica leads is
// a different fact from which instance is primary.
func (s *Server) cnpgHAOperatorLease(ctx context.Context, r *http.Request, c cnpgHAClients, cache *k8s.ResourceCache, now time.Time) CNPGHALease {
	acc, _, deployments := s.cnpgOperatorDeployments(r, cache, s.cnpgOperatorScope(r))
	var namespace string
	for _, d := range deployments {
		if d.Labels[cnpgOperatorNameLabel] == cnpgOperatorNameValue {
			namespace = d.Namespace
			break
		}
	}
	if namespace == "" {
		reason := "The operator Deployment is not visible to you, so its namespace is unknown"
		if acc.state == cnpgCoverageFull {
			reason = "No operator Deployment labelled " + cnpgOperatorNameLabel + "=" + cnpgOperatorNameValue + " was found"
		}
		return CNPGHALease{CNPGHASource: CNPGHASource{State: cnpgHAStateUnavailable, Reason: reason}, Name: cnpgOperatorLeaseName}
	}
	grant := cnpgGrant{"get", "coordination.k8s.io", "leases", ""}
	if !s.canRead(r, "coordination.k8s.io", "leases", namespace, "get") {
		return CNPGHALease{CNPGHASource: cnpgHADenied(grant, namespace), Namespace: namespace, Name: cnpgOperatorLeaseName}
	}
	l, err := c.typed.CoordinationV1().Leases(namespace).Get(ctx, cnpgOperatorLeaseName, metav1.GetOptions{})
	if err != nil {
		src := cnpgHAReadError(err, grant, namespace)
		if src.State == cnpgHAStateNotFound {
			src.Reason = "No leader-election Lease: the operator may run with leader election off"
		}
		return CNPGHALease{CNPGHASource: src, Namespace: namespace, Name: cnpgOperatorLeaseName}
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

func (s *Server) cnpgHAJobs(r *http.Request, cache *k8s.ResourceCache, cluster *unstructured.Unstructured) CNPGHAJobs {
	namespace := cluster.GetNamespace()
	out := CNPGHAJobs{Items: []CNPGHAJob{}}
	grant := cnpgGrant{"list", "batch", "jobs", ""}
	if !s.canRead(r, "batch", "jobs", namespace, "list") {
		out.CNPGHASource = cnpgHADenied(grant, namespace)
		return out
	}
	lister := cache.Jobs()
	within := capacityNamespacesWithinCache(cache, "jobs", []string{namespace})
	if reason := cnpgUncachedReason("Instance Jobs", "Jobs", namespace, lister == nil, within.unavailable, cache.IsKindReady("jobs")); reason != "" {
		out.CNPGHASource = CNPGHASource{State: cnpgHAStateUnavailable, Reason: reason}
		return out
	}
	list, err := lister.Jobs(namespace).List(labels.SelectorFromSet(labels.Set{cnpgClusterLabel: cluster.GetName()}))
	if err != nil {
		out.CNPGHASource = CNPGHASource{State: cnpgHAStateError, Reason: err.Error()}
		return out
	}
	out.CNPGHASource = CNPGHASource{State: cnpgHAStateOK}
	for _, j := range list {
		if !cnpgControlledBy(j.OwnerReferences, cnpgGroup, "Cluster", cluster.GetName(), cluster.GetUID()) {
			continue
		}
		out.Items = append(out.Items, cnpgHAJobOf(j))
	}
	sort.Slice(out.Items, func(i, j int) bool {
		if out.Items[i].StartTime != out.Items[j].StartTime {
			return out.Items[i].StartTime > out.Items[j].StartTime
		}
		return out.Items[i].Name < out.Items[j].Name
	})
	return out
}

func cnpgHAJobOf(j *batchv1.Job) CNPGHAJob {
	item := CNPGHAJob{Name: j.Name, Role: j.Labels[cnpgJobRoleLabel], Instance: j.Labels["cnpg.io/instanceName"], Phase: "pending"}
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
		item.Phase = "running"
	}
	return item
}

func (s *Server) cnpgHARWEndpoints(ctx context.Context, r *http.Request, c cnpgHAClients, cluster *unstructured.Unstructured) CNPGHAEndpoints {
	namespace := cluster.GetNamespace()
	svc := cluster.GetName() + "-rw"
	out := CNPGHAEndpoints{Service: svc, Pods: []string{}}
	grant := cnpgGrant{"list", "discovery.k8s.io", "endpointslices", ""}
	if !s.canRead(r, "discovery.k8s.io", "endpointslices", namespace, "list") {
		out.CNPGHASource = cnpgHADenied(grant, namespace)
		return out
	}
	list, err := c.typed.DiscoveryV1().EndpointSlices(namespace).List(ctx, metav1.ListOptions{LabelSelector: discoveryv1.LabelServiceName + "=" + svc})
	if err != nil {
		out.CNPGHASource = cnpgHAReadError(err, grant, namespace)
		return out
	}
	out.CNPGHASource = CNPGHASource{State: cnpgHAStateOK}
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
func (s *Server) cnpgHACertificates(ctx context.Context, r *http.Request, c cnpgHAClients, cluster *unstructured.Unstructured) []CNPGHACertificate {
	namespace := cluster.GetNamespace()
	exp, _, _ := unstructured.NestedStringMap(cluster.Object, "status", "certificates", "expirations")
	purposes := map[string][]string{}
	for _, f := range []string{"serverCASecret", "serverTLSSecret", "replicationTLSSecret", "clientCASecret"} {
		if n, _, _ := unstructured.NestedString(cluster.Object, "status", "certificates", f); n != "" {
			purposes[n] = append(purposes[n], f)
		}
	}
	user := issues.CNPGUserCertificateSecrets(cluster)
	canGetSecrets := s.canRead(r, "", "secrets", namespace, "get")
	grant := cnpgGrant{"get", "", "secrets", ""}

	out := make([]CNPGHACertificate, 0, len(exp))
	for secret, raw := range exp {
		cert := CNPGHACertificate{Secret: secret, Raw: raw, Purposes: purposes[secret], Renewal: "operator"}
		if t, ok := issues.ParseCNPGCertExpiry(raw); ok {
			cert.ExpiresAt = t.UTC().Format(time.RFC3339)
		}
		if _, ok := user[secret]; ok {
			cert.Renewal = "user"
			src := CNPGHASource{State: cnpgHAStateOK}
			switch {
			case !canGetSecrets:
				src = cnpgHADenied(grant, namespace)
			default:
				m, err := c.meta.Resource(cnpgSecretsGVR).Namespace(namespace).Get(ctx, secret, metav1.GetOptions{})
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
