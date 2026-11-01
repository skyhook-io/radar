package k8s

import (
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

const (
	gceIncompatibleAttach = `AttachVolume.Attach failed for volume "pvc-d28294a7" : rpc error: code = Internal desc = Failed to Attach: failed cloud service attach disk call: googleapi: Error 400: pd-ssd disk type cannot be used by n4d-standard-4 machine type., badRequest`
	attachBackoffEcho     = `AttachVolume.Attach failed for volume "pvc-d28294a7" : rpc error: code = InvalidArgument desc = ControllerPublish not permitted on node "projects/p/zones/us-east1-b/instances/n4d-node" due to backoff condition`
)

// redisOnNode builds the shape behind a real outage: a StatefulSet pod whose
// pd-ssd (premium-rwo) volume was scheduled onto a Hyperdisk-only N4D node.
func redisOnNode(machineType string) []runtime.Object {
	pod := postBindContainerCreatingPod("redis", "redis-master-0", "n4d-node", time.Now().Add(-40*time.Minute))
	pod.Spec.Volumes = []corev1.Volume{{
		Name:         "data",
		VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "redis-data-redis-master-0"}},
	}}
	return []runtime.Object{
		pod,
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n4d-node", Labels: map[string]string{"node.kubernetes.io/instance-type": machineType}}},
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "redis-data-redis-master-0", Namespace: "redis"},
			Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "pvc-d28294a7"},
		},
		&corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: "pvc-d28294a7", Annotations: map[string]string{"pv.kubernetes.io/provisioned-by": "pd.csi.storage.gke.io"}},
			Spec: corev1.PersistentVolumeSpec{
				StorageClassName:       "premium-rwo",
				PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: "pd.csi.storage.gke.io", VolumeHandle: "d"}},
			},
		},
		&storagev1.StorageClass{
			ObjectMeta:  metav1.ObjectMeta{Name: "premium-rwo"},
			Provisioner: "pd.csi.storage.gke.io",
			Parameters:  map[string]string{"type": "pd-ssd"},
		},
	}
}

func attachEvent(name, msg string, first, last time.Time) *corev1.Event {
	return &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: name, Namespace: "redis"},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "redis", Name: "redis-master-0"},
		Reason:         "FailedAttachVolume",
		Type:           corev1.EventTypeWarning,
		Message:        msg,
		FirstTimestamp: metav1.NewTime(first),
		LastTimestamp:  metav1.NewTime(last),
	}
}

func redisAttachProblem(t *testing.T, objs ...runtime.Object) Detection {
	t.Helper()
	if err := InitTestResourceCache(fake.NewClientset(objs...)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	var got []Detection
	for _, p := range DetectPostBindProblems(GetResourceCache(), "redis") {
		if p.Name == "redis-master-0" {
			got = append(got, p)
		}
	}
	if len(got) != 1 {
		t.Fatalf("want one post-bind row for redis-master-0, got %+v", got)
	}
	if got[0].Reason != "VolumeAttach" {
		t.Fatalf("reason = %q, want VolumeAttach", got[0].Reason)
	}
	return got[0]
}

// The latest event is the attacher's backoff echo; the Compute Engine
// rejection that names the cause is older and outside the recency window.
func TestDetectPostBindProblems_AttachBackoffShowsRealCause(t *testing.T) {
	defer ResetTestState()
	now := time.Now()
	objs := append(redisOnNode("n4d-standard-4"),
		attachEvent("real", gceIncompatibleAttach, now.Add(-38*time.Minute), now.Add(-20*time.Minute)),
		attachEvent("echo", attachBackoffEcho, now.Add(-37*time.Minute), now.Add(-10*time.Second)),
	)
	p := redisAttachProblem(t, objs...)

	if !strings.Contains(p.Message, "pd-ssd disk type cannot be used by n4d-standard-4") || strings.Contains(p.Message, "backoff condition") {
		t.Errorf("message should carry the real error, not the echo: %q", p.Message)
	}
	if !strings.Contains(p.Cause, "pd-ssd volumes can't attach to n4d-standard-4 nodes") || !strings.Contains(p.Cause, "supports only Hyperdisk") {
		t.Errorf("cause = %q", p.Cause)
	}
	if !strings.Contains(p.Action, "Hyperdisk StorageClass") || !strings.Contains(p.Action, "node.kubernetes.io/instance-type") {
		t.Errorf("action = %q", p.Action)
	}
	if wantOnset := now.Add(-38 * time.Minute); p.OnsetAt.After(wantOnset.Add(time.Second)) {
		t.Errorf("onset %v should come from the first real failure (%v), not the echo", p.OnsetAt, wantOnset)
	}
}

// Only backoff echoes remain: the cause is derived from the node's machine
// type and the volume's StorageClass.
func TestDetectPostBindProblems_AttachBackoffOnlyDerivesCause(t *testing.T) {
	defer ResetTestState()
	now := time.Now()
	p := redisAttachProblem(t, append(redisOnNode("n4d-standard-4"),
		attachEvent("echo", attachBackoffEcho, now.Add(-37*time.Minute), now.Add(-10*time.Second)))...)

	if !strings.Contains(p.Message, "backoff condition") {
		t.Errorf("with no informative event the message stays the echo: %q", p.Message)
	}
	if !strings.Contains(p.Cause, "pd-ssd volumes can't attach to n4d-standard-4 nodes") {
		t.Errorf("cause should be derived from node + StorageClass: %q", p.Cause)
	}
}

// A backoff echo on a node that does support Persistent Disk says nothing
// about disk compatibility, so no cause is invented.
func TestDetectPostBindProblems_AttachBackoffOnCompatibleNode(t *testing.T) {
	defer ResetTestState()
	now := time.Now()
	p := redisAttachProblem(t, append(redisOnNode("e2-standard-2"),
		attachEvent("echo", attachBackoffEcho, now.Add(-37*time.Minute), now.Add(-10*time.Second)))...)
	if p.Cause != "" || p.Action != "" {
		t.Errorf("no cause expected on a PD-capable node: %q / %q", p.Cause, p.Action)
	}
}

// A recreated StatefulSet pod keeps its name; events recorded against the
// predecessor's UID — older or still within the recency window — must not
// become the replacement's blocker or cause.
func TestDetectPostBindProblems_AttachIgnoresPredecessorPodEvents(t *testing.T) {
	defer ResetTestState()
	now := time.Now()
	objs := redisOnNode("e2-standard-2")
	objs[0].(*corev1.Pod).UID = "current"
	stale := attachEvent("stale", gceIncompatibleAttach, now.Add(-38*time.Minute), now.Add(-30*time.Minute))
	recent := attachEvent("recent", gceIncompatibleAttach, now.Add(-6*time.Minute), now.Add(-2*time.Minute))
	stale.InvolvedObject.UID, recent.InvolvedObject.UID = "predecessor", "predecessor"
	if err := InitTestResourceCache(fake.NewClientset(append(objs, stale, recent)...)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	for _, p := range DetectPostBindProblems(GetResourceCache(), "redis") {
		if p.Name != "redis-master-0" {
			continue
		}
		if p.Reason == "VolumeAttach" || strings.Contains(p.Message, "pd-ssd disk type cannot be used") || p.Cause != "" {
			t.Errorf("predecessor's attach failure leaked into the replacement pod: %+v", p)
		}
	}
}

// Backoff is per disk: an echo for one volume must not borrow a second
// volume's older error.
func TestDetectPostBindProblems_AttachEchoMatchesItsOwnVolume(t *testing.T) {
	defer ResetTestState()
	now := time.Now()
	otherVolume := strings.Replace(gceIncompatibleAttach, "pvc-d28294a7", "pvc-other", 1)
	p := redisAttachProblem(t, append(redisOnNode("e2-standard-2"),
		attachEvent("other", otherVolume, now.Add(-38*time.Minute), now.Add(-20*time.Minute)),
		attachEvent("echo", attachBackoffEcho, now.Add(-5*time.Minute), now.Add(-10*time.Second)))...)
	if !strings.Contains(p.Message, "backoff condition") || strings.Contains(p.Message, "pvc-other") {
		t.Errorf("echo for pvc-d28294a7 must keep its own message: %q", p.Message)
	}
}

// An echo for a volume that isn't Persistent Disk gets no PD cause, even when
// the pod also mounts a PD volume on the same Hyperdisk-only node.
func TestDetectPostBindProblems_AttachEchoFallbackUsesEchoedVolume(t *testing.T) {
	defer ResetTestState()
	now := time.Now()
	hyperdiskEcho := strings.Replace(attachBackoffEcho, "pvc-d28294a7", "pvc-hyperdisk", 1)
	p := redisAttachProblem(t, append(redisOnNode("n4d-standard-4"),
		attachEvent("echo", hyperdiskEcho, now.Add(-5*time.Minute), now.Add(-10*time.Second)))...)
	if p.Cause != "" {
		t.Errorf("echo for pvc-hyperdisk must not be blamed on the pod's pd-ssd volume: %q", p.Cause)
	}
}

func TestDetectPostBindProblems_UnrelatedAttachFailureHasNoDiskCause(t *testing.T) {
	defer ResetTestState()
	now := time.Now()
	msg := `AttachVolume.Attach failed for volume "pvc-d28294a7" : rpc error: code = DeadlineExceeded desc = context deadline exceeded`
	p := redisAttachProblem(t, append(redisOnNode("n4d-standard-4"),
		attachEvent("timeout", msg, now.Add(-5*time.Minute), now.Add(-10*time.Second)))...)
	if p.Cause != "" {
		t.Errorf("a timeout must not be blamed on disk compatibility: %q", p.Cause)
	}
	if !strings.Contains(p.Message, "context deadline exceeded") {
		t.Errorf("message = %q", p.Message)
	}
}

// sharedClaimDeployment builds a 2-replica Deployment whose pods share the
// ReadWriteOnce claim "data" (PV "pv-data"): web-a runs on node-a and web-b is
// stuck creating on node-b. holderOwner is the controller of web-a.
func sharedClaimDeployment(holderOwner metav1.OwnerReference) []runtime.Object {
	return sharedClaimDeploymentEvent(holderOwner, `Multi-Attach error for volume "pv-data" Volume is already used by pod(s) web-a`)
}

func sharedClaimDeploymentEvent(holderOwner metav1.OwnerReference, eventMessage string) []runtime.Object {
	two := int32(2)
	controller := true
	rsOwner := metav1.OwnerReference{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-1", Controller: &controller}
	volumes := []corev1.Volume{{
		Name:         "data",
		VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}},
	}}
	mounts := []corev1.VolumeMount{{Name: "data", MountPath: "/data"}}

	holder := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web-a", Namespace: "redis", OwnerReferences: []metav1.OwnerReference{holderOwner}},
		Spec:       corev1.PodSpec{NodeName: "node-a", Volumes: volumes, Containers: []corev1.Container{{Name: "main", VolumeMounts: mounts}}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	stuck := postBindContainerCreatingPod("redis", "web-b", "node-b", time.Now().Add(-5*time.Minute))
	stuck.OwnerReferences = []metav1.OwnerReference{rsOwner}
	stuck.Spec.Volumes = volumes
	stuck.Spec.Containers[0].VolumeMounts = mounts

	return []runtime.Object{
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "redis"},
			Spec: appsv1.DeploymentSpec{
				Replicas: &two,
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Volumes: volumes, Containers: []corev1.Container{{Name: "main", VolumeMounts: mounts}}}},
			},
		},
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "web-1", Namespace: "redis", OwnerReferences: []metav1.OwnerReference{
			{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", Controller: &controller},
		}}},
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "redis"},
			Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "pv-data", AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}},
			Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}},
		},
		holder,
		stuck,
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: "multi", Namespace: "redis"},
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "redis", Name: "web-b"},
			Reason:         "FailedAttachVolume",
			Type:           corev1.EventTypeWarning,
			Message:        eventMessage,
			FirstTimestamp: metav1.NewTime(time.Now().Add(-4 * time.Minute)),
			LastTimestamp:  metav1.NewTime(time.Now().Add(-30 * time.Second)),
		},
	}
}

func multiAttachProblem(t *testing.T, objs []runtime.Object) Detection {
	t.Helper()
	if err := InitTestResourceCache(fake.NewClientset(objs...)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	for _, p := range DetectPostBindProblems(GetResourceCache(), "redis") {
		if p.Name == "web-b" && p.Reason == "VolumeMultiAttach" {
			return p
		}
	}
	t.Fatal("want a VolumeMultiAttach row for web-b")
	return Detection{}
}

// A replica of the same Deployment holds the ReadWriteOnce claim on another
// node: the attach failure names the shared claim as its cause.
func TestDetectPostBindProblems_MultiAttachNamesSharedClaim(t *testing.T) {
	defer ResetTestState()
	controller := true
	p := multiAttachProblem(t, sharedClaimDeployment(metav1.OwnerReference{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-1", Controller: &controller}))
	if p.Severity != "critical" {
		t.Errorf("severity = %q, want critical", p.Severity)
	}
	for _, want := range []string{"Deployment web runs 2 replicas", "ReadWriteOnce PVC data", "A replica on another node holds it"} {
		if !strings.Contains(p.Cause, want) {
			t.Errorf("cause missing %q: %q", want, p.Cause)
		}
	}
	if !strings.Contains(p.Message, "node-b") || !strings.Contains(p.Message, "already used by pod(s) web-a") {
		t.Errorf("message should keep the pod-specific evidence: %q", p.Message)
	}
	if !strings.Contains(p.Action, "ReadWriteMany") {
		t.Errorf("action = %q", p.Action)
	}
}

// Kubernetes 1.37 words the same event "Waiting for detach" and may append a
// count of holders in other namespaces; the cause still names the shared claim.
func TestDetectPostBindProblems_WaitingForDetachNamesSharedClaim(t *testing.T) {
	defer ResetTestState()
	controller := true
	p := multiAttachProblem(t, sharedClaimDeploymentEvent(
		metav1.OwnerReference{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-1", Controller: &controller},
		`Waiting for detach for volume "pv-data" Volume is already used by pod(s) web-a and 1 pod(s) in different namespaces`))
	if p.Severity != "critical" || !strings.Contains(p.Cause, "share ReadWriteOnce PVC data") {
		t.Errorf("severity %q, cause %q", p.Severity, p.Cause)
	}
}

// The holder belongs to another workload, so the Deployment's replica count
// isn't the cause and none is invented.
func TestDetectPostBindProblems_MultiAttachForeignHolderHasNoCause(t *testing.T) {
	defer ResetTestState()
	controller := true
	p := multiAttachProblem(t, sharedClaimDeployment(metav1.OwnerReference{APIVersion: "batch/v1", Kind: "Job", Name: "backup", Controller: &controller}))
	if p.Cause != "" || p.Action != "" {
		t.Errorf("no cause expected for a foreign holder: %q / %q", p.Cause, p.Action)
	}
}
