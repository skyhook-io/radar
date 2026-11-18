package substrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/skyhook-io/radar/internal/config"
)

func TestStoreRevisionAndValidation(t *testing.T) {
	s := &Store{Path: filepath.Join(t.TempDir(), "substrate.json")}
	saved := &Saved{Context: "dev", Target: "fp", Identity: config.TargetIdentity{Server: "https://cluster-a", User: "developer"}, Settings: Settings{Identity: IdentityShared}}
	_, _, rev, err := s.GetRevision("binding")
	if err != nil || rev == "" {
		t.Fatalf("missing: %q %v", rev, err)
	}
	next, err := s.Update(context.Background(), rev, "binding", saved)
	if err != nil || next == rev {
		t.Fatalf("save: %q %v", next, err)
	}
	if _, err := s.Update(context.Background(), rev, "binding", nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale writer: %v", err)
	}
	got, ok, current, err := s.GetRevision("binding")
	if err != nil || !ok || got.Identity != saved.Identity || current != next {
		t.Fatalf("roundtrip: %+v %v %v", got, ok, err)
	}
	stat, _ := os.Stat(s.Path)
	if stat.Mode().Perm() != 0600 {
		t.Errorf("permissions: %v", stat.Mode())
	}
	data, _ := os.ReadFile(s.Path)
	changed := strings.Replace(string(data), "developer", "different", 1)
	if len(changed) != len(data) {
		t.Fatal("fixture must preserve size")
	}
	if err := os.WriteFile(s.Path, []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(s.Path, stat.ModTime(), stat.ModTime())
	_, newer, edited, err := s.ReadSince(next)
	if err != nil || !edited || newer == next {
		t.Fatalf("same-size edit: %v %v", edited, err)
	}
	for _, invalid := range []string{"", "{", `{"version":2,"contexts":{}}`, `{"version":1,"unknown":true}`, `{"version":1,"contexts":{}} {}`, `{"version":1,"contexts":{"b":{"settings":{"identity":"root"}}}}`} {
		os.WriteFile(s.Path, []byte(invalid), 0600)
		if _, _, _, err := s.GetRevision("binding"); err == nil {
			t.Errorf("accepted %q", invalid)
		}
		if _, err := s.Update(context.Background(), newer, "binding", saved); err == nil {
			t.Errorf("overwrote invalid %q", invalid)
		}
	}
	os.WriteFile(s.Path, []byte(strings.Repeat(" ", 1<<20+1)), 0600)
	if _, _, _, err := s.GetRevision("binding"); err == nil {
		t.Error("oversized file accepted")
	}
}

func TestStoreChecksRevisionUnderLock(t *testing.T) {
	s := &Store{Path: filepath.Join(t.TempDir(), "substrate.json")}
	_, _, rev, _ := s.GetRevision("b")
	lock := flock.New(s.Path + ".lock")
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	done := make(chan error, 1)
	go func() {
		_, err := s.Update(context.Background(), rev, "b", &Saved{Settings: Settings{Identity: IdentityShared}})
		done <- err
	}()
	os.WriteFile(s.Path, []byte("{\"version\":1,\"contexts\":{}}\n"), 0600)
	lock.Unlock()
	select {
	case err := <-done:
		if !errors.Is(err, ErrConflict) {
			t.Errorf("revision was not checked under lock: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("writer stuck")
	}
}

func TestSelectionRetiresGenerationOnExternalEdit(t *testing.T) {
	for _, entry := range []string{"Snapshot", "Peek", "Call", "AtespaceActors"} {
		t.Run(entry, func(t *testing.T) {
			m, k, s := stubManager(t, fixedKube)
			m.conn = &conn{key: connKey(k, s), client: &stubControl{actor: "old"}}
			if _, tier := m.Snapshot(context.Background()); tier.State != TierConnected {
				t.Fatalf("seed: %+v", tier)
			}
			gen, ctx := m.generation()
			data, _ := os.ReadFile(m.Store.Path)
			data = []byte(strings.Replace(string(data), "cluster-a", "cluster-b", 1))
			if err := os.WriteFile(m.Store.Path, data, 0600); err != nil {
				t.Fatal(err)
			}
			var tier *APITier
			switch entry {
			case "Snapshot":
				_, v := m.Snapshot(context.Background())
				tier = &v
			case "Peek":
				_, v := m.Peek(context.Background())
				tier = &v
			case "Call":
				tier, _, _ = m.Call(context.Background(), nil)
			case "AtespaceActors":
				_, _, tier = m.AtespaceActors(context.Background(), "team")
			}
			if tier == nil || tier.Code != CodeTargetChanged {
				t.Fatalf("served stale cache: %+v", tier)
			}
			next, _ := m.generation()
			if next == gen || ctx.Err() == nil || m.good != nil || m.conn != nil {
				t.Fatal("old generation not retired")
			}
		})
	}
}

func TestInstallationEnvironment(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"defaults": {}, "disabled": {"RADAR_SUBSTRATE_API": "disabled"},
		"endpoint": {"RADAR_SUBSTRATE_ENDPOINT": "other.example:8443", "RADAR_SUBSTRATE_SERVER_NAME": "other.example", "RADAR_SUBSTRATE_ATESPACES": "team-a,team-b"},
		"bad api":  {"RADAR_SUBSTRATE_API": "auto"}, "empty api": {"RADAR_SUBSTRATE_API": ""}, "bad endpoint": {"RADAR_SUBSTRATE_ENDPOINT": "https://api:443"},
		"port": {"RADAR_SUBSTRATE_ENDPOINT": "api:65536"}, "empty endpoint": {"RADAR_SUBSTRATE_ENDPOINT": ""}, "server name": {"RADAR_SUBSTRATE_SERVER_NAME": "INVALID"},
		"CA": {"RADAR_SUBSTRATE_CA_FILE": "/nonexistent/ca.pem"}, "relative CA": {"RADAR_SUBSTRATE_CA_FILE": "ca.pem"}, "empty CA": {"RADAR_SUBSTRATE_CA_FILE": ""},
		"atespace": {"RADAR_SUBSTRATE_ATESPACES": "bad_name"}, "duplicate": {"RADAR_SUBSTRATE_ATESPACES": "team,team"}, "empty entry": {"RADAR_SUBSTRATE_ATESPACES": "team,"},
		"cap": {"RADAR_SUBSTRATE_ATESPACES": strings.Repeat("team,", 64) + "last"},
	} {
		t.Run(name, func(t *testing.T) {
			c := ParseInstallation(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
			valid := name == "defaults" || name == "disabled" || name == "endpoint"
			if (c.Err == nil) != valid {
				t.Fatalf("%+v", c)
			}
			if name == "defaults" && (c.Settings.Endpoint != "api.ate-system.svc:443" || c.Trust() != "clusterTrustBundle" || !c.Enabled) {
				t.Fatalf("defaults: %+v", c)
			}
			if name == "disabled" && c.Enabled {
				t.Fatal("off switch lost")
			}
		})
	}
	pem, _ := testCertPEM(t, "installation")
	path := filepath.Join(t.TempDir(), "ca.pem")
	os.WriteFile(path, []byte(pem), 0600)
	c := ParseInstallation(func(k string) (string, bool) { return path, k == "RADAR_SUBSTRATE_CA_FILE" })
	if c.Err != nil || c.Trust() != "caFile" {
		t.Fatalf("CA: %+v", c)
	}
}
