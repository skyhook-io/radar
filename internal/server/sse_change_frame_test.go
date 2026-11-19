package server

import (
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
)

func TestStreamsChangeFrame(t *testing.T) {
	cases := []struct {
		name   string
		change k8s.ResourceChange
		want   bool
	}{
		{"Argo Application add", k8s.ResourceChange{Kind: "Application", Group: "argoproj.io", Operation: "add"}, true},
		{"Flux Kustomization add", k8s.ResourceChange{Kind: "Kustomization", Group: "kustomize.toolkit.fluxcd.io", Operation: "add"}, true},
		{"Flux HelmRelease add", k8s.ResourceChange{Kind: "HelmRelease", Group: "helm.toolkit.fluxcd.io", Operation: "add"}, true},
		{"Flux GitRepository add", k8s.ResourceChange{Kind: "GitRepository", Group: "source.toolkit.fluxcd.io", Operation: "add"}, true},
		{"Flux Alert add", k8s.ResourceChange{Kind: "Alert", Group: "notification.toolkit.fluxcd.io", Operation: "add"}, true},
		{"kustomize.config.k8s.io is not Flux", k8s.ResourceChange{Kind: "Kustomization", Group: "kustomize.config.k8s.io", Operation: "add"}, false},
		{"ConfigMap add", k8s.ResourceChange{Kind: "ConfigMap", Operation: "add"}, false},
		{"GitOps update without a diff", k8s.ResourceChange{Kind: "Kustomization", Group: "kustomize.toolkit.fluxcd.io", Operation: "update"}, false},
		{"update with a diff", k8s.ResourceChange{Kind: "Deployment", Operation: "update", Diff: &k8s.DiffInfo{}}, true},
		{"delete", k8s.ResourceChange{Kind: "ConfigMap", Operation: "delete"}, true},
		{"Pod add", k8s.ResourceChange{Kind: "Pod", Operation: "add"}, true},
		{"Event", k8s.ResourceChange{Kind: "Event", Operation: "update"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := streamsChangeFrame(tc.change); got != tc.want {
				t.Fatalf("streamsChangeFrame(%+v) = %v, want %v", tc.change, got, tc.want)
			}
		})
	}
}

// A GitOps add frame still goes through the per-client gate: a user who can't
// read Kustomizations in the namespace never receives it.
func TestGitOpsAddFrameIsGatedPerClient(t *testing.T) {
	b := &SSEBroadcaster{clients: map[chan SSEEvent]ClientInfo{}}
	allowed := make(chan SSEEvent, 1)
	denied := make(chan SSEEvent, 1)
	otherNS := make(chan SSEEvent, 1)
	canReadKustomizations := func(group, resource, namespace, verb string) bool {
		return group == "kustomize.toolkit.fluxcd.io" && resource == "kustomizations"
	}
	b.clients[allowed] = ClientInfo{Authorize: canReadKustomizations}
	b.clients[denied] = ClientInfo{Authorize: func(string, string, string, string) bool { return false }}
	b.clients[otherNS] = ClientInfo{Namespaces: []string{"other"}, Authorize: canReadKustomizations}

	change := k8s.ResourceChange{Kind: "Kustomization", Namespace: "apps", Name: "web", Operation: "add", Group: "kustomize.toolkit.fluxcd.io", Resource: "kustomizations"}
	if !streamsChangeFrame(change) {
		t.Fatal("a Kustomization add should stream")
	}
	b.broadcastResourceChange(SSEEvent{Event: "k8s_event", Data: resourceChangeEventData(change, change.Group)}, change.Namespace, change.Group, change.Resource, change.Kind)

	select {
	case <-allowed:
	case <-time.After(time.Second):
		t.Fatal("a client that can read Kustomizations did not receive the add")
	}
	for name, ch := range map[string]chan SSEEvent{"denied": denied, "other namespace": otherNS} {
		select {
		case ev := <-ch:
			t.Fatalf("%s client received %+v", name, ev)
		default:
		}
	}
}
