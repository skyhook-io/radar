package kueuequeues

import (
	"fmt"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

type workloadOpt func(*unstructured.Unstructured)

func queue(name string) workloadOpt {
	return func(u *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(u.Object, name, "spec", "queueName")
	}
}

func reserved(clusterQueue string) workloadOpt {
	return func(u *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(u.Object, clusterQueue, "status", "admission", "clusterQueue")
	}
}

func finished() workloadOpt {
	return func(u *unstructured.Unstructured) {
		_ = unstructured.SetNestedSlice(u.Object, []any{map[string]any{"type": "Finished", "status": "True", "reason": "Succeeded"}}, "status", "conditions")
	}
}

func deleting() workloadOpt {
	return func(u *unstructured.Unstructured) {
		now := metav1.Now()
		u.SetDeletionTimestamp(&now)
		u.SetFinalizers([]string{"kueue.x-k8s.io/resource-in-use"})
	}
}

func workload(namespace, name string, opts ...workloadOpt) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "kueue.x-k8s.io/v1beta2", "kind": "Workload"}}
	u.SetNamespace(namespace)
	u.SetName(name)
	u.SetUID(types.UID("uid-" + namespace + "-" + name))
	for _, opt := range opts {
		opt(u)
	}
	return u
}

// readLocalQueues maps namespace/name to the ClusterQueue it points at, for
// the namespaces listed in read.
type readLocalQueues struct {
	targets map[string]string
	read    map[string]bool
}

func (l readLocalQueues) Target(namespace, name string) (string, bool) {
	if !l.read[namespace] {
		return "", false
	}
	return l.targets[namespace+"/"+name], true
}

func summary(members []Member) []string {
	out := make([]string, 0, len(members))
	for _, m := range members {
		out = append(out, fmt.Sprintf("%s/%s %v submitted=%s reserved=%s", m.Workload.GetNamespace(), m.Workload.GetName(), m.Kinds, m.SubmittedTo, m.ReservedIn))
	}
	return out
}

func TestClusterQueueMembership(t *testing.T) {
	lqs := readLocalQueues{
		targets: map[string]string{"team/lq": "cq", "team/other": "other-cq", "moved/lq": "new-cq"},
		read:    map[string]bool{"team": true, "moved": true},
	}
	workloads := []*unstructured.Unstructured{
		workload("team", "submitted", queue("lq")),
		workload("team", "both", queue("lq"), reserved("cq")),
		// Its LocalQueue points elsewhere, but its reservation names cq.
		workload("team", "reserved-only", queue("other"), reserved("cq")),
		// Edge case 1: the LocalQueue was re-created pointing at new-cq while
		// the Workload still reports its reservation in cq.
		workload("moved", "stale-reservation", queue("lq"), reserved("cq")),
		// Edge case 2: finished, still reporting its admission.
		workload("team", "finished", queue("other"), reserved("cq"), finished()),
		// Edge case 3: being deleted.
		workload("team", "deleting", queue("lq"), deleting()),
		// Its LocalQueue doesn't exist in a namespace whose LocalQueues were read.
		workload("team", "missing-lq", queue("absent")),
		// LocalQueues unread here: submission can't be established.
		workload("unread", "unresolved", queue("lq")),
		workload("unread", "unresolved-reserved", queue("lq"), reserved("cq")),
		workload("team", "elsewhere", queue("other")),
		workload("team", "no-queue"),
	}
	// Edge case 4: the same Workload found twice (under both index keys).
	workloads = append(workloads, workloads[1])

	got := summary(ClusterQueueMembers("cq", workloads, lqs))
	want := []string{
		"moved/stale-reservation [reservation] submitted=new-cq reserved=cq",
		"team/both [submission reservation] submitted=cq reserved=cq",
		"team/deleting [submission] submitted=cq reserved=",
		"team/finished [reservation] submitted=other-cq reserved=cq",
		"team/reserved-only [reservation] submitted=other-cq reserved=cq",
		"team/submitted [submission] submitted=cq reserved=",
		"unread/unresolved-reserved [reservation] submitted= reserved=cq",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("members of cq:\n got %q\nwant %q", got, want)
	}

	// Edge case 1 from the other side: the new ClusterQueue has it by submission.
	got = summary(ClusterQueueMembers("new-cq", workloads, lqs))
	want = []string{"moved/stale-reservation [submission] submitted=new-cq reserved=cq"}
	if !slices.Equal(got, want) {
		t.Fatalf("members of new-cq:\n got %q\nwant %q", got, want)
	}

	counts := Count(ClusterQueueMembers("cq", workloads, lqs))
	if counts != (Counts{Members: 7, Submission: 3, Reservation: 5}) {
		t.Fatalf("counts = %+v", counts)
	}
}

func TestLocalQueueMembership(t *testing.T) {
	workloads := []*unstructured.Unstructured{
		workload("team", "a", queue("lq"), reserved("cq")),
		// Edge case 1 on the LocalQueue: reserved in the ClusterQueue it used to point at.
		workload("team", "b", queue("lq"), reserved("old-cq")),
		workload("team", "c", queue("lq"), finished(), reserved("cq")),
		workload("team", "d", queue("lq"), deleting()),
		workload("team", "other-queue", queue("other")),
		workload("elsewhere", "same-name", queue("lq")),
		workload("team", "no-queue"),
	}
	workloads = append(workloads, workloads[0])
	got := summary(LocalQueueMembers("team", "lq", "cq", workloads))
	want := []string{
		"team/a [submission] submitted=cq reserved=cq",
		"team/b [submission] submitted=cq reserved=old-cq",
		"team/c [submission] submitted=cq reserved=cq",
		"team/d [submission] submitted=cq reserved=",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("members of team/lq:\n got %q\nwant %q", got, want)
	}
	if got := LocalQueueMembers("team", "", "cq", workloads); len(got) != 0 {
		t.Fatalf("a LocalQueue without a name has no members: %v", summary(got))
	}
}

func TestFilterAndPageByKey(t *testing.T) {
	var workloads []*unstructured.Unstructured
	for i := range 7 {
		opts := []workloadOpt{queue("lq")}
		if i%2 == 0 {
			opts = append(opts, reserved("cq"))
		}
		workloads = append(workloads, workload("team", fmt.Sprintf("w%d", i), opts...))
	}
	lqs := readLocalQueues{targets: map[string]string{"team/lq": "cq"}, read: map[string]bool{"team": true}}
	members := ClusterQueueMembers("cq", workloads, lqs)
	if got := len(Filter(members, Reservation)); got != 4 {
		t.Fatalf("reservation members = %d, want 4", got)
	}
	if got := len(Filter(members, "")); got != 7 {
		t.Fatalf("all members = %d, want 7", got)
	}

	var read []string
	after := ""
	for {
		page, more := Page(members, after, 3)
		for _, m := range page {
			read = append(read, m.Workload.GetName())
		}
		if !more {
			break
		}
		after = Key(page[len(page)-1])
	}
	if !slices.Equal(read, []string{"w0", "w1", "w2", "w3", "w4", "w5", "w6"}) {
		t.Fatalf("paged = %v", read)
	}
}

// Membership changes between page reads: nothing read twice, nothing that
// stayed a member skipped, a member added behind the cursor missed until the
// list is read again.
func TestPagingWhileMembershipChanges(t *testing.T) {
	lqs := readLocalQueues{targets: map[string]string{"team/lq": "cq"}, read: map[string]bool{"team": true}}
	names := []string{"a", "c", "e", "g", "i"}
	build := func(names []string) []Member {
		var ws []*unstructured.Unstructured
		for _, n := range names {
			ws = append(ws, workload("team", n, queue("lq")))
		}
		return ClusterQueueMembers("cq", ws, lqs)
	}
	first, more := Page(build(names), "", 2)
	if !more || first[1].Workload.GetName() != "c" {
		t.Fatalf("first page = %v", summary(first))
	}
	// Between reads: "a" leaves, "b" joins behind the cursor, "d" joins ahead of it, "e" leaves.
	second, _ := Page(build([]string{"b", "c", "d", "g", "i"}), Key(first[1]), 2)
	var got []string
	for _, m := range second {
		got = append(got, m.Workload.GetName())
	}
	if !slices.Equal(got, []string{"d", "g"}) {
		t.Fatalf("second page = %v, want d and g", got)
	}
	// A cursor past every member ends the list.
	if page, more := Page(build(names), "team\x00z", 2); len(page) != 0 || more {
		t.Fatalf("past the end = %v, %v", summary(page), more)
	}
}
