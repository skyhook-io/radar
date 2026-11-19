package topology

import (
	"context"
	"fmt"
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/pkg/gitops"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// An object the request already read as absent is not handed to relation
// matching, without its object, when the informer adds it before an indexed
// read finds it.
func TestNeighborhoodSessionIndexedKeepsAnEarlierAbsentRead(t *testing.T) {
	svc := projectionObject(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop"}}, "", "Service")
	late := projectionObject(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "late", Namespace: "shop"}}, "", "Service")
	base := newProjectionSession(t, svc)
	s := &neighborhoodSession{Session: base, ctx: context.Background(), access: &neighborhoodAccess{catalog: base.Catalog(), decisions: map[resourceid.GroupKind]map[string]ownership.Decision{}}, objects: map[resourceid.Ref]neighborhoodObject{}, indexes: map[string]relations.IndexedRead{}}
	if _, read := s.borrow(projectionRef(late), false); read.State != ownership.Absent {
		t.Fatalf("late Service read %v before it exists", read.State)
	}
	base.objects[projectionRef(late)] = late
	read := s.Indexed(resourceid.GroupKind{Kind: "Service"}, "", nil, ownership.SearchScope{AllNamespaces: true})
	for _, o := range read.Objects {
		if o.Object == nil {
			t.Fatalf("indexed %s without its object", o.Ref)
		}
		if o.Ref == projectionRef(late) {
			t.Fatal("indexed an object this request read as absent")
		}
	}
	if len(read.Objects) != 1 {
		t.Fatalf("indexed %d Services, want web", len(read.Objects))
	}
}

// A ConfigMap reaching Pods of a Job run and of an earlier run with the same
// name shows them as two groups, as the graph builder does.
func TestWalkNeighborhoodSplitsPodsOfSameNamedJobRuns(t *testing.T) {
	controller := true
	cm := projectionObject(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "settings", Namespace: "shop"}}, "", "ConfigMap")
	job := projectionObject(&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "run", Namespace: "shop", UID: "current"}}, "batch", "Job")
	objects := []runtime.Object{cm, job}
	for _, uid := range []types.UID{"current", "earlier"} {
		for i := 0; i < 6; i++ {
			objects = append(objects, projectionObject(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("run-%s-%d", uid, i), Namespace: "shop", UID: types.UID(fmt.Sprintf("%s-%d", uid, i)),
				Labels:          map[string]string{"app": "batch", "batch.kubernetes.io/job-name": "run"},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: "run", UID: uid, Controller: &controller}},
			}, Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "settings", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "settings"}}}}}}}, "", "Pod"))
		}
	}
	sub := walkTestNeighborhood(t, newProjectionSession(t, objects...), cm, NeighborhoodOptions{Hops: 1, Profile: ProfileAll})
	var counts []any
	for _, n := range sub.Nodes {
		if n.Kind == KindPodGroup {
			counts = append(counts, n.Data["podCount"])
		}
	}
	if len(counts) != 2 || counts[0] != 6 || counts[1] != 6 {
		t.Fatalf("Job runs grouped as %v, want two groups of 6: %+v", counts, sub.Nodes)
	}
}

// An indexed read hands relation matching each object it found, and the
// request keeps those objects: matching a Deployment against a namespace of
// Services reads no Service again by name.
func TestNeighborhoodSessionIndexedReadsEachCandidateOnce(t *testing.T) {
	root := projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", UID: "web"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}}}}}, "apps", "Deployment")
	objects := []runtime.Object{root, projectionObject(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "web"}}}, "", "Service")}
	for i := 0; i < 1000; i++ {
		objects = append(objects, projectionObject(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("other-%04d", i), Namespace: "shop"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": fmt.Sprint(i)}}}, "", "Service"))
	}
	s := newProjectionSession(t, objects...)
	sub := walkTestNeighborhood(t, s, root, NeighborhoodOptions{})
	if !slices.ContainsFunc(sub.Nodes, func(n Node) bool { return n.Kind == KindService && n.Name == "web" }) {
		t.Fatalf("the fixture no longer reaches the matching Service: %+v", sub.Nodes)
	}
	reads := 0
	for _, r := range s.reads {
		if r.Kind == "Service" {
			reads++
		}
	}
	if reads != 0 {
		t.Errorf("read %d Services by name after the indexed read found them", reads)
	}
}

// inventorySession reads each manager's member list from its current object,
// as the caches do, which liveInventory can change after the walk read the
// manager, and captures a subject as the caches can.
type inventorySession struct {
	*projectionSession
	liveInventory map[resourceid.Ref]*unstructured.Unstructured
	subject       resourceid.Ref
	captured      runtime.Object
}

func (s *inventorySession) CaptureSubject(ref resourceid.Ref, obj runtime.Object) relations.Session {
	c := *s
	c.subject, c.captured = ref, obj
	return &c
}

func (s *inventorySession) Members(manager resourceid.Ref) ownership.MembersRead {
	u := s.liveInventory[manager]
	if manager == s.subject {
		u = s.captured.(*unstructured.Unstructured)
	}
	if u == nil {
		return s.projectionSession.Members(manager)
	}
	read := ownership.MembersRead{Manager: ownership.ObjectRead{State: ownership.Present, Object: ownership.Observe(manager, u)}, Membership: ownership.MembersRecorded}
	list, _ := gitops.FluxMembers(u)
	for _, e := range list.Entries {
		read.Members = append(read.Members, resourceid.NewRef(e.Group, e.Kind, e.Namespace, e.Name))
	}
	return read
}

// RecordingManagers finds the managers whose current inventory lists ref, as
// the caches' index does.
func (s *inventorySession) RecordingManagers(ref resourceid.Ref) ownership.RecordingManagersRead {
	var read ownership.RecordingManagersRead
	for manager, u := range s.liveInventory {
		list, _ := gitops.FluxMembers(u)
		for _, e := range list.Entries {
			if resourceid.NewRef(e.Group, e.Kind, e.Namespace, e.Name) == ref {
				read.Managers = append(read.Managers, ownership.Observe(manager, u))
			}
		}
	}
	return read
}

// A manager's members are read from the manager as the walk read it: an
// inventory the controller rewrote after that read adds no member edge, one
// way or the other.
func TestWalkNeighborhoodMembersFollowTheReadManager(t *testing.T) {
	inventory := func(entries ...string) map[string]any {
		var list []any
		for _, id := range entries {
			list = append(list, map[string]any{"id": id, "v": "v1"})
		}
		return map[string]any{"inventory": map[string]any{"entries": list}}
	}
	dep := projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", UID: "web", Labels: map[string]string{"app": "web"}}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}}}}}, "apps", "Deployment")
	svc := projectionObject(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "shop", UID: "api"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "web"}}}, "", "Service")
	manager := projectionCR("kustomize.toolkit.fluxcd.io", "Kustomization", "flux-system", "apps", map[string]any{"status": inventory("shop_web_apps_Deployment")})
	manager.SetUID("apps")
	rewritten := manager.DeepCopy()
	rewritten.Object["status"] = inventory("shop_web_apps_Deployment", "shop_api__Service")
	for _, hops := range []int{1, 2} {
		t.Run(fmt.Sprintf("hops=%d", hops), func(t *testing.T) {
			membersFollowTheReadManager(t, hops, dep, svc, manager, rewritten)
		})
	}
}

func membersFollowTheReadManager(t *testing.T, hops int, dep, svc runtime.Object, manager, rewritten *unstructured.Unstructured) {
	sub, err := WalkNeighborhood(context.Background(), &inventorySession{projectionSession: newRecordingProjectionSession(t, dep, svc, manager), liveInventory: map[resourceid.Ref]*unstructured.Unstructured{projectionRef(manager): rewritten}}, refOf(projectionRef(dep)), NeighborhoodOptions{Hops: hops}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, n := range sub.Nodes {
		names[n.ID] = n.Name
	}
	edges := map[string]bool{}
	for _, e := range sub.Edges {
		if e.Type == EdgeManages {
			edges[names[e.Source]+" manages "+names[e.Target]] = true
		}
	}
	if len(sub.Nodes) != 3 || !edges["apps manages web"] {
		t.Fatalf("the fixture no longer reaches the manager, its member and the Service: nodes %+v edges %v", sub.Nodes, edges)
	}
	if edges["apps manages api"] {
		t.Error("a member the manager listed only after the walk read it was drawn")
	}
}
