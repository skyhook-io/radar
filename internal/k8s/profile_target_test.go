package k8s

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/client-go/rest"
)

func TestProfileTargetIdentity(t *testing.T) {
	t.Cleanup(SetTestProfileSource("/test/team", "dev", "developer"))
	cfg := &rest.Config{Host: "https://cluster", BearerToken: "old", TLSClientConfig: rest.TLSClientConfig{CAData: []byte("trust")}}
	previous := SetTestConfig(cfg)
	t.Cleanup(func() { SetTestConfig(previous) })
	original, err := CurrentProfileTarget()
	if err != nil {
		t.Fatal(err)
	}
	cfg.BearerToken = "rotated"
	same, _ := CurrentProfileTarget()
	if same.Fingerprint != original.Fingerprint {
		t.Fatal("token rotation invalidated profile")
	}
	for _, field := range []string{"server", "ca", "tls-name", "insecure", "user"} {
		t.Run(field, func(t *testing.T) {
			changed := rest.CopyConfig(cfg)
			switch field {
			case "server":
				changed.Host = "https://other"
			case "ca":
				changed.CAData = []byte("new trust")
			case "tls-name":
				changed.ServerName = "new"
			case "insecure":
				changed.Insecure = true
			case "user":
				t.Cleanup(SetTestProfileSource("/test/team", "dev", "other-user"))
			}
			SetTestConfig(changed)
			t.Cleanup(func() { SetTestConfig(cfg) })
			got, err := CurrentProfileTarget()
			if err != nil || got.Fingerprint == original.Fingerprint {
				t.Fatalf("target change missed: %v", err)
			}
		})
	}
	path := filepath.Join(t.TempDir(), "ca")
	if err := os.WriteFile(path, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.CAData = nil
	cfg.CAFile = path
	first, err := CurrentProfileTarget()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("second"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := CurrentProfileTarget()
	if err != nil || second.Fingerprint == first.Fingerprint {
		t.Fatal("changed CA file contents ignored")
	}
	second.OperationGeneration++
	if first.Same(second) {
		t.Fatal("stale generation accepted")
	}
}

func TestClusterConfigurationRejectsBusyOperation(t *testing.T) {
	contextOpMu.Lock()
	err := TryClusterConfiguration(func(func() error) error { t.Fatal("entered held context lock"); return nil })
	contextOpMu.Unlock()
	if !errors.Is(err, ErrContextConfigurationBusy) {
		t.Fatalf("held lock: %v", err)
	}
	activeContextOperations.Add(1)
	err = TryClusterConfiguration(func(func() error) error { t.Fatal("leapfrogged queued switch"); return nil })
	activeContextOperations.Add(-1)
	if !errors.Is(err, ErrContextConfigurationBusy) {
		t.Fatalf("queued switch: %v", err)
	}
	if err := TryClusterConfiguration(func(func() error) error { return nil }); err != nil {
		t.Fatal(err)
	}
}
