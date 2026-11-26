package argocd

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/argoapi"
)

func TestCandidateDoesNotReplaceActiveArgoConnection(t *testing.T) {
	live := httptest.NewServer((&fakeArgo{token: "live", loggedIn: true}).handler())
	defer live.Close()
	candidate := httptest.NewServer((&fakeArgo{token: "candidate", loggedIn: true}).handler())
	defer candidate.Close()
	m := newTestManager(config.Config{})
	t.Cleanup(m.Reset)
	m.SetConfig(live.URL, "live", false, true)
	if err := m.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	client, _ := m.Get()
	for _, token := range []string{"wrong", "candidate"} {
		err := PrepareCandidateFor(k8s.Current(), k8s.IsInCluster(), k8s.Current().ContextBinding(), argoapi.Connection{URL: candidate.URL, Token: token})(context.Background())
		if token == "wrong" && !errors.Is(err, ErrTokenInvalid) {
			t.Fatalf("rejected candidate: %v", err)
		}
		if token == "candidate" && err != nil {
			t.Fatal(err)
		}
		current, connected := m.Get()
		if !connected || current != client || m.Address() != live.URL {
			t.Fatal("candidate replaced active connection")
		}
	}
}
