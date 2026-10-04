package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
)

func cnpgOperatorDeployment() *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cnpg-controller-manager", Namespace: "cnpg-system", Generation: 1,
			Labels: map[string]string{"app.kubernetes.io/name": "cloudnative-pg"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(1),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "cloudnative-pg"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app.kubernetes.io/name": "cloudnative-pg"}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{
					{Name: "sidecar", Image: "busybox:1.36"},
					{
						Name:    "manager",
						Image:   "ghcr.io/cloudnative-pg/cloudnative-pg:1.27.0",
						Command: []string{"/manager"},
						Args: []string{
							"controller", "--leader-elect",
							"--config-map-name=$(OPERATOR_DEPLOYMENT_NAME)-config",
							"--secret-name", "$(OPERATOR_DEPLOYMENT_NAME)-config",
						},
						Env: []corev1.EnvVar{
							{Name: "OPERATOR_DEPLOYMENT_NAME", Value: "cnpg-controller-manager"},
							{Name: "MONITORING_QUERIES_CONFIGMAP", Value: "cnpg-default-monitoring"},
						},
					},
				}},
			},
		},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 1, Replicas: 1, ReadyReplicas: 1},
	}
}

func cnpgPluginDeployment() *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "barman-cloud", Namespace: "cnpg-system"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "barman-cloud"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "barman-cloud"}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{
					{Name: "barman-cloud", Image: "ghcr.io/cloudnative-pg/plugin-barman-cloud:v0.5.0"},
				}},
			},
		},
	}
}

func cnpgPluginService() *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "barman-cloud", Namespace: "cnpg-system",
			Labels: map[string]string{"cnpg.io/pluginName": "barman-cloud.cloudnative-pg.io"},
		},
		Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "barman-cloud"}},
	}
}

func cnpgOperatorConfigMapObj() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "cnpg-controller-manager-config", Namespace: "cnpg-system"},
		Data:       map[string]string{"INHERITED_ANNOTATIONS": "team/*"},
	}
}

// seedCNPGOperator creates typed objects in the shared fake cluster and waits
// until the cache serves each one.
func seedCNPGOperator(t *testing.T, deployments []*appsv1.Deployment, services []*corev1.Service, configMaps []*corev1.ConfigMap) {
	t.Helper()
	ctx := context.Background()
	for _, d := range deployments {
		if _, err := testFakeClient.AppsV1().Deployments(d.Namespace).Create(ctx, d, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create deployment %s: %v", d.Name, err)
		}
		t.Cleanup(func() {
			_ = testFakeClient.AppsV1().Deployments(d.Namespace).Delete(context.Background(), d.Name, metav1.DeleteOptions{})
		})
	}
	for _, svc := range services {
		if _, err := testFakeClient.CoreV1().Services(svc.Namespace).Create(ctx, svc, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create service %s: %v", svc.Name, err)
		}
		t.Cleanup(func() {
			_ = testFakeClient.CoreV1().Services(svc.Namespace).Delete(context.Background(), svc.Name, metav1.DeleteOptions{})
		})
	}
	for _, cm := range configMaps {
		if _, err := testFakeClient.CoreV1().ConfigMaps(cm.Namespace).Create(ctx, cm, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create configmap %s: %v", cm.Name, err)
		}
		t.Cleanup(func() {
			_ = testFakeClient.CoreV1().ConfigMaps(cm.Namespace).Delete(context.Background(), cm.Name, metav1.DeleteOptions{})
		})
	}
	cache := k8s.GetResourceCache()
	deadline := time.Now().Add(5 * time.Second)
	for {
		missing := 0
		for _, d := range deployments {
			if _, err := cache.Deployments().Deployments(d.Namespace).Get(d.Name); err != nil {
				missing++
			}
		}
		for _, svc := range services {
			if _, err := cache.Services().Services(svc.Namespace).Get(svc.Name); err != nil {
				missing++
			}
		}
		for _, cm := range configMaps {
			if l := cache.ConfigMaps(); l == nil {
				missing++
			} else if _, err := l.ConfigMaps(cm.Namespace).Get(cm.Name); err != nil {
				missing++
			}
		}
		if missing == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d operator fixtures did not reach the cache", missing)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func seedFullCNPGOperator(t *testing.T) {
	t.Helper()
	seedCNPGOperator(t,
		[]*appsv1.Deployment{cnpgOperatorDeployment(), cnpgPluginDeployment()},
		[]*corev1.Service{cnpgPluginService()},
		[]*corev1.ConfigMap{cnpgOperatorConfigMapObj()},
	)
}

func readCNPGOperator(t *testing.T, resp *http.Response) (CNPGOperatorResponse, []byte) {
	t.Helper()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, body)
	}
	var out CNPGOperatorResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out, body
}

func getCNPGOperatorNoAuth(t *testing.T, query string) (CNPGOperatorResponse, []byte) {
	t.Helper()
	resp, err := http.Get(testServer.URL + "/api/cnpg/operator" + query)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	return readCNPGOperator(t, resp)
}

func findConfigRef(refs []CNPGOperatorConfigRef, kind, purpose string) *CNPGOperatorConfigRef {
	for i := range refs {
		if refs[i].Kind == kind && refs[i].Purpose == purpose {
			return &refs[i]
		}
	}
	return nil
}

func TestCNPGOperator_DiscoversOperatorPluginAndConfig(t *testing.T) {
	seedFullCNPGOperator(t)

	got, body := getCNPGOperatorNoAuth(t, "")
	for _, key := range []string{"deployments", "services"} {
		if got.Coverage[key].State != kindCoverageFull {
			t.Errorf("coverage[%s] = %+v, want full", key, got.Coverage[key])
		}
	}
	if len(got.Components) != 2 {
		t.Fatalf("components = %+v, want operator then plugin", got.Components)
	}
	op, plugin := got.Components[0], got.Components[1]
	if op.Role != "operator" || op.Namespace != "cnpg-system" || op.Deployment != "cnpg-controller-manager" ||
		op.Image != "ghcr.io/cloudnative-pg/cloudnative-pg:1.27.0" || op.Version != "1.27.0" {
		t.Errorf("operator = %+v", op)
	}
	if op.ReadyReplicas == nil || *op.ReadyReplicas != 1 || op.Replicas == nil || *op.Replicas != 1 {
		t.Errorf("operator readiness = %v/%v, want 1/1", op.ReadyReplicas, op.Replicas)
	}
	if plugin.Role != "plugin" || plugin.PluginName != "barman-cloud.cloudnative-pg.io" || plugin.Deployment != "barman-cloud" || plugin.Version != "v0.5.0" {
		t.Errorf("plugin = %+v", plugin)
	}
	if plugin.ReadyReplicas != nil {
		t.Errorf("plugin readyReplicas = %d, want null when the controller has reported no status", *plugin.ReadyReplicas)
	}

	cm := findConfigRef(got.Config, "ConfigMap", "operator")
	if cm == nil || cm.Name != "cnpg-controller-manager-config" || cm.Namespace != "cnpg-system" || cm.CNPGOperatorConfigMapState == nil ||
		!cm.Readable || cm.Exists == nil || !*cm.Exists || cm.Data["INHERITED_ANNOTATIONS"] != "team/*" {
		t.Errorf("operator ConfigMap = %+v", cm)
	}
	secret := findConfigRef(got.Config, "Secret", "operator")
	if secret == nil || secret.Name != "cnpg-controller-manager-config" {
		t.Errorf("operator Secret = %+v, want the space-separated --secret-name resolved", secret)
	}
	mon := findConfigRef(got.Config, "ConfigMap", "monitoring")
	if mon == nil || mon.Name != "cnpg-default-monitoring" || mon.Readable || mon.Exists == nil || *mon.Exists {
		t.Errorf("monitoring ConfigMap = %+v, want exists=false readable=false", mon)
	}

	var raw struct {
		Config     []map[string]any `json:"config"`
		Components []map[string]any `json:"components"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("raw decode: %v", err)
	}
	for _, ref := range raw.Config {
		if ref["kind"] != "Secret" {
			continue
		}
		for _, k := range []string{"data", "exists", "readable", "keys"} {
			if _, ok := ref[k]; ok {
				t.Errorf("Secret reference carries %q: %v", k, ref)
			}
		}
	}
	if v, ok := raw.Components[1]["readyReplicas"]; !ok || v != nil {
		t.Errorf("plugin readyReplicas JSON = %v (present=%v), want explicit null", v, ok)
	}
}

func TestCNPGOperator_VersionFallsBackToLabelAndNeverInvents(t *testing.T) {
	digest := cnpgOperatorDeployment()
	digest.Name = "pinned"
	digest.Labels["app.kubernetes.io/version"] = "1.26.1"
	digest.Spec.Template.Spec.Containers[1].Image = "ghcr.io/cloudnative-pg/cloudnative-pg@sha256:abc"
	bare := cnpgOperatorDeployment()
	bare.Name = "bare"
	bare.Spec.Template.Spec.Containers[1].Image = "ghcr.io/cloudnative-pg/cloudnative-pg"
	seedCNPGOperator(t, []*appsv1.Deployment{digest, bare}, nil, nil)

	got, _ := getCNPGOperatorNoAuth(t, "")
	versions := map[string]string{}
	for _, c := range got.Components {
		versions[c.Deployment] = c.Version
	}
	if versions["pinned"] != "1.26.1" {
		t.Errorf("digest-pinned version = %q, want the version label", versions["pinned"])
	}
	if v, ok := versions["bare"]; !ok || v != "" {
		t.Errorf("untagged, unlabelled version = %q (found=%v), want empty", v, ok)
	}
}

func TestCNPGOperator_IgnoresNamespaceViewFilter(t *testing.T) {
	seedFullCNPGOperator(t)
	got, _ := getCNPGOperatorNoAuth(t, "?namespaces=default")
	if len(got.Components) == 0 || got.Components[0].Deployment != "cnpg-controller-manager" {
		t.Errorf("components = %+v, want the operator in cnpg-system despite a view filter on default", got.Components)
	}

	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"cnpg-system", "default"}}
	allow(perms, "apps", "deployments", "", true)
	allow(perms, "", "services", "", true)
	env.srv.permCache.Set("viewer", nil, perms)
	authed, _ := readCNPGOperator(t, env.authGet(t, "/api/cnpg/operator?namespaces=default", "viewer", ""))
	if len(authed.Components) == 0 || authed.Components[0].Namespace != "cnpg-system" {
		t.Errorf("auth components = %+v, want the operator despite the view filter", authed.Components)
	}
}

func TestCNPGOperator_ConfigMapDataNeedsGet(t *testing.T) {
	seedFullCNPGOperator(t)
	env := newAuthTestServer(t)
	for _, u := range []struct {
		name  string
		getCM bool
	}{{"reads-cm", true}, {"no-cm", false}} {
		perms := &auth.UserPermissions{AllowedNamespaces: []string{"cnpg-system"}}
		allow(perms, "apps", "deployments", "", true)
		allow(perms, "", "services", "", true)
		perms.SetCanI("get", "", "configmaps", "cnpg-system", u.getCM)
		env.srv.permCache.Set(u.name, nil, perms)
	}

	got, _ := readCNPGOperator(t, env.authGet(t, "/api/cnpg/operator", "reads-cm", ""))
	cm := findConfigRef(got.Config, "ConfigMap", "operator")
	if cm == nil || cm.CNPGOperatorConfigMapState == nil || !cm.Readable || cm.Data["INHERITED_ANNOTATIONS"] != "team/*" {
		t.Errorf("with get configmaps: %+v", cm)
	}

	got, body := readCNPGOperator(t, env.authGet(t, "/api/cnpg/operator", "no-cm", ""))
	cm = findConfigRef(got.Config, "ConfigMap", "operator")
	if cm == nil || cm.CNPGOperatorConfigMapState == nil || cm.Readable || cm.Exists != nil || cm.Data != nil || cm.Reason == "" {
		t.Errorf("without get configmaps: %+v, want unreadable, existence unknown, no data, a reason", cm)
	}
	var raw struct {
		Config []map[string]any `json:"config"`
	}
	_ = json.Unmarshal(body, &raw)
	for _, ref := range raw.Config {
		if ref["kind"] == "ConfigMap" && ref["purpose"] == "operator" && ref["data"] != nil {
			t.Errorf("ConfigMap data returned without get: %v", ref)
		}
	}
}

func cnpgOperatorTestPod(name string, labels map[string]string, statuses ...corev1.ContainerStatus) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "cnpg-system", UID: types.UID(name + "-uid"), Labels: labels},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			StartTime:         &metav1.Time{Time: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
			Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			ContainerStatuses: statuses,
		},
	}
}

func TestCNPGOperatorComponentPods(t *testing.T) {
	started := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	running := corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(started)}}
	opLabels := map[string]string{"app.kubernetes.io/name": "cloudnative-pg"}
	crashed := cnpgOperatorTestPod("cnpg-controller-manager-a", opLabels,
		corev1.ContainerStatus{Name: "sidecar", RestartCount: 3, State: running, LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason: "Completed", ExitCode: 0, FinishedAt: metav1.NewTime(started.Add(-48 * time.Hour)),
		}}},
		corev1.ContainerStatus{Name: "manager", RestartCount: 30, State: running, LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason: "OOMKilled", ExitCode: 137, FinishedAt: metav1.NewTime(started.Add(-time.Minute)),
		}}},
	)
	steady := cnpgOperatorTestPod("cnpg-controller-manager-b", opLabels, corev1.ContainerStatus{Name: "manager", State: running})
	other := cnpgOperatorTestPod("unrelated", map[string]string{"app": "other"})
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	d := cnpgOperatorDeployment()
	read := s.cnpgDeploymentPods(req, k8sfake.NewSimpleClientset(steady, crashed, other), d)

	comp := withCNPGComponentPods(cnpgOperatorComponent(d, cnpgOperatorRoleOperator, "", cnpgOperatorContainerOf(d)), read, cnpgOperatorContainerOf(d))
	if comp.PodCoverage == nil || comp.PodCoverage.State != cnpgReadOK || len(comp.Pods) != 2 {
		t.Fatalf("component = %+v", comp)
	}
	a, b := comp.Pods[0], comp.Pods[1]
	if a.Name != "cnpg-controller-manager-a" || !a.Ready || a.Restarts != 33 || a.StartedAt != "2026-10-04T09:00:00Z" {
		t.Errorf("crashed pod = %+v, want restarts summed over containers and the manager container's start", a)
	}
	if lt := a.LastTermination; lt == nil || lt.Container != "manager" || lt.Reason != "OOMKilled" || lt.ExitCode != 137 || lt.FinishedAt != "2026-10-04T08:59:00Z" {
		t.Errorf("lastTermination = %+v, want the most recently ended container's", a.LastTermination)
	}
	if b.Name != "cnpg-controller-manager-b" || b.Restarts != 0 || b.LastTermination != nil {
		t.Errorf("steady pod = %+v", b)
	}

	diag := cnpgOperatorPodOf(&read.pods[0])
	if diag.Restarts != 33 || diag.LastTermination == nil || diag.LastTermination.Reason != "OOMKilled" || diag.StartedAt != "2026-09-01T00:00:00Z" {
		t.Errorf("diagnosis pod = %+v, want the same restarts and termination, with the Pod's own start", diag)
	}

	notRunning := cnpgOperatorTestPod("cnpg-controller-manager-c", opLabels, corev1.ContainerStatus{Name: "manager", RestartCount: 70,
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}})
	comp = withCNPGComponentPods(CNPGOperatorComponent{}, cnpgDeploymentPodRead{pods: []corev1.Pod{*notRunning}, coverage: ReadSource{State: cnpgReadOK}}, cnpgOperatorContainerOf(d))
	if p := comp.Pods[0]; p.StartedAt != "" || p.Restarts != 70 {
		t.Errorf("a container that is not running has no current start: %+v", p)
	}
}

func TestCNPGOperatorComponentPodsDenied(t *testing.T) {
	typed := k8sfake.NewSimpleClientset()
	typed.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apiForbidden("pods")
	})
	d := cnpgPluginDeployment()
	read := (&Server{}).cnpgDeploymentPods(httptest.NewRequest(http.MethodGet, "/", nil), typed, d)
	comp := withCNPGComponentPods(cnpgOperatorComponent(d, cnpgOperatorRolePlugin, "barman-cloud.cloudnative-pg.io", firstContainer(d)), read, firstContainer(d))
	if g := comp.PodCoverage.Grant; comp.PodCoverage.State != cnpgReadDenied || g == nil || g.Verb != "list" || g.Resource != "pods" || g.Namespace != "cnpg-system" {
		t.Fatalf("podCoverage = %+v, want denied naming list pods in cnpg-system", comp.PodCoverage)
	}
	if comp.Pods != nil {
		t.Errorf("pods = %+v, want none when they cannot be read", comp.Pods)
	}

	seedFullCNPGOperator(t)
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"cnpg-system"}}
	allow(perms, "apps", "deployments", "", true)
	allow(perms, "", "services", "", true)
	allow(perms, "", "pods", "cnpg-system", false)
	env.srv.permCache.Set("no-pods", nil, perms)
	got, body := readCNPGOperator(t, env.authGet(t, "/api/cnpg/operator", "no-pods", ""))
	if len(got.Components) != 2 {
		t.Fatalf("components = %+v, want both despite the Pods denial", got.Components)
	}
	var raw struct {
		Components []map[string]any `json:"components"`
	}
	_ = json.Unmarshal(body, &raw)
	for i, c := range got.Components {
		if c.PodCoverage == nil || c.PodCoverage.State != cnpgReadDenied || c.PodCoverage.Grant == nil || c.PodCoverage.Grant.Resource != "pods" {
			t.Errorf("%s podCoverage = %+v", c.Deployment, c.PodCoverage)
		}
		if v, ok := raw.Components[i]["pods"]; !ok || v != nil {
			t.Errorf("%s pods JSON = %v, want null", c.Deployment, v)
		}
	}
}

func TestCNPGOperator_DeniedDeploymentsWithholdComponents(t *testing.T) {
	seedFullCNPGOperator(t)
	env := newAuthTestServer(t)

	partial := &auth.UserPermissions{AllowedNamespaces: []string{"cnpg-system", "default"}}
	allow(partial, "apps", "deployments", "", false)
	allow(partial, "apps", "deployments", "cnpg-system", false)
	allow(partial, "apps", "deployments", "default", true)
	allow(partial, "", "services", "", true)
	env.srv.permCache.Set("partial", nil, partial)

	got, _ := readCNPGOperator(t, env.authGet(t, "/api/cnpg/operator", "partial", ""))
	cov := got.Coverage["deployments"]
	if cov.State != kindCoveragePartial || len(cov.DeniedNamespaces) != 1 || cov.DeniedNamespaces[0] != "cnpg-system" {
		t.Errorf("deployments coverage = %+v, want partial denied [cnpg-system]", cov)
	}
	for _, c := range got.Components {
		if c.Namespace == "cnpg-system" {
			t.Errorf("component from a namespace whose Deployments are denied: %+v", c)
		}
	}
	if len(got.Config) != 0 {
		t.Errorf("config = %+v, want none without a visible operator", got.Config)
	}

	none := &auth.UserPermissions{AllowedNamespaces: []string{"cnpg-system"}}
	allow(none, "apps", "deployments", "", false)
	allow(none, "apps", "deployments", "cnpg-system", false)
	allow(none, "", "services", "", false)
	allow(none, "", "services", "cnpg-system", false)
	env.srv.permCache.Set("none", nil, none)

	got, _ = readCNPGOperator(t, env.authGet(t, "/api/cnpg/operator", "none", ""))
	if got.Coverage["deployments"].State != kindCoverageDenied || got.Coverage["services"].State != kindCoverageDenied {
		t.Errorf("coverage = %+v, want both denied", got.Coverage)
	}
	if got.Components == nil || len(got.Components) != 0 || got.Config == nil {
		t.Errorf("components=%v config=%v, want empty arrays", got.Components, got.Config)
	}
}
