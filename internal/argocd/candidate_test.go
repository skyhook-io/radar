package argocd

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/pkg/argoapi"
)

func TestCandidateDoesNotReplaceActiveArgoConnection(t *testing.T) {
	live := httptest.NewServer((&fakeArgo{token: "live", loggedIn: true}).handler())
	defer live.Close()
	candidate := httptest.NewServer((&fakeArgo{token: "candidate", loggedIn: true}).handler())
	defer candidate.Close()
	previous := defaultManager
	defaultManager = newTestManager(config.Config{})
	t.Cleanup(func() { defaultManager.Reset(); defaultManager = previous })
	defaultManager.SetConfig(live.URL, "live", false, true)
	if err := defaultManager.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	client, _ := defaultManager.Get()
	for _, token := range []string{"wrong", "candidate"} {
		err := ProbeCandidate(context.Background(), argoapi.Connection{URL: candidate.URL, Token: token})
		if token == "wrong" && !errors.Is(err, ErrTokenInvalid) {
			t.Fatalf("rejected candidate: %v", err)
		}
		if token == "candidate" && err != nil {
			t.Fatal(err)
		}
		current, connected := defaultManager.Get()
		if !connected || current != client || defaultManager.Address() != live.URL {
			t.Fatal("candidate replaced active connection")
		}
	}
}
