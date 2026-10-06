package cnpg

import (
	"context"
	"log"
	"net/http"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/imageutil"
	integration "github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
)

const (
	cnpgOperatorNameLabel   = "app.kubernetes.io/name"
	cnpgOperatorNameValue   = "cloudnative-pg"
	cnpgVersionLabel        = "app.kubernetes.io/version"
	cnpgPluginNameLabel     = "cnpg.io/pluginName"
	cnpgOperatorContainer   = "manager"
	cnpgOperatorDeployVar   = "OPERATOR_DEPLOYMENT_NAME"
	cnpgMonitoringQueriesCM = "MONITORING_QUERIES_CONFIGMAP"

	cnpgOperatorRoleOperator = "operator"
	cnpgOperatorRolePlugin   = "plugin"

	cnpgConfigPurposeOperator   = "operator"
	cnpgConfigPurposeMonitoring = "monitoring"
)

// CNPGOperatorComponent is one operator or plugin Deployment. Version is the
// image tag, else the app.kubernetes.io/version label, else empty. Replica
// counts are nil when unreported, which is not zero. Pods is null unless
// PodCoverage is ok; PodCoverage is absent for a plugin Service with no
// matching Deployment.
type CNPGOperatorComponent struct {
	Role          string                     `json:"role"`
	PluginName    string                     `json:"pluginName,omitempty"`
	Namespace     string                     `json:"namespace"`
	Deployment    string                     `json:"deployment"`
	Image         string                     `json:"image"`
	Version       string                     `json:"version"`
	ReadyReplicas *int32                     `json:"readyReplicas"`
	Replicas      *int32                     `json:"replicas"`
	Pods          []CNPGOperatorComponentPod `json:"pods"`
	PodCoverage   *integration.ReadSource    `json:"podCoverage,omitempty"`
}

// CNPGOperatorComponentPod is one of a component's Pods. StartedAt is when
// the component's container last started, empty while it is not running;
// Restarts is summed over the Pod's containers.
type CNPGOperatorComponentPod struct {
	Name            string                    `json:"name"`
	Ready           bool                      `json:"ready"`
	Restarts        int32                     `json:"restarts"`
	StartedAt       string                    `json:"startedAt,omitempty"`
	LastTermination *CNPGContainerTermination `json:"lastTermination,omitempty"`
}

// CNPGOperatorConfigMapState is present only on ConfigMap references. A Secret
// reference never carries it: the endpoint never reads Secrets.
type CNPGOperatorConfigMapState struct {
	Exists   *bool             `json:"exists"`
	Readable bool              `json:"readable"`
	Reason   string            `json:"reason,omitempty"`
	Data     map[string]string `json:"data"`
}

// CNPGOperatorConfigRef is a ConfigMap or Secret the operator is configured
// to read.
type CNPGOperatorConfigRef struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Purpose   string `json:"purpose"`
	*CNPGOperatorConfigMapState
}

// CNPGOperatorResponse is GET /api/cnpg/operator.
type CNPGOperatorResponse struct {
	Coverage   map[string]integration.KindCoverage `json:"coverage"`
	Components []CNPGOperatorComponent             `json:"components"`
	Config     []CNPGOperatorConfigRef             `json:"config"`
	// Diagnosis is one entry per operator Deployment: leader Lease, watched
	// namespaces, webhook reachability, reconcile counters and recent events.
	Diagnosis []CNPGOperatorDiagnosis `json:"diagnosis"`
}

func (s *Reader) Operator(ctx context.Context) (*CNPGOperatorResponse, error) {
	if !s.Observations.Connected {
		return nil, ErrCNPGDisconnected
	}
	cache := s.Observations.Cache
	if cache == nil {
		return nil, &ReadFailure{http.StatusServiceUnavailable, "Resource cache not available"}
	}
	scope := s.Observations.OperatorScope(ctx)
	resp := CNPGOperatorResponse{
		Coverage:   map[string]integration.KindCoverage{},
		Components: []CNPGOperatorComponent{},
		Config:     []CNPGOperatorConfigRef{},
	}

	depAcc, deployments := s.operatorDeployments(ctx, cache, scope)
	resp.Coverage["deployments"] = depAcc.Coverage()
	svcAcc, services := s.operatorServices(ctx, cache, scope)
	resp.Coverage["services"] = svcAcc.Coverage()

	typed := s.Clients.Typed
	podReads := map[*appsv1.Deployment]cnpgDeploymentPodRead{}
	podsOf := func(d *appsv1.Deployment) cnpgDeploymentPodRead {
		if read, ok := podReads[d]; ok {
			return read
		}
		read := s.deploymentPods(ctx, typed, d)
		podReads[d] = read
		return read
	}
	component := func(d *appsv1.Deployment, role, pluginName string, c *corev1.Container) CNPGOperatorComponent {
		return withCNPGComponentPods(cnpgOperatorComponent(d, role, pluginName, c), podsOf(d), c)
	}

	var operators []*appsv1.Deployment
	for _, d := range deployments {
		if d.Labels[cnpgOperatorNameLabel] == cnpgOperatorNameValue {
			operators = append(operators, d)
			resp.Components = append(resp.Components, component(d, cnpgOperatorRoleOperator, "", cnpgOperatorContainerOf(d)))
		}
	}

	byNamespace := map[string][]*appsv1.Deployment{}
	for _, d := range deployments {
		byNamespace[d.Namespace] = append(byNamespace[d.Namespace], d)
	}
	var plugins []CNPGOperatorComponent
	for _, svc := range services {
		pluginName := svc.Labels[cnpgPluginNameLabel]
		if pluginName == "" || !depAcc.Covers(svc.Namespace) {
			continue
		}
		matched := false
		if len(svc.Spec.Selector) > 0 {
			sel := labels.SelectorFromSet(svc.Spec.Selector)
			for _, d := range byNamespace[svc.Namespace] {
				if sel.Matches(labels.Set(d.Spec.Template.Labels)) {
					matched = true
					plugins = append(plugins, component(d, cnpgOperatorRolePlugin, pluginName, firstContainer(d)))
				}
			}
		}
		if !matched {
			plugins = append(plugins, CNPGOperatorComponent{Role: cnpgOperatorRolePlugin, PluginName: pluginName, Namespace: svc.Namespace})
		}
	}
	sort.SliceStable(plugins, func(i, j int) bool {
		a, b := plugins[i], plugins[j]
		if a.PluginName != b.PluginName {
			return a.PluginName < b.PluginName
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Deployment < b.Deployment
	})
	resp.Components = append(resp.Components, plugins...)

	resp.Config = s.operatorConfig(ctx, cache, operators)
	resp.Diagnosis = s.operatorDiagnoses(ctx, typed, operators, podsOf)
	return &resp, nil
}

// withCNPGComponentPods adds a component's Pods: restarts and the last
// termination show a crash-looping operator or plugin that its Deployment's
// ready count, read between crashes, can hide.
func withCNPGComponentPods(comp CNPGOperatorComponent, read cnpgDeploymentPodRead, c *corev1.Container) CNPGOperatorComponent {
	coverage := read.coverage
	comp.PodCoverage = &coverage
	if coverage.State != cnpgReadOK {
		return comp
	}
	comp.Pods = make([]CNPGOperatorComponentPod, 0, len(read.pods))
	for i := range read.pods {
		p := &read.pods[i]
		pod := CNPGOperatorComponentPod{Name: p.Name, Ready: cnpgActionPodReady(p)}
		if c != nil {
			pod.StartedAt = cnpgOperatorProcessStart(p, c.Name)
		}
		pod.Restarts, pod.LastTermination = cnpgPodRestarts(p)
		comp.Pods = append(comp.Pods, pod)
	}
	return comp
}

func (s *Reader) operatorDeployments(ctx context.Context, cache *k8s.ResourceCache, scope []string) (integration.KindAccess, []*appsv1.Deployment) {
	acc, read := s.Observations.TypedScope(ctx, cache, scope, "apps", "deployments")
	if acc.State == integration.KindCoverageDenied || acc.State == integration.KindCoverageUncached {
		return acc, nil
	}
	lister := cache.Deployments()
	if lister == nil || !cache.IsKindReady("deployments") {
		return integration.KindAccess{State: integration.KindCoverageSyncing}, nil
	}
	var out []*appsv1.Deployment
	if read == nil {
		out, _ = lister.List(labels.Everything())
	} else {
		for _, ns := range read {
			items, _ := lister.Deployments(ns).List(labels.Everything())
			out = append(out, items...)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return acc, out
}

func (s *Reader) operatorServices(ctx context.Context, cache *k8s.ResourceCache, scope []string) (integration.KindAccess, []*corev1.Service) {
	acc, read := s.Observations.TypedScope(ctx, cache, scope, "", "services")
	if acc.State == integration.KindCoverageDenied || acc.State == integration.KindCoverageUncached {
		return acc, nil
	}
	lister := cache.Services()
	if lister == nil || !cache.IsKindReady("services") {
		return integration.KindAccess{State: integration.KindCoverageSyncing}, nil
	}
	hasPlugin, err := labels.Parse(cnpgPluginNameLabel)
	if err != nil {
		log.Printf("[cnpg] Failed to build plugin selector: %v", err)
		return integration.KindAccess{State: integration.KindCoverageError}, nil
	}
	var out []*corev1.Service
	if read == nil {
		out, _ = lister.List(hasPlugin)
	} else {
		for _, ns := range read {
			items, _ := lister.Services(ns).List(hasPlugin)
			out = append(out, items...)
		}
	}
	return acc, out
}

func cnpgOperatorContainerOf(d *appsv1.Deployment) *corev1.Container {
	for i := range d.Spec.Template.Spec.Containers {
		if d.Spec.Template.Spec.Containers[i].Name == cnpgOperatorContainer {
			return &d.Spec.Template.Spec.Containers[i]
		}
	}
	return firstContainer(d)
}

func firstContainer(d *appsv1.Deployment) *corev1.Container {
	if len(d.Spec.Template.Spec.Containers) == 0 {
		return nil
	}
	return &d.Spec.Template.Spec.Containers[0]
}

func cnpgOperatorComponent(d *appsv1.Deployment, role, pluginName string, c *corev1.Container) CNPGOperatorComponent {
	out := CNPGOperatorComponent{
		Role:       role,
		PluginName: pluginName,
		Namespace:  d.Namespace,
		Deployment: d.Name,
		Replicas:   d.Spec.Replicas,
	}
	if c != nil {
		out.Image = c.Image
		out.Version = imageutil.ImageTag(c.Image)
	}
	if out.Version == "" {
		out.Version = d.Labels[cnpgVersionLabel]
	}
	if out.Version == "" {
		out.Version = d.Spec.Template.Labels[cnpgVersionLabel]
	}
	// The typed status cannot tell an omitted readyReplicas from zero; a status
	// the controller has observed at least once states it authoritatively.
	if d.Status.ObservedGeneration > 0 {
		ready := d.Status.ReadyReplicas
		out.ReadyReplicas = &ready
	}
	return out
}

// cnpgOperatorArg returns the value of --flag=value or --flag value from a
// container's command and args.
func cnpgOperatorArg(c *corev1.Container, flag string) string {
	argv := append(append([]string{}, c.Command...), c.Args...)
	for i, a := range argv {
		if v, ok := strings.CutPrefix(a, flag+"="); ok {
			return v
		}
		if a == flag && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

func cnpgOperatorEnv(c *corev1.Container, name string) string {
	for _, e := range c.Env {
		if e.Name == name && e.ValueFrom == nil {
			return e.Value
		}
	}
	return ""
}

// cnpgOperatorExpand resolves $(OPERATOR_DEPLOYMENT_NAME) the way the kubelet
// would: from the container's literal env, which the shipped manifests set to
// the Deployment's own name. Any other reference is left verbatim, as the
// kubelet leaves an unresolvable one.
func cnpgOperatorExpand(v string, c *corev1.Container, d *appsv1.Deployment) string {
	ref := "$(" + cnpgOperatorDeployVar + ")"
	if !strings.Contains(v, ref) {
		return v
	}
	name := cnpgOperatorEnv(c, cnpgOperatorDeployVar)
	if name == "" {
		name = d.Name
	}
	return strings.ReplaceAll(v, ref, name)
}

func (s *Reader) operatorConfig(ctx context.Context, cache *k8s.ResourceCache, operators []*appsv1.Deployment) []CNPGOperatorConfigRef {
	out := []CNPGOperatorConfigRef{}
	seen := map[string]bool{}
	add := func(ref CNPGOperatorConfigRef) {
		key := ref.Kind + "\x00" + ref.Namespace + "\x00" + ref.Name + "\x00" + ref.Purpose
		if ref.Name == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, ref)
	}
	for _, d := range operators {
		c := cnpgOperatorContainerOf(d)
		if c == nil {
			continue
		}
		if name := cnpgOperatorExpand(cnpgOperatorArg(c, "--config-map-name"), c, d); name != "" {
			add(s.operatorConfigMap(ctx, cache, d.Namespace, name, cnpgConfigPurposeOperator))
		}
		if name := cnpgOperatorExpand(cnpgOperatorArg(c, "--secret-name"), c, d); name != "" {
			add(CNPGOperatorConfigRef{Kind: "Secret", Namespace: d.Namespace, Name: name, Purpose: cnpgConfigPurposeOperator})
		}
		if name := cnpgOperatorEnv(c, cnpgMonitoringQueriesCM); name != "" {
			add(s.operatorConfigMap(ctx, cache, d.Namespace, name, cnpgConfigPurposeMonitoring))
		}
	}
	return out
}

func (s *Reader) operatorConfigMap(ctx context.Context, cache *k8s.ResourceCache, namespace, name, purpose string) CNPGOperatorConfigRef {
	ref := CNPGOperatorConfigRef{Kind: "ConfigMap", Namespace: namespace, Name: name, Purpose: purpose}
	state := &CNPGOperatorConfigMapState{}
	ref.CNPGOperatorConfigMapState = state
	if !s.Access.CanRead(ctx, "", "configmaps", namespace, "get") {
		state.Reason = "no permission to get ConfigMaps in " + namespace
		return ref
	}
	lister := cache.ConfigMaps()
	if lister == nil {
		state.Reason = "ConfigMaps are still loading"
		return ref
	}
	if !integration.CacheCoversNamespace(cache, "configmaps", namespace) {
		state.Reason = "Radar does not watch ConfigMaps in " + namespace
		return ref
	}
	cm, err := lister.ConfigMaps(namespace).Get(name)
	switch {
	case apierrors.IsNotFound(err):
		exists := false
		state.Exists = &exists
		state.Reason = "not found"
		return ref
	case err != nil:
		log.Printf("[cnpg] Failed to read ConfigMap %s/%s: %v", namespace, name, err)
		state.Reason = "could not read the ConfigMap"
		return ref
	}
	exists := true
	state.Exists = &exists
	state.Readable = true
	state.Data = map[string]string{}
	for k, v := range cm.Data {
		state.Data[k] = v
	}
	return ref
}
