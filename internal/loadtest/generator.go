// Package loadtest generates topology-realistic synthetic Kubernetes objects
// for driving Radar's UI at high object counts without a real cluster.
//
// The objects form a coherent Deployment -> ReplicaSet -> Pod hierarchy with
// matching Service selectors, spread across namespaces and nodes, so the
// resources browser, topology graph and workload views all render meaningful
// relationships rather than a flat pile of orphan pods.
//
// A Generator both seeds an initial population (delivered through the fake
// clientset's initial LIST) and scales the live population up or down through
// the fake clientset's Create/Delete paths. Live mutations travel over the
// fake clientset's watch channel, which panics if more than
// watch.DefaultChanSize (100) events queue before the informer drains them, so
// scaling is batched below that bound and paced against the informer's observed
// count between batches.
package loadtest

import (
	"context"
	"fmt"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
)

// Defaults for a Config. Chosen so a 50k-pod population produces a plausible
// cluster shape (~250 apps over 10 namespaces on 50 nodes) rather than one
// giant workload.
const (
	DefaultNodes      = 50
	DefaultNamespaces = 10
	DefaultPodsPerApp = 200
	DefaultImage      = "registry.k8s.io/pause:3.9"

	// batchSize keeps live mutations per drain-cycle below the fake watch
	// channel's DefaultChanSize (100) so it never panics with "channel full".
	batchSize = 90

	// appBatchSize bounds how many apps are created/deleted per drain-cycle.
	// Each app touches five distinct watchers with one event apiece, so the
	// per-watcher burst equals appBatchSize — kept below the 100 buffer.
	appBatchSize = 80

	// hardMaxPods bounds a single scale request so a fat-fingered value can't
	// try to allocate an unbounded number of objects.
	hardMaxPods = 2_000_000
)

// Config describes the synthetic population.
type Config struct {
	Pods       int    // number of pods to materialize
	Nodes      int    // number of fake nodes to spread pods across
	Namespaces int    // number of namespaces to spread apps across
	PodsPerApp int    // pods per Deployment/ReplicaSet (sets the app count)
	Image      string // container image reported on every pod
}

func (c Config) withDefaults() Config {
	if c.Nodes <= 0 {
		c.Nodes = DefaultNodes
	}
	if c.Namespaces <= 0 {
		c.Namespaces = DefaultNamespaces
	}
	if c.PodsPerApp <= 0 {
		c.PodsPerApp = DefaultPodsPerApp
	}
	if c.Image == "" {
		c.Image = DefaultImage
	}
	if c.Pods < 0 {
		c.Pods = 0
	}
	return c
}

// Generator produces and mutates a deterministic synthetic population. The set
// of objects is a pure function of the target pod count, so scaling is a diff
// against the previous target rather than stateful bookkeeping.
type Generator struct {
	cfg     Config
	mu      sync.Mutex
	current int // pods materialized after the last Seed/ScaleTo
	// App skeletons need two high-water marks because a partially-written app
	// (some of its five objects created, then a failure) must be handled two
	// different ways: cleanup must delete it, but a create retry must finish it.
	//   appsCompleted   — apps whose five objects all succeeded; where a create
	//                     resumes, so a partial app is re-completed rather than
	//                     skipped.
	//   appsMaterialized — the furthest app index any object was written for; the
	//                     upper bound cleanup deletes down from, so a partial
	//                     app's stray objects are never orphaned.
	// On success both equal the app count; they only diverge after a failure.
	appsCompleted    int
	appsMaterialized int
}

// New returns a Generator for cfg (with defaults applied).
func New(cfg Config) *Generator {
	return &Generator{cfg: cfg.withDefaults()}
}

// Config returns the effective (defaulted) configuration.
func (g *Generator) Config() Config { return g.cfg }

// AppCount returns the number of fully-created Deployment/ReplicaSet/Service/
// ConfigMap/Secret apps.
func (g *Generator) AppCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.appsCompleted
}

// Result reports the outcome of a scale operation.
type Result struct {
	From       int  `json:"from"`
	To         int  `json:"to"`
	Apps       int  `json:"apps"`
	Nodes      int  `json:"nodes"`
	Namespaces int  `json:"namespaces"`
	Converged  bool `json:"converged"`
}

// ---- deterministic naming / layout ----

func appsFor(pods, perApp int) int {
	if pods <= 0 {
		return 0
	}
	return (pods + perApp - 1) / perApp
}

func podsInApp(app, pods, perApp int) int {
	base := app * perApp
	if base >= pods {
		return 0
	}
	if pods-base < perApp {
		return pods - base
	}
	return perApp
}

func (g *Generator) appIndex(pod int) int      { return pod / g.cfg.PodsPerApp }
func (g *Generator) nsName(app int) string     { return fmt.Sprintf("loadtest-%02d", app%g.cfg.Namespaces) }
func (g *Generator) appName(app int) string    { return fmt.Sprintf("app-%04d", app) }
func (g *Generator) rsName(app int) string     { return fmt.Sprintf("app-%04d-rs", app) }
func (g *Generator) cmName(app int) string     { return fmt.Sprintf("app-%04d-config", app) }
func (g *Generator) secretName(app int) string { return fmt.Sprintf("app-%04d-secret", app) }
func (g *Generator) nodeName(i int) string     { return fmt.Sprintf("loadtest-node-%03d", i%g.cfg.Nodes) }
func (g *Generator) podName(pod int) string {
	return fmt.Sprintf("app-%04d-%06d", g.appIndex(pod), pod)
}
func (g *Generator) deployUID(app int) types.UID {
	return types.UID(fmt.Sprintf("loadtest-deploy-%04d", app))
}
func (g *Generator) rsUID(app int) types.UID {
	return types.UID(fmt.Sprintf("loadtest-rs-%04d", app))
}
func (g *Generator) appLabels(app int) map[string]string {
	return map[string]string{"app": g.appName(app), "loadtest": "true"}
}

// ---- object builders ----

func (g *Generator) buildNamespace(i int) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("loadtest-%02d", i), Labels: map[string]string{"loadtest": "true"}},
		Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
	}
}

func (g *Generator) buildNode(i int) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("loadtest-node-%03d", i), Labels: map[string]string{"loadtest": "true"}},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
			Capacity: corev1.ResourceList{
				corev1.ResourcePods:   resource.MustParse("1100"),
				corev1.ResourceCPU:    resource.MustParse("8"),
				corev1.ResourceMemory: resource.MustParse("32Gi"),
			},
			Allocatable: corev1.ResourceList{
				corev1.ResourcePods:   resource.MustParse("1100"),
				corev1.ResourceCPU:    resource.MustParse("8"),
				corev1.ResourceMemory: resource.MustParse("32Gi"),
			},
		},
	}
}

func (g *Generator) podTemplate(app int) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: g.appLabels(app)},
		Spec:       g.podSpec(0, app),
	}
}

func (g *Generator) buildDeployment(app, replicas int) *appsv1.Deployment {
	r := int32(replicas)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      g.appName(app),
			Namespace: g.nsName(app),
			UID:       g.deployUID(app),
			Labels:    g.appLabels(app),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &r,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": g.appName(app)}},
			Template: g.podTemplate(app),
		},
		Status: appsv1.DeploymentStatus{Replicas: r, ReadyReplicas: r, AvailableReplicas: r, UpdatedReplicas: r},
	}
}

func (g *Generator) buildReplicaSet(app, replicas int) *appsv1.ReplicaSet {
	r := int32(replicas)
	return &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      g.rsName(app),
			Namespace: g.nsName(app),
			UID:       g.rsUID(app),
			Labels:    g.appLabels(app),
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1", Kind: "Deployment", Name: g.appName(app),
				UID: g.deployUID(app), Controller: boolPtr(true),
			}},
		},
		Spec: appsv1.ReplicaSetSpec{
			Replicas: &r,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": g.appName(app)}},
			Template: g.podTemplate(app),
		},
		Status: appsv1.ReplicaSetStatus{Replicas: r, ReadyReplicas: r, AvailableReplicas: r},
	}
}

func (g *Generator) buildConfigMap(app int) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: g.cmName(app), Namespace: g.nsName(app), Labels: g.appLabels(app)},
		Data:       map[string]string{"app.conf": fmt.Sprintf("name=%s\nreplicas=%d\n", g.appName(app), g.cfg.PodsPerApp)},
	}
}

func (g *Generator) buildSecret(app int) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: g.secretName(app), Namespace: g.nsName(app), Labels: g.appLabels(app)},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"token": []byte("c3ludGhldGljLWxvYWR0ZXN0")},
	}
}

func (g *Generator) buildService(app int) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      g.appName(app),
			Namespace: g.nsName(app),
			Labels:    g.appLabels(app),
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": g.appName(app)},
			Ports:    []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromInt(80)}},
		},
	}
}

func (g *Generator) podSpec(pod, app int) corev1.PodSpec {
	return corev1.PodSpec{
		NodeName: g.nodeName(pod),
		Containers: []corev1.Container{{
			Name:  "app",
			Image: g.cfg.Image,
			EnvFrom: []corev1.EnvFromSource{{
				SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: g.secretName(app)}},
			}},
			Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("10m"),
				corev1.ResourceMemory: resource.MustParse("16Mi"),
			}},
		}},
		Volumes: []corev1.Volume{{
			Name: "config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: g.cmName(app)}},
			},
		}},
	}
}

func (g *Generator) buildPod(pod int) *corev1.Pod {
	app := g.appIndex(pod)
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      g.podName(pod),
			Namespace: g.nsName(app),
			Labels:    g.appLabels(app),
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1", Kind: "ReplicaSet", Name: g.rsName(app),
				UID: g.rsUID(app), Controller: boolPtr(true),
			}},
		},
		Spec: g.podSpec(pod, app),
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "app", Ready: true, Started: boolPtr(true),
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			}},
		},
	}
}

// SeedObjects returns the full object population for the configured pod count.
// These are handed to fake.NewClientset, so they arrive through the informers'
// initial LIST (never the watch channel) and are safe at any count.
func (g *Generator) SeedObjects() []runtime.Object {
	g.mu.Lock()
	defer g.mu.Unlock()

	objs := make([]runtime.Object, 0, g.cfg.Namespaces+g.cfg.Nodes+g.cfg.Pods+appsFor(g.cfg.Pods, g.cfg.PodsPerApp)*5)
	for i := 0; i < g.cfg.Namespaces; i++ {
		objs = append(objs, g.buildNamespace(i))
	}
	for i := 0; i < g.cfg.Nodes; i++ {
		objs = append(objs, g.buildNode(i))
	}
	apps := appsFor(g.cfg.Pods, g.cfg.PodsPerApp)
	for j := 0; j < apps; j++ {
		r := podsInApp(j, g.cfg.Pods, g.cfg.PodsPerApp)
		objs = append(objs, g.buildConfigMap(j), g.buildSecret(j), g.buildDeployment(j, r), g.buildReplicaSet(j, r), g.buildService(j))
	}
	for i := 0; i < g.cfg.Pods; i++ {
		objs = append(objs, g.buildPod(i))
	}
	g.current = g.cfg.Pods
	g.appsCompleted = apps
	g.appsMaterialized = apps
	return objs
}

// Current returns the pod count materialized after the last operation.
func (g *Generator) Current() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.current
}

// ScaleTo brings the live pod population to target by creating or deleting
// objects through client, pacing each batch against the informer so the fake
// watch channel never overflows. count reports the object count the informer
// cache currently holds for a Kind ("Pod", "Deployment"); it is the drain
// signal every watched kind is paced against. It returns once the Pod informer
// has converged on target (Converged=true) or the settle timeout elapses.
func (g *Generator) ScaleTo(ctx context.Context, client kubernetes.Interface, target int, count func(kind string) int) (Result, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if target < 0 {
		target = 0
	}
	if target > hardMaxPods {
		return Result{}, fmt.Errorf("target %d exceeds max %d", target, hardMaxPods)
	}

	from := g.current
	appsWant := appsFor(target, g.cfg.PodsPerApp)
	// The app count before this scale's mutations — the correct basis for
	// reconciling the old boundary app's replica count. Derived from the tracked
	// app mark, not appsFor(from): pods can lag the app count after a partial
	// failure, which would otherwise point the reconcile at the wrong app.
	prevApps := g.appsCompleted

	// Create resumes from the last fully-completed app, so a partially-written
	// app from an earlier failed attempt is finished rather than skipped.
	if appsWant > g.appsCompleted {
		if err := g.createApps(ctx, client, g.appsCompleted, appsWant, target, count); err != nil {
			return Result{}, err
		}
	}

	switch {
	case target > from:
		if err := g.createPods(ctx, client, from, target, count); err != nil {
			return Result{}, err
		}
	case target < from:
		if err := g.deletePods(ctx, client, target, from, count); err != nil {
			return Result{}, err
		}
	}

	if err := g.reconcileBoundaryApps(ctx, client, prevApps, appsWant, target); err != nil {
		return Result{}, err
	}

	// Cleanup deletes down from the furthest app any object was written for, so a
	// partial app's stray objects are removed, not orphaned.
	if g.appsMaterialized > appsWant {
		if err := g.deleteApps(ctx, client, appsWant, g.appsMaterialized, count); err != nil {
			return Result{}, err
		}
	}

	g.current = target
	g.appsCompleted = appsWant
	g.appsMaterialized = appsWant
	converged := waitFor(ctx, func() bool { return count("Pod") == target }, 60*time.Second)

	return Result{
		From: from, To: target, Apps: appsWant,
		Nodes: g.cfg.Nodes, Namespaces: g.cfg.Namespaces, Converged: converged,
	}, nil
}

// createApps materializes app skeletons (ConfigMap/Secret/Deployment/ReplicaSet/
// Service) for indices [first, last). Each app touches five distinct watchers
// with one event apiece, so it batches below the fake watch buffer and paces
// against the Deployment informer between batches — the same drain discipline
// as pods, extended to every watched kind. appsMaterialized is bumped to cover
// app j BEFORE its objects are written (so a mid-app failure still leaves the
// app in the cleanup range) and appsCompleted only AFTER all five succeed (so a
// retry resumes on the unfinished app rather than skipping it). Creates are
// idempotent, so re-running over an already-complete app is a no-op.
func (g *Generator) createApps(ctx context.Context, client kubernetes.Interface, first, last, target int, count func(kind string) int) error {
	j := first
	for j < last {
		end := min(j+appBatchSize, last)
		for ; j < end; j++ {
			if j+1 > g.appsMaterialized {
				g.appsMaterialized = j + 1
			}
			r := podsInApp(j, target, g.cfg.PodsPerApp)
			ns := g.nsName(j)
			if _, err := client.CoreV1().ConfigMaps(ns).Create(ctx, g.buildConfigMap(j), metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
				return err
			}
			if _, err := client.CoreV1().Secrets(ns).Create(ctx, g.buildSecret(j), metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
				return err
			}
			if _, err := client.AppsV1().Deployments(ns).Create(ctx, g.buildDeployment(j, r), metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
				return err
			}
			if _, err := client.AppsV1().ReplicaSets(ns).Create(ctx, g.buildReplicaSet(j, r), metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
				return err
			}
			if _, err := client.CoreV1().Services(ns).Create(ctx, g.buildService(j), metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
				return err
			}
			g.appsCompleted = j + 1
		}
		created := j
		if !waitFor(ctx, func() bool { return count("Deployment") >= created }, 30*time.Second) {
			return fmt.Errorf("informer did not drain after creating %d apps (Deployment=%d)", created, count("Deployment"))
		}
	}
	return nil
}

// deleteApps removes app skeletons for indices [first, last), from the top down,
// batched and paced against the Deployment informer so no watcher overflows.
func (g *Generator) deleteApps(ctx context.Context, client kubernetes.Interface, first, last int, count func(kind string) int) error {
	j := last
	for j > first {
		start := max(j-appBatchSize, first)
		for ; j > start; j-- {
			ns := g.nsName(j - 1)
			_ = client.CoreV1().Services(ns).Delete(ctx, g.appName(j-1), metav1.DeleteOptions{})
			_ = client.AppsV1().ReplicaSets(ns).Delete(ctx, g.rsName(j-1), metav1.DeleteOptions{})
			_ = client.AppsV1().Deployments(ns).Delete(ctx, g.appName(j-1), metav1.DeleteOptions{})
			_ = client.CoreV1().Secrets(ns).Delete(ctx, g.secretName(j-1), metav1.DeleteOptions{})
			_ = client.CoreV1().ConfigMaps(ns).Delete(ctx, g.cmName(j-1), metav1.DeleteOptions{})
			if g.appsCompleted > j-1 {
				g.appsCompleted = j - 1
			}
			g.appsMaterialized = j - 1
		}
		remaining := j
		if !waitFor(ctx, func() bool { return count("Deployment") <= remaining }, 30*time.Second) {
			return fmt.Errorf("informer did not drain after deleting apps down to %d (Deployment=%d)", remaining, count("Deployment"))
		}
	}
	return nil
}

// reconcileBoundaryApps fixes the replica counts of the (at most two) apps whose
// pod share changed but that still exist after the scale: the old last app (now
// possibly full) and the new last app (now the partial boundary). Fully-
// populated interior apps keep PodsPerApp and newly-created apps were already
// set correctly at creation, so neither is touched.
func (g *Generator) reconcileBoundaryApps(ctx context.Context, client kubernetes.Interface, appsHave, appsWant, target int) error {
	boundary := map[int]bool{}
	if j := appsHave - 1; j >= 0 && j < appsWant {
		boundary[j] = true
	}
	if j := appsWant - 1; j >= 0 {
		boundary[j] = true
	}
	for j := range boundary {
		r := podsInApp(j, target, g.cfg.PodsPerApp)
		if err := patchReplicas(ctx, client, g.nsName(j), g.appName(j), g.rsName(j), r); err != nil {
			return err
		}
	}
	return nil
}

func patchReplicas(ctx context.Context, client kubernetes.Interface, ns, deploy, rs string, replicas int) error {
	r := int32(replicas)
	if d, err := client.AppsV1().Deployments(ns).Get(ctx, deploy, metav1.GetOptions{}); err == nil {
		d.Spec.Replicas = &r
		d.Status.Replicas, d.Status.ReadyReplicas, d.Status.AvailableReplicas, d.Status.UpdatedReplicas = r, r, r, r
		if _, err := client.AppsV1().Deployments(ns).Update(ctx, d, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	if s, err := client.AppsV1().ReplicaSets(ns).Get(ctx, rs, metav1.GetOptions{}); err == nil {
		s.Spec.Replicas = &r
		s.Status.Replicas, s.Status.ReadyReplicas, s.Status.AvailableReplicas = r, r, r
		if _, err := client.AppsV1().ReplicaSets(ns).Update(ctx, s, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	return nil
}

// createPods materializes pods [from, to), batched below the fake watch buffer
// and paced against the Pod informer. g.current advances per pod actually
// written, so a mid-batch failure leaves the generator's idea of the population
// consistent with what the client holds (and cleanup covers every written pod).
func (g *Generator) createPods(ctx context.Context, client kubernetes.Interface, from, to int, count func(kind string) int) error {
	i := from
	for i < to {
		end := min(i+batchSize, to)
		for ; i < end; i++ {
			if _, err := client.CoreV1().Pods(g.nsName(g.appIndex(i))).Create(ctx, g.buildPod(i), metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
				return err
			}
			g.current = i + 1
		}
		created := i
		if !waitFor(ctx, func() bool { return count("Pod") >= created }, 30*time.Second) {
			return fmt.Errorf("informer did not drain after creating %d pods (observed %d)", created, count("Pod"))
		}
	}
	return nil
}

// deletePods removes pods [from, to) — the tail above the new target — batched
// and paced against the Pod informer, advancing g.current per pod actually
// removed.
func (g *Generator) deletePods(ctx context.Context, client kubernetes.Interface, from, to int, count func(kind string) int) error {
	i := to
	for i > from {
		start := max(i-batchSize, from)
		for ; i > start; i-- {
			if err := client.CoreV1().Pods(g.nsName(g.appIndex(i-1))).Delete(ctx, g.podName(i-1), metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
			g.current = i - 1
		}
		remaining := i
		if !waitFor(ctx, func() bool { return count("Pod") <= remaining }, 30*time.Second) {
			return fmt.Errorf("informer did not drain after deleting down to %d pods (observed %d)", remaining, count("Pod"))
		}
	}
	return nil
}

// waitFor polls cond until it returns true or the timeout elapses. Returns
// whether cond became true. The poll interval is deliberately short so the
// scaler paces tightly against the informer draining the watch channel.
func waitFor(ctx context.Context, cond func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		time.Sleep(time.Millisecond)
	}
}

func boolPtr(b bool) *bool { return &b }
