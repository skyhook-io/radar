package k8s

import (
	"errors"
	"os"
	"testing"

	"k8s.io/client-go/util/homedir"
)

func TestRelationshipContextSourceResolvesKubeconfigList(t *testing.T) {
	dir := t.TempDir()
	first := writeKubeconfig(t, dir, "a.yaml", "first", []kubeEntry{
		{ctxName: "first", clusterName: "shared", userName: "shared"},
		{ctxName: "duplicate", clusterName: "shared", userName: "shared"},
	})
	second := writeKubeconfig(t, dir, "b.yaml", "second", []kubeEntry{
		{ctxName: "second", clusterName: "shared", userName: "shared"},
		{ctxName: "duplicate", clusterName: "shared", userName: "shared"},
	})
	t.Setenv("KUBECONFIG", first+string(os.PathListSeparator)+second)
	for _, tc := range []struct{ name, source, inFile string }{
		{"first", first, "first"},
		{"second", second, "second"},
		{"duplicate", first, "duplicate"},
		{"duplicate (b)", second, "duplicate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			preferred, err := ResolveContextSource(InitOptions{}, tc.name)
			if err != nil {
				t.Fatal(err)
			}
			if preferred != (ContextRef{Name: tc.name, SourceFile: tc.source, InFileName: tc.inFile}) {
				t.Fatalf("resolved %+v", preferred)
			}
			// Follow Initialize's source selection without building clients.
			sources, err := resolveKubeconfigSources(InitOptions{PreferredContext: preferred}, os.Getenv("KUBECONFIG"), homedir.HomeDir())
			if err != nil {
				t.Fatal(err)
			}
			registry, configs := buildContextRegistry(sources.paths)
			name, entry, ok := pickInitialContext(sources.paths, registry, configs, preferred)
			if !ok || name != tc.name || entry.SourceFile != tc.source || entry.InFileName != tc.inFile {
				t.Fatalf("startup selected %q, %+v, %v", name, entry, ok)
			}
		})
	}
	if _, err := ResolveContextSource(InitOptions{}, "missing"); !errors.Is(err, errKubeconfigContextNotFound) {
		t.Fatalf("missing context: %v", err)
	}
	preferred, err := ResolveContextSource(InitOptions{KubeconfigPath: second}, "second")
	if err != nil || preferred.SourceFile != second {
		t.Fatalf("explicit single-file source: %+v, %v", preferred, err)
	}
	t.Setenv("KUBECONFIG", second)
	preferred, err = ResolveContextSource(InitOptions{}, "second")
	if err != nil || preferred.SourceFile != second || preferred.InFileName != "second" {
		t.Fatalf("single-file environment: %+v, %v", preferred, err)
	}
}
