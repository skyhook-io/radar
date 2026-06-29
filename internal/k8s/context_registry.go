package k8s

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/skyhook-io/radar/internal/errorlog"
)

// setupIsolatedLoad populates contextRegistry, perFileConfigs, and contextName
// from the given kubeconfig files, then returns LoadingRules + Overrides that
// load *only* the initial file via ExplicitPath. Only called from doInit,
// inside initOnce, so no concurrent readers exist yet — writes to the globals
// are safe without clientMu.
//
// This is how Radar avoids client-go's Precedence merge when there's more
// than one kubeconfig file: each file stays an island. A SwitchContext later
// looks up the target entry in the registry and loads that one file, so
// shared user/cluster names across files never collide — see issue #519.
func setupIsolatedLoad(paths []string) (
	*clientcmd.ClientConfigLoadingRules,
	*clientcmd.ConfigOverrides,
	error,
) {
	registry, fileConfigs := buildContextRegistry(paths)
	if len(registry) == 0 {
		return nil, nil, fmt.Errorf("no contexts found across %d kubeconfig files", len(paths))
	}
	qName, entry, ok := pickInitialContext(paths, registry, fileConfigs)
	if !ok {
		return nil, nil, fmt.Errorf("no usable context found across %d kubeconfig files", len(paths))
	}
	contextRegistry = registry
	perFileConfigs = fileConfigs
	perFileMtimes = make(map[string]time.Time, len(paths))
	for _, p := range paths {
		if info, err := os.Stat(p); err == nil {
			perFileMtimes[p] = info.ModTime()
		}
	}
	contextName = qName
	return &clientcmd.ClientConfigLoadingRules{ExplicitPath: entry.SourceFile},
		&clientcmd.ConfigOverrides{CurrentContext: entry.InFileName},
		nil
}

// contextEntry locates a kubeconfig context by its source file and the name
// it has inside that file. The registry uses this to route SwitchContext to
// a specific file (loaded in isolation via ExplicitPath) so that clusters,
// users, and contexts with shared names across files don't clobber each
// other via client-go's Precedence merge.
type contextEntry struct {
	SourceFile string // absolute path to the kubeconfig on disk
	InFileName string // context name as it appears inside SourceFile
}

// buildContextRegistry loads each kubeconfig file in isolation and produces:
//   - registry: user-facing context name → (file, original name in that file)
//   - fileConfigs: per-file parsed Configs so GetAvailableContexts can
//     enumerate contexts without re-reading disk
//
// When two files contain a context with the same name, the second one is
// qualified with its source filename (e.g. "kas-107 (file2)"). Users and
// clusters are never renamed — they stay scoped to their own file and are
// only referenced via ExplicitPath loads, so cross-file name collisions
// on AuthInfo/Cluster maps are structurally impossible.
//
// Files that fail to parse are skipped with a log line; the caller already
// ran isValidKubeconfig() during directory discovery, so this is defense
// against files that became unreadable between scan and load.
func buildContextRegistry(paths []string) (map[string]contextEntry, map[string]*clientcmdapi.Config) {
	registry := make(map[string]contextEntry)
	fileConfigs := make(map[string]*clientcmdapi.Config)
	for _, path := range paths {
		cfg, err := clientcmd.LoadFromFile(path)
		if err != nil {
			// Non-fatal: skip and continue. discoverKubeconfigs has
			// already validated these, so a failure here is surprising
			// enough to log. The file's basename is safe to surface
			// via errorlog (see scrubPathError's privacy contract).
			log.Printf("[k8s-init] skipping kubeconfig %q during registry build: %v", filepath.Base(path), err)
			errorlog.Record("k8s-init", "warning",
				"kubeconfig %q failed to load during registry build: %s",
				filepath.Base(path), scrubPathError(err))
			continue
		}
		fileConfigs[path] = cfg
		for name := range cfg.Contexts {
			qName := qualifyContextName(registry, name, path)
			registry[qName] = contextEntry{
				SourceFile: path,
				InFileName: name,
			}
		}
	}
	return registry, fileConfigs
}

// kubeconfigSourceLabel returns a short, human-recognisable label for the
// kubeconfig file at `path`. Used both as the disambiguation suffix in
// qualifyContextName and as the frontend-facing Source on ContextInfo.
//
// When the basename is generic (`config` / `kubeconfig`) the parent
// directory carries the meaning — users typically organise as
// ~/.kube-cluster-paris/config, where "config" alone disambiguates
// nothing. We fall back to the parent dir name (leading dot stripped)
// in that case. Otherwise the file basename without extension is the
// right label (e.g. "paris.yaml" -> "paris").
func kubeconfigSourceLabel(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if base == "config" || base == "kubeconfig" {
		parent := filepath.Base(filepath.Dir(path))
		if parent != "" && parent != "." && parent != string(filepath.Separator) {
			return strings.TrimPrefix(parent, ".")
		}
	}
	return base
}

// qualifyContextName returns a globally-unique context name for a context
// called `name` coming from file `path`. When `name` isn't taken yet, it
// returns `name` unchanged — most contexts across most files don't collide
// (it's the users/clusters that typically share names, not the context
// names themselves), and the user-visible dropdown should show the
// original names wherever possible. On collision it falls back to
// "<name> (<source-label>)", and then "<name> (<source-label> #N)"
// for further collisions from a third+ file with the same source label.
func qualifyContextName(registry map[string]contextEntry, name, path string) string {
	if _, taken := registry[name]; !taken {
		return name
	}
	base := kubeconfigSourceLabel(path)
	qualified := fmt.Sprintf("%s (%s)", name, base)
	if _, taken := registry[qualified]; !taken {
		return qualified
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s (%s #%d)", name, base, i)
		if _, taken := registry[candidate]; !taken {
			return candidate
		}
	}
}

// pickInitialContext chooses which context Radar should start in when
// using per-file isolated loading. It walks `paths` in order and returns
// the first non-empty CurrentContext from any file — matching client-go's
// Precedence merge, which picks the first file's CurrentContext. If no
// file declares a CurrentContext, it returns the first context from the
// first file with any contexts at all, so Radar can still come up.
//
// Returns (qualifiedName, entry, found). `found == false` means there
// isn't a single usable context anywhere — the caller should surface this
// as an init error.
func pickInitialContext(
	paths []string,
	registry map[string]contextEntry,
	fileConfigs map[string]*clientcmdapi.Config,
) (string, contextEntry, bool) {
	// First pass: honor CurrentContext in file order.
	for _, path := range paths {
		cfg, ok := fileConfigs[path]
		if !ok || cfg.CurrentContext == "" {
			continue
		}
		// Find the registry entry whose (SourceFile, InFileName) matches.
		for qName, entry := range registry {
			if entry.SourceFile == path && entry.InFileName == cfg.CurrentContext {
				return qName, entry, true
			}
		}
	}
	// Fallback: any context from the first file that has one.
	for _, path := range paths {
		cfg, ok := fileConfigs[path]
		if !ok {
			continue
		}
		for name := range cfg.Contexts {
			for qName, entry := range registry {
				if entry.SourceFile == path && entry.InFileName == name {
					return qName, entry, true
				}
			}
		}
	}
	return "", contextEntry{}, false
}

// refreshContextRegistry reconciles the in-memory contextRegistry +
// perFileConfigs against what's actually on disk RIGHT NOW. Returns
// new map values (registry, fileConfigs, fileMtimes) plus a `changed`
// flag. When `changed` is false the returned maps are guaranteed to
// equal the inputs and callers can keep the originals.
//
// The original registry is built ONCE in setupIsolatedLoad and was
// never refreshed in multi-file mode. That left the cluster
// dropdown showing entries for:
//
//   - kubeconfig files the user removed from a watched directory,
//   - kubeconfig files rewritten by `kubectl config delete-context`,
//   - kubeconfig files written then deleted by CAPI when a managed
//     cluster was destroyed.
//
// All three look like "junk clusters" to the user (the entry is
// there but selecting it errors out). This helper is the surgical
// fix: same per-file isolation guarantees as buildContextRegistry,
// just incremental — it ONLY touches files whose mtime moved or
// whose path no longer exists on disk.
//
// Concurrency: returns NEW maps instead of mutating in place so
// callers (GetAvailableContexts under Lock, plus snapshot-style
// readers like SwitchContext under RLock) can atomically swap the
// package globals. The maps the inputs point at are never mutated,
// preserving the post-init "publish once, never modify" invariant
// that the snapshot-then-read pattern relies on.
func refreshContextRegistry(
	registry map[string]contextEntry,
	fileConfigs map[string]*clientcmdapi.Config,
	fileMtimes map[string]time.Time,
) (
	map[string]contextEntry,
	map[string]*clientcmdapi.Config,
	map[string]time.Time,
	bool,
) {
	if registry == nil {
		return registry, fileConfigs, fileMtimes, false
	}
	// Defensive nil-check on fileMtimes: callers
	// (GetAvailableContexts) already lazy-init this, but the helper
	// is exported and a future caller that forgets would otherwise
	// panic on the first `fileMtimes[path] = mtime` write below.
	if fileMtimes == nil {
		return registry, fileConfigs, fileMtimes, false
	}
	// Group registry entries by source file so we can decide
	// per-file: keep, re-parse, or drop everything pointing at it.
	// Seed byFile from BOTH the registry AND fileMtimes — if a
	// previous refresh dropped every context for a file (e.g. user
	// removed all kubectl-config-delete-context'd from a single
	// file), the file's path stays in fileMtimes but no longer
	// appears in registry. Without seeding from fileMtimes, that
	// file would never be re-stat'd and any newly-added contexts
	// to it would be invisible until the user restarted Radar.
	byFile := make(map[string][]string)
	for qName, entry := range registry {
		byFile[entry.SourceFile] = append(byFile[entry.SourceFile], qName)
	}
	for path := range fileMtimes {
		if _, ok := byFile[path]; !ok {
			byFile[path] = nil
		}
	}
	// Walk each file once, deciding whether to keep / drop / re-parse.
	// We start from a lazy "no changes" state and only allocate fresh
	// maps when something actually changes. This keeps the steady-state
	// cost (most calls find nothing to do) at a few stat()s.
	newRegistry := registry
	newFileConfigs := fileConfigs
	newFileMtimes := fileMtimes
	changed := false
	cloneOnce := func() {
		if changed {
			return
		}
		changed = true
		nr := make(map[string]contextEntry, len(registry))
		for k, v := range registry {
			nr[k] = v
		}
		nfc := make(map[string]*clientcmdapi.Config, len(fileConfigs))
		for k, v := range fileConfigs {
			nfc[k] = v
		}
		nm := make(map[string]time.Time, len(fileMtimes))
		for k, v := range fileMtimes {
			nm[k] = v
		}
		newRegistry = nr
		newFileConfigs = nfc
		newFileMtimes = nm
	}
	for path, qNames := range byFile {
		info, statErr := os.Stat(path)
		if statErr != nil {
			// File is gone (or unreadable). Drop every registry
			// entry pointing at it AND its cached config. This
			// is the CAPI-cluster-destroyed and
			// "user removed file from kubeconfig dir" cases.
			cloneOnce()
			for _, qName := range qNames {
				delete(newRegistry, qName)
			}
			delete(newFileConfigs, path)
			delete(newFileMtimes, path)
			continue
		}
		mtime := info.ModTime()
		if cached, ok := fileMtimes[path]; ok && cached.Equal(mtime) {
			// Unchanged — keep the cached parse + entries.
			continue
		}
		// File is new to us OR has been rewritten on disk. Re-parse
		// and rebuild ONLY this file's entries.
		cfg, err := clientcmd.LoadFromFile(path)
		if err != nil {
			// Couldn't parse the rewritten file. Don't drop the
			// existing entries — the user may be mid-edit, and
			// silently pruning the dropdown while they save would
			// be more confusing than a stale entry. Log and skip.
			log.Printf("[k8s-init] refresh: skipping kubeconfig %q (parse failed): %v",
				filepath.Base(path), err)
			errorlog.Record("k8s-init", "warning",
				"refresh: kubeconfig %q failed to load: %s",
				filepath.Base(path), scrubPathError(err))
			continue
		}
		cloneOnce()
		newFileConfigs[path] = cfg
		newFileMtimes[path] = mtime
		// Replace this file's entries in the registry. Names that
		// are no longer in the file get dropped; new ones are added.
		liveNames := make(map[string]struct{}, len(cfg.Contexts))
		for name := range cfg.Contexts {
			liveNames[name] = struct{}{}
		}
		for _, qName := range qNames {
			if _, alive := liveNames[newRegistry[qName].InFileName]; !alive {
				delete(newRegistry, qName)
			}
		}
		// Add any contexts that are new in this file. We deliberately
		// re-use qualifyContextName to keep the cross-file collision
		// behaviour consistent with the initial build.
		for name := range cfg.Contexts {
			already := false
			for _, qName := range qNames {
				if e, ok := newRegistry[qName]; ok && e.SourceFile == path && e.InFileName == name {
					already = true
					break
				}
			}
			if already {
				continue
			}
			qName := qualifyContextName(newRegistry, name, path)
			newRegistry[qName] = contextEntry{
				SourceFile: path,
				InFileName: name,
			}
		}
	}
	return newRegistry, newFileConfigs, newFileMtimes, changed
}

// aggregateExecPluginCommands walks every context across every per-file
// config and returns the unique sorted set of exec-plugin basenames plus
// the list of AuthInfos that reference exec blocks with empty Commands.
// Mirrors collectExecPluginCommands for single-Config usage but handles
// the multi-file case — deduplicating across files and scoping AuthInfo
// names by file so a "me" with empty Command in file-a doesn't get
// confused with a valid "me" in file-b.
func aggregateExecPluginCommands(
	paths []string,
	fileConfigs map[string]*clientcmdapi.Config,
) (cmds []string, emptyCommandAuthInfos []string) {
	seenCmds := make(map[string]struct{})
	seenEmpty := make(map[string]struct{})
	for _, path := range paths {
		cfg, ok := fileConfigs[path]
		if !ok {
			continue
		}
		fileCmds, fileEmpty := collectExecPluginCommands(cfg)
		for _, c := range fileCmds {
			if _, dup := seenCmds[c]; dup {
				continue
			}
			seenCmds[c] = struct{}{}
			cmds = append(cmds, c)
		}
		base := kubeconfigSourceLabel(path)
		for _, ai := range fileEmpty {
			// Scope by file so diagnostics aren't ambiguous when the same
			// AuthInfo name appears in multiple files.
			scoped := fmt.Sprintf("%s (%s)", ai, base)
			if _, dup := seenEmpty[scoped]; dup {
				continue
			}
			seenEmpty[scoped] = struct{}{}
			emptyCommandAuthInfos = append(emptyCommandAuthInfos, scoped)
		}
	}
	// Sort for stable output (matches collectExecPluginCommands' contract).
	sort.Strings(cmds)
	sort.Strings(emptyCommandAuthInfos)
	return cmds, emptyCommandAuthInfos
}
