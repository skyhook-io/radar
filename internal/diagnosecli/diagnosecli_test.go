package diagnosecli

import (
	"os"
	"strings"
	"testing"
)

func TestNormalizeKind(t *testing.T) {
	cases := map[string]string{
		"pod": "Pod", "pods": "Pod", "po": "Pod",
		"deploy": "Deployment", "deployments": "Deployment",
		"sts": "StatefulSet", "svc": "Service", "ns": "Namespace",
		"Pod": "Pod", "CronJob": "CronJob",
		// Unknown kinds pass through title-cased (CRDs, etc.).
		"kafkacluster": "Kafkacluster", "HelmRelease": "HelmRelease",
	}
	for in, want := range cases {
		if got := normalizeKind(in); got != want {
			t.Errorf("normalizeKind(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRendererVerdictShapes(t *testing.T) {
	// Smoke: every verdict shape renders without panicking and mentions its
	// anchor word (plain-text path, no TTY).
	r := &renderer{w: nil, color: false}
	_ = r
	conf := 0.9
	rec := 1
	shapes := []struct {
		d    diagnosis
		want string
	}{
		{diagnosis{Healthy: true, Report: "All good."}, "No problems found"},
		{diagnosis{Inconclusive: true, Report: "RBAC blocked reads."}, "Couldn't determine"},
		{diagnosis{RootCause: "bad `image` tag", Remediation: []string{"fix the **tag**"},
			RecommendedIndex: &rec, RecommendedReason: "targeted", Confidence: &conf}, "Root cause"},
		{diagnosis{Report: "narration only"}, "narration only"},
	}
	for _, c := range shapes {
		out := captureVerdict(t, c.d)
		if !strings.Contains(out, c.want) {
			t.Errorf("verdict output missing %q:\n%s", c.want, out)
		}
	}
}

func TestRendererVerdictRepeatsWatchURL(t *testing.T) {
	tmp, err := createTempFile(t)
	if err != nil {
		t.Fatal(err)
	}
	r := &renderer{w: tmp, color: false}
	r.header(runSummary{ID: "run-123", Kind: "Pod", Name: "checkout", Agent: "codex"}, "http://localhost:9280")
	r.verdict(diagnosis{Healthy: true})
	if _, err := tmp.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 8192)
	n, _ := tmp.Read(buf)
	got := string(buf[:n])
	if url := "http://localhost:9280/?ai-run=run-123"; strings.Count(got, url) != 2 {
		t.Fatalf("watch URL should appear in the header and final footer:\n%s", got)
	}
}

func captureVerdict(t *testing.T, d diagnosis) string {
	t.Helper()
	tmp, err := createTempFile(t)
	if err != nil {
		t.Fatal(err)
	}
	r := &renderer{w: tmp, color: false}
	r.verdict(d)
	if _, err := tmp.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 8192)
	n, _ := tmp.Read(buf)
	return string(buf[:n])
}

func createTempFile(t *testing.T) (*os.File, error) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "render")
	if err == nil {
		t.Cleanup(func() { _ = f.Close() })
	}
	return f, err
}

// TestInterleavedFlagParsing pins kubectl-style invocation: flags may follow
// the positional target (`radar diagnose pod/web -n prod --json`).
func TestInterleavedFlagParsing(t *testing.T) {
	fs, o := newFlagSet()
	var positionals []string
	rest := []string{"pod/web", "-n", "prod", "--json"}
	for {
		if err := fs.Parse(rest); err != nil {
			t.Fatal(err)
		}
		if fs.NArg() == 0 {
			break
		}
		positionals = append(positionals, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(positionals) != 1 || positionals[0] != "pod/web" {
		t.Fatalf("positionals = %v", positionals)
	}
	if o.namespace != "prod" || !o.jsonOut {
		t.Fatalf("flags not parsed: ns=%q json=%v", o.namespace, o.jsonOut)
	}
}
