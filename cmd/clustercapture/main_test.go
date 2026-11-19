package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunReportsKubeconfigCause(t *testing.T) {
	dir := t.TempDir()
	kubeconfig := filepath.Join(dir, "config")
	if err := os.WriteFile(kubeconfig, []byte("apiVersion: v1\nkind: Config\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", kubeconfig)
	args := os.Args
	defer func() { os.Args = args }()
	os.Args = []string{"clustercapture", "-context", "absent", "-out", filepath.Join(dir, "c.ndjson.gz")}
	err := run()
	if err == nil || !strings.Contains(err.Error(), `context "absent" does not exist`) {
		t.Fatalf("err = %v, want the kubeconfig loader's cause", err)
	}
}
