package prometheus

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/prom"
)

func TestProfileResolverIsolationAndLaunch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	store := &config.ProfileStore{Path: filepath.Join(t.TempDir(), "clusters.json")}
	_, revision, _ := store.Read()
	_, err := store.Update(context.Background(), revision, func(file *config.ClusterProfiles) error {
		for _, key := range []string{"a", "b"} {
			file.Profiles[key] = config.ClusterProfile{Context: key, Target: key, Prometheus: prom.Connection{URL: "https://" + key, Headers: map[string]string{"Authorization": "secret-" + key}}}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	a := k8s.ProfileTarget{Binding: "a", Fingerprint: "a", ClientGeneration: 1}
	b := k8s.ProfileTarget{Binding: "b", Fingerprint: "b", ClientGeneration: 2}
	resolver := NewProfileResolver(store, a, &prom.Connection{URL: "https://launch"})
	for _, target := range []k8s.ProfileTarget{a, b, a} {
		got := resolver.Resolve(target, false)
		if target.Binding == "a" {
			if got.View.State != "launch" || got.Connection.URL != "https://launch" || len(got.Connection.Headers) != 0 {
				t.Fatalf("launch inherited saved headers: %+v", got)
			}
		} else if got.View.State != "saved" || got.Connection.Headers["Authorization"] != "secret-b" {
			t.Fatalf("wrong profile: %+v", got)
		}
		data, _ := json.Marshal(got.View)
		if strings.Contains(string(data), "secret-") {
			t.Fatal("secret exposed in view")
		}
	}
	a.Fingerprint = "new-target"
	got := resolver.Resolve(a, false)
	if got.View.State != "target_changed" {
		t.Fatalf("override survived target change: %+v", got.View)
	}
}

func TestProfileResolverMemoRefresh(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	store := &config.ProfileStore{Path: filepath.Join(t.TempDir(), "clusters.json")}
	target := k8s.ProfileTarget{Binding: "a", Fingerprint: "a", ClientGeneration: 1}
	resolver := NewProfileResolver(store, target, nil)
	first := resolver.Resolve(target, false)
	if first.View.Revision == first.fileRevision {
		t.Fatal("credential-bearing file digest exposed")
	}
	other := NewProfileResolver(store, target, nil).Resolve(target, false)
	if other.View.Revision == first.View.Revision {
		t.Fatal("browser revisions are not process-private")
	}
	_, err := store.Update(context.Background(), first.fileRevision, func(file *config.ClusterProfiles) error {
		file.Profiles["a"] = config.ClusterProfile{Context: "a", Target: "a", Prometheus: prom.Connection{URL: "https://new", Headers: map[string]string{"Authorization": "new-secret"}}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolver.Resolve(target, false).View.State != "auto" {
		t.Fatal("parallel consumer loaded a different revision")
	}
	updated := resolver.Resolve(target, true)
	updated.Connection.Headers["Authorization"] = "mutated"
	if resolver.Resolve(target, false).Connection.Headers["Authorization"] != "new-secret" {
		t.Fatal("live refresh or immutable snapshot failed")
	}
	target.OperationGeneration++
	if resolver.Resolve(target, false).Connection.URL != "https://new" {
		t.Fatal("retry did not reload profile")
	}
}

func TestProfileResolverMissingEnvironmentFailsClosed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	store := &config.ProfileStore{Path: filepath.Join(t.TempDir(), "clusters.json")}
	_, rev, _ := store.Read()
	_, err := store.Update(context.Background(), rev, func(file *config.ClusterProfiles) error {
		file.Profiles["a"] = config.ClusterProfile{Context: "a", Target: "a", Prometheus: prom.Connection{URL: "https://prom", HeadersFromEnv: map[string]string{"Authorization": "RADAR_TEST_UNSET_PROFILE_SECRET"}}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	target := k8s.ProfileTarget{Binding: "a", Fingerprint: "a"}
	got := NewProfileResolver(store, target, nil).Resolve(target, false)
	if got.Err == nil || got.View.State != "error" || !got.View.HeadersManaged {
		t.Fatalf("missing environment allowed activation: %+v", got.View)
	}
}
