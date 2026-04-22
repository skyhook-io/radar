package k8s

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// writeKubeconfig writes a minimal but valid kubeconfig to a temp file in
// dir and returns its path. Each (ctxName, userName, clusterName) entry
// becomes a context with matching Cluster/AuthInfo references. currentCtx
// sets the CurrentContext field; pass "" to omit it.
func writeKubeconfig(t *testing.T, dir, filename, currentCtx string, entries []kubeEntry) string {
	t.Helper()
	cfg := clientcmdapi.NewConfig()
	for _, e := range entries {
		cfg.Contexts[e.ctxName] = &clientcmdapi.Context{
			Cluster:   e.clusterName,
			AuthInfo:  e.userName,
			Namespace: e.namespace,
		}
		if _, ok := cfg.Clusters[e.clusterName]; !ok {
			cfg.Clusters[e.clusterName] = &clientcmdapi.Cluster{
				Server: "https://" + e.clusterName,
				// Base64 of "ca" — client-go validates presence on load.
				InsecureSkipTLSVerify: true,
			}
		}
		if _, ok := cfg.AuthInfos[e.userName]; !ok {
			ai := &clientcmdapi.AuthInfo{}
			if e.execCommand != "" {
				ai.Exec = &clientcmdapi.ExecConfig{
					APIVersion: "client.authentication.k8s.io/v1beta1",
					Command:    e.execCommand,
				}
			} else {
				ai.Token = "fake-token-for-" + e.userName
			}
			cfg.AuthInfos[e.userName] = ai
		}
	}
	cfg.CurrentContext = currentCtx

	path := filepath.Join(dir, filename)
	data, err := clientcmd.Write(*cfg)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

type kubeEntry struct {
	ctxName     string
	userName    string
	clusterName string
	namespace   string
	execCommand string // empty = token auth
}

func TestBuildContextRegistry_NoCollisions(t *testing.T) {
	dir := t.TempDir()
	f1 := writeKubeconfig(t, dir, "a.yaml", "ctx-a", []kubeEntry{
		{ctxName: "ctx-a", userName: "user-a", clusterName: "cluster-a"},
	})
	f2 := writeKubeconfig(t, dir, "b.yaml", "ctx-b", []kubeEntry{
		{ctxName: "ctx-b", userName: "user-b", clusterName: "cluster-b"},
	})

	registry, fileConfigs := buildContextRegistry([]string{f1, f2})

	if len(registry) != 2 {
		t.Fatalf("registry size: got %d, want 2", len(registry))
	}
	if _, ok := registry["ctx-a"]; !ok {
		t.Errorf("missing ctx-a in registry")
	}
	if _, ok := registry["ctx-b"]; !ok {
		t.Errorf("missing ctx-b in registry")
	}
	if registry["ctx-a"].SourceFile != f1 {
		t.Errorf("ctx-a sourceFile: got %s, want %s", registry["ctx-a"].SourceFile, f1)
	}
	if _, ok := fileConfigs[f1]; !ok {
		t.Errorf("fileConfigs missing %s", f1)
	}
}

// Core issue #519 scenario: two files share user AND cluster names but have
// distinct context names. Both contexts should be registered under their
// original names, and each entry should resolve to its own source file so
// ExplicitPath loading gives the correct credentials.
func TestBuildContextRegistry_SharedUserAndCluster_DistinctContexts(t *testing.T) {
	dir := t.TempDir()
	f1 := writeKubeconfig(t, dir, "kas-107.yaml", "kas-107", []kubeEntry{
		{ctxName: "kas-107", userName: "me", clusterName: "gitlab_kas"},
	})
	f2 := writeKubeconfig(t, dir, "kas-108.yaml", "kas-108", []kubeEntry{
		{ctxName: "kas-108", userName: "me", clusterName: "gitlab_kas"},
	})

	registry, _ := buildContextRegistry([]string{f1, f2})

	if len(registry) != 2 {
		t.Fatalf("registry size: got %d, want 2 — shared users/clusters must not collapse distinct contexts", len(registry))
	}
	if registry["kas-107"].SourceFile != f1 {
		t.Errorf("kas-107 must resolve to file 1, got %s", registry["kas-107"].SourceFile)
	}
	if registry["kas-108"].SourceFile != f2 {
		t.Errorf("kas-108 must resolve to file 2, got %s", registry["kas-108"].SourceFile)
	}
	// Neither should be renamed — the original names don't collide.
	for qName := range registry {
		if qName != "kas-107" && qName != "kas-108" {
			t.Errorf("unexpected renamed context %q; distinct names must not be qualified", qName)
		}
	}
}

// When context names themselves collide across files, later files get their
// context name qualified with the source file's basename.
func TestBuildContextRegistry_ContextNameCollision(t *testing.T) {
	dir := t.TempDir()
	f1 := writeKubeconfig(t, dir, "prod.yaml", "my-ctx", []kubeEntry{
		{ctxName: "my-ctx", userName: "user-a", clusterName: "cluster-a"},
	})
	f2 := writeKubeconfig(t, dir, "staging.yaml", "my-ctx", []kubeEntry{
		{ctxName: "my-ctx", userName: "user-b", clusterName: "cluster-b"},
	})

	registry, _ := buildContextRegistry([]string{f1, f2})

	if len(registry) != 2 {
		t.Fatalf("registry size: got %d, want 2", len(registry))
	}
	if _, ok := registry["my-ctx"]; !ok {
		t.Errorf("first file's context should keep its original name")
	}
	if registry["my-ctx"].SourceFile != f1 {
		t.Errorf("my-ctx should resolve to f1")
	}
	if _, ok := registry["my-ctx (staging)"]; !ok {
		names := []string{}
		for n := range registry {
			names = append(names, n)
		}
		sort.Strings(names)
		t.Errorf("expected qualified name 'my-ctx (staging)' in registry; got: %v", names)
	}
	if registry["my-ctx (staging)"].SourceFile != f2 {
		t.Errorf("qualified context should resolve to f2")
	}
	if registry["my-ctx (staging)"].InFileName != "my-ctx" {
		t.Errorf("original name must remain 'my-ctx' inside f2")
	}
}

// Three-way collision: same context name across three files, all sharing the
// same basename (two with different extensions). Third should fall back to
// the numeric-suffix form.
func TestBuildContextRegistry_ThreeWayCollision(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	dirC := t.TempDir()
	f1 := writeKubeconfig(t, dirA, "env.yaml", "ctx", []kubeEntry{
		{ctxName: "ctx", userName: "u1", clusterName: "c1"},
	})
	// Same basename after trimming extension — forces numeric suffix path.
	f2 := writeKubeconfig(t, dirB, "env.yml", "ctx", []kubeEntry{
		{ctxName: "ctx", userName: "u2", clusterName: "c2"},
	})
	f3 := writeKubeconfig(t, dirC, "env.yaml", "ctx", []kubeEntry{
		{ctxName: "ctx", userName: "u3", clusterName: "c3"},
	})

	registry, _ := buildContextRegistry([]string{f1, f2, f3})

	if len(registry) != 3 {
		t.Fatalf("registry size: got %d, want 3", len(registry))
	}
	// f1: plain "ctx"
	if e, ok := registry["ctx"]; !ok || e.SourceFile != f1 {
		t.Errorf("'ctx' should resolve to f1")
	}
	// f2: "ctx (env)"
	if e, ok := registry["ctx (env)"]; !ok || e.SourceFile != f2 {
		t.Errorf("'ctx (env)' should resolve to f2")
	}
	// f3: "ctx (env #2)" — same basename as f2 after ext trim.
	if e, ok := registry["ctx (env #2)"]; !ok || e.SourceFile != f3 {
		names := []string{}
		for n := range registry {
			names = append(names, n)
		}
		sort.Strings(names)
		t.Errorf("'ctx (env #2)' should resolve to f3; registry has: %v", names)
	}
}

func TestPickInitialContext_PrefersFirstFileCurrentContext(t *testing.T) {
	dir := t.TempDir()
	f1 := writeKubeconfig(t, dir, "first.yaml", "from-first", []kubeEntry{
		{ctxName: "from-first", userName: "u1", clusterName: "c1"},
	})
	f2 := writeKubeconfig(t, dir, "second.yaml", "from-second", []kubeEntry{
		{ctxName: "from-second", userName: "u2", clusterName: "c2"},
	})

	paths := []string{f1, f2}
	registry, fileConfigs := buildContextRegistry(paths)
	qName, entry, ok := pickInitialContext(paths, registry, fileConfigs)
	if !ok {
		t.Fatal("expected initial context")
	}
	if qName != "from-first" {
		t.Errorf("expected 'from-first', got %q", qName)
	}
	if entry.SourceFile != f1 {
		t.Errorf("expected entry from f1, got %s", entry.SourceFile)
	}
}

func TestPickInitialContext_FallsBackWhenCurrentContextEmpty(t *testing.T) {
	dir := t.TempDir()
	// First file has no CurrentContext; second does.
	f1 := writeKubeconfig(t, dir, "first.yaml", "", []kubeEntry{
		{ctxName: "from-first", userName: "u1", clusterName: "c1"},
	})
	f2 := writeKubeconfig(t, dir, "second.yaml", "from-second", []kubeEntry{
		{ctxName: "from-second", userName: "u2", clusterName: "c2"},
	})

	paths := []string{f1, f2}
	registry, fileConfigs := buildContextRegistry(paths)
	qName, _, ok := pickInitialContext(paths, registry, fileConfigs)
	if !ok {
		t.Fatal("expected initial context")
	}
	if qName != "from-second" {
		t.Errorf("expected 'from-second', got %q", qName)
	}
}

func TestPickInitialContext_NoCurrentContextAnywhere(t *testing.T) {
	dir := t.TempDir()
	f1 := writeKubeconfig(t, dir, "first.yaml", "", []kubeEntry{
		{ctxName: "only-ctx", userName: "u1", clusterName: "c1"},
	})

	paths := []string{f1}
	registry, fileConfigs := buildContextRegistry(paths)
	qName, _, ok := pickInitialContext(paths, registry, fileConfigs)
	if !ok {
		t.Fatal("expected initial context from any-ctx fallback")
	}
	if qName != "only-ctx" {
		t.Errorf("expected 'only-ctx', got %q", qName)
	}
}

// Regression guard for the #519 class of bug. Simulates what SwitchContext does:
// resolve the qualified name through the registry, then load the target with
// ExplicitPath. Two files share user and cluster names but carry distinct
// tokens / server URLs. Each context must resolve to *its own* file's
// definitions — which is exactly what client-go's Precedence merge would
// have broken.
func TestSwitchContextRouting_SharedNames_RoutesToCorrectFile(t *testing.T) {
	dir := t.TempDir()
	f1 := writeKubeconfig(t, dir, "file-a.yaml", "kas-107", []kubeEntry{
		{ctxName: "kas-107", userName: "me", clusterName: "shared"},
	})
	f2 := writeKubeconfig(t, dir, "file-b.yaml", "kas-108", []kubeEntry{
		{ctxName: "kas-108", userName: "me", clusterName: "shared"},
	})
	// Replace the shared user/cluster definitions with per-file unique
	// tokens and server URLs so the test can observe which file a later
	// ExplicitPath load actually reads from.
	setUserTokenAndServer(t, f1, "me", "token-from-a", "shared", "https://server-a.test")
	setUserTokenAndServer(t, f2, "me", "token-from-b", "shared", "https://server-b.test")

	registry, _ := buildContextRegistry([]string{f1, f2})

	entryA, ok := registry["kas-107"]
	if !ok {
		t.Fatal("kas-107 missing from registry")
	}
	loadedA, err := clientcmd.LoadFromFile(entryA.SourceFile)
	if err != nil {
		t.Fatalf("load %s: %v", entryA.SourceFile, err)
	}
	if got := loadedA.AuthInfos["me"].Token; got != "token-from-a" {
		t.Errorf("kas-107 token: got %q, want token-from-a", got)
	}
	if got := loadedA.Clusters["shared"].Server; got != "https://server-a.test" {
		t.Errorf("kas-107 server: got %q, want https://server-a.test", got)
	}

	entryB, ok := registry["kas-108"]
	if !ok {
		t.Fatal("kas-108 missing from registry")
	}
	loadedB, err := clientcmd.LoadFromFile(entryB.SourceFile)
	if err != nil {
		t.Fatalf("load %s: %v", entryB.SourceFile, err)
	}
	if got := loadedB.AuthInfos["me"].Token; got != "token-from-b" {
		t.Errorf("kas-108 token: got %q, want token-from-b (Precedence-merge regression would show token-from-a)", got)
	}
	if got := loadedB.Clusters["shared"].Server; got != "https://server-b.test" {
		t.Errorf("kas-108 server: got %q, want https://server-b.test", got)
	}
}

func setUserTokenAndServer(t *testing.T, path, userName, token, clusterName, server string) {
	t.Helper()
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil {
		t.Fatalf("load %s: %v", path, err)
	}
	cfg.AuthInfos[userName] = &clientcmdapi.AuthInfo{Token: token}
	cfg.Clusters[clusterName] = &clientcmdapi.Cluster{
		Server:                server,
		InsecureSkipTLSVerify: true,
	}
	data, err := clientcmd.Write(*cfg)
	if err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("writeback %s: %v", path, err)
	}
}

func TestAggregateExecPluginCommands_EmptyCommandScopedByFile(t *testing.T) {
	dir := t.TempDir()
	// Each file has a user with an exec block but an EMPTY command — a
	// classic user misconfiguration. The aggregator must report both
	// separately so diagnostics can point at the right file.
	f1 := writeKubeconfig(t, dir, "alpha.yaml", "ctx-a", []kubeEntry{
		{ctxName: "ctx-a", userName: "oidc", clusterName: "c1", execCommand: ""},
	})
	f2 := writeKubeconfig(t, dir, "beta.yaml", "ctx-b", []kubeEntry{
		{ctxName: "ctx-b", userName: "oidc", clusterName: "c2", execCommand: ""},
	})
	// Manually inject an empty-command exec block (writeKubeconfig's
	// execCommand="" falls through to a token — we want an actual exec with
	// empty Command to hit the aggregator's emptyCommandAuthInfos path).
	injectEmptyExec(t, f1, "oidc")
	injectEmptyExec(t, f2, "oidc")

	paths := []string{f1, f2}
	_, fileConfigs := buildContextRegistry(paths)
	_, empty := aggregateExecPluginCommands(paths, fileConfigs)

	if len(empty) != 2 {
		t.Fatalf("expected 2 scoped empty-command entries, got %d: %v", len(empty), empty)
	}
	// Should be sorted; "oidc (alpha)" < "oidc (beta)".
	if empty[0] != "oidc (alpha)" || empty[1] != "oidc (beta)" {
		t.Errorf("empty-command AuthInfos not scoped by file basename: got %v, want [oidc (alpha) oidc (beta)]", empty)
	}
}

func injectEmptyExec(t *testing.T, path, userName string) {
	t.Helper()
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil {
		t.Fatalf("load %s: %v", path, err)
	}
	cfg.AuthInfos[userName] = &clientcmdapi.AuthInfo{
		Exec: &clientcmdapi.ExecConfig{
			APIVersion: "client.authentication.k8s.io/v1beta1",
			Command:    "", // the bit we care about
		},
	}
	data, err := clientcmd.Write(*cfg)
	if err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("writeback %s: %v", path, err)
	}
}

func TestAggregateExecPluginCommands_UniqueAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	// File 1: user 'oidc' with kubectl exec plugin.
	f1 := writeKubeconfig(t, dir, "a.yaml", "ctx-a", []kubeEntry{
		{ctxName: "ctx-a", userName: "oidc", clusterName: "c1", execCommand: "kubectl"},
	})
	// File 2: same user name, different exec plugin — under Precedence merge
	// this second one would be silently dropped. Aggregation must see both.
	f2 := writeKubeconfig(t, dir, "b.yaml", "ctx-b", []kubeEntry{
		{ctxName: "ctx-b", userName: "oidc", clusterName: "c2", execCommand: "gke-gcloud-auth-plugin"},
	})

	paths := []string{f1, f2}
	_, fileConfigs := buildContextRegistry(paths)
	cmds, empty := aggregateExecPluginCommands(paths, fileConfigs)

	if len(empty) != 0 {
		t.Errorf("expected no empty-command AuthInfos, got %v", empty)
	}
	wantCmds := map[string]bool{"kubectl": false, "gke-gcloud-auth-plugin": false}
	for _, c := range cmds {
		if _, ok := wantCmds[c]; ok {
			wantCmds[c] = true
		}
	}
	for c, seen := range wantCmds {
		if !seen {
			t.Errorf("expected exec plugin %q in aggregated list, got %v", c, cmds)
		}
	}
}
