package k8s

import (
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// Generic "config" basenames in distinctly-named parent dirs must
// disambiguate via parent dir, not basename — three kubeconfigs at
// ~/.kube-cluster-{paris,london,rome}/config sharing context name
// "admin@cluster" should yield three distinct qualified names, not
// "admin@cluster (config)" / "(config #2)".
func TestBuildContextRegistry_GenericFilenameUsesParentDir(t *testing.T) {
	dirParis := filepath.Join(t.TempDir(), ".kube-cluster-paris")
	dirLondon := filepath.Join(t.TempDir(), ".kube-cluster-london")
	dirRome := filepath.Join(t.TempDir(), ".kube-cluster-rome")
	for _, d := range []string{dirParis, dirLondon, dirRome} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	f1 := writeKubeconfig(t, dirParis, "config", "admin@cluster", []kubeEntry{
		{ctxName: "admin@cluster", userName: "admin", clusterName: "cluster"},
	})
	f2 := writeKubeconfig(t, dirLondon, "config", "admin@cluster", []kubeEntry{
		{ctxName: "admin@cluster", userName: "admin", clusterName: "cluster"},
	})
	f3 := writeKubeconfig(t, dirRome, "config", "admin@cluster", []kubeEntry{
		{ctxName: "admin@cluster", userName: "admin", clusterName: "cluster"},
	})

	registry, _ := buildContextRegistry([]string{f1, f2, f3})

	if len(registry) != 3 {
		t.Fatalf("registry size: got %d, want 3", len(registry))
	}
	// First file keeps the original name.
	if e, ok := registry["admin@cluster"]; !ok || e.SourceFile != f1 {
		t.Errorf("'admin@cluster' should resolve to f1")
	}
	// Subsequent collisions use the leading-dot-stripped parent dir name.
	if e, ok := registry["admin@cluster (kube-cluster-london)"]; !ok || e.SourceFile != f2 {
		names := []string{}
		for n := range registry {
			names = append(names, n)
		}
		sort.Strings(names)
		t.Errorf("'admin@cluster (kube-cluster-london)' should resolve to f2; registry has: %v", names)
	}
	if e, ok := registry["admin@cluster (kube-cluster-rome)"]; !ok || e.SourceFile != f3 {
		names := []string{}
		for n := range registry {
			names = append(names, n)
		}
		sort.Strings(names)
		t.Errorf("'admin@cluster (kube-cluster-rome)' should resolve to f3; registry has: %v", names)
	}
}

func TestKubeconfigSourceLabel(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		// Generic filenames -> parent dir, leading dot stripped.
		{"/home/u/.kube-cluster-paris/config", "kube-cluster-paris"},
		{"/home/u/.kube/config", "kube"},
		{"/home/u/clusters/prod/kubeconfig", "prod"},
		// Meaningful filenames -> filename without extension.
		{"/home/u/.kube/configs/prod.yaml", "prod"},
		{"/home/u/clusters/staging.yml", "staging"},
		{"/tmp/eks-east.kubeconfig.yaml", "eks-east.kubeconfig"},
		// Edge: file at root with generic name — parent is "/", fall through to base.
		{"/config", "config"},
		// Relative paths — SourceFile is normalised to absolute upstream,
		// but pin the helper's behaviour so a future drift doesn't sneak
		// silently past callers like aggregateExecPluginCommands.
		{"config", "config"},                                // no parent
		{"./config", "config"},                              // current-dir parent rejected
		{"kube-cluster-paris/config", "kube-cluster-paris"}, // relative parent honored
	}
	for _, c := range cases {
		if got := kubeconfigSourceLabel(c.path); got != c.want {
			t.Errorf("kubeconfigSourceLabel(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

// In multi-kubeconfig mode, every ContextInfo returned from
// GetAvailableContexts must carry the source label of the file it came
// from, even when context names don't collide. The frontend uses this
// to render a "from kubeconfig X" affordance, and the contract is
// invisible from the registry-level tests above.
func TestGetAvailableContexts_PopulatesSourceInMultiFileMode(t *testing.T) {
	parisDir := filepath.Join(t.TempDir(), ".kube-cluster-paris")
	londonDir := filepath.Join(t.TempDir(), ".kube-cluster-london")
	for _, d := range []string{parisDir, londonDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	f1 := writeKubeconfig(t, parisDir, "config", "ctx-paris", []kubeEntry{
		{ctxName: "ctx-paris", userName: "u1", clusterName: "c1"},
	})
	f2 := writeKubeconfig(t, londonDir, "config", "ctx-london", []kubeEntry{
		{ctxName: "ctx-london", userName: "u2", clusterName: "c2"},
	})

	clientMu.Lock()
	prevRegistry := contextRegistry
	prevConfigs := perFileConfigs
	prevMtimes := perFileMtimes
	prevPaths := kubeconfigPaths
	prevName := contextName
	registry, fileConfigs := buildContextRegistry([]string{f1, f2})
	mtimes := make(map[string]time.Time, 2)
	for _, p := range []string{f1, f2} {
		if info, err := os.Stat(p); err == nil {
			mtimes[p] = info.ModTime()
		}
	}
	contextRegistry = registry
	perFileConfigs = fileConfigs
	perFileMtimes = mtimes
	kubeconfigPaths = []string{f1, f2}
	contextName = "ctx-paris"
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		contextRegistry = prevRegistry
		perFileConfigs = prevConfigs
		perFileMtimes = prevMtimes
		kubeconfigPaths = prevPaths
		contextName = prevName
		clientMu.Unlock()
	})

	contexts, err := GetAvailableContexts()
	if err != nil {
		t.Fatalf("GetAvailableContexts: %v", err)
	}
	bySource := map[string]string{} // qName -> source
	for _, c := range contexts {
		bySource[c.Name] = c.Source
	}
	if got, want := bySource["ctx-paris"], "kube-cluster-paris"; got != want {
		t.Errorf("ctx-paris Source: got %q, want %q (all: %v)", got, want, bySource)
	}
	if got, want := bySource["ctx-london"], "kube-cluster-london"; got != want {
		t.Errorf("ctx-london Source: got %q, want %q (all: %v)", got, want, bySource)
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

// SKY-834 bug 52: kubeconfig files rewritten or deleted on disk
// after Radar startup kept showing their old contexts in the
// cluster dropdown — the in-memory registry was built once in
// setupIsolatedLoad and never refreshed in multi-file mode. The
// user saw "junk clusters" that errored out on switch.
//
// refreshContextRegistry is the surgical fix: same per-file
// isolation as buildContextRegistry, but driven by mtime so it
// only re-parses files that actually changed.

func loadFixture(t *testing.T, paths []string) (
	map[string]contextEntry,
	map[string]*clientcmdapi.Config,
	map[string]time.Time,
) {
	t.Helper()
	registry, fileConfigs := buildContextRegistry(paths)
	mtimes := make(map[string]time.Time, len(paths))
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat fixture %s: %v", p, err)
		}
		mtimes[p] = info.ModTime()
	}
	return registry, fileConfigs, mtimes
}

func TestRefreshContextRegistry_DropsRemovedFile(t *testing.T) {
	// CAPI scenario: a file that was watched at startup is removed
	// from disk (the cluster was destroyed and the controller
	// cleaned up). All registry entries pointing at that file MUST
	// disappear from the dropdown on the next refresh, otherwise
	// the user sees a junk row that errors on switch.
	dir := t.TempDir()
	f1 := writeKubeconfig(t, dir, "alive.yaml", "ctx-alive", []kubeEntry{
		{ctxName: "ctx-alive", userName: "u", clusterName: "c1"},
	})
	f2 := writeKubeconfig(t, dir, "doomed.yaml", "ctx-doomed", []kubeEntry{
		{ctxName: "ctx-doomed", userName: "u", clusterName: "c2"},
	})

	registry, fileConfigs, mtimes := loadFixture(t, []string{f1, f2})
	if _, ok := registry["ctx-doomed"]; !ok {
		t.Fatalf("setup: expected ctx-doomed in registry, got %v", keysOf(registry))
	}

	if err := os.Remove(f2); err != nil {
		t.Fatalf("remove fixture: %v", err)
	}

	newRegistry, newFileConfigs, newMtimes, changed := refreshContextRegistry(registry, fileConfigs, mtimes)
	if !changed {
		t.Errorf("expected refresh to report a change after deleting %s", filepath.Base(f2))
	}
	if _, ok := newRegistry["ctx-doomed"]; ok {
		t.Errorf("ctx-doomed still in registry after file removed: %v", keysOf(newRegistry))
	}
	if _, ok := newRegistry["ctx-alive"]; !ok {
		t.Errorf("ctx-alive should still be in registry: %v", keysOf(newRegistry))
	}
	if _, ok := newFileConfigs[f2]; ok {
		t.Errorf("perFileConfigs still has entry for removed file %s", filepath.Base(f2))
	}
	if _, ok := newMtimes[f2]; ok {
		t.Errorf("perFileMtimes still has entry for removed file %s", filepath.Base(f2))
	}
	// Original maps must be untouched — refresh returns fresh maps so
	// snapshot readers (SwitchContext, WriteKubeconfigForCurrentContext)
	// can iterate the captured maps without locking.
	if _, ok := registry["ctx-doomed"]; !ok {
		t.Errorf("input registry was mutated; expected immutability")
	}
	if _, ok := fileConfigs[f2]; !ok {
		t.Errorf("input fileConfigs was mutated; expected immutability")
	}
	if _, ok := mtimes[f2]; !ok {
		t.Errorf("input mtimes was mutated; expected immutability")
	}
}

func TestRefreshContextRegistry_DropsContextRemovedFromFile(t *testing.T) {
	// `kubectl config delete-context` rewrites the kubeconfig in
	// place: same file, different mtime, fewer contexts. The
	// removed context MUST disappear from the dropdown.
	dir := t.TempDir()
	f1 := writeKubeconfig(t, dir, "two.yaml", "ctx-keep", []kubeEntry{
		{ctxName: "ctx-keep", userName: "u", clusterName: "c1"},
		{ctxName: "ctx-delete", userName: "u", clusterName: "c2"},
	})

	registry, fileConfigs, mtimes := loadFixture(t, []string{f1})
	if _, ok := registry["ctx-delete"]; !ok {
		t.Fatalf("setup: expected ctx-delete in registry")
	}

	rewriteKubeconfig(t, f1, []kubeEntry{
		{ctxName: "ctx-keep", userName: "u", clusterName: "c1"},
	})

	newRegistry, _, _, changed := refreshContextRegistry(registry, fileConfigs, mtimes)
	if !changed {
		t.Errorf("expected refresh to report a change after rewriting %s", filepath.Base(f1))
	}
	if _, ok := newRegistry["ctx-delete"]; ok {
		t.Errorf("ctx-delete still in registry after rewrite: %v", keysOf(newRegistry))
	}
	if _, ok := newRegistry["ctx-keep"]; !ok {
		t.Errorf("ctx-keep should still be in registry: %v", keysOf(newRegistry))
	}
	if _, ok := registry["ctx-delete"]; !ok {
		t.Errorf("input registry was mutated; expected immutability")
	}
}

func TestRefreshContextRegistry_PicksUpNewContextInSameFile(t *testing.T) {
	// `kubectl config set-context foo` adds a new entry to an
	// existing file. The new context should appear after refresh
	// without needing a Radar restart.
	dir := t.TempDir()
	f1 := writeKubeconfig(t, dir, "one.yaml", "ctx-original", []kubeEntry{
		{ctxName: "ctx-original", userName: "u", clusterName: "c1"},
	})
	registry, fileConfigs, mtimes := loadFixture(t, []string{f1})

	rewriteKubeconfig(t, f1, []kubeEntry{
		{ctxName: "ctx-original", userName: "u", clusterName: "c1"},
		{ctxName: "ctx-new", userName: "u", clusterName: "c2"},
	})

	newRegistry, _, _, changed := refreshContextRegistry(registry, fileConfigs, mtimes)
	if !changed {
		t.Errorf("expected refresh to report a change after add")
	}
	if _, ok := newRegistry["ctx-new"]; !ok {
		t.Errorf("ctx-new not picked up after refresh: %v", keysOf(newRegistry))
	}
	if _, ok := newRegistry["ctx-original"]; !ok {
		t.Errorf("ctx-original disappeared from registry: %v", keysOf(newRegistry))
	}
}

func TestRefreshContextRegistry_NoOpWhenNothingChanged(t *testing.T) {
	dir := t.TempDir()
	f1 := writeKubeconfig(t, dir, "stable.yaml", "ctx-a", []kubeEntry{
		{ctxName: "ctx-a", userName: "u", clusterName: "c1"},
	})
	registry, fileConfigs, mtimes := loadFixture(t, []string{f1})
	before := keysOf(registry)

	newRegistry, _, _, changed := refreshContextRegistry(registry, fileConfigs, mtimes)
	if changed {
		t.Errorf("expected no change on stable disk state, got changed=true")
	}
	after := keysOf(newRegistry)
	sort.Strings(before)
	sort.Strings(after)
	if len(before) != len(after) {
		t.Errorf("registry shape changed during no-op refresh: %v vs %v", before, after)
	}
}

func TestRefreshContextRegistry_NilMtimeMapNoOp(t *testing.T) {
	// Defensive: if perFileMtimes is nil (e.g. a future code path
	// forgets to initialise it after MergeAndSwitchContext promoted
	// single-file → isolated-load), refresh must not panic. The
	// production path already nil-inits before calling, but the
	// helper is exported and we want depth.
	dir := t.TempDir()
	f1 := writeKubeconfig(t, dir, "a.yaml", "ctx-a", []kubeEntry{
		{ctxName: "ctx-a", userName: "u", clusterName: "c1"},
	})
	registry, fileConfigs := buildContextRegistry([]string{f1})
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("refresh panicked on nil mtimes: %v", r)
		}
	}()
	var nilMtimes map[string]time.Time
	newRegistry, _, _, changed := refreshContextRegistry(registry, fileConfigs, nilMtimes)
	if changed {
		t.Errorf("refresh on nil mtimes should report no-op (changed=false)")
	}
	// Registry must be untouched on the nil-mtimes no-op path.
	if _, ok := newRegistry["ctx-a"]; !ok {
		t.Errorf("nil-mtimes refresh should not have modified the registry")
	}
}

func TestRefreshContextRegistry_SeedsByFileFromMtimesEvenWhenRegistryEmptyForFile(t *testing.T) {
	// Regression: if every context in a file got removed by a
	// previous refresh, the file path stayed in fileMtimes but
	// wasn't in the registry — so the next refresh's byFile only
	// included paths still represented in the registry, and the
	// emptied file would never be re-stat'd. Any new contexts
	// later added to that file would be invisible until restart.
	dir := t.TempDir()
	f1 := writeKubeconfig(t, dir, "a.yaml", "ctx-a", []kubeEntry{
		{ctxName: "ctx-a", userName: "u", clusterName: "c1"},
	})
	registry, fileConfigs := buildContextRegistry([]string{f1})
	mtimes := map[string]time.Time{}
	for _, p := range []string{f1} {
		if info, err := os.Stat(p); err == nil {
			mtimes[p] = info.ModTime()
		}
	}
	// Simulate "all contexts in f1 were removed by a prior refresh"
	// while leaving the mtime cache intact.
	delete(registry, "ctx-a")
	if len(registry) != 0 {
		t.Fatalf("setup: expected empty registry, got %v", registry)
	}
	// Wait, then rewrite f1 to add a brand-new context. The mtime
	// cache will still hold the OLD timestamp, so refresh should
	// see the file as changed and rebuild it.
	rewriteKubeconfig(t, f1, []kubeEntry{
		{ctxName: "ctx-a-fresh", userName: "u", clusterName: "c1"},
		{ctxName: "ctx-b-fresh", userName: "u", clusterName: "c1"},
	})
	newRegistry, _, _, _ := refreshContextRegistry(registry, fileConfigs, mtimes)
	if _, ok := newRegistry["ctx-a-fresh"]; !ok {
		t.Errorf("refresh should have picked up ctx-a-fresh; got %v", newRegistry)
	}
	if _, ok := newRegistry["ctx-b-fresh"]; !ok {
		t.Errorf("refresh should have picked up ctx-b-fresh; got %v", newRegistry)
	}
}

func TestRefreshContextRegistry_BadParseDoesNotDropExisting(t *testing.T) {
	// Defensive case: user is mid-edit and saved a syntactically
	// broken kubeconfig (mtime moved, parse fails). We deliberately
	// keep the previous registry entries — silently pruning the
	// dropdown while the user saves would be more confusing than a
	// momentarily stale entry.
	dir := t.TempDir()
	f1 := writeKubeconfig(t, dir, "broken.yaml", "ctx-a", []kubeEntry{
		{ctxName: "ctx-a", userName: "u", clusterName: "c1"},
	})
	registry, fileConfigs, mtimes := loadFixture(t, []string{f1})

	if err := os.WriteFile(f1, []byte("not: valid: yaml: at: all"), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	newRegistry, _, _, _ := refreshContextRegistry(registry, fileConfigs, mtimes)
	if _, ok := newRegistry["ctx-a"]; !ok {
		t.Errorf("ctx-a was dropped on parse failure; expected to keep it: %v", keysOf(newRegistry))
	}
}

func keysOf(m map[string]contextEntry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func rewriteKubeconfig(t *testing.T, path string, entries []kubeEntry) {
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
				Server:                "https://" + e.clusterName,
				InsecureSkipTLSVerify: true,
			}
		}
		if _, ok := cfg.AuthInfos[e.userName]; !ok {
			cfg.AuthInfos[e.userName] = &clientcmdapi.AuthInfo{Token: "fake-token-for-" + e.userName}
		}
	}
	data, err := clientcmd.Write(*cfg)
	if err != nil {
		t.Fatalf("rewrite serialize: %v", err)
	}
	// Force a different mtime even if the test writes within the
	// same filesystem-resolution tick (HFS+ is 1s).
	time.Sleep(15 * time.Millisecond)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("rewrite %s: %v", path, err)
	}
	now := time.Now()
	if err := os.Chtimes(path, now, now); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

// Regression: GetAvailableContexts triggers refreshContextRegistry under
// the write lock; concurrent callers that snapshot the live maps under
// RLock and iterate after unlocking must not race with the refresh. The
// previous shape mutated maps in place, so a refresh during another
// caller's post-RLock iteration triggered Go's "concurrent map read and
// map write" panic. The fix swaps maps atomically rather than mutating —
// this test runs both patterns concurrently with the race detector.
func TestGetAvailableContexts_ConcurrentRefreshAndSnapshotIterate(t *testing.T) {
	dir := t.TempDir()
	f1 := writeKubeconfig(t, dir, "a.yaml", "ctx-a", []kubeEntry{
		{ctxName: "ctx-a", userName: "u", clusterName: "c1"},
	})
	f2 := writeKubeconfig(t, dir, "b.yaml", "ctx-b", []kubeEntry{
		{ctxName: "ctx-b", userName: "u", clusterName: "c2"},
	})

	// Stand up the package globals to look like multi-file isolated-load
	// mode. Restore them after the test so other tests in this package
	// don't see a polluted state.
	clientMu.Lock()
	prevRegistry := contextRegistry
	prevConfigs := perFileConfigs
	prevMtimes := perFileMtimes
	prevPaths := kubeconfigPaths
	prevName := contextName
	registry, fileConfigs := buildContextRegistry([]string{f1, f2})
	mtimes := make(map[string]time.Time, 2)
	for _, p := range []string{f1, f2} {
		if info, err := os.Stat(p); err == nil {
			mtimes[p] = info.ModTime()
		}
	}
	contextRegistry = registry
	perFileConfigs = fileConfigs
	perFileMtimes = mtimes
	kubeconfigPaths = []string{f1, f2}
	contextName = "ctx-a"
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		contextRegistry = prevRegistry
		perFileConfigs = prevConfigs
		perFileMtimes = prevMtimes
		kubeconfigPaths = prevPaths
		contextName = prevName
		clientMu.Unlock()
	})

	const iterations = 200
	const writers = 4
	const snapshotters = 4
	var wg sync.WaitGroup
	var stop atomic.Bool

	// Writer goroutines: rewrite kubeconfig files on disk so the next
	// GetAvailableContexts call observes a changed mtime and re-parses,
	// exercising the refresh path that previously mutated maps in place.
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < iterations && !stop.Load(); j++ {
				target := f1
				if j%2 == 1 {
					target = f2
				}
				ctxBase := "ctx-a"
				if target == f2 {
					ctxBase = "ctx-b"
				}
				rewriteKubeconfig(t, target, []kubeEntry{
					{ctxName: ctxBase, userName: "u", clusterName: "c1"},
				})
				if _, err := GetAvailableContexts(); err != nil {
					t.Errorf("GetAvailableContexts: %v", err)
					stop.Store(true)
					return
				}
			}
		}(i)
	}

	// Snapshotter goroutines: replicate SwitchContext /
	// WriteKubeconfigForCurrentContext's bare-reference snapshot pattern,
	// then iterate after releasing the lock. Without map immutability
	// this races with refresh and panics.
	for i := 0; i < snapshotters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations && !stop.Load(); j++ {
				clientMu.RLock()
				snapReg := contextRegistry
				snapConfigs := perFileConfigs
				clientMu.RUnlock()

				// Iterate outside the lock — same shape as SwitchContext.
				for qName, entry := range snapReg {
					_ = qName
					cfg, ok := snapConfigs[entry.SourceFile]
					if !ok {
						continue
					}
					for name := range cfg.Contexts {
						_ = name
					}
				}
			}
		}()
	}

	wg.Wait()
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
