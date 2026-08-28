//go:build !windows

package k8s

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func TestDiscoverKubeconfigsSkipsFIFOAndKeepsRegularSymlink(t *testing.T) {
	dir := t.TempDir()
	regular := writeKubeconfig(t, dir, "regular.yaml", "prod", []kubeEntry{
		{ctxName: "prod", userName: "user", clusterName: "cluster"},
	})
	symlink := filepath.Join(dir, "linked.yaml")
	if err := os.Symlink(regular, symlink); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	directory := filepath.Join(dir, "nested")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	directorySymlink := filepath.Join(dir, "nested-link.yaml")
	if err := os.Symlink(directory, directorySymlink); err != nil {
		t.Fatalf("directory symlink: %v", err)
	}
	fifo := filepath.Join(dir, "blocked.pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	done := make(chan []string, 1)
	go func() { done <- discoverKubeconfigs([]string{dir}) }()

	var discovered []string
	select {
	case discovered = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("directory discovery blocked while inspecting a FIFO")
	}

	found := make(map[string]bool, len(discovered))
	for _, path := range discovered {
		found[path] = true
	}
	if !found[regular] || !found[symlink] {
		t.Fatalf("regular kubeconfig and symlink must be discovered, got %v", discovered)
	}
	if found[fifo] {
		t.Fatalf("FIFO must be skipped, got %v", discovered)
	}
	if found[directorySymlink] {
		t.Fatalf("symlink to a directory must be skipped, got %v", discovered)
	}
}

func TestBuildContextRegistrySkipsFIFOWithoutBlocking(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "registry.pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	done := make(chan struct{}, 1)
	go func() {
		registry, configs := buildContextRegistry([]string{fifo})
		if len(registry) != 0 || len(configs) != 0 {
			t.Errorf("FIFO registry result = (%v, %v)", registry, configs)
		}
		done <- struct{}{}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("registry build blocked while opening a FIFO")
	}
}

func TestRefreshContextRegistryDropsSourceThatBecomesFIFO(t *testing.T) {
	dir := t.TempDir()
	path := writeKubeconfig(t, dir, "watched.yaml", "watched", []kubeEntry{
		{ctxName: "watched", userName: "user", clusterName: "cluster"},
	})
	registry, configs, mtimes := loadFixture(t, []string{path})
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove kubeconfig: %v", err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	type refreshResult struct {
		registry map[string]contextEntry
		configs  map[string]*clientcmdapi.Config
		mtimes   map[string]time.Time
		changed  bool
	}
	done := make(chan refreshResult, 1)
	go func() {
		refreshedRegistry, refreshedConfigs, refreshedMtimes, changed := refreshContextRegistry(
			registry, configs, mtimes, []string{path},
		)
		done <- refreshResult{refreshedRegistry, refreshedConfigs, refreshedMtimes, changed}
	}()

	select {
	case result := <-done:
		if !result.changed {
			t.Fatal("non-regular replacement did not change the registry")
		}
		if _, ok := result.registry["watched"]; ok {
			t.Fatal("non-regular replacement remained selectable")
		}
		if _, ok := result.configs[path]; ok {
			t.Fatal("non-regular replacement retained its cached config")
		}
		if _, ok := result.mtimes[path]; ok {
			t.Fatal("non-regular replacement retained its cached mtime")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("registry refresh blocked while opening a FIFO")
	}
}

func TestContextSwitchRejectsSourceThatBecomesFIFOBeforeTeardown(t *testing.T) {
	dir := t.TempDir()
	current := writeKubeconfig(t, dir, "current.yaml", "current", []kubeEntry{
		{ctxName: "current", userName: "u1", clusterName: "c1"},
	})
	target := writeKubeconfig(t, dir, "target.yaml", "target", []kubeEntry{
		{ctxName: "target", userName: "u2", clusterName: "c2"},
	})
	registry, configs, mtimes := loadFixture(t, []string{current, target})

	clientMu.Lock()
	previousRegistry := contextRegistry
	previousConfigs := perFileConfigs
	previousMtimes := perFileMtimes
	previousPaths := kubeconfigPaths
	previousMode := kubeconfigMode
	previousStarted := initializationStarted
	contextRegistry = registry
	perFileConfigs = configs
	perFileMtimes = mtimes
	kubeconfigPaths = []string{current, target}
	kubeconfigMode = "multi-dir"
	initializationStarted = true
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		contextRegistry = previousRegistry
		perFileConfigs = previousConfigs
		perFileMtimes = previousMtimes
		kubeconfigPaths = previousPaths
		kubeconfigMode = previousMode
		initializationStarted = previousStarted
		clientMu.Unlock()
	})

	stopped := false
	SetSessionStopper(func() { stopped = true })
	t.Cleanup(func() { SetSessionStopper(nil) })
	if err := os.Remove(target); err != nil {
		t.Fatalf("remove target kubeconfig: %v", err)
	}
	if err := syscall.Mkfifo(target, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- PerformContextSwitch("target") }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrContextSwitchPreflight) {
			t.Fatalf("expected ErrContextSwitchPreflight, got %v", err)
		}
		if stopped {
			t.Fatal("sessions were stopped for a non-regular context source")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("context-switch preflight blocked while opening a FIFO")
	}
}

func TestSwitchContextRejectsFIFOSourceWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	target := writeKubeconfig(t, dir, "target.yaml", "target", []kubeEntry{
		{ctxName: "target", userName: "u2", clusterName: "c2"},
	})
	registry, configs, mtimes := loadFixture(t, []string{target})
	if err := os.Remove(target); err != nil {
		t.Fatalf("remove target kubeconfig: %v", err)
	}
	if err := syscall.Mkfifo(target, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	clientMu.Lock()
	previousRegistry := contextRegistry
	previousConfigs := perFileConfigs
	previousMtimes := perFileMtimes
	previousPaths := kubeconfigPaths
	previousMode := kubeconfigMode
	previousStarted := initializationStarted
	contextRegistry = registry
	perFileConfigs = configs
	perFileMtimes = mtimes
	kubeconfigPaths = []string{target}
	kubeconfigMode = "multi-dir"
	initializationStarted = true
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		contextRegistry = previousRegistry
		perFileConfigs = previousConfigs
		perFileMtimes = previousMtimes
		kubeconfigPaths = previousPaths
		kubeconfigMode = previousMode
		initializationStarted = previousStarted
		clientMu.Unlock()
	})

	done := make(chan error, 1)
	go func() { done <- SwitchContext("target") }()
	select {
	case err := <-done:
		if !errors.Is(err, errKubeconfigNotRegular) {
			t.Fatalf("expected errKubeconfigNotRegular, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SwitchContext blocked while opening a FIFO")
	}
}

func TestGetAvailableContextsRejectsSingleFIFOSourceWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	path := writeKubeconfig(t, dir, "single.yaml", "single", []kubeEntry{
		{ctxName: "single", userName: "user", clusterName: "cluster"},
	})
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove kubeconfig: %v", err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	clientMu.Lock()
	previousRegistry := contextRegistry
	previousPath := kubeconfigPath
	previousMode := kubeconfigMode
	previousStarted := initializationStarted
	contextRegistry = nil
	kubeconfigPath = path
	kubeconfigMode = "single"
	initializationStarted = true
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		contextRegistry = previousRegistry
		kubeconfigPath = previousPath
		kubeconfigMode = previousMode
		initializationStarted = previousStarted
		clientMu.Unlock()
	})

	done := make(chan error, 1)
	go func() {
		_, err := GetAvailableContexts()
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, errKubeconfigNotRegular) {
			t.Fatalf("expected errKubeconfigNotRegular, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("single-file context listing blocked while opening a FIFO")
	}
}

func TestWriteKubeconfigForCurrentContextDoesNotReadFIFO(t *testing.T) {
	dir := t.TempDir()
	path := writeKubeconfig(t, dir, "active.yaml", "active", []kubeEntry{
		{ctxName: "active", userName: "user", clusterName: "cluster"},
	})
	loaded, err := clientcmd.LoadFromFile(path)
	if err != nil {
		t.Fatalf("load active config: %v", err)
	}
	activeConfig := loaded

	clientMu.Lock()
	previousRegistry := contextRegistry
	previousConfigs := perFileConfigs
	previousPath := kubeconfigPath
	previousName := contextName
	previousActiveFile := activeSourceFile
	previousActiveName := activeSourceName
	previousActiveConfig := activeSourceConfig
	contextRegistry = map[string]contextEntry{"active": {SourceFile: path, InFileName: "active"}}
	perFileConfigs = map[string]*clientcmdapi.Config{path: activeConfig.DeepCopy()}
	kubeconfigPath = ""
	contextName = "active"
	activeSourceFile = path
	activeSourceName = "active"
	activeSourceConfig = activeConfig.DeepCopy()
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		contextRegistry = previousRegistry
		perFileConfigs = previousConfigs
		kubeconfigPath = previousPath
		contextName = previousName
		activeSourceFile = previousActiveFile
		activeSourceName = previousActiveName
		activeSourceConfig = previousActiveConfig
		clientMu.Unlock()
	})

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove active kubeconfig: %v", err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	type result struct {
		path string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		tmpPath, writeErr := WriteKubeconfigForCurrentContext()
		done <- result{path: tmpPath, err: writeErr}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("WriteKubeconfigForCurrentContext: %v", got.err)
		}
		t.Cleanup(func() { _ = os.Remove(got.path) })
		written, loadErr := clientcmd.LoadFromFile(got.path)
		if loadErr != nil {
			t.Fatalf("load generated active snapshot: %v", loadErr)
		}
		if written.CurrentContext != "active" {
			t.Fatalf("generated active snapshot context = %q", written.CurrentContext)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("current-context kubeconfig export blocked while opening a FIFO")
	}
}

func TestWriteKubeconfigForCurrentContextRejectsSingleFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "single.pipe")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	clientMu.Lock()
	previousRegistry := contextRegistry
	previousPath := kubeconfigPath
	previousName := contextName
	previousActiveFile := activeSourceFile
	previousActiveName := activeSourceName
	previousActiveConfig := activeSourceConfig
	contextRegistry = nil
	kubeconfigPath = path
	contextName = "single"
	activeSourceFile = ""
	activeSourceName = ""
	activeSourceConfig = nil
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		contextRegistry = previousRegistry
		kubeconfigPath = previousPath
		contextName = previousName
		activeSourceFile = previousActiveFile
		activeSourceName = previousActiveName
		activeSourceConfig = previousActiveConfig
		clientMu.Unlock()
	})

	done := make(chan error, 1)
	go func() {
		_, err := WriteKubeconfigForCurrentContext()
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, errKubeconfigNotRegular) {
			t.Fatalf("expected errKubeconfigNotRegular, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("single-file kubeconfig export blocked while opening a FIFO")
	}
}

func TestMergeAndSwitchContextErrorDoesNotPublishStaleReplacement(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("directory permissions do not prevent removal as root")
	}
	dir := t.TempDir()
	primary := writeKubeconfig(t, dir, "primary.yaml", "primary", []kubeEntry{
		{ctxName: "primary", userName: "u1", clusterName: "c1"},
	})
	existing := writeKubeconfig(t, dir, "workload.yaml", "workload", []kubeEntry{
		{ctxName: "workload", userName: "u2", clusterName: "c2"},
	})
	incoming := writeKubeconfig(t, t.TempDir(), "incoming.yaml", "workload", []kubeEntry{
		{ctxName: "workload", userName: "u2", clusterName: "c2"},
		{ctxName: "workload-canary", userName: "u3", clusterName: "c3"},
	})
	data, err := os.ReadFile(incoming)
	if err != nil {
		t.Fatalf("read incoming kubeconfig: %v", err)
	}
	registry, configs := buildContextRegistry([]string{primary, existing})
	delete(registry, "workload")

	clientMu.Lock()
	previousRegistry := contextRegistry
	previousConfigs := perFileConfigs
	previousMtimes := perFileMtimes
	previousPaths := kubeconfigPaths
	previousCAPI := capiKubeconfigs
	previousCount := totalContextCount
	contextRegistry = registry
	perFileConfigs = configs
	perFileMtimes = map[string]time.Time{}
	kubeconfigPaths = []string{primary}
	capiKubeconfigs = map[string]string{"workload": existing}
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

	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("lock directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if _, _, _, err := MergeAndSwitchContext(data, "workload", "workload"); err == nil {
		t.Fatal("stale source replacement unexpectedly succeeded")
	}

	clientMu.RLock()
	_, publishedCanary := perFileConfigs[existing].Contexts["workload-canary"]
	registeredPath := capiKubeconfigs["workload"]
	clientMu.RUnlock()
	if publishedCanary {
		t.Fatal("failed replacement published the rewritten config")
	}
	if registeredPath != existing {
		t.Fatalf("failed replacement changed CAPI registration to %q", registeredPath)
	}
}

func TestMergeAndSwitchContextReplacesFIFOWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	primary := writeKubeconfig(t, dir, "primary.yaml", "primary", []kubeEntry{
		{ctxName: "primary", userName: "u1", clusterName: "c1"},
	})
	existing := writeKubeconfig(t, dir, "workload.yaml", "workload", []kubeEntry{
		{ctxName: "workload", userName: "u2", clusterName: "c2"},
	})
	incoming := writeKubeconfig(t, t.TempDir(), "incoming.yaml", "workload", []kubeEntry{
		{ctxName: "workload", userName: "u2", clusterName: "c2"},
		{ctxName: "workload-canary", userName: "u3", clusterName: "c3"},
	})
	data, err := os.ReadFile(incoming)
	if err != nil {
		t.Fatalf("read incoming kubeconfig: %v", err)
	}
	registry, configs := buildContextRegistry([]string{primary, existing})

	clientMu.Lock()
	previousRegistry := contextRegistry
	previousConfigs := perFileConfigs
	previousMtimes := perFileMtimes
	previousPaths := kubeconfigPaths
	previousCAPI := capiKubeconfigs
	previousCount := totalContextCount
	contextRegistry = registry
	perFileConfigs = configs
	perFileMtimes = map[string]time.Time{}
	kubeconfigPaths = []string{primary, existing}
	capiKubeconfigs = map[string]string{"workload": existing}
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

	if err := os.Remove(existing); err != nil {
		t.Fatalf("remove existing kubeconfig: %v", err)
	}
	if err := syscall.Mkfifo(existing, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	type mergeResult struct {
		qualifiedName string
		path          string
		created       bool
		err           error
	}
	done := make(chan mergeResult, 1)
	go func() {
		qualifiedName, path, created, mergeErr := MergeAndSwitchContext(data, "workload", "workload")
		done <- mergeResult{qualifiedName: qualifiedName, path: path, created: created, err: mergeErr}
	}()

	var result mergeResult
	select {
	case result = <-done:
	case <-time.After(2 * time.Second):
		fd, unblockErr := syscall.Open(existing, syscall.O_RDONLY|syscall.O_NONBLOCK, 0)
		if unblockErr != nil {
			t.Fatalf("CAPI kubeconfig refresh blocked and FIFO could not be opened: %v", unblockErr)
		}
		select {
		case result = <-done:
		case <-time.After(time.Second):
			_ = syscall.Close(fd)
			t.Fatal("CAPI kubeconfig refresh remained blocked after opening the FIFO")
		}
		_ = syscall.Close(fd)
		t.Fatal("CAPI kubeconfig refresh blocked while opening a FIFO")
	}
	if result.err != nil {
		t.Fatalf("MergeAndSwitchContext: %v", result.err)
	}
	if !result.created || result.path == existing || result.qualifiedName != "workload" {
		t.Fatalf("replacement result = (%q, %q, %t)", result.qualifiedName, result.path, result.created)
	}
	t.Cleanup(func() { _ = os.Remove(result.path) })
	if info, err := os.Stat(result.path); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("replacement path is not a regular file: info=%v err=%v", info, err)
	}
	clientMu.RLock()
	registeredPath := capiKubeconfigs["workload"]
	clientMu.RUnlock()
	if registeredPath != result.path {
		t.Fatalf("registered CAPI path = %q, want %q", registeredPath, result.path)
	}
}

func TestMergeAndSwitchContextKeepsActiveFIFORegistered(t *testing.T) {
	dir := t.TempDir()
	primary := writeKubeconfig(t, dir, "primary.yaml", "primary", []kubeEntry{
		{ctxName: "primary", userName: "u1", clusterName: "c1"},
	})
	existing := writeKubeconfig(t, dir, "workload.yaml", "workload", []kubeEntry{
		{ctxName: "workload", userName: "u2", clusterName: "c2"},
	})
	incoming := writeKubeconfig(t, t.TempDir(), "incoming.yaml", "workload", []kubeEntry{
		{ctxName: "workload", userName: "u2", clusterName: "c2"},
	})
	data, err := os.ReadFile(incoming)
	if err != nil {
		t.Fatalf("read incoming kubeconfig: %v", err)
	}
	registry, configs := buildContextRegistry([]string{primary, existing})

	clientMu.Lock()
	previousRegistry := contextRegistry
	previousConfigs := perFileConfigs
	previousMtimes := perFileMtimes
	previousPaths := kubeconfigPaths
	previousCAPI := capiKubeconfigs
	previousActiveFile := activeSourceFile
	previousCount := totalContextCount
	contextRegistry = registry
	perFileConfigs = configs
	perFileMtimes = map[string]time.Time{}
	kubeconfigPaths = []string{primary, existing}
	capiKubeconfigs = map[string]string{"workload": existing}
	activeSourceFile = existing
	totalContextCount = len(registry)
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		contextRegistry = previousRegistry
		perFileConfigs = previousConfigs
		perFileMtimes = previousMtimes
		kubeconfigPaths = previousPaths
		capiKubeconfigs = previousCAPI
		activeSourceFile = previousActiveFile
		totalContextCount = previousCount
		clientMu.Unlock()
	})

	if err := os.Remove(existing); err != nil {
		t.Fatalf("remove existing kubeconfig: %v", err)
	}
	if err := syscall.Mkfifo(existing, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, _, _, mergeErr := MergeAndSwitchContext(data, "workload", "workload")
		done <- mergeErr
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "failed to refresh active CAPI kubeconfig") {
			t.Fatalf("active CAPI refresh error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("active CAPI kubeconfig refresh blocked while opening a FIFO")
	}

	clientMu.RLock()
	registeredPath := capiKubeconfigs["workload"]
	_, stillRegistered := contextRegistry["workload"]
	clientMu.RUnlock()
	if registeredPath != existing || !stillRegistered {
		t.Fatalf("active source was dropped: path=%q registered=%t", registeredPath, stillRegistered)
	}
	if info, err := os.Stat(existing); err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("active FIFO was replaced: info=%v err=%v", info, err)
	}
}

func TestHasUsableKubeconfigRejectsFIFOWithoutBlocking(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "primary.pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := hasUsableKubeconfig(fifo)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("hasUsableKubeconfig error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("primary kubeconfig validation blocked on a FIFO")
	}
}

func TestResolveKubeconfigSourcesSkipsFIFOSecondaryWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	regular := writeKubeconfig(t, dir, "regular.yaml", "prod", []kubeEntry{
		{ctxName: "prod", userName: "user", clusterName: "cluster"},
	})
	fifo := filepath.Join(dir, "secondary.pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	done := make(chan struct {
		sources kubeconfigSources
		err     error
	}, 1)
	go func() {
		sources, err := resolveKubeconfigSources(InitOptions{}, regular+string(os.PathListSeparator)+fifo, dir)
		done <- struct {
			sources kubeconfigSources
			err     error
		}{sources: sources, err: err}
	}()

	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("resolveKubeconfigSources: %v", result.err)
		}
		if len(result.sources.paths) != 1 || result.sources.paths[0] != regular {
			t.Fatalf("paths = %v, want [%q]", result.sources.paths, regular)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("KUBECONFIG resolution blocked on a FIFO secondary")
	}
}

func TestInitRejectsDefaultFIFOWithoutBlockingAfterInClusterFallback(t *testing.T) {
	home := t.TempDir()
	kubeDir := filepath.Join(home, ".kube")
	if err := os.Mkdir(kubeDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	fifo := filepath.Join(kubeDir, "config")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("KUBECONFIG", "")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")
	ResetTestState()
	t.Cleanup(ResetTestState)

	done := make(chan error, 1)
	go func() { done <- doInit(InitOptions{}) }()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("doInit error = %v", err)
		}
		if IsInCluster() {
			t.Fatal("failed local kubeconfig initialization was classified as in-cluster")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("default kubeconfig resolution blocked on a FIFO")
	}
}
