package server

import (
	"log"
	"net/http"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"

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
// counts are nil when unreported, which is not zero.
type CNPGOperatorComponent struct {
	Role          string `json:"role"`
	PluginName    string `json:"pluginName,omitempty"`
	Namespace     string `json:"namespace"`
	Deployment    string `json:"deployment"`
	Image         string `json:"image"`
	Version       string `json:"version"`
	ReadyReplicas *int32 `json:"readyReplicas"`
	Replicas      *int32 `json:"replicas"`
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
	Coverage   map[string]KindCoverage `json:"coverage"`
	Components []CNPGOperatorComponent `json:"components"`
	Config     []CNPGOperatorConfigRef `json:"config"`
	// Diagnosis is one entry per operator Deployment: leader Lease, watched
	// namespaces, webhook reachability, reconcile counters and recent events.
	Diagnosis []CNPGOperatorDiagnosis `json:"diagnosis"`
}

// handleCNPGOperator serves GET /api/cnpg/operator: the operator and plugin
// Deployments, their versions and readiness, and where the operator's
// configuration lives.
//
// The operator runs in its own namespace (cnpg-system by default) while
// people filter the view to their application namespaces. Following the view
// filter would report "no operator" to anyone looking at their databases, so
// scope follows permission here, as it does for the catalog reverse lookups.
func (s *Server) handleCNPGOperator(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}

	scope := s.cnpgOperatorScope(r)
	resp := CNPGOperatorResponse{
		Coverage:   map[string]KindCoverage{},
		Components: []CNPGOperatorComponent{},
		Config:     []CNPGOperatorConfigRef{},
	}

	depAcc, deployments := s.cnpgOperatorDeployments(r, cache, scope)
	resp.Coverage["deployments"] = depAcc.coverage()
	svcAcc, services := s.cnpgOperatorServices(r, cache, scope)
	resp.Coverage["services"] = svcAcc.coverage()

	var operators []*appsv1.Deployment
	for _, d := range deployments {
		if d.Labels[cnpgOperatorNameLabel] == cnpgOperatorNameValue {
			operators = append(operators, d)
			resp.Components = append(resp.Components, cnpgOperatorComponent(d, cnpgOperatorRoleOperator, "", cnpgOperatorContainerOf(d)))
		}
	}

	byNamespace := map[string][]*appsv1.Deployment{}
	for _, d := range deployments {
		byNamespace[d.Namespace] = append(byNamespace[d.Namespace], d)
	}
	var plugins []CNPGOperatorComponent
	for _, svc := range services {
		pluginName := svc.Labels[cnpgPluginNameLabel]
		if pluginName == "" || !depAcc.covers(svc.Namespace) {
			continue
		}
		matched := false
		if len(svc.Spec.Selector) > 0 {
			sel := labels.SelectorFromSet(svc.Spec.Selector)
			for _, d := range byNamespace[svc.Namespace] {
				if sel.Matches(labels.Set(d.Spec.Template.Labels)) {
					matched = true
					plugins = append(plugins, cnpgOperatorComponent(d, cnpgOperatorRolePlugin, pluginName, firstContainer(d)))
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

	resp.Config = s.cnpgOperatorConfig(r, cache, operators)
	resp.Diagnosis = s.cnpgOperatorDiagnoses(r, operators)
	s.writeJSON(w, resp)
}

// cnpgOperatorScope is the caller's RBAC scope without the view filter. A
// Radar forced into one namespace still answers only for that namespace.
func (s *Server) cnpgOperatorScope(r *http.Request) []string {
	if k8s.ForceNamespaceScope {
		target := k8s.GetNamespaceScopeTarget()
		if target == "" {
			return []string{}
		}
		return s.getUserNamespaces(r, []string{target})
	}
	return s.getUserNamespaces(r, nil)
}

func (s *Server) cnpgOperatorDeployments(r *http.Request, cache *k8s.ResourceCache, scope []string) (kindAccess, []*appsv1.Deployment) {
	acc, read := s.typedKindScope(r, cache, scope, "apps", "deployments")
	if acc.state == kindCoverageDenied || acc.state == kindCoverageError {
		return acc, nil
	}
	lister := cache.Deployments()
	if lister == nil || !cache.IsKindReady("deployments") {
		return kindAccess{state: kindCoverageSyncing}, nil
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

func (s *Server) cnpgOperatorServices(r *http.Request, cache *k8s.ResourceCache, scope []string) (kindAccess, []*corev1.Service) {
	acc, read := s.typedKindScope(r, cache, scope, "", "services")
	if acc.state == kindCoverageDenied || acc.state == kindCoverageError {
		return acc, nil
	}
	lister := cache.Services()
	if lister == nil || !cache.IsKindReady("services") {
		return kindAccess{state: kindCoverageSyncing}, nil
	}
	hasPlugin, err := labels.Parse(cnpgPluginNameLabel)
	if err != nil {
		log.Printf("[cnpg] Failed to build plugin selector: %v", err)
		return kindAccess{state: kindCoverageError}, nil
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
		out.Version = imageTag(c.Image)
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

func (s *Server) cnpgOperatorConfig(r *http.Request, cache *k8s.ResourceCache, operators []*appsv1.Deployment) []CNPGOperatorConfigRef {
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
			add(s.cnpgOperatorConfigMap(r, cache, d.Namespace, name, cnpgConfigPurposeOperator))
		}
		if name := cnpgOperatorExpand(cnpgOperatorArg(c, "--secret-name"), c, d); name != "" {
			add(CNPGOperatorConfigRef{Kind: "Secret", Namespace: d.Namespace, Name: name, Purpose: cnpgConfigPurposeOperator})
		}
		if name := cnpgOperatorEnv(c, cnpgMonitoringQueriesCM); name != "" {
			add(s.cnpgOperatorConfigMap(r, cache, d.Namespace, name, cnpgConfigPurposeMonitoring))
		}
	}
	return out
}

func (s *Server) cnpgOperatorConfigMap(r *http.Request, cache *k8s.ResourceCache, namespace, name, purpose string) CNPGOperatorConfigRef {
	ref := CNPGOperatorConfigRef{Kind: "ConfigMap", Namespace: namespace, Name: name, Purpose: purpose}
	state := &CNPGOperatorConfigMapState{}
	ref.CNPGOperatorConfigMapState = state
	if !s.canRead(r, "", "configmaps", namespace, "get") {
		state.Reason = "no permission to get ConfigMaps in " + namespace
		return ref
	}
	lister := cache.ConfigMaps()
	if lister == nil {
		state.Reason = "ConfigMaps are still loading"
		return ref
	}
	if !cacheCoversNamespace(cache, "configmaps", namespace) {
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
