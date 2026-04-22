package k8s

import (
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"

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

// qualifyContextName returns a globally-unique context name for a context
// called `name` coming from file `path`. When `name` isn't taken yet, it
// returns `name` unchanged — most contexts across most files don't collide
// (it's the users/clusters that typically share names, not the context
// names themselves), and the user-visible dropdown should show the
// original names wherever possible. On collision it falls back to
// "<name> (<file-basename-without-ext>)", and then "<name> (<base> #N)"
// for further collisions from a third+ file with the same basename.
func qualifyContextName(registry map[string]contextEntry, name, path string) string {
	if _, taken := registry[name]; !taken {
		return name
	}
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
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
		base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
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
