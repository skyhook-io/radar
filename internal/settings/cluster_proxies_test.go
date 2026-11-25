package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSetClusterProxy(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("USERPROFILE", tmpDir)

	if got, err := ClusterProxies(); err != nil || len(got) != 0 {
		t.Fatalf("no file should read as no proxies, got %v", got)
	}
	if err := SetClusterProxy("prod", "socks5://user:secret@bastion:1080"); err != nil {
		t.Fatal(err)
	}
	if err := SetClusterProxy("staging", "http://proxy:3128"); err != nil {
		t.Fatal(err)
	}
	if got, _ := ClusterProxies(); got["prod"] != "socks5://user:secret@bastion:1080" {
		t.Fatalf("prod proxy = %q", got)
	}
	info, err := os.Stat(clusterProxiesPath())
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("file mode = %o, want 600: a proxy URL can carry credentials", perm)
	}
	if err := SetClusterProxy("prod", ""); err != nil {
		t.Fatal(err)
	}
	got, _ := ClusterProxies()
	if _, ok := got["prod"]; ok || got["staging"] != "http://proxy:3128" {
		t.Fatalf("after clearing prod: %v", got)
	}
	if s := Load(); s.Theme != "" || len(s.ActiveNamespaces) != 0 {
		t.Fatalf("proxies must not leak into settings.json: %+v", s)
	}
}

// A damaged file must not read as "no proxies": the context would connect
// directly, and the next save would drop every other entry.
func TestClusterProxiesRefusesADamagedFile(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("USERPROFILE", tmpDir)
	if err := SetClusterProxy("prod", "http://proxy:3128"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(clusterProxiesPath(), []byte(`{"prod": "http://proxy:3128",`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ClusterProxies(); err == nil {
		t.Fatal("a damaged file must be an error")
	}
	if err := SetClusterProxy("staging", "http://other:3128"); err == nil {
		t.Fatal("saving over a damaged file must be refused")
	}
	if data, _ := os.ReadFile(clusterProxiesPath()); string(data) != `{"prod": "http://proxy:3128",` {
		t.Fatalf("damaged file was overwritten: %s", data)
	}
}

func TestClusterProxiesRefusesANullFile(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("USERPROFILE", tmpDir)
	if err := SetClusterProxy("prod", "http://proxy:3128"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(clusterProxiesPath(), []byte("null"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ClusterProxies(); err == nil {
		t.Fatal("a null store must be an error, not an empty one")
	}
	if err := SetClusterProxy("staging", "http://other:3128"); err == nil {
		t.Fatal("saving over a null store must be refused")
	}
}

// The store carries credentials: a temp file someone left readable at a
// predictable path must not become the store.
func TestSetClusterProxyDoesNotReuseALooseTempFile(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("USERPROFILE", tmpDir)
	if err := os.MkdirAll(filepath.Dir(clusterProxiesPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	loose := clusterProxiesPath() + ".tmp"
	if err := os.WriteFile(loose, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loose, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SetClusterProxy("prod", "socks5://user:secret@bastion:1080"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(clusterProxiesPath())
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("store mode = %o, want 600", perm)
	}
}
