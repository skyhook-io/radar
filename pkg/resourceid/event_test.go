package resourceid

import "testing"

func TestEventSubject(t *testing.T) {
	for _, tc := range []struct {
		name                                  string
		apiVersion, kind, ns, obj, uid, evtNs string
		wantNs, wantUID, wantGroup            string
		wantSource                            GroupSource
	}{
		{
			name:       "namespaced subject keeps its own namespace",
			apiVersion: "v1", kind: "Pod", ns: "shop", obj: "web-1", uid: "u1", evtNs: "shop",
			wantNs: "shop", wantUID: "u1", wantGroup: "", wantSource: GroupObserved,
		},
		{
			name:       "Node event stored in default is cluster-scoped, and kubelet's name-as-UID is no incarnation",
			apiVersion: "v1", kind: "Node", obj: "node-a", uid: "node-a", evtNs: "default",
			wantNs: "", wantUID: "", wantGroup: "", wantSource: GroupObserved,
		},
		{
			name:       "Node event with a real UID keeps it",
			apiVersion: "v1", kind: "Node", obj: "node-a", uid: "0b8c-real", evtNs: "default",
			wantNs: "", wantUID: "0b8c-real", wantSource: GroupObserved,
		},
		{
			name: "kubelet's Node reference has no apiVersion",
			kind: "Node", obj: "node-a", uid: "node-a", evtNs: "default",
			wantNs: "", wantUID: "", wantSource: GroupMissing,
		},
		{
			name:       "built-in namespaced subject without involvedObject.namespace falls back to the Event's namespace",
			apiVersion: "v1", kind: "Pod", obj: "web-1", uid: "u1", evtNs: "shop",
			wantNs: "shop", wantUID: "u1", wantSource: GroupObserved,
		},
		{
			name: "a missing group does not prove built-in scope",
			kind: "Job", obj: "train", uid: "j1", evtNs: "default",
			wantNs: "", wantUID: "j1", wantSource: GroupMissing,
		},
		{
			name:       "cluster-scoped CRD subject stays cluster-scoped",
			apiVersion: "scheduling.volcano.sh/v1beta1", kind: "Queue", obj: "gpu", uid: "q1", evtNs: "default",
			wantNs: "", wantUID: "q1", wantGroup: "scheduling.volcano.sh", wantSource: GroupObserved,
		},
		{
			name:       "a CRD sharing a built-in Kind does not borrow the built-in's scope",
			apiVersion: "batch.volcano.sh/v1alpha1", kind: "Job", obj: "train", uid: "j1", evtNs: "ml",
			wantNs: "", wantUID: "j1", wantGroup: "batch.volcano.sh", wantSource: GroupObserved,
		},
		{
			name:       "a CRD Node's UID is kept even when it equals the name",
			apiVersion: "example.com/v1", kind: "Node", obj: "n", uid: "n", evtNs: "default",
			wantNs: "", wantUID: "n", wantGroup: "example.com", wantSource: GroupObserved,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := EventSubject(tc.apiVersion, tc.kind, tc.ns, tc.obj, tc.uid, tc.evtNs)
			if got.Namespace != tc.wantNs || got.UID != tc.wantUID || got.Group != tc.wantGroup || got.GroupSource != tc.wantSource {
				t.Fatalf("got ns=%q uid=%q group=%q (%s), want ns=%q uid=%q group=%q (%s)",
					got.Namespace, got.UID, got.Group, got.GroupSource, tc.wantNs, tc.wantUID, tc.wantGroup, tc.wantSource)
			}
			if got.Kind != tc.kind || got.Name != tc.obj {
				t.Fatalf("subject renamed: %+v", got)
			}
		})
	}
}
