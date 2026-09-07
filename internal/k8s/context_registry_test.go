package k8s

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/skyhook-io/radar/internal/errorlog"
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
	errorlog.Reset()
	t.Cleanup(errorlog.Reset)
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
	entries := errorlog.GetEntries()
	if len(entries) != 1 || !strings.Contains(entries[0].Message, "saved namespace and integration preferences") {
		t.Fatalf("collision warnings = %+v", entries)
	}
	if strings.Contains(entries[0].Message, "my-ctx") {
		t.Fatalf("shareable collision warning leaked context names: %q", entries[0].Message)
	}
}

func TestRecordContextQualificationsDeduplicatesFiles(t *testing.T) {
	errorlog.Reset()
	t.Cleanup(errorlog.Reset)
	recordContextQualifications([]string{"team-b.yaml", "team-b.yaml", "team-a.yaml"})

	entries := errorlog.GetEntries()
	if len(entries) != 1 {
		t.Fatalf("collision warnings = %+v", entries)
	}
	message := entries[0].Message
	if !strings.Contains(message, "3 context(s)") || !strings.Contains(message, "[team-a.yaml team-b.yaml]") {
		t.Fatalf("collision warning = %q", message)
	}
	if strings.Count(message, "team-b.yaml") != 1 {
		t.Fatalf("collision warning repeated a source filename: %q", message)
	}
}

// Three-way collision: same context name across three files, all sharing the
// same basename (two with different extensions). Third should fall back to
// the numeric-suffix form.
func TestBuildContextRegistry_ThreeWayCollision(t *testing.T) {
	errorlog.Reset()
	t.Cleanup(errorlog.Reset)
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
	entries := errorlog.GetEntries()
	if len(entries) != 1 || !strings.Contains(entries[0].Message, "2 context(s)") {
		t.Fatalf("collision warnings = %+v", entries)
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

func TestGetAvailableContexts_OneDirectoryFileUsesRegistry(t *testing.T) {
	dir := t.TempDir()
	path := writeKubeconfig(t, dir, "only.yaml", "only", []kubeEntry{
		{ctxName: "only", userName: "user", clusterName: "cluster"},
	})
	registry, fileConfigs := buildContextRegistry([]string{path})

	clientMu.Lock()
	prevRegistry := contextRegistry
	prevConfigs := perFileConfigs
	prevMtimes := perFileMtimes
	prevPaths := kubeconfigPaths
	prevPath := kubeconfigPath
	prevName := contextName
	contextRegistry = registry
	perFileConfigs = fileConfigs
	perFileMtimes = map[string]time.Time{}
	kubeconfigPaths = []string{path}
	kubeconfigPath = ""
	contextName = "only"
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		contextRegistry = prevRegistry
		perFileConfigs = prevConfigs
		perFileMtimes = prevMtimes
		kubeconfigPaths = prevPaths
		kubeconfigPath = prevPath
		contextName = prevName
		clientMu.Unlock()
	})

	contexts, err := GetAvailableContexts()
	if err != nil {
		t.Fatalf("GetAvailableContexts: %v", err)
	}
	if len(contexts) != 1 || contexts[0].Name != "only" || contexts[0].OriginalName != "only" || contexts[0].Source != "only" {
		t.Fatalf("contexts = %+v", contexts)
	}
	sourceFile, inFileName, ok := GetContextSource("only")
	if !ok || sourceFile != path || inFileName != "only" {
		t.Fatalf("context source = (%q, %q, %t)", sourceFile, inFileName, ok)
	}
}

func TestGetContextSourceDirectModeUsesResolvedPath(t *testing.T) {
	clientMu.Lock()
	prevRegistry := contextRegistry
	prevPath := kubeconfigPath
	prevName := contextName
	prevActiveFile := activeSourceFile
	prevActiveName := activeSourceName
	contextRegistry = nil
	kubeconfigPath = "/resolved/home/.kube/config"
	contextName = "prod"
	activeSourceFile = ""
	activeSourceName = ""
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		contextRegistry = prevRegistry
		kubeconfigPath = prevPath
		contextName = prevName
		activeSourceFile = prevActiveFile
		activeSourceName = prevActiveName
		clientMu.Unlock()
	})

	sourceFile, inFileName, ok := GetContextSource("prod")
	if !ok || sourceFile != "/resolved/home/.kube/config" || inFileName != "prod" {
		t.Fatalf("context source = (%q, %q, %t)", sourceFile, inFileName, ok)
	}
}

func TestGetContextSourceDirectModeResolvesNonCurrentContext(t *testing.T) {
	dir := t.TempDir()
	path := writeKubeconfig(t, dir, "config", "prod", []kubeEntry{
		{ctxName: "prod", userName: "u1", clusterName: "c1"},
		{ctxName: "jp", userName: "u2", clusterName: "c2"},
	})

	clientMu.Lock()
	prevRegistry := contextRegistry
	prevPath := kubeconfigPath
	prevName := contextName
	prevActiveFile := activeSourceFile
	prevActiveName := activeSourceName
	contextRegistry = nil
	kubeconfigPath = path
	contextName = "prod"
	activeSourceFile = ""
	activeSourceName = ""
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		contextRegistry = prevRegistry
		kubeconfigPath = prevPath
		contextName = prevName
		activeSourceFile = prevActiveFile
		activeSourceName = prevActiveName
		clientMu.Unlock()
	})

	sourceFile, inFileName, ok := GetContextSource("jp")
	if !ok || sourceFile != path || inFileName != "jp" {
		t.Fatalf("context source = (%q, %q, %t)", sourceFile, inFileName, ok)
	}
}

func TestGetContextSourceDirectModeConcurrentContextChange(t *testing.T) {
	dir := t.TempDir()
	path := writeKubeconfig(t, dir, "config", "prod", []kubeEntry{
		{ctxName: "prod", userName: "u1", clusterName: "c1"},
	})

	clientMu.Lock()
	prevRegistry := contextRegistry
	prevPath := kubeconfigPath
	prevName := contextName
	prevActiveFile := activeSourceFile
	prevActiveName := activeSourceName
	contextRegistry = nil
	kubeconfigPath = path
	contextName = "prod"
	activeSourceFile = ""
	activeSourceName = ""
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		contextRegistry = prevRegistry
		kubeconfigPath = prevPath
		contextName = prevName
		activeSourceFile = prevActiveFile
		activeSourceName = prevActiveName
		clientMu.Unlock()
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			clientMu.Lock()
			if i%2 == 0 {
				contextName = "prod"
			} else {
				contextName = "other"
			}
			clientMu.Unlock()
		}
	}()
	for i := 0; i < 100; i++ {
		GetContextSource("missing")
	}
	<-done
}

func TestActiveContextSourceSurvivesRegistryRefresh(t *testing.T) {
	dir := t.TempDir()
	primary := writeKubeconfig(t, dir, "primary.yaml", "primary", []kubeEntry{
		{ctxName: "primary", userName: "u1", clusterName: "c1"},
	})
	secondary := writeKubeconfig(t, dir, "secondary.yaml", "prod", []kubeEntry{
		{ctxName: "prod", userName: "u2", clusterName: "c2"},
	})
	secondaryConfig, err := clientcmd.LoadFromFile(secondary)
	if err != nil {
		t.Fatalf("load secondary: %v", err)
	}
	secondaryConfig.Clusters["c2"].InsecureSkipTLSVerify = false
	secondaryConfig.Clusters["c2"].CertificateAuthority = "ca.crt"
	if err := clientcmd.WriteToFile(*secondaryConfig, secondary); err != nil {
		t.Fatalf("add relative certificate authority: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ca.crt"), []byte("test-ca"), 0o600); err != nil {
		t.Fatalf("write certificate authority: %v", err)
	}
	registry, fileConfigs, mtimes := loadFixture(t, []string{primary, secondary})
	activeConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: secondary},
		&clientcmd.ConfigOverrides{},
	).RawConfig()
	if err != nil {
		t.Fatalf("load active config: %v", err)
	}

	clientMu.Lock()
	prevRegistry := contextRegistry
	prevConfigs := perFileConfigs
	prevMtimes := perFileMtimes
	prevPaths := kubeconfigPaths
	prevPath := kubeconfigPath
	prevName := contextName
	prevMode := kubeconfigMode
	prevContextCount := totalContextCount
	prevDirectoryCount := kubeconfigDirectoryFileCount
	prevActiveFile := activeSourceFile
	prevActiveName := activeSourceName
	prevActiveConfig := activeSourceConfig
	contextRegistry = registry
	perFileConfigs = fileConfigs
	perFileMtimes = mtimes
	kubeconfigPaths = []string{primary, secondary}
	kubeconfigPath = ""
	contextName = "prod"
	kubeconfigMode = "multi-source"
	totalContextCount = 2
	kubeconfigDirectoryFileCount = 1
	activeSourceFile = secondary
	activeSourceName = "prod"
	activeSourceConfig = activeConfig.DeepCopy()
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		contextRegistry = prevRegistry
		perFileConfigs = prevConfigs
		perFileMtimes = prevMtimes
		kubeconfigPaths = prevPaths
		kubeconfigPath = prevPath
		contextName = prevName
		kubeconfigMode = prevMode
		totalContextCount = prevContextCount
		kubeconfigDirectoryFileCount = prevDirectoryCount
		activeSourceFile = prevActiveFile
		activeSourceName = prevActiveName
		activeSourceConfig = prevActiveConfig
		clientMu.Unlock()
	})

	rotated := fileConfigs[secondary].DeepCopy()
	rotated.AuthInfos["u2"].Token = "rotated-token"
	if err := clientcmd.WriteToFile(*rotated, secondary); err != nil {
		t.Fatalf("rotate secondary credentials: %v", err)
	}
	rotatedPath, err := WriteKubeconfigForCurrentContext()
	if err != nil {
		t.Fatalf("WriteKubeconfigForCurrentContext after credential rotation: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(rotatedPath) })
	rotatedWritten, err := clientcmd.LoadFromFile(rotatedPath)
	if err != nil {
		t.Fatalf("load generated rotated kubeconfig: %v", err)
	}
	if got := rotatedWritten.AuthInfos["u2"].Token; got != "rotated-token" {
		t.Fatalf("generated credential = %q, want rotated-token", got)
	}

	repointed := rotated.DeepCopy()
	repointed.Clusters["c2"].Server = "https://replacement.example"
	if err := clientcmd.WriteToFile(*repointed, secondary); err != nil {
		t.Fatalf("repoint secondary target: %v", err)
	}
	repointedPath, err := WriteKubeconfigForCurrentContext()
	if err != nil {
		t.Fatalf("WriteKubeconfigForCurrentContext after target change: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(repointedPath) })
	repointedWritten, err := clientcmd.LoadFromFile(repointedPath)
	if err != nil {
		t.Fatalf("load generated target-safe kubeconfig: %v", err)
	}
	if got := repointedWritten.Clusters["c2"].Server; got != "https://c2" {
		t.Fatalf("generated target = %q, want active target https://c2", got)
	}

	if err := os.Remove(secondary); err != nil {
		t.Fatalf("remove secondary: %v", err)
	}
	contexts, err := GetAvailableContexts()
	if err != nil {
		t.Fatalf("GetAvailableContexts: %v", err)
	}
	for _, context := range contexts {
		if context.Name == "prod" {
			t.Fatalf("deleted active source remains in switcher: %+v", contexts)
		}
	}
	summary := GetKubeconfigSummary()
	if summary.FileCount != 1 || summary.DirectoryFileCount != 0 || summary.ContextCount != 1 {
		t.Fatalf("summary after deletion = %+v", summary)
	}
	if sourceFile, inFileName, ok := GetContextSource("prod"); !ok || sourceFile != secondary || inFileName != "prod" {
		t.Fatalf("active source after deletion = (%q, %q, %t)", sourceFile, inFileName, ok)
	}
	tmpPath, err := WriteKubeconfigForCurrentContext()
	if err != nil {
		t.Fatalf("WriteKubeconfigForCurrentContext: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(tmpPath) })
	written, err := clientcmd.LoadFromFile(tmpPath)
	if err != nil {
		t.Fatalf("load generated kubeconfig: %v", err)
	}
	if written.CurrentContext != "prod" || written.Contexts["prod"] == nil || written.Clusters["c2"] == nil {
		t.Fatalf("generated active snapshot = %+v", written)
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
	qName, entry, ok := pickInitialContext(paths, registry, fileConfigs, ContextRef{})
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
	qName, _, ok := pickInitialContext(paths, registry, fileConfigs, ContextRef{})
	if !ok {
		t.Fatal("expected initial context")
	}
	if qName != "from-second" {
		t.Errorf("expected 'from-second', got %q", qName)
	}
}

func TestPickInitialContext_UsesLaterCurrentWhenPrimaryHasNone(t *testing.T) {
	dir := t.TempDir()
	primary := writeKubeconfig(t, dir, "primary.yaml", "", []kubeEntry{
		{ctxName: "primary", userName: "u1", clusterName: "c1"},
	})
	additional := writeKubeconfig(t, dir, "additional.yaml", "additional", []kubeEntry{
		{ctxName: "additional", userName: "u2", clusterName: "c2"},
	})

	paths := []string{primary, additional}
	registry, fileConfigs := buildContextRegistry(paths)
	qName, entry, ok := pickInitialContext(paths, registry, fileConfigs, ContextRef{})
	if !ok {
		t.Fatal("expected initial context")
	}
	if qName != "additional" || entry.SourceFile != additional {
		t.Fatalf("initial context = %q from %q, want additional from %q", qName, entry.SourceFile, additional)
	}
}

func TestPickInitialContext_NoCurrentContextAnywhere(t *testing.T) {
	dir := t.TempDir()
	f1 := writeKubeconfig(t, dir, "first.yaml", "", []kubeEntry{
		{ctxName: "only-ctx", userName: "u1", clusterName: "c1"},
	})

	paths := []string{f1}
	registry, fileConfigs := buildContextRegistry(paths)
	qName, _, ok := pickInitialContext(paths, registry, fileConfigs, ContextRef{})
	if !ok {
		t.Fatal("expected initial context from any-ctx fallback")
	}
	if qName != "only-ctx" {
		t.Errorf("expected 'only-ctx', got %q", qName)
	}
}

func TestPickInitialContext_NoCurrentContextUsesAlphabeticalFallback(t *testing.T) {
	dir := t.TempDir()
	f1 := writeKubeconfig(t, dir, "first.yaml", "", []kubeEntry{
		{ctxName: "zeta", userName: "u1", clusterName: "c1"},
		{ctxName: "alpha", userName: "u2", clusterName: "c2"},
	})

	registry, fileConfigs := buildContextRegistry([]string{f1})
	qName, _, ok := pickInitialContext([]string{f1}, registry, fileConfigs, ContextRef{})
	if !ok || qName != "alpha" {
		t.Fatalf("fallback context = %q, found=%t; want alpha", qName, ok)
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

	newRegistry, newFileConfigs, newMtimes, changed := refreshContextRegistry(registry, fileConfigs, mtimes, []string{f1, f2})
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

func TestRefreshContextRegistry_RestoresRemovedFile(t *testing.T) {
	dir := t.TempDir()
	f1 := writeKubeconfig(t, dir, "restored.yaml", "ctx-old", []kubeEntry{
		{ctxName: "ctx-old", userName: "u", clusterName: "c1"},
	})
	registry, fileConfigs, mtimes := loadFixture(t, []string{f1})
	if err := os.Remove(f1); err != nil {
		t.Fatalf("remove fixture: %v", err)
	}

	registry, fileConfigs, mtimes, changed := refreshContextRegistry(registry, fileConfigs, mtimes, []string{f1})
	if !changed || len(registry) != 0 {
		t.Fatalf("refresh after removal = changed %t, registry %v", changed, keysOf(registry))
	}
	_, _, _, changed = refreshContextRegistry(registry, fileConfigs, mtimes, []string{f1})
	if changed {
		t.Fatal("missing source should remain a no-op until restored")
	}

	rewriteKubeconfig(t, f1, []kubeEntry{
		{ctxName: "ctx-restored", userName: "u", clusterName: "c2"},
	})
	registry, fileConfigs, mtimes, changed = refreshContextRegistry(registry, fileConfigs, mtimes, []string{f1})
	if !changed {
		t.Fatal("restored source should report a registry change")
	}
	entry, ok := registry["ctx-restored"]
	if !ok || entry.SourceFile != f1 || entry.InFileName != "ctx-restored" {
		t.Fatalf("restored context = %+v, found=%t", entry, ok)
	}
	if fileConfigs[f1] == nil || mtimes[f1].IsZero() {
		t.Fatalf("restored source caches missing: config=%t mtime=%v", fileConfigs[f1] != nil, mtimes[f1])
	}
}

func TestRefreshContextRegistry_PreservesQualifiedSurvivorIdentity(t *testing.T) {
	errorlog.Reset()
	t.Cleanup(errorlog.Reset)
	dir := t.TempDir()
	primary := writeKubeconfig(t, dir, "primary.yaml", "prod", []kubeEntry{
		{ctxName: "prod", userName: "u1", clusterName: "c1"},
	})
	secondary := writeKubeconfig(t, dir, "secondary.yaml", "prod", []kubeEntry{
		{ctxName: "prod", userName: "u2", clusterName: "c2"},
	})

	registry, fileConfigs, mtimes := loadFixture(t, []string{primary, secondary})
	if _, ok := registry["prod (secondary)"]; !ok {
		t.Fatalf("setup registry = %v", keysOf(registry))
	}
	if err := os.Remove(primary); err != nil {
		t.Fatalf("remove primary fixture: %v", err)
	}

	newRegistry, _, _, changed := refreshContextRegistry(registry, fileConfigs, mtimes, []string{primary, secondary})
	if !changed {
		t.Fatal("expected refresh to report the removed primary source")
	}
	if _, ok := newRegistry["prod"]; ok {
		t.Fatalf("removed primary context remains: %v", keysOf(newRegistry))
	}
	entry, ok := newRegistry["prod (secondary)"]
	if !ok || entry.InFileName != "prod" || entry.SourceFile != secondary {
		t.Fatalf("qualified survivor = %+v, found=%t", entry, ok)
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

	newRegistry, _, _, changed := refreshContextRegistry(registry, fileConfigs, mtimes, []string{f1})
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

	newRegistry, _, _, changed := refreshContextRegistry(registry, fileConfigs, mtimes, []string{f1})
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

func TestRefreshContextRegistry_QualificationIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	first := writeKubeconfig(t, dir, "a.yaml", "old-a", []kubeEntry{
		{ctxName: "old-a", userName: "u1", clusterName: "c1"},
	})
	second := writeKubeconfig(t, dir, "b.yaml", "old-b", []kubeEntry{
		{ctxName: "old-b", userName: "u2", clusterName: "c2"},
	})
	registry, fileConfigs := buildContextRegistry([]string{first, second})
	rewriteKubeconfig(t, first, []kubeEntry{{ctxName: "prod", userName: "u1", clusterName: "c1"}})
	rewriteKubeconfig(t, second, []kubeEntry{{ctxName: "prod", userName: "u2", clusterName: "c2"}})

	for i := 0; i < 20; i++ {
		mtimes := map[string]time.Time{first: {}, second: {}}
		refreshed, _, _, changed := refreshContextRegistry(registry, fileConfigs, mtimes, []string{second, first})
		if !changed {
			t.Fatalf("iteration %d: expected changed registry", i)
		}
		if got := refreshed["prod"].SourceFile; got != second {
			t.Fatalf("iteration %d: bare prod source = %q, want %q", i, got, second)
		}
		if got := refreshed["prod (a)"].SourceFile; got != first {
			t.Fatalf("iteration %d: qualified prod source = %q, want %q", i, got, first)
		}
	}
}

func TestRefreshContextRegistry_NoOpWhenNothingChanged(t *testing.T) {
	dir := t.TempDir()
	f1 := writeKubeconfig(t, dir, "stable.yaml", "ctx-a", []kubeEntry{
		{ctxName: "ctx-a", userName: "u", clusterName: "c1"},
	})
	registry, fileConfigs, mtimes := loadFixture(t, []string{f1})
	before := keysOf(registry)

	newRegistry, _, _, changed := refreshContextRegistry(registry, fileConfigs, mtimes, []string{f1})
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
	newRegistry, _, _, changed := refreshContextRegistry(registry, fileConfigs, nilMtimes, []string{f1})
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
	newRegistry, _, _, _ := refreshContextRegistry(registry, fileConfigs, mtimes, []string{f1})
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
	errorlog.Reset()
	t.Cleanup(errorlog.Reset)

	if err := os.WriteFile(f1, []byte("not: valid: yaml: at: all"), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	firstBrokenMtime := mtimes[f1].Add(time.Second)
	if err := os.Chtimes(f1, firstBrokenMtime, firstBrokenMtime); err != nil {
		t.Fatalf("set broken mtime: %v", err)
	}

	newRegistry, newFileConfigs, newMtimes, changed := refreshContextRegistry(registry, fileConfigs, mtimes, []string{f1})
	if !changed || !newMtimes[f1].Equal(firstBrokenMtime) {
		t.Fatalf("first broken revision = changed %t, mtime %v", changed, newMtimes[f1])
	}
	if _, ok := newRegistry["ctx-a"]; !ok {
		t.Errorf("ctx-a was dropped on parse failure; expected to keep it: %v", keysOf(newRegistry))
	}
	if entries := errorlog.GetEntries(); len(entries) != 1 {
		t.Fatalf("first broken revision warnings = %+v", entries)
	}

	newRegistry, newFileConfigs, newMtimes, changed = refreshContextRegistry(newRegistry, newFileConfigs, newMtimes, []string{f1})
	if changed {
		t.Fatal("unchanged broken revision should not refresh again")
	}
	if entries := errorlog.GetEntries(); len(entries) != 1 {
		t.Fatalf("unchanged broken revision warnings = %+v", entries)
	}

	secondBrokenMtime := firstBrokenMtime.Add(time.Second)
	if err := os.Chtimes(f1, secondBrokenMtime, secondBrokenMtime); err != nil {
		t.Fatalf("set second broken mtime: %v", err)
	}
	_, _, newMtimes, changed = refreshContextRegistry(newRegistry, newFileConfigs, newMtimes, []string{f1})
	if !changed || !newMtimes[f1].Equal(secondBrokenMtime) {
		t.Fatalf("second broken revision = changed %t, mtime %v", changed, newMtimes[f1])
	}
	if entries := errorlog.GetEntries(); len(entries) != 2 {
		t.Fatalf("second broken revision warnings = %+v", entries)
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

func TestGetAvailableContexts_ConcurrentSingleToRegistryPromotion(t *testing.T) {
	dir := t.TempDir()
	primary := writeKubeconfig(t, dir, "primary.yaml", "primary", []kubeEntry{
		{ctxName: "primary", userName: "u1", clusterName: "c1"},
	})
	workload := writeKubeconfig(t, dir, "workload.yaml", "workload", []kubeEntry{
		{ctxName: "workload", userName: "u2", clusterName: "c2"},
	})
	registryState, configState, mtimeState := loadFixture(t, []string{primary, workload})

	clientMu.Lock()
	prevRegistry := contextRegistry
	prevConfigs := perFileConfigs
	prevMtimes := perFileMtimes
	prevPaths := kubeconfigPaths
	prevPath := kubeconfigPath
	prevMode := kubeconfigMode
	prevName := contextName
	prevStarted := initializationStarted
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		contextRegistry = prevRegistry
		perFileConfigs = prevConfigs
		perFileMtimes = prevMtimes
		kubeconfigPaths = prevPaths
		kubeconfigPath = prevPath
		kubeconfigMode = prevMode
		contextName = prevName
		initializationStarted = prevStarted
		clientMu.Unlock()
	})

	setSingle := func() {
		contextRegistry = nil
		perFileConfigs = nil
		perFileMtimes = nil
		kubeconfigPaths = nil
		kubeconfigPath = primary
		kubeconfigMode = "single"
	}
	setRegistry := func() {
		contextRegistry = registryState
		perFileConfigs = configState
		perFileMtimes = mtimeState
		kubeconfigPaths = []string{primary, workload}
		kubeconfigPath = ""
		kubeconfigMode = "multi-source"
	}
	clientMu.Lock()
	initializationStarted = true
	contextName = "primary"
	setSingle()
	clientMu.Unlock()

	const iterations = 500
	const readers = 4
	start := make(chan struct{})
	errs := make(chan error, readers)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < iterations; i++ {
			clientMu.Lock()
			if i%2 == 0 {
				setRegistry()
			} else {
				setSingle()
			}
			clientMu.Unlock()
			runtime.Gosched()
		}
	}()
	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < iterations; i++ {
				contexts, err := GetAvailableContexts()
				if err != nil {
					errs <- err
					return
				}
				if len(contexts) == 0 {
					errs <- errors.New("context snapshot was empty")
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestMergeAndSwitchContext_ReusedPathPublishesFreshMaps(t *testing.T) {
	dir := t.TempDir()
	primary := writeKubeconfig(t, dir, "primary.yaml", "primary", []kubeEntry{
		{ctxName: "primary", userName: "u1", clusterName: "c1"},
	})
	workload := writeKubeconfig(t, dir, "workload.yaml", "workload", []kubeEntry{
		{ctxName: "workload", userName: "u2", clusterName: "c2"},
	})
	registry, fileConfigs := buildContextRegistry([]string{primary, workload})
	mtimes := map[string]time.Time{primary: {}, workload: {}}

	clientMu.Lock()
	prevRegistry := contextRegistry
	prevConfigs := perFileConfigs
	prevMtimes := perFileMtimes
	prevPaths := kubeconfigPaths
	prevCAPI := capiKubeconfigs
	prevActiveFile := activeSourceFile
	prevActiveName := activeSourceName
	prevContextName := contextName
	contextRegistry = registry
	perFileConfigs = fileConfigs
	perFileMtimes = mtimes
	kubeconfigPaths = []string{primary, workload}
	capiKubeconfigs = map[string]string{"workload": workload}
	activeSourceFile = workload
	activeSourceName = "workload"
	contextName = "workload"
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		contextRegistry = prevRegistry
		perFileConfigs = prevConfigs
		perFileMtimes = prevMtimes
		kubeconfigPaths = prevPaths
		capiKubeconfigs = prevCAPI
		activeSourceFile = prevActiveFile
		activeSourceName = prevActiveName
		contextName = prevContextName
		clientMu.Unlock()
	})

	publishedConfigs := fileConfigs
	publishedMtimes := mtimes
	incoming := fileConfigs[workload].DeepCopy()
	incoming.AuthInfos["u2"].Token = "rotated-token"
	incoming.Contexts["workload-canary"] = &clientcmdapi.Context{Cluster: "c3", AuthInfo: "u3"}
	incoming.Clusters["c3"] = &clientcmdapi.Cluster{Server: "https://c3", InsecureSkipTLSVerify: true}
	incoming.AuthInfos["u3"] = &clientcmdapi.AuthInfo{Token: "canary-token"}
	data, err := clientcmd.Write(*incoming)
	if err != nil {
		t.Fatalf("serialize incoming kubeconfig: %v", err)
	}
	type mergeResult struct {
		qualifiedName string
		path          string
		created       bool
		err           error
	}
	operationBaseline := activeContextOperations.Load()
	contextOpMu.Lock()
	merged := make(chan mergeResult, 1)
	go func() {
		qName, path, created, mergeErr := MergeAndSwitchContext(data, "workload", "workload")
		merged <- mergeResult{qualifiedName: qName, path: path, created: created, err: mergeErr}
	}()
	deadline := time.Now().Add(time.Second)
	for activeContextOperations.Load() != operationBaseline+1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if activeContextOperations.Load() != operationBaseline+1 {
		contextOpMu.Unlock()
		t.Fatal("merge did not queue behind the context operation lock")
	}
	select {
	case <-merged:
		contextOpMu.Unlock()
		t.Fatal("merge completed while another context operation held the lock")
	default:
	}
	contextOpMu.Unlock()
	var result mergeResult
	select {
	case result = <-merged:
	case <-time.After(time.Second):
		t.Fatal("merge did not finish after the context operation lock was released")
	}
	qName, path, created, err := result.qualifiedName, result.path, result.created, result.err
	if err != nil {
		t.Fatalf("MergeAndSwitchContext: %v", err)
	}
	if created {
		t.Fatal("reused CAPI source reported as newly created")
	}
	if qName != "workload" || path != workload {
		t.Fatalf("reuse result = (%q, %q), want (%q, %q)", qName, path, "workload", workload)
	}
	if DiscardFailedMergedContext(path, created) {
		t.Fatal("failed-switch cleanup discarded a reused CAPI source")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("reused CAPI source was removed by failed-switch cleanup: %v", err)
	}
	if got := publishedConfigs[workload].AuthInfos["u2"].Token; got == "rotated-token" {
		t.Fatal("previously published config map was mutated in place")
	}
	if got := publishedMtimes[workload]; !got.IsZero() {
		t.Fatalf("previously published mtime map was mutated in place: %v", got)
	}

	clientMu.RLock()
	refreshedConfig := perFileConfigs[workload]
	refreshedMtime := perFileMtimes[workload]
	clientMu.RUnlock()
	if got := refreshedConfig.AuthInfos["u2"].Token; got != "rotated-token" {
		t.Fatalf("current cached credential = %q, want rotated-token", got)
	}
	if !refreshedMtime.IsZero() {
		t.Fatalf("cached mtime advanced before registry reconciliation: %v", refreshedMtime)
	}

	contexts, err := GetAvailableContexts()
	if err != nil {
		t.Fatalf("GetAvailableContexts after CAPI rewrite: %v", err)
	}
	var foundCanary bool
	for _, context := range contexts {
		if context.OriginalName == "workload-canary" {
			foundCanary = true
			break
		}
	}
	if !foundCanary {
		t.Fatalf("rewritten CAPI context was not reconciled: %+v", contexts)
	}

	renamed := refreshedConfig.DeepCopy()
	delete(renamed.Contexts, "workload-canary")
	renamed.Contexts["workload-stable"] = &clientcmdapi.Context{Cluster: "c3", AuthInfo: "u3"}
	renamedData, err := clientcmd.Write(*renamed)
	if err != nil {
		t.Fatalf("serialize renamed CAPI kubeconfig: %v", err)
	}
	time.Sleep(15 * time.Millisecond)
	if _, _, created, err := MergeAndSwitchContext(renamedData, "workload", "workload"); err != nil {
		t.Fatalf("MergeAndSwitchContext after context rename: %v", err)
	} else if created {
		t.Fatal("renamed CAPI source refresh reported as newly created")
	}
	future := time.Now().Add(time.Second)
	if err := os.Chtimes(workload, future, future); err != nil {
		t.Fatalf("advance rewritten CAPI mtime: %v", err)
	}
	contexts, err = GetAvailableContexts()
	if err != nil {
		t.Fatalf("GetAvailableContexts after CAPI context rename: %v", err)
	}
	foundStable := false
	for _, context := range contexts {
		switch context.OriginalName {
		case "workload-canary":
			t.Fatalf("removed CAPI context remained registered: %+v", contexts)
		case "workload-stable":
			foundStable = true
		}
	}
	if !foundStable {
		t.Fatalf("renamed CAPI context was not registered: %+v", contexts)
	}

	renamedRoot := renamed.DeepCopy()
	delete(renamedRoot.Contexts, "workload")
	renamedRoot.Contexts["workload-renamed"] = &clientcmdapi.Context{Cluster: "c2", AuthInfo: "u2"}
	renamedRoot.CurrentContext = "workload-renamed"
	renamedRootData, err := clientcmd.Write(*renamedRoot)
	if err != nil {
		t.Fatalf("serialize CAPI kubeconfig with renamed root context: %v", err)
	}
	qualifiedName, reusedPath, created, err := MergeAndSwitchContext(renamedRootData, "workload-renamed", "workload")
	if err != nil {
		t.Fatalf("MergeAndSwitchContext after root context rename: %v", err)
	}
	if qualifiedName != "workload-renamed" || reusedPath != workload || created {
		t.Fatalf("root rename result = (%q, %q, %t)", qualifiedName, reusedPath, created)
	}
	contexts, err = GetAvailableContexts()
	if err != nil {
		t.Fatalf("GetAvailableContexts after root context rename: %v", err)
	}
	foundRenamedRoot := false
	for _, context := range contexts {
		switch context.OriginalName {
		case "workload":
			t.Fatalf("stale root CAPI context remained registered: %+v", contexts)
		case "workload-renamed":
			foundRenamedRoot = true
		}
	}
	if !foundRenamedRoot {
		t.Fatalf("renamed root CAPI context was not registered: %+v", contexts)
	}
}

func TestMergeAndSwitchContext_RecreatesMissingActiveSourceWithStableBinding(t *testing.T) {
	dir := t.TempDir()
	primary := writeKubeconfig(t, dir, "primary.yaml", "primary", []kubeEntry{
		{ctxName: "primary", userName: "u1", clusterName: "c1"},
	})
	workload := writeKubeconfig(t, dir, "workload.yaml", "workload", []kubeEntry{
		{ctxName: "workload", userName: "u2", clusterName: "c2"},
	})
	data, err := os.ReadFile(workload)
	if err != nil {
		t.Fatalf("read workload kubeconfig: %v", err)
	}
	registry, configs := buildContextRegistry([]string{primary, workload})
	binding := CAPIClusterSafetyBinding("kcb1_management", "clusters", "workload")

	clientMu.Lock()
	previousRegistry := contextRegistry
	previousConfigs := perFileConfigs
	previousMtimes := perFileMtimes
	previousPaths := kubeconfigPaths
	previousMode := kubeconfigMode
	previousCAPI := capiKubeconfigs
	previousActiveFile := activeSourceFile
	previousActiveName := activeSourceName
	previousContextName := contextName
	previousCount := totalContextCount
	contextRegistry = registry
	perFileConfigs = configs
	perFileMtimes = map[string]time.Time{primary: {}, workload: {}}
	kubeconfigPaths = []string{primary, workload}
	kubeconfigMode = "multi-source"
	capiKubeconfigs = map[string]string{binding: workload}
	activeSourceFile = workload
	activeSourceName = "workload"
	contextName = "workload"
	totalContextCount = len(registry)
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		contextRegistry = previousRegistry
		perFileConfigs = previousConfigs
		perFileMtimes = previousMtimes
		kubeconfigPaths = previousPaths
		kubeconfigMode = previousMode
		capiKubeconfigs = previousCAPI
		activeSourceFile = previousActiveFile
		activeSourceName = previousActiveName
		contextName = previousContextName
		totalContextCount = previousCount
		clientMu.Unlock()
	})

	if err := os.Remove(workload); err != nil {
		t.Fatalf("remove active workload kubeconfig: %v", err)
	}
	contexts, err := GetAvailableContexts()
	if err != nil {
		t.Fatalf("refresh after removing active workload kubeconfig: %v", err)
	}
	for _, context := range contexts {
		if context.OriginalName == "workload" {
			t.Fatalf("deleted active workload remained selectable: %+v", contexts)
		}
	}
	qualifiedName, path, created, err := MergeAndSwitchContext(data, "workload", binding)
	if err != nil {
		t.Fatalf("MergeAndSwitchContext: %v", err)
	}
	if qualifiedName != "workload" || path != workload || created {
		t.Fatalf("refresh result = (%q, %q, %t)", qualifiedName, path, created)
	}
	if info, err := os.Stat(workload); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("recreated source is not a regular file: info=%v err=%v", info, err)
	}
	contexts, err = GetAvailableContexts()
	if err != nil {
		t.Fatalf("refresh after recreating active workload kubeconfig: %v", err)
	}
	foundWorkload := false
	for _, context := range contexts {
		if context.Name == qualifiedName && context.OriginalName == "workload" {
			foundWorkload = true
		}
	}
	if !foundWorkload {
		t.Fatalf("recreated active workload is not selectable: %+v", contexts)
	}
	clientMu.Lock()
	got := sourceSafetyBindingLocked(workload, "workload")
	clientMu.Unlock()
	if got != binding {
		t.Fatalf("active CAPI binding = %q, want %q", got, binding)
	}
}

func TestMergeAndSwitchContext_DoesNotReuseSameNamedDifferentCAPICluster(t *testing.T) {
	dir := t.TempDir()
	primary := writeKubeconfig(t, dir, "primary.yaml", "primary", []kubeEntry{
		{ctxName: "primary", userName: "u1", clusterName: "c1"},
	})
	workload := writeKubeconfig(t, dir, "workload.yaml", "workload", []kubeEntry{
		{ctxName: "workload", userName: "u2", clusterName: "c2"},
	})
	data, err := os.ReadFile(workload)
	if err != nil {
		t.Fatalf("read workload kubeconfig: %v", err)
	}

	clientMu.Lock()
	previousRegistry := contextRegistry
	previousConfigs := perFileConfigs
	previousMtimes := perFileMtimes
	previousPaths := kubeconfigPaths
	previousPath := kubeconfigPath
	previousMode := kubeconfigMode
	previousCAPI := capiKubeconfigs
	previousPromotion := preCapiPromotion
	previousStarted := initializationStarted
	previousDirectoryCount := kubeconfigDirectoryFileCount
	previousCount := totalContextCount
	contextRegistry = nil
	perFileConfigs = nil
	perFileMtimes = nil
	kubeconfigPaths = nil
	kubeconfigPath = primary
	kubeconfigMode = "single"
	capiKubeconfigs = map[string]string{}
	preCapiPromotion = nil
	initializationStarted = true
	totalContextCount = 1
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		contextRegistry = previousRegistry
		perFileConfigs = previousConfigs
		perFileMtimes = previousMtimes
		kubeconfigPaths = previousPaths
		kubeconfigPath = previousPath
		kubeconfigMode = previousMode
		capiKubeconfigs = previousCAPI
		preCapiPromotion = previousPromotion
		initializationStarted = previousStarted
		kubeconfigDirectoryFileCount = previousDirectoryCount
		totalContextCount = previousCount
		clientMu.Unlock()
	})

	firstBinding := CAPIClusterSafetyBinding("kcb1_management-a", "clusters", "workload")
	secondBinding := CAPIClusterSafetyBinding("kcb1_management-b", "clusters", "workload")
	firstName, firstPath, firstCreated, err := MergeAndSwitchContext(data, "workload", firstBinding)
	if err != nil || !firstCreated {
		t.Fatalf("first merge: created=%t, err=%v", firstCreated, err)
	}
	t.Cleanup(func() { _ = os.Remove(firstPath) })
	secondName, secondPath, secondCreated, err := MergeAndSwitchContext(data, "workload", secondBinding)
	if err != nil || !secondCreated {
		t.Fatalf("second merge: created=%t, err=%v", secondCreated, err)
	}
	t.Cleanup(func() { _ = os.Remove(secondPath) })
	if firstPath == secondPath || firstName == secondName {
		t.Fatalf("same-named distinct CAPI clusters were conflated: first=(%q, %q), second=(%q, %q)", firstName, firstPath, secondName, secondPath)
	}
	clientMu.RLock()
	firstTracked := capiKubeconfigs[firstBinding]
	secondTracked := capiKubeconfigs[secondBinding]
	clientMu.RUnlock()
	if firstTracked != firstPath || secondTracked != secondPath {
		t.Fatalf("tracked CAPI sources = (%q, %q), want (%q, %q)", firstTracked, secondTracked, firstPath, secondPath)
	}
}

func TestSwitchContextMissingSourceErrorIsSanitizedAndClassified(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "private", "prod.yaml")
	clientMu.Lock()
	previousRegistry := contextRegistry
	previousMode := kubeconfigMode
	previousStarted := initializationStarted
	contextRegistry = map[string]contextEntry{
		"prod": {SourceFile: missing, InFileName: "prod"},
	}
	kubeconfigMode = "multi-source"
	initializationStarted = true
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		contextRegistry = previousRegistry
		kubeconfigMode = previousMode
		initializationStarted = previousStarted
		clientMu.Unlock()
	})

	err := SwitchContext("prod")
	if err == nil {
		t.Fatal("SwitchContext unexpectedly succeeded")
	}
	if strings.Contains(err.Error(), missing) || !strings.Contains(err.Error(), "stat: no such file or directory") {
		t.Fatalf("SwitchContext error was not sanitized: %v", err)
	}
	if got := ClassifyError(err); got != "config" {
		t.Fatalf("ClassifyError = %q, want config", got)
	}
}

func TestSwitchContextMalformedSourceErrorIsSanitizedAndClassified(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "prod.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create kubeconfig directory: %v", err)
	}
	if err := os.WriteFile(path, []byte("contexts: ["), 0o600); err != nil {
		t.Fatalf("write malformed kubeconfig: %v", err)
	}

	clientMu.Lock()
	previousRegistry := contextRegistry
	previousMode := kubeconfigMode
	previousStarted := initializationStarted
	contextRegistry = map[string]contextEntry{
		"prod": {SourceFile: path, InFileName: "prod"},
	}
	kubeconfigMode = "multi-source"
	initializationStarted = true
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		contextRegistry = previousRegistry
		kubeconfigMode = previousMode
		initializationStarted = previousStarted
		clientMu.Unlock()
	})

	err := SwitchContext("prod")
	if err == nil {
		t.Fatal("SwitchContext unexpectedly succeeded")
	}
	if strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "invalid kubeconfig syntax") {
		t.Fatalf("SwitchContext error was not sanitized: %v", err)
	}
	if got := ClassifyError(err); got != "config" {
		t.Fatalf("ClassifyError = %q, want config", got)
	}
}

func TestDropKubeconfigSourceKeepsSingleFileContextCount(t *testing.T) {
	clientMu.Lock()
	previousRegistry := contextRegistry
	previousPaths := kubeconfigPaths
	previousCount := totalContextCount
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		contextRegistry = previousRegistry
		kubeconfigPaths = previousPaths
		totalContextCount = previousCount
		clientMu.Unlock()
	})

	clientMu.Lock()
	contextRegistry = nil
	kubeconfigPaths = []string{"single"}
	totalContextCount = 1
	dropKubeconfigSourceLocked("single")
	got := totalContextCount
	clientMu.Unlock()

	if got != 1 {
		t.Fatalf("single-file context count = %d, want 1", got)
	}
}

func TestMergeAndSwitchContext_ReplacesPrunedCAPIPathWithoutDuplicate(t *testing.T) {
	dir := t.TempDir()
	primary := writeKubeconfig(t, dir, "primary.yaml", "primary", []kubeEntry{
		{ctxName: "primary", userName: "u1", clusterName: "c1"},
	})
	workload := writeKubeconfig(t, dir, "workload.yaml", "workload", []kubeEntry{
		{ctxName: "workload", userName: "u2", clusterName: "c2"},
	})
	data, err := os.ReadFile(workload)
	if err != nil {
		t.Fatalf("read workload kubeconfig: %v", err)
	}
	registry, fileConfigs := buildContextRegistry([]string{primary, workload})
	mtimes := map[string]time.Time{}
	for _, path := range []string{primary, workload} {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("stat %s: %v", path, statErr)
		}
		mtimes[path] = info.ModTime()
	}

	clientMu.Lock()
	previousRegistry := contextRegistry
	previousConfigs := perFileConfigs
	previousMtimes := perFileMtimes
	previousPaths := kubeconfigPaths
	previousCAPI := capiKubeconfigs
	previousCount := totalContextCount
	contextRegistry = registry
	perFileConfigs = fileConfigs
	perFileMtimes = mtimes
	kubeconfigPaths = []string{primary, workload}
	capiKubeconfigs = map[string]string{"workload": workload}
	totalContextCount = len(registry)
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		contextRegistry = previousRegistry
		perFileConfigs = previousConfigs
		perFileMtimes = previousMtimes
		kubeconfigPaths = previousPaths
		capiKubeconfigs = previousCAPI
		totalContextCount = previousCount
		clientMu.Unlock()
	})

	if err := os.Remove(workload); err != nil {
		t.Fatalf("remove workload kubeconfig: %v", err)
	}
	if _, err := GetAvailableContexts(); err != nil {
		t.Fatalf("refresh after removing workload kubeconfig: %v", err)
	}

	qualifiedName, replacement, created, err := MergeAndSwitchContext(data, "workload", "workload")
	if err != nil {
		t.Fatalf("MergeAndSwitchContext: %v", err)
	}
	if !created {
		t.Fatal("replacement CAPI source was not reported as newly created")
	}
	t.Cleanup(func() { _ = os.Remove(replacement) })
	if qualifiedName != "workload" {
		t.Fatalf("qualified name = %q, want workload", qualifiedName)
	}
	if replacement == workload {
		t.Fatalf("replacement reused pruned source %q", workload)
	}
	if _, err := os.Stat(workload); !os.IsNotExist(err) {
		t.Fatalf("pruned source still exists after replacement: %v", err)
	}

	contexts, err := GetAvailableContexts()
	if err != nil {
		t.Fatalf("GetAvailableContexts after replacement: %v", err)
	}
	workloadContexts := 0
	for _, context := range contexts {
		if context.OriginalName == "workload" {
			workloadContexts++
		}
	}
	if workloadContexts != 1 {
		t.Fatalf("workload context count = %d, want 1: %+v", workloadContexts, contexts)
	}

	clientMu.RLock()
	paths := append([]string(nil), kubeconfigPaths...)
	registeredPath := capiKubeconfigs["workload"]
	clientMu.RUnlock()
	if registeredPath != replacement {
		t.Fatalf("registered CAPI path = %q, want %q", registeredPath, replacement)
	}
	for _, path := range paths {
		if path == workload {
			t.Fatalf("pruned source remained in kubeconfig path order: %v", paths)
		}
	}
}

func TestMergeAndSwitchContext_PromotionEntersRegistryMode(t *testing.T) {
	dir := t.TempDir()
	primary := writeKubeconfig(t, dir, "primary.yaml", "primary", []kubeEntry{
		{ctxName: "primary", userName: "u1", clusterName: "c1"},
	})
	workload := writeKubeconfig(t, dir, "workload.yaml", "workload", []kubeEntry{
		{ctxName: "workload", userName: "u2", clusterName: "c2"},
	})
	data, err := os.ReadFile(workload)
	if err != nil {
		t.Fatalf("read workload kubeconfig: %v", err)
	}

	clientMu.Lock()
	prevRegistry := contextRegistry
	prevConfigs := perFileConfigs
	prevMtimes := perFileMtimes
	prevPaths := kubeconfigPaths
	prevPath := kubeconfigPath
	prevMode := kubeconfigMode
	prevCAPI := capiKubeconfigs
	prevPromotion := preCapiPromotion
	prevStarted := initializationStarted
	prevDirectoryCount := kubeconfigDirectoryFileCount
	prevContextCount := totalContextCount
	contextRegistry = nil
	perFileConfigs = nil
	perFileMtimes = nil
	kubeconfigPaths = nil
	kubeconfigPath = primary
	kubeconfigMode = "single"
	capiKubeconfigs = map[string]string{}
	preCapiPromotion = nil
	initializationStarted = true
	kubeconfigDirectoryFileCount = 0
	totalContextCount = 1
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		contextRegistry = prevRegistry
		perFileConfigs = prevConfigs
		perFileMtimes = prevMtimes
		kubeconfigPaths = prevPaths
		kubeconfigPath = prevPath
		kubeconfigMode = prevMode
		capiKubeconfigs = prevCAPI
		preCapiPromotion = prevPromotion
		initializationStarted = prevStarted
		kubeconfigDirectoryFileCount = prevDirectoryCount
		totalContextCount = prevContextCount
		clientMu.Unlock()
	})

	qName, tmpPath, created, err := MergeAndSwitchContext(data, "workload", "workload")
	if err != nil {
		t.Fatalf("MergeAndSwitchContext: %v", err)
	}
	if !created {
		t.Fatal("promoting CAPI source was not reported as newly created")
	}
	t.Cleanup(func() { _ = os.Remove(tmpPath) })
	if qName != "workload" {
		t.Fatalf("qualified name = %q, want workload", qName)
	}
	if got := GetKubeconfigPath(); got != "" {
		t.Fatalf("registry-backed kubeconfig path = %q, want empty for rest.Config consumers", got)
	}
	if got := GetKubeconfigSummary().Mode; got != "multi-source" {
		t.Fatalf("promoted kubeconfig mode = %q, want multi-source", got)
	}
	if IsInCluster() {
		t.Fatal("registry promotion was misclassified as in-cluster after clearing the single-file path")
	}
	summary := GetKubeconfigSummary()
	if summary.FileCount != 2 || summary.DirectoryFileCount != 0 || summary.ContextCount != 2 {
		t.Fatalf("promoted kubeconfig summary = %+v", summary)
	}
	if !DiscardFailedMergedContext(tmpPath, created) {
		t.Fatal("inactive merged context was not discarded")
	}
	if got := GetKubeconfigPath(); got != primary {
		t.Fatalf("restored kubeconfig path = %q, want %q", got, primary)
	}
	restoredSummary := GetKubeconfigSummary()
	if restoredSummary.Mode != "single" || restoredSummary.FileCount != 1 || restoredSummary.ContextCount != 1 {
		t.Fatalf("restored kubeconfig summary = %+v", restoredSummary)
	}

	secondWorkload := writeKubeconfig(t, dir, "workload-two.yaml", "workload-two", []kubeEntry{
		{ctxName: "workload-two", userName: "u3", clusterName: "c3"},
	})
	secondData, err := os.ReadFile(secondWorkload)
	if err != nil {
		t.Fatalf("read second workload kubeconfig: %v", err)
	}
	_, firstPath, firstCreated, err := MergeAndSwitchContext(data, "workload", "workload")
	if err != nil || !firstCreated {
		t.Fatalf("recreate first workload: created=%t, err=%v", firstCreated, err)
	}
	t.Cleanup(func() { _ = os.Remove(firstPath) })
	_, secondPath, secondCreated, err := MergeAndSwitchContext(secondData, "workload-two", "workload-two")
	if err != nil || !secondCreated {
		t.Fatalf("create second workload: created=%t, err=%v", secondCreated, err)
	}
	t.Cleanup(func() { _ = os.Remove(secondPath) })
	if !DiscardFailedMergedContext(firstPath, firstCreated) {
		t.Fatal("first inactive workload was not discarded")
	}
	clientMu.RLock()
	snapshotDeferred := preCapiPromotion != nil
	clientMu.RUnlock()
	if !snapshotDeferred {
		t.Fatal("pre-promotion snapshot was consumed while another CAPI source remained")
	}
	if !DiscardFailedMergedContext(secondPath, secondCreated) {
		t.Fatal("second inactive workload was not discarded")
	}
	if got := GetKubeconfigPath(); got != primary {
		t.Fatalf("kubeconfig path after final discard = %q, want %q", got, primary)
	}
	if summary := GetKubeconfigSummary(); summary.Mode != "single" || summary.FileCount != 1 || summary.ContextCount != 1 {
		t.Fatalf("summary after final discard = %+v", summary)
	}
}

func TestDiscardFailedMergedContextRestoresInClusterPromotion(t *testing.T) {
	dir := t.TempDir()
	workload := writeKubeconfig(t, dir, "workload.yaml", "workload", []kubeEntry{
		{ctxName: "workload", userName: "u2", clusterName: "c2"},
	})
	data, err := os.ReadFile(workload)
	if err != nil {
		t.Fatalf("read workload kubeconfig: %v", err)
	}

	clientMu.Lock()
	previousRegistry := contextRegistry
	previousConfigs := perFileConfigs
	previousMtimes := perFileMtimes
	previousPaths := kubeconfigPaths
	previousPath := kubeconfigPath
	previousMode := kubeconfigMode
	previousCAPI := capiKubeconfigs
	previousPromotion := preCapiPromotion
	previousStarted := initializationStarted
	previousDirectoryCount := kubeconfigDirectoryFileCount
	previousContextCount := totalContextCount
	previousContextName := contextName
	previousActiveSource := activeSourceFile
	contextRegistry = nil
	perFileConfigs = nil
	perFileMtimes = nil
	kubeconfigPaths = nil
	kubeconfigPath = ""
	kubeconfigMode = "in-cluster"
	capiKubeconfigs = map[string]string{}
	preCapiPromotion = nil
	initializationStarted = true
	kubeconfigDirectoryFileCount = 0
	totalContextCount = 1
	contextName = "in-cluster"
	activeSourceFile = ""
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		contextRegistry = previousRegistry
		perFileConfigs = previousConfigs
		perFileMtimes = previousMtimes
		kubeconfigPaths = previousPaths
		kubeconfigPath = previousPath
		kubeconfigMode = previousMode
		capiKubeconfigs = previousCAPI
		preCapiPromotion = previousPromotion
		initializationStarted = previousStarted
		kubeconfigDirectoryFileCount = previousDirectoryCount
		totalContextCount = previousContextCount
		contextName = previousContextName
		activeSourceFile = previousActiveSource
		clientMu.Unlock()
	})

	_, tmpPath, created, err := MergeAndSwitchContext(data, "workload", "workload")
	if err != nil {
		t.Fatalf("MergeAndSwitchContext: %v", err)
	}
	if !created {
		t.Fatal("promoting CAPI source was not reported as newly created")
	}
	t.Cleanup(func() { _ = os.Remove(tmpPath) })

	operationBaseline := activeContextOperations.Load()
	contextOpMu.Lock()
	discarded := make(chan bool, 1)
	go func() {
		discarded <- DiscardFailedMergedContext(tmpPath, created)
	}()
	deadline := time.Now().Add(time.Second)
	for activeContextOperations.Load() != operationBaseline+1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if activeContextOperations.Load() != operationBaseline+1 {
		contextOpMu.Unlock()
		t.Fatal("discard did not queue behind the context operation lock")
	}
	select {
	case <-discarded:
		contextOpMu.Unlock()
		t.Fatal("discard completed while another context operation held the lock")
	default:
	}
	clientMu.Lock()
	activeSourceFile = tmpPath
	clientMu.Unlock()
	contextOpMu.Unlock()
	select {
	case wasDiscarded := <-discarded:
		if wasDiscarded {
			t.Fatal("active merged context was discarded")
		}
	case <-time.After(time.Second):
		t.Fatal("discard did not finish after the context operation lock was released")
	}
	if _, err := os.Stat(tmpPath); err != nil {
		t.Fatalf("active merged kubeconfig was removed: %v", err)
	}

	clientMu.Lock()
	activeSourceFile = ""
	clientMu.Unlock()
	if !DiscardFailedMergedContext(tmpPath, created) {
		t.Fatal("inactive merged context was not discarded")
	}
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatalf("inactive merged kubeconfig still exists: %v", err)
	}
	if !IsInCluster() {
		t.Fatal("discard did not restore in-cluster mode")
	}

	clientMu.RLock()
	defer clientMu.RUnlock()
	if contextRegistry != nil || perFileConfigs != nil || perFileMtimes != nil {
		t.Fatal("discard left the promoted registry initialized")
	}
	if kubeconfigPath != "" || len(kubeconfigPaths) != 0 || kubeconfigMode != "in-cluster" {
		t.Fatalf("discarded source state = path %q, paths %v, mode %q", kubeconfigPath, kubeconfigPaths, kubeconfigMode)
	}
	if len(capiKubeconfigs) != 0 || preCapiPromotion != nil || totalContextCount != 1 {
		t.Fatalf("discarded CAPI state = kubeconfigs %v, promotion %v, contexts %d", capiKubeconfigs, preCapiPromotion, totalContextCount)
	}
}

func TestLoadedDirectoryKubeconfigCountExcludesPrimaryAndCAPI(t *testing.T) {
	configs := map[string]*clientcmdapi.Config{
		"/primary": {},
		"/dir/a":   {},
		"/dir/b":   {},
		"/capi":    {},
	}
	directoryPaths := map[string]struct{}{"/dir/a": {}, "/dir/b": {}}

	if got := loadedDirectoryKubeconfigCount(configs, directoryPaths); got != 2 {
		t.Fatalf("directory count = %d, want 2", got)
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
