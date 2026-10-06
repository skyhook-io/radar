package cnpg

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	auth "github.com/skyhook-io/radar/internal/auth"
	integration "github.com/skyhook-io/radar/internal/integration"
)

const (
	// Constants in CloudNativePG's controller, not configurable; the leader
	// Lease name (cnpgOperatorLeaseName) is one too.
	cnpgMutatingWebhookConfig     = "cnpg-mutating-webhook-configuration"
	cnpgValidatingWebhookConfig   = "cnpg-validating-webhook-configuration"
	cnpgOperatorDefaultMetrics    = 8080
	cnpgOperatorMetricsPath       = "/metrics"
	cnpgOperatorMetricsCap        = 4 << 20
	cnpgOperatorMetricsTTL        = 25 * time.Second
	cnpgOperatorEventLimit        = 20
	cnpgWatchNamespaceEnv         = "WATCH_NAMESPACE"
	cnpgEndpointSliceServiceLabel = "kubernetes.io/service-name"
)

var (
	cnpgGrantGetLeases        = auth.Grant{Verb: "get", Group: "coordination.k8s.io", Resource: "leases"}
	cnpgGrantListLeases       = auth.Grant{Verb: "list", Group: "coordination.k8s.io", Resource: "leases"}
	cnpgGrantGetMutatingWH    = auth.Grant{Verb: "get", Group: "admissionregistration.k8s.io", Resource: "mutatingwebhookconfigurations"}
	cnpgGrantGetValidatingWH  = auth.Grant{Verb: "get", Group: "admissionregistration.k8s.io", Resource: "validatingwebhookconfigurations"}
	cnpgGrantListEndpointSlcs = auth.Grant{Verb: "list", Group: "discovery.k8s.io", Resource: "endpointslices"}
	grantGetPodsProxy         = auth.Grant{Verb: "get", Resource: "pods", Subresource: "proxy"}
)

type CNPGOperatorPod struct {
	Name            string                    `json:"name"`
	UID             string                    `json:"uid"`
	Phase           string                    `json:"phase"`
	Ready           bool                      `json:"ready"`
	StartedAt       string                    `json:"startedAt,omitempty"`
	Restarts        int32                     `json:"restarts"`
	LastTermination *CNPGContainerTermination `json:"lastTermination,omitempty"`
	Leader          bool                      `json:"leader"`
}

// CNPGContainerTermination is how a container's previous run ended
// (lastState.terminated), for the Pod's container that ended most recently.
type CNPGContainerTermination struct {
	Container  string `json:"container"`
	Reason     string `json:"reason"`
	ExitCode   int32  `json:"exitCode"`
	FinishedAt string `json:"finishedAt,omitempty"`
}

// CNPGOperatorLeader: State ok | disabled (no --leader-elect) | notFound |
// denied | error. Stale means the holder has not renewed within the lease
// duration, so no operator instance is leading.
type CNPGOperatorLeader struct {
	integration.ReadSource
	Lease                string `json:"lease,omitempty"`
	Holder               string `json:"holder,omitempty"`
	HolderPod            string `json:"holderPod,omitempty"`
	HolderIsCurrentPod   bool   `json:"holderIsCurrentPod"`
	RenewTime            string `json:"renewTime,omitempty"`
	AcquireTime          string `json:"acquireTime,omitempty"`
	LeaseDurationSeconds *int32 `json:"leaseDurationSeconds,omitempty"`
	Transitions          *int32 `json:"transitions,omitempty"`
	Stale                bool   `json:"stale"`
}

// CNPGOperatorWatch: All when WATCH_NAMESPACE is unset or empty. Source names
// where the value came from; Unresolved is set when it comes from somewhere
// Radar does not follow (a ConfigMap or Secret key reference).
type CNPGOperatorWatch struct {
	All        bool     `json:"all"`
	Namespaces []string `json:"namespaces"`
	Source     string   `json:"source"`
	Unresolved string   `json:"unresolved,omitempty"`
}

type CNPGOperatorWebhook struct {
	Name          string `json:"name"`
	FailurePolicy string `json:"failurePolicy"`
	CABundleSet   bool   `json:"caBundleSet"`
	Service       string `json:"service,omitempty"`
	URL           bool   `json:"url"`
}

type CNPGOperatorWebhookConfig struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	integration.ReadSource
	Webhooks []CNPGOperatorWebhook `json:"webhooks"`
}

// CNPGOperatorWebhookService: endpoint counts are nil when the slices could
// not be read.
type CNPGOperatorWebhookService struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	integration.ReadSource
	ReadyEndpoints    *int `json:"readyEndpoints"`
	NotReadyEndpoints *int `json:"notReadyEndpoints"`
}

type CNPGOperatorControllerStats struct {
	Controller string             `json:"controller"`
	Errors     *float64           `json:"errors"`
	Total      *float64           `json:"total"`
	Results    map[string]float64 `json:"results"`
}

// CNPGOperatorReconcilePod is one operator Pod's controller-runtime counters,
// cumulative since that Pod's process started (StartedAt).
type CNPGOperatorReconcilePod struct {
	Pod       string `json:"pod"`
	Leader    bool   `json:"leader"`
	StartedAt string `json:"startedAt,omitempty"`
	CNPGRuntimeSource
	Controllers []CNPGOperatorControllerStats `json:"controllers"`
}

type CNPGOperatorEvents struct {
	integration.ReadSource
	Items []CNPGRecoveryEvent `json:"items"`
}

type CNPGOperatorDiagnosis struct {
	Namespace   string                       `json:"namespace"`
	Deployment  string                       `json:"deployment"`
	Pods        []CNPGOperatorPod            `json:"pods"`
	PodCoverage integration.ReadSource       `json:"podCoverage"`
	Leader      CNPGOperatorLeader           `json:"leader"`
	Watch       CNPGOperatorWatch            `json:"watch"`
	Webhooks    []CNPGOperatorWebhookConfig  `json:"webhooks"`
	Services    []CNPGOperatorWebhookService `json:"webhookServices"`
	MetricsPort int                          `json:"metricsPort"`
	Reconcile   []CNPGOperatorReconcilePod   `json:"reconcile"`
	Events      CNPGOperatorEvents           `json:"events"`
}

// cnpgOperatorDiagnoses takes podsOf so the Pods each operator Deployment's
// component already read are not listed again.
func (s *Reader) operatorDiagnoses(ctx context.Context, typed kubernetes.Interface, operators []*appsv1.Deployment, podsOf func(*appsv1.Deployment) cnpgDeploymentPodRead) []CNPGOperatorDiagnosis {
	out := []CNPGOperatorDiagnosis{}
	if typed == nil {
		return out
	}
	webhooks, services := s.operatorWebhooks(ctx, typed)
	for _, d := range operators {
		diag := CNPGOperatorDiagnosis{Namespace: d.Namespace, Deployment: d.Name, Webhooks: webhooks, Services: services, Pods: []CNPGOperatorPod{}, Reconcile: []CNPGOperatorReconcilePod{}}
		c := cnpgOperatorContainerOf(d)
		diag.Watch = cnpgOperatorWatchOf(c, d.Namespace)
		diag.MetricsPort = cnpgOperatorMetricsPort(c)
		read := podsOf(d)
		pods := read.pods
		diag.PodCoverage = read.coverage
		for i := range pods {
			diag.Pods = append(diag.Pods, cnpgOperatorPodOf(&pods[i]))
		}
		diag.Leader = s.operatorLeader(ctx, typed, d, c, pods)
		leaderPod := cnpgOperatorLeadingPod(diag.Leader)
		for i := range diag.Pods {
			diag.Pods[i].Leader = leaderPod != "" && diag.Pods[i].Name == leaderPod
		}
		diag.Reconcile = s.operatorReconcile(ctx, d, pods, diag.MetricsPort, leaderPod)
		diag.Events = s.operatorEvents(ctx, typed, d, pods)
		out = append(out, diag)
	}
	return out
}

// cnpgOperatorLeadingPod is the current Pod that leads, or "". A holder
// whose lease expired leads nothing, whatever the Lease still names.
func cnpgOperatorLeadingPod(l CNPGOperatorLeader) string {
	if l.State != cnpgReadOK || l.Stale || !l.HolderIsCurrentPod {
		return ""
	}
	return l.HolderPod
}

// cnpgDeploymentPodRead is the outcome of listing one Deployment's Pods.
type cnpgDeploymentPodRead struct {
	pods     []corev1.Pod
	coverage integration.ReadSource
}

// cnpgDeploymentPods lists the Pods a Deployment's selector matches, as the
// caller, sorted by name.
func (s *Reader) deploymentPods(ctx context.Context, typed kubernetes.Interface, d *appsv1.Deployment) cnpgDeploymentPodRead {
	if d.Spec.Selector == nil {
		return cnpgDeploymentPodRead{coverage: integration.ReadSource{State: cnpgReadError, Reason: "the Deployment has no selector"}}
	}
	selector, err := metav1.LabelSelectorAsSelector(d.Spec.Selector)
	if err != nil {
		return cnpgDeploymentPodRead{coverage: integration.ReadSource{State: cnpgReadError, Reason: err.Error()}}
	}
	var out cnpgDeploymentPodRead
	out.coverage = s.gatedRead(ctx, GrantListPods, d.Namespace, func() error {
		if typed == nil {
			return errors.New("cluster client unavailable")
		}
		list, err := typed.CoreV1().Pods(d.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
		if err == nil {
			out.pods = list.Items
		}
		return err
	})
	sort.Slice(out.pods, func(i, j int) bool { return out.pods[i].Name < out.pods[j].Name })
	return out
}

func cnpgOperatorPodOf(p *corev1.Pod) CNPGOperatorPod {
	op := CNPGOperatorPod{Name: p.Name, UID: string(p.UID), Phase: string(p.Status.Phase), Ready: cnpgActionPodReady(p)}
	if p.Status.StartTime != nil {
		op.StartedAt = p.Status.StartTime.UTC().Format(time.RFC3339)
	}
	op.Restarts, op.LastTermination = cnpgPodRestarts(p)
	return op
}

// cnpgPodRestarts sums restarts over a Pod's containers and returns how the
// container that ended most recently ended last time. Restart counts
// accumulate over the Pod's life while each container keeps only its
// previous run, so the newest termination is the one that explains a
// current crash loop.
func cnpgPodRestarts(p *corev1.Pod) (int32, *CNPGContainerTermination) {
	var restarts int32
	var newest *corev1.ContainerStateTerminated
	var container string
	for _, st := range p.Status.ContainerStatuses {
		restarts += st.RestartCount
		t := st.LastTerminationState.Terminated
		if t != nil && (newest == nil || t.FinishedAt.After(newest.FinishedAt.Time)) {
			newest, container = t, st.Name
		}
	}
	if newest == nil {
		return restarts, nil
	}
	out := &CNPGContainerTermination{Container: container, Reason: newest.Reason, ExitCode: newest.ExitCode}
	if !newest.FinishedAt.IsZero() {
		out.FinishedAt = newest.FinishedAt.UTC().Format(time.RFC3339)
	}
	return restarts, out
}

func cnpgOperatorWatchOf(c *corev1.Container, operatorNamespace string) CNPGOperatorWatch {
	out := CNPGOperatorWatch{All: true, Namespaces: []string{}, Source: "WATCH_NAMESPACE is not set, so the operator watches every namespace"}
	if c == nil {
		return out
	}
	for _, e := range c.Env {
		if e.Name != cnpgWatchNamespaceEnv {
			continue
		}
		switch {
		case e.ValueFrom == nil:
			out.Source = "env WATCH_NAMESPACE"
			for _, ns := range strings.Split(e.Value, ",") {
				if ns = strings.TrimSpace(ns); ns != "" {
					out.Namespaces = append(out.Namespaces, ns)
				}
			}
			out.All = len(out.Namespaces) == 0
		case e.ValueFrom.FieldRef != nil && e.ValueFrom.FieldRef.FieldPath == "metadata.namespace":
			out.Source = "env WATCH_NAMESPACE from the Pod's namespace"
			out.All, out.Namespaces = false, []string{operatorNamespace}
		default:
			out.All = false
			out.Source = "env WATCH_NAMESPACE"
			switch {
			case e.ValueFrom.ConfigMapKeyRef != nil:
				out.Unresolved = fmt.Sprintf("set from ConfigMap %s key %s", e.ValueFrom.ConfigMapKeyRef.Name, e.ValueFrom.ConfigMapKeyRef.Key)
			case e.ValueFrom.SecretKeyRef != nil:
				out.Unresolved = fmt.Sprintf("set from Secret %s key %s", e.ValueFrom.SecretKeyRef.Name, e.ValueFrom.SecretKeyRef.Key)
			default:
				out.Unresolved = "set from a field reference Radar does not resolve"
			}
		}
	}
	return out
}

func cnpgOperatorMetricsPort(c *corev1.Container) int {
	if c == nil {
		return cnpgOperatorDefaultMetrics
	}
	for _, p := range c.Ports {
		if p.Name == "metrics" && p.ContainerPort > 0 {
			return int(p.ContainerPort)
		}
	}
	if v := cnpgOperatorArg(c, "--metrics-bind-address"); v != "" {
		if i := strings.LastIndexByte(v, ':'); i >= 0 {
			if n, err := strconv.Atoi(v[i+1:]); err == nil && n > 0 {
				return n
			}
		}
	}
	return cnpgOperatorDefaultMetrics
}

func cnpgOperatorLeaderElect(c *corev1.Container) bool {
	if c == nil {
		return false
	}
	for _, a := range append(append([]string{}, c.Command...), c.Args...) {
		if a == "--leader-elect" || a == "--leader-elect=true" {
			return true
		}
	}
	return false
}

// controller-runtime's holder identity is "<hostname>_<uuid>", and a Pod's
// hostname is its name.
func cnpgLeaseHolderPod(holder string) string {
	if i := strings.LastIndexByte(holder, '_'); i > 0 {
		return holder[:i]
	}
	return holder
}

func (s *Reader) operatorLeader(ctx context.Context, typed kubernetes.Interface, d *appsv1.Deployment, c *corev1.Container, pods []corev1.Pod) CNPGOperatorLeader {
	out := CNPGOperatorLeader{Lease: cnpgOperatorLeaseName}
	if !cnpgOperatorLeaderElect(c) {
		out.State = "disabled"
		out.Reason = "the operator runs without --leader-elect"
		return out
	}
	var lease *coordinationv1.Lease
	out.ReadSource = s.gatedRead(ctx, cnpgGrantGetLeases, d.Namespace, func() error {
		l, err := typed.CoordinationV1().Leases(d.Namespace).Get(ctx, cnpgOperatorLeaseName, metav1.GetOptions{})
		lease = l
		return err
	})
	if out.State != cnpgReadOK {
		return out
	}
	spec := lease.Spec
	if spec.HolderIdentity != nil {
		out.Holder = *spec.HolderIdentity
		out.HolderPod = cnpgLeaseHolderPod(out.Holder)
	}
	if spec.RenewTime != nil {
		out.RenewTime = spec.RenewTime.UTC().Format(time.RFC3339)
	}
	if spec.AcquireTime != nil {
		out.AcquireTime = spec.AcquireTime.UTC().Format(time.RFC3339)
	}
	out.LeaseDurationSeconds = spec.LeaseDurationSeconds
	out.Transitions = spec.LeaseTransitions
	for _, p := range pods {
		if p.Name == out.HolderPod {
			out.HolderIsCurrentPod = true
		}
	}
	view := &leaseView{namespace: d.Namespace, name: cnpgOperatorLeaseName, holder: out.Holder, duration: spec.LeaseDurationSeconds}
	if spec.RenewTime != nil {
		t := spec.RenewTime.Time
		view.renew = &t
	}
	if judged := cnpgHALeaseFrom(view, time.Now()); judged.Expired != nil {
		out.Stale = *judged.Expired
	}
	if out.Holder == "" {
		out.Stale = true
	}
	return out
}

func (s *Reader) operatorWebhooks(ctx context.Context, typed kubernetes.Interface) ([]CNPGOperatorWebhookConfig, []CNPGOperatorWebhookService) {
	configs := []CNPGOperatorWebhookConfig{}
	type svcKey struct{ ns, name string }
	svcs := map[svcKey]bool{}
	add := func(kind, name string, g auth.Grant, read func() ([]admissionv1.WebhookClientConfig, []string, []string, error)) {
		cfg := CNPGOperatorWebhookConfig{Kind: kind, Name: name, Webhooks: []CNPGOperatorWebhook{}}
		if s.Access.Permission(ctx, g) == integration.PermissionDenied {
			cfg.ReadSource = integration.ReadSource{State: cnpgReadDenied, Grant: g.Ref()}
			configs = append(configs, cfg)
			return
		}
		clients, names, policies, err := read()
		cfg.ReadSource = cnpgReadOutcome(err, g, "")
		for i, cc := range clients {
			wh := CNPGOperatorWebhook{Name: names[i], FailurePolicy: policies[i], CABundleSet: len(cc.CABundle) > 0, URL: cc.URL != nil}
			if cc.Service != nil {
				wh.Service = cc.Service.Namespace + "/" + cc.Service.Name
				svcs[svcKey{cc.Service.Namespace, cc.Service.Name}] = true
			}
			cfg.Webhooks = append(cfg.Webhooks, wh)
		}
		configs = append(configs, cfg)
	}
	policy := func(p *admissionv1.FailurePolicyType) string {
		if p == nil {
			return string(admissionv1.Fail)
		}
		return string(*p)
	}
	add("MutatingWebhookConfiguration", cnpgMutatingWebhookConfig, cnpgGrantGetMutatingWH, func() ([]admissionv1.WebhookClientConfig, []string, []string, error) {
		c, err := typed.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(ctx, cnpgMutatingWebhookConfig, metav1.GetOptions{})
		if err != nil {
			return nil, nil, nil, err
		}
		var cc []admissionv1.WebhookClientConfig
		var names, pols []string
		for _, w := range c.Webhooks {
			cc, names, pols = append(cc, w.ClientConfig), append(names, w.Name), append(pols, policy(w.FailurePolicy))
		}
		return cc, names, pols, nil
	})
	add("ValidatingWebhookConfiguration", cnpgValidatingWebhookConfig, cnpgGrantGetValidatingWH, func() ([]admissionv1.WebhookClientConfig, []string, []string, error) {
		c, err := typed.AdmissionregistrationV1().ValidatingWebhookConfigurations().Get(ctx, cnpgValidatingWebhookConfig, metav1.GetOptions{})
		if err != nil {
			return nil, nil, nil, err
		}
		var cc []admissionv1.WebhookClientConfig
		var names, pols []string
		for _, w := range c.Webhooks {
			cc, names, pols = append(cc, w.ClientConfig), append(names, w.Name), append(pols, policy(w.FailurePolicy))
		}
		return cc, names, pols, nil
	})

	keys := make([]svcKey, 0, len(svcs))
	for k := range svcs {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].ns+"/"+keys[i].name < keys[j].ns+"/"+keys[j].name })
	services := []CNPGOperatorWebhookService{}
	for _, k := range keys {
		svc := CNPGOperatorWebhookService{Namespace: k.ns, Name: k.name}
		var slices []discoveryv1.EndpointSlice
		svc.ReadSource = s.gatedRead(ctx, cnpgGrantListEndpointSlcs, k.ns, func() error {
			list, err := typed.DiscoveryV1().EndpointSlices(k.ns).List(ctx, metav1.ListOptions{LabelSelector: cnpgEndpointSliceServiceLabel + "=" + k.name})
			if err == nil {
				slices = list.Items
			}
			return err
		})
		if svc.State == cnpgReadOK {
			ready, notReady := cnpgEndpointCounts(slices)
			svc.ReadyEndpoints, svc.NotReadyEndpoints = &ready, &notReady
		}
		services = append(services, svc)
	}
	return configs, services
}

// cnpgEndpointCounts counts endpoints; an endpoint whose ready condition is
// unset counts as ready, as the EndpointSlice API defines it.
func cnpgEndpointCounts(slices []discoveryv1.EndpointSlice) (int, int) {
	ready, notReady := 0, 0
	for _, sl := range slices {
		for _, ep := range sl.Endpoints {
			if ep.Conditions.Ready == nil || *ep.Conditions.Ready {
				ready++
			} else {
				notReady++
			}
		}
	}
	return ready, notReady
}

// cnpgOperatorProcessStart is when the operator container last started: the
// counters reset with every container restart, which the Pod's own start
// time does not reflect.
func cnpgOperatorProcessStart(p *corev1.Pod, container string) string {
	for _, st := range p.Status.ContainerStatuses {
		if st.Name == container && st.State.Running != nil && !st.State.Running.StartedAt.IsZero() {
			return st.State.Running.StartedAt.UTC().Format(time.RFC3339)
		}
	}
	return ""
}

func (s *Reader) operatorReconcile(callerCtx context.Context, d *appsv1.Deployment, pods []corev1.Pod, port int, leaderPod string) []CNPGOperatorReconcilePod {
	out := []CNPGOperatorReconcilePod{}
	container := cnpgOperatorContainer
	if c := cnpgOperatorContainerOf(d); c != nil {
		container = c.Name
	}
	if len(pods) == 0 {
		return out
	}
	allowed := s.Access.Permission(callerCtx, grantGetPodsProxy.In(d.Namespace)) != integration.PermissionDenied
	var client kubernetes.Interface
	if allowed {
		client = s.Clients.Proxy
	}
	identity := s.Identity
	run := newCNPGRuntimeRunner(callerCtx)
	results := make([]CNPGOperatorReconcilePod, len(pods))
	for i := range pods {
		p := &pods[i]
		res := &results[i]
		res.Pod, res.Leader, res.Controllers = p.Name, p.Name == leaderPod, []CNPGOperatorControllerStats{}
		res.StartedAt = cnpgOperatorProcessStart(p, container)
		switch {
		case !allowed:
			res.CNPGRuntimeSource = CNPGRuntimeSource{State: runtimeStateDenied, Error: "reading the operator's metrics needs " + grantGetPodsProxy.In(d.Namespace).String()}
			continue
		case client == nil:
			res.CNPGRuntimeSource = CNPGRuntimeSource{State: runtimeStateError, Error: "cluster client unavailable"}
			continue
		case p.Status.Phase != corev1.PodRunning:
			res.CNPGRuntimeSource = CNPGRuntimeSource{State: runtimeStateUnreachable, Error: "the Pod is " + string(p.Status.Phase)}
			continue
		}
		target := proxyTarget{namespace: p.Namespace, pod: p.Name, podUID: p.UID, port: port, path: cnpgOperatorMetricsPath, scheme: "http", limit: cnpgOperatorMetricsCap}
		run.do(func(ctx context.Context) {
			outcome := memoized(ctx, identity, target, cnpgOperatorMetricsTTL, func(ctx context.Context) cnpgProxyOutcome {
				return proxyGetWithFallback(ctx, client, target)
			})
			res.CNPGRuntimeSource = outcome.source()
			if outcome.state != runtimeStateOK {
				return
			}
			samples, reason, err := cnpgPromText(outcome)
			if err != nil {
				res.State, res.Error = runtimeStateError, "could not parse the operator's metrics: "+err.Error()
				return
			}
			res.Reason = reason
			res.Controllers = cnpgReconcileStats(samples)
			if len(res.Controllers) == 0 {
				res.State = "partial"
				res.Reason = cnpgJoinReasons(res.Reason, "no controller_runtime_reconcile series on this endpoint")
			}
		})
	}
	run.wait()
	return append(out, results...)
}

func cnpgReconcileStats(samples map[string][]cnpgSample) []CNPGOperatorControllerStats {
	byController := map[string]*CNPGOperatorControllerStats{}
	get := func(name string) *CNPGOperatorControllerStats {
		if st, ok := byController[name]; ok {
			return st
		}
		st := &CNPGOperatorControllerStats{Controller: name, Results: map[string]float64{}}
		byController[name] = st
		return st
	}
	for _, sm := range samples["controller_runtime_reconcile_errors_total"] {
		v := sm.value
		get(sm.labels["controller"]).Errors = &v
	}
	for _, sm := range samples["controller_runtime_reconcile_total"] {
		st := get(sm.labels["controller"])
		st.Results[sm.labels["result"]] += sm.value
		total := sm.value
		if st.Total != nil {
			total += *st.Total
		}
		st.Total = &total
	}
	out := make([]CNPGOperatorControllerStats, 0, len(byController))
	for _, st := range byController {
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Controller < out[j].Controller })
	return out
}

func (s *Reader) operatorEvents(ctx context.Context, typed kubernetes.Interface, d *appsv1.Deployment, pods []corev1.Pod) CNPGOperatorEvents {
	out := CNPGOperatorEvents{Items: []CNPGRecoveryEvent{}}
	var events []corev1.Event
	out.ReadSource = s.gatedRead(ctx, cnpgGrantListEvents, d.Namespace, func() error {
		list, err := typed.CoreV1().Events(d.Namespace).List(ctx, metav1.ListOptions{})
		if err == nil {
			events = list.Items
		}
		return err
	})
	if out.State != cnpgReadOK {
		return out
	}
	subjects := map[string]bool{"Deployment/" + d.Name: true}
	for _, p := range pods {
		subjects["Pod/"+p.Name] = true
		for _, ref := range p.OwnerReferences {
			if ref.Kind == "ReplicaSet" {
				subjects["ReplicaSet/"+ref.Name] = true
			}
		}
	}
	for _, e := range events {
		if e.InvolvedObject.Kind == "ReplicaSet" && strings.HasPrefix(e.InvolvedObject.Name, d.Name+"-") {
			subjects["ReplicaSet/"+e.InvolvedObject.Name] = true
		}
		if e.InvolvedObject.Kind == "Lease" && e.InvolvedObject.Name == cnpgOperatorLeaseName {
			subjects["Lease/"+cnpgOperatorLeaseName] = true
		}
	}
	out.Items = cnpgRecoveryEventsOf(events, subjects, cnpgOperatorEventLimit)
	return out
}
