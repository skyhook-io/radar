package relations

import (
	"fmt"
	"reflect"
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// cluster is a Session over a fixed set of objects, every kind loaded in full
// except the ones in unread.
type cluster struct {
	ownership.ReadSession
	objects []runtime.Object
	unread  map[resourceid.GroupKind]readreason.Code
}

func refOf(obj runtime.Object) resourceid.Ref {
	m, _ := apimeta.Accessor(obj)
	gvk := obj.GetObjectKind().GroupVersionKind()
	return resourceid.Ref{Group: gvk.Group, Kind: gvk.Kind, Namespace: m.GetNamespace(), Name: m.GetName()}
}

func (cluster) Catalog() *ownership.Catalog { return ownership.NewCatalog(nil, true) }

func (c cluster) Object(ref resourceid.Ref) ownership.ObjectRead {
	_, read := c.Borrow(ref)
	return read
}

func (c cluster) Borrow(ref resourceid.Ref) (runtime.Object, ownership.ObjectRead) {
	if reason, ok := c.unread[ref.GroupKind()]; ok {
		return nil, ownership.ObjectRead{State: ownership.Unavailable, Gap: ownership.Gap{Kind: ref.GroupKind(), Namespace: ref.Namespace, Reason: reason}}
	}
	for _, obj := range c.objects {
		if refOf(obj) == ref {
			return obj, ownership.ObjectRead{State: ownership.Present}
		}
	}
	return nil, ownership.ObjectRead{State: ownership.Absent}
}

func (c cluster) Indexed(gk resourceid.GroupKind, index string, keys []string, scope ownership.SearchScope) IndexedRead {
	var r IndexedRead
	if reason, ok := c.unread[gk]; ok {
		r.Gaps = []ownership.Gap{{Kind: gk, Reason: reason}}
		return r
	}
	for _, obj := range c.objects {
		ref := refOf(obj)
		if ref.GroupKind() != gk || !slices.Contains(scope.Namespaces, ref.Namespace) {
			continue
		}
		if index == cache.NamespaceIndex && slices.Contains(keys, ref.Namespace) {
			r.Objects = append(r.Objects, IndexedObject{Ref: ref, Object: obj})
		}
	}
	return r
}

// denies refuses the kinds it names.
type denies []string

func (d denies) CanRead(gk resourceid.GroupKind, _ string) ownership.Decision {
	if slices.Contains(d, gk.Kind) {
		return ownership.Denied
	}
	return ownership.Allowed
}
func (denies) Namespaces() ([]string, bool) { return nil, true }

// undecided can't decide anything.
type undecided struct{}

func (undecided) CanRead(resourceid.GroupKind, string) ownership.Decision { return ownership.Undecided }
func (undecided) Namespaces() ([]string, bool)                            { return nil, true }

func meta(name string) metav1.ObjectMeta { return metav1.ObjectMeta{Namespace: "shop", Name: name} }

func typed(obj runtime.Object, group, version, kind string) runtime.Object {
	obj.GetObjectKind().SetGroupVersionKind(schema.GroupVersionKind{Group: group, Version: version, Kind: kind})
	return obj
}

func configMap(name string) runtime.Object {
	return typed(&corev1.ConfigMap{ObjectMeta: meta(name)}, "", "v1", "ConfigMap")
}
func secret(name string) runtime.Object {
	return typed(&corev1.Secret{ObjectMeta: meta(name)}, "", "v1", "Secret")
}

func mounts(names ...string) corev1.PodSpec {
	var spec corev1.PodSpec
	for _, n := range names {
		spec.Volumes = append(spec.Volumes, corev1.Volume{Name: n, VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: n}}}})
	}
	return spec
}

func pod(name string, spec corev1.PodSpec) runtime.Object {
	return typed(&corev1.Pod{ObjectMeta: meta(name), Spec: spec}, "", "v1", "Pod")
}
func deployment(name string, spec corev1.PodSpec) runtime.Object {
	d := &appsv1.Deployment{ObjectMeta: meta(name)}
	d.Spec.Template.Spec = spec
	return typed(d, "apps", "v1", "Deployment")
}
func replicaSet(name string, replicas *int32, spec corev1.PodSpec) runtime.Object {
	rs := &appsv1.ReplicaSet{ObjectMeta: meta(name)}
	rs.Spec.Replicas = replicas
	rs.Spec.Template.Spec = spec
	return typed(rs, "apps", "v1", "ReplicaSet")
}

func ref(group, kind, name string) resourceid.Ref {
	return resourceid.Ref{Group: group, Kind: kind, Namespace: "shop", Name: name}
}

func consumerRefs(a ReferrersAnswer) []resourceid.Ref {
	var out []resourceid.Ref
	for _, c := range a.Referrers {
		out = append(out, c.Ref)
	}
	return out
}

func TestConfigReferencesStateEachDeclaredTarget(t *testing.T) {
	spec := mounts("settings", "gone")
	spec.ServiceAccountName = "web"
	spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "registry"}}
	spec.Containers = []corev1.Container{{Name: "app", EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "settings"}}}}}}
	subject := deployment("web", spec)
	c := cluster{objects: []runtime.Object{subject, configMap("settings")}, unread: map[resourceid.GroupKind]readreason.Code{{Kind: "ServiceAccount"}: readreason.KindSyncPending}}
	got := NewView(c, denies{"Secret"}).References(ConfigReferences, refOf(subject), subject)
	want := ReferencesAnswer{
		Declared: []Declared{
			{Ref: ref("", "ConfigMap", "gone"), Paths: []string{"volumes[].configMap.name"}, State: TargetAbsent},
			{Ref: ref("", "ConfigMap", "settings"), Paths: []string{"containers[].envFrom[].configMapRef.name", "volumes[].configMap.name"}, State: TargetPresent},
			{Ref: ref("", "Secret", "registry"), Paths: []string{"imagePullSecrets[].name"}, State: TargetWithheld},
			{Ref: ref("", "ServiceAccount", "web"), Paths: []string{"serviceAccountName"}, State: TargetUnread,
				Gap: ownership.Gap{Kind: resourceid.GroupKind{Kind: "ServiceAccount"}, Namespace: "shop", Reason: readreason.KindSyncPending}},
		},
		Coverage: Coverage{NotChecked: []ownership.Gap{{Kind: resourceid.GroupKind{Kind: "ServiceAccount"}, Namespace: "shop", Reason: readreason.KindSyncPending}}, Narrowed: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

func TestConfigReferencesReadsTheSubjectWhenNotGivenIt(t *testing.T) {
	subject := pod("web-1", mounts("settings"))
	c := cluster{objects: []runtime.Object{subject, configMap("settings")}}
	got := NewView(c, ownership.Unrestricted()).References(ConfigReferences, refOf(subject), nil)
	if len(got.Declared) != 1 || got.Declared[0].State != TargetPresent || !got.Coverage.Complete() {
		t.Fatalf("got %+v", got)
	}
	unread := cluster{unread: map[resourceid.GroupKind]readreason.Code{{Kind: "Pod"}: readreason.KindSyncPending}}
	if got := NewView(unread, ownership.Unrestricted()).References(ConfigReferences, refOf(subject), nil); got.Coverage.Complete() {
		t.Fatalf("an unread subject answered complete: %+v", got)
	}
}

func TestReplicaSetsScaledToZeroDeclareNothing(t *testing.T) {
	zero, one := int32(0), int32(1)
	old := replicaSet("web-old", &zero, mounts("settings"))
	current := replicaSet("web-new", &one, mounts("settings"))
	unset := replicaSet("web-unset", nil, mounts("settings"))
	c := cluster{objects: []runtime.Object{old, current, unset, configMap("settings")}}
	v := NewView(c, ownership.Unrestricted())
	if got := v.References(ConfigReferences, refOf(old), old); len(got.Declared) != 0 {
		t.Errorf("a ReplicaSet scaled to zero declares %+v", got.Declared)
	}
	if got := v.References(OwnConfigReferences, refOf(old), old); len(got.Declared) != 1 || got.Declared[0].Ref != ref("", "ConfigMap", "settings") {
		t.Errorf("a ReplicaSet scaled to zero lost its own template: %+v", got.Declared)
	}
	got := consumerRefs(v.Referrers(ConfigReferences, ref("", "ConfigMap", "settings"), nil))
	want := []resourceid.Ref{ref("apps", "ReplicaSet", "web-new"), ref("apps", "ReplicaSet", "web-unset")}
	if !slices.Equal(got, want) {
		t.Errorf("consumers %v, want %v", got, want)
	}
}

func TestConfigConsumersListEachDeclarerInKindOrder(t *testing.T) {
	cronJob := &batchv1.CronJob{ObjectMeta: meta("nightly")}
	cronJob.Spec.JobTemplate.Spec.Template.Spec = mounts("settings")
	ingress := &networkingv1.Ingress{ObjectMeta: meta("front"), Spec: networkingv1.IngressSpec{TLS: []networkingv1.IngressTLS{{SecretName: "settings"}}}}
	rollout := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Rollout", "metadata": map[string]any{"namespace": "shop", "name": "checkout"},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"volumes": []any{map[string]any{"name": "s", "configMap": map[string]any{"name": "settings"}}}}}},
	}}
	c := cluster{objects: []runtime.Object{
		configMap("settings"), pod("web-1", mounts("settings")), deployment("web", mounts("settings")), deployment("other", mounts("unrelated")),
		typed(cronJob, "batch", "v1", "CronJob"), typed(ingress, "networking.k8s.io", "v1", "Ingress"), rollout,
	}}
	got := consumerRefs(NewView(c, ownership.Unrestricted()).Referrers(ConfigReferences, ref("", "ConfigMap", "settings"), nil))
	want := []resourceid.Ref{ref("apps", "Deployment", "web"), ref("argoproj.io", "Rollout", "checkout"), ref("batch", "CronJob", "nightly"), ref("", "Pod", "web-1")}
	if !slices.Equal(got, want) {
		t.Fatalf("consumers %v, want %v (an Ingress declares Secrets only)", got, want)
	}
	secretConsumers := consumerRefs(NewView(cluster{objects: []runtime.Object{secret("settings"), typed(ingress, "networking.k8s.io", "v1", "Ingress")}}, ownership.Unrestricted()).Referrers(ConfigReferences, ref("", "Secret", "settings"), nil))
	if !slices.Equal(secretConsumers, []resourceid.Ref{ref("networking.k8s.io", "Ingress", "front")}) {
		t.Fatalf("Secret consumers %v", secretConsumers)
	}
}

func TestReferrersAnswerOnlyAsTheCallerMayRead(t *testing.T) {
	c := cluster{objects: []runtime.Object{configMap("settings"), pod("web-1", mounts("settings")), deployment("web", mounts("settings")), secret("tls")}}
	got := NewView(c, denies{"Pod"}).Referrers(ConfigReferences, ref("", "ConfigMap", "settings"), nil)
	if !slices.Equal(consumerRefs(got), []resourceid.Ref{ref("apps", "Deployment", "web")}) || !got.Coverage.Narrowed {
		t.Errorf("a caller who may not read Pods got %+v", got)
	}
	if got := NewView(c, denies{"Secret"}).Referrers(ConfigReferences, ref("", "Secret", "tls"), nil); len(got.Referrers) != 0 || !got.Coverage.Narrowed {
		t.Errorf("a caller who may not read the Secret learned its consumers: %+v", got)
	}
	if got := NewView(c, undecided{}).Referrers(ConfigReferences, ref("", "ConfigMap", "settings"), nil); len(got.Referrers) != 0 || got.Coverage.Complete() ||
		!slices.ContainsFunc(got.Coverage.NotChecked, func(g ownership.Gap) bool { return g.Reason == readreason.UserPermissionCheckFailed }) {
		t.Errorf("an undecided check granted a read, or claimed a complete answer: %+v", got)
	}
}

func TestConfigConsumersReportUnreadKinds(t *testing.T) {
	c := cluster{objects: []runtime.Object{configMap("settings"), deployment("web", mounts("settings"))}, unread: map[resourceid.GroupKind]readreason.Code{{Kind: "Pod"}: readreason.KindSyncPending}}
	got := NewView(c, ownership.Unrestricted()).Referrers(ConfigReferences, ref("", "ConfigMap", "settings"), nil)
	if got.Coverage.Complete() || !slices.ContainsFunc(got.Coverage.NotChecked, func(g ownership.Gap) bool { return g.Kind.Kind == "Pod" }) {
		t.Fatalf("Pods still loading, but coverage %+v", got.Coverage)
	}
}

func TestConfigConsumersAreCappedAndCounted(t *testing.T) {
	objects := []runtime.Object{configMap("settings")}
	for i := range 7 {
		objects = append(objects, pod(fmt.Sprintf("web-%d", i), mounts("settings")))
	}
	got := NewView(cluster{objects: objects}, ownership.Unrestricted()).WithLimit(5).Referrers(ConfigReferences, ref("", "ConfigMap", "settings"), nil)
	if len(got.Referrers) != 5 || !got.Coverage.Truncated || got.Coverage.Total != 7 || got.Coverage.Complete() {
		t.Fatalf("got %d consumers, coverage %+v", len(got.Referrers), got.Coverage)
	}
}

func TestConfigReferencesSayWhenARolloutTemplateIsPartlyUnreadable(t *testing.T) {
	rollout := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Rollout", "metadata": map[string]any{"namespace": "shop", "name": "checkout"},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"nodeSelector": "not-a-map",
			"volumes":      []any{map[string]any{"name": "s", "configMap": map[string]any{"name": "settings"}}},
		}}},
	}}
	got := NewView(cluster{objects: []runtime.Object{configMap("settings")}}, ownership.Unrestricted()).References(ConfigReferences, refOf(rollout), rollout)
	if len(got.Declared) != 1 || got.Declared[0].State != TargetPresent || got.Coverage.Complete() {
		t.Fatalf("got %+v", got)
	}
}

func TestReferrersAnswerOnlyForASubjectRadarHolds(t *testing.T) {
	declarer := pod("web-1", mounts("settings"))
	if got := NewView(cluster{objects: []runtime.Object{declarer}}, ownership.Unrestricted()).Referrers(ConfigReferences, ref("", "ConfigMap", "settings"), nil); len(got.Referrers) != 0 || !got.Coverage.Complete() {
		t.Errorf("a ConfigMap that doesn't exist got %+v", got)
	}
	unread := cluster{objects: []runtime.Object{declarer}, unread: map[resourceid.GroupKind]readreason.Code{{Kind: "ConfigMap"}: readreason.KindSyncPending}}
	got := NewView(unread, ownership.Unrestricted()).Referrers(ConfigReferences, ref("", "ConfigMap", "settings"), nil)
	if len(got.Referrers) != 0 || got.Coverage.Complete() {
		t.Errorf("a ConfigMap Radar couldn't read got %+v", got)
	}
}

func TestReferencesAnswerOnlyAsTheCallerMayReadTheSubject(t *testing.T) {
	subject := pod("web-1", mounts("settings"))
	c := cluster{objects: []runtime.Object{subject, configMap("settings")}}
	if got := NewView(c, denies{"Pod"}).References(ConfigReferences, refOf(subject), subject); len(got.Declared) != 0 || !got.Coverage.Narrowed {
		t.Errorf("a caller who may not read the Pod learned what it declares: %+v", got)
	}
	if got := NewView(c, undecided{}).References(ConfigReferences, refOf(subject), subject); len(got.Declared) != 0 || got.Coverage.Complete() {
		t.Errorf("a caller whose access couldn't be checked got %+v; want nothing, incomplete", got)
	}
}

func TestPartlyUnreadableRolloutsLeaveConsumersIncomplete(t *testing.T) {
	broken := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Rollout", "metadata": map[string]any{"namespace": "shop", "name": "checkout"},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"nodeSelector": "not-a-map",
			"volumes":      []any{map[string]any{"name": "s", "configMap": map[string]any{"name": "settings"}}},
		}}},
	}}
	got := NewView(cluster{objects: []runtime.Object{configMap("settings"), broken}}, ownership.Unrestricted()).Referrers(ConfigReferences, ref("", "ConfigMap", "settings"), nil)
	if !slices.Equal(consumerRefs(got), []resourceid.Ref{ref("argoproj.io", "Rollout", "checkout")}) || got.Coverage.Complete() {
		t.Fatalf("got %+v", got)
	}
}

func TestARolloutTemplateThatIsNotAnObjectIsUnread(t *testing.T) {
	rollout := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Rollout", "metadata": map[string]any{"namespace": "shop", "name": "checkout"},
		"spec": map[string]any{"template": map[string]any{"spec": "not-an-object"}},
	}}
	if got := NewView(cluster{}, ownership.Unrestricted()).References(ConfigReferences, refOf(rollout), rollout); got.Coverage.Complete() {
		t.Fatalf("a template Radar couldn't read answered complete: %+v", got)
	}
	none := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Rollout", "metadata": map[string]any{"namespace": "shop", "name": "by-ref"},
		"spec": map[string]any{"workloadRef": map[string]any{"kind": "Deployment", "name": "web"}},
	}}
	if got := NewView(cluster{}, ownership.Unrestricted()).References(ConfigReferences, refOf(none), none); !got.Coverage.Complete() || len(got.Declared) != 0 {
		t.Fatalf("a Rollout with no template got %+v", got)
	}
}

// A target whose access couldn't be checked is unread, not withheld: the
// answer can't say the caller may not read it.
func TestAnUndecidedTargetIsUnreadNotWithheld(t *testing.T) {
	subject := pod("web-1", mounts("settings"))
	access := targetUndecided{}
	got := NewView(cluster{objects: []runtime.Object{subject, configMap("settings")}}, access).References(ConfigReferences, refOf(subject), subject)
	if len(got.Declared) != 1 || got.Declared[0].State != TargetUnread || got.Declared[0].Gap.Reason != readreason.UserPermissionCheckFailed || got.Coverage.Narrowed || got.Coverage.Complete() {
		t.Fatalf("got %+v", got)
	}
}

type targetUndecided struct{}

func (targetUndecided) CanRead(gk resourceid.GroupKind, _ string) ownership.Decision {
	if gk.Kind == "ConfigMap" {
		return ownership.Undecided
	}
	return ownership.Allowed
}
func (targetUndecided) Namespaces() ([]string, bool) { return nil, true }

func TestAFetchedSubjectTheCacheLacksLeavesConsumersIncomplete(t *testing.T) {
	cm := configMap("settings")
	c := cluster{objects: []runtime.Object{pod("web-1", mounts("settings"))}}
	got := NewView(c, ownership.Unrestricted()).Referrers(ConfigReferences, ref("", "ConfigMap", "settings"), cm)
	if got.Coverage.Complete() || !slices.ContainsFunc(got.Coverage.NotChecked, func(g ownership.Gap) bool { return g.Reason == readreason.ObjectNotFound }) {
		t.Fatalf("a ConfigMap the host read but the cache lacks got %+v", got)
	}
}
