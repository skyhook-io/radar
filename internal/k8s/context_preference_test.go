package k8s

import (
	"path/filepath"
	"testing"
)

func TestPickInitialContextResolvesTheRecordedContext(t *testing.T) {
	dir := t.TempDir()
	f1 := writeKubeconfig(t, dir, "first.yaml", "from-first", []kubeEntry{
		{ctxName: "from-first", userName: "u1", clusterName: "c1"},
	})
	f2 := writeKubeconfig(t, dir, "second.yaml", "from-second", []kubeEntry{
		{ctxName: "from-second", userName: "u2", clusterName: "c2"},
	})

	paths := []string{f1, f2}
	registry, fileConfigs := buildContextRegistry(paths)
	saved := ContextRef{Name: "from-second", SourceFile: f2, InFileName: "from-second"}
	qName, entry, ok := pickInitialContext(paths, registry, fileConfigs, saved)
	if !ok {
		t.Fatal("pickInitialContext() found no context")
	}
	if qName != "from-second" {
		t.Errorf("qName = %q, want the recorded context %q", qName, "from-second")
	}
	if entry.SourceFile != f2 {
		t.Errorf("entry.SourceFile = %q, want %q", entry.SourceFile, f2)
	}
}

func TestPickInitialContextIgnoresAContextThatIsGone(t *testing.T) {
	dir := t.TempDir()
	f1 := writeKubeconfig(t, dir, "first.yaml", "from-first", []kubeEntry{
		{ctxName: "from-first", userName: "u1", clusterName: "c1"},
	})

	paths := []string{f1}
	registry, fileConfigs := buildContextRegistry(paths)
	saved := ContextRef{Name: "ghost", SourceFile: f1, InFileName: "ghost"}
	qName, _, ok := pickInitialContext(paths, registry, fileConfigs, saved)
	if !ok {
		t.Fatal("pickInitialContext() found no context")
	}
	if qName != "from-first" {
		t.Errorf("qName = %q, want the current-context fallback %q", qName, "from-first")
	}
}

// The scenario the source file exists for: two kubeconfigs define "dev", and a
// file added later takes over the unqualified name. Resolving by name alone
// would connect to the newcomer's cluster under the name the user last used.
func TestPickInitialContextPrefersTheRecordedSourceFileOverTheName(t *testing.T) {
	dir := t.TempDir()
	newcomer := writeKubeconfig(t, dir, "aaa.yaml", "dev", []kubeEntry{
		{ctxName: "dev", userName: "u1", clusterName: "someone-elses-cluster"},
	})
	worked := writeKubeconfig(t, dir, "bbb.yaml", "dev", []kubeEntry{
		{ctxName: "dev", userName: "u2", clusterName: "the-cluster-i-was-on"},
	})

	paths := []string{newcomer, worked}
	registry, fileConfigs := buildContextRegistry(paths)
	if entry := registry["dev"]; entry.SourceFile != newcomer {
		t.Fatalf("precondition: expected %q to own the unqualified name, got %q", newcomer, entry.SourceFile)
	}

	saved := ContextRef{Name: "dev", SourceFile: worked, InFileName: "dev"}
	_, entry, ok := pickInitialContext(paths, registry, fileConfigs, saved)
	if !ok {
		t.Fatal("pickInitialContext() found no context")
	}
	if entry.SourceFile != worked {
		t.Errorf("entry.SourceFile = %q, want the recorded file %q — the name was reassigned", entry.SourceFile, worked)
	}
}

// Once the recorded file is gone, a same-named context in another file is not
// evidence that it is the same cluster. Radar opens current-context instead of
// guessing — losing the convenience beats landing somewhere the user didn't pick.
func TestPickInitialContextDoesNotAdoptASameNamedContextFromAnotherFile(t *testing.T) {
	dir := t.TempDir()
	current := writeKubeconfig(t, dir, "first.yaml", "from-first", []kubeEntry{
		{ctxName: "from-first", userName: "u1", clusterName: "c1"},
	})
	impostor := writeKubeconfig(t, dir, "second.yaml", "", []kubeEntry{
		{ctxName: "prod", userName: "u2", clusterName: "someone-elses-prod"},
	})

	paths := []string{current, impostor}
	registry, fileConfigs := buildContextRegistry(paths)
	saved := ContextRef{Name: "prod", SourceFile: filepath.Join(dir, "deleted.yaml"), InFileName: "prod"}
	qName, entry, ok := pickInitialContext(paths, registry, fileConfigs, saved)
	if !ok {
		t.Fatal("pickInitialContext() found no context")
	}
	if qName != "from-first" || entry.SourceFile != current {
		t.Errorf("resolved %q from %q, want the current-context fallback %q from %q",
			qName, entry.SourceFile, "from-first", current)
	}
}

// A ref carrying only a name — nothing records one today — is not resolvable:
// the name is exactly the part another file can take over.
func TestPickInitialContextIgnoresANameOnlyReference(t *testing.T) {
	dir := t.TempDir()
	f1 := writeKubeconfig(t, dir, "first.yaml", "from-first", []kubeEntry{
		{ctxName: "from-first", userName: "u1", clusterName: "c1"},
		{ctxName: "other", userName: "u2", clusterName: "c2"},
	})

	paths := []string{f1}
	registry, fileConfigs := buildContextRegistry(paths)
	qName, _, ok := pickInitialContext(paths, registry, fileConfigs, ContextRef{Name: "other"})
	if !ok {
		t.Fatal("pickInitialContext() found no context")
	}
	if qName != "from-first" {
		t.Errorf("qName = %q, want the current-context fallback %q", qName, "from-first")
	}
}
