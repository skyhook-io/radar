package k8s

import (
	"log"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/skyhook-io/radar/internal/errorlog"
)

// ContextRef identifies a kubeconfig context precisely enough to survive a
// restart: the name Radar displays, plus the file it came from and the name it
// carries inside that file.
//
// The name alone is ambiguous once more than one kubeconfig is loaded. Two
// files can define the same context name, and which one keeps the unqualified
// form depends on the order discoverKubeconfigs walks the directory — so
// adding a file can silently reassign the name to a different cluster. A
// caller persisting a context across restarts must carry the file too.
//
// SourceFile + InFileName are the identity; Name is the label Radar shows. A
// ref carrying only a Name resolves to nothing, because a name is exactly the
// thing another file can take over.
type ContextRef struct {
	Name       string
	SourceFile string
	InFileName string
}

// Empty reports whether the ref names nothing to resolve.
func (r ContextRef) Empty() bool {
	return r.SourceFile == "" || r.InFileName == ""
}

// ContextSourceFor returns the full reference for a context Radar currently
// knows, so callers persisting it can record where it came from. Outside the
// registry there is only one kubeconfig loaded, and it is the only place the
// active context can have come from.
func ContextSourceFor(name string) ContextRef {
	sourceFile, inFileName, ok := GetContextSource(name)
	if ok {
		return ContextRef{Name: name, SourceFile: sourceFile, InFileName: inFileName}
	}
	return ContextRef{Name: name}
}

// ContextReferenceKnownMissing reports whether Radar successfully loaded the
// recorded kubeconfig file and that file no longer defines the recorded
// context. A false result is intentionally inconclusive: the file may only be
// temporarily unavailable or outside this run's configured kubeconfig set.
func ContextReferenceKnownMissing(ref ContextRef) bool {
	if ref.Empty() {
		return false
	}

	clientMu.RLock()
	defer clientMu.RUnlock()

	if contextRegistry != nil {
		for path, cfg := range perFileConfigs {
			if path != ref.SourceFile {
				continue
			}
			_, exists := cfg.Contexts[ref.InFileName]
			return !exists
		}
		return false
	}

	if kubeconfigPath == "" || kubeconfigPath != ref.SourceFile {
		return false
	}
	cfg, err := clientcmd.LoadFromFile(kubeconfigPath)
	if err != nil {
		return false
	}
	_, exists := cfg.Contexts[ref.InFileName]
	return !exists
}

// IsEphemeralContext reports whether a context lives in a temp kubeconfig Radar
// wrote itself for a CAPI workload cluster. That file is gone on the next run,
// so callers that persist a context across restarts must skip those.
func IsEphemeralContext(name string) bool {
	clientMu.RLock()
	defer clientMu.RUnlock()

	entry, ok := contextRegistry[name]
	if !ok {
		return false
	}
	for _, tmpPath := range capiKubeconfigs {
		if tmpPath == entry.SourceFile {
			return true
		}
	}
	return false
}

// applyContextPreference points overrides at the preferred context when the
// kubeconfig at path is the file it was recorded from and still defines a
// usable context.
// Validating first matters twice: a context that has since been renamed or
// deleted would otherwise fail the whole startup, and the override has to be in
// place before the deferred loader builds its inner config — it captures
// CurrentContext on the first RawConfig()/ClientConfig() call and caches it.
func applyContextPreference(path string, preferred ContextRef, overrides *clientcmd.ConfigOverrides) {
	if preferred.Empty() || preferred.SourceFile != path {
		reportContextPreferenceMiss(preferred)
		return
	}
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil {
		reportContextPreferenceMiss(preferred)
		return
	}
	if clientcmd.ConfirmUsable(*cfg, preferred.InFileName) != nil {
		reportContextPreferenceMiss(preferred)
		return
	}
	overrides.CurrentContext = preferred.InFileName
}

// reportContextPreferenceMiss explains why Radar did not come up where the last
// session left it. Falling back to current-context is the safe answer — the
// name alone is what another kubeconfig can take over — but a silent redirect
// leaves the user staring at a cluster they didn't pick, so it goes to the
// diagnostics surface and not only to the log.
func reportContextPreferenceMiss(preferred ContextRef) {
	name := preferred.Name
	if name == "" {
		name = preferred.InFileName
	}
	if name == "" {
		return
	}
	log.Printf("[k8s-init] last used context %q is missing or unusable where it was recorded; using current-context", name)
	errorlog.Record("k8s-init", "warning",
		"could not reopen on %q: that context is missing or unusable in the kubeconfig it was recorded from. Starting on the kubeconfig's current-context instead.",
		name)
}
