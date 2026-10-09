package promtest

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func Run(t *testing.T, image string, input []map[string]string, tests []map[string]any) {
	t.Helper()
	fixture := map[string]any{"evaluation_interval": "1m", "tests": []map[string]any{{"interval": "1m", "input_series": input, "promql_expr_test": tests}}}
	data, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rules.yml"), data, 0644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "--network", "none", "--read-only", "--tmpfs", "/tmp", "--entrypoint", "/bin/promtool", "-v", dir+":/tests:ro", image, "test", "rules", "/tests/rules.yml")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("PromQL evaluation: %v\n%s", err, output)
	}
}
