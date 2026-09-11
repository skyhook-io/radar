package app

import (
	"log"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/settings"
)

// remembersLastContext reports whether this process may record the active
// cluster and start on it next time. Only cmd/desktop opts in.
//
// The auth and cloud-tunnel checks cannot fire today — Desktop configures
// neither. They stand guard on the invariant: the remembered cluster is one
// user's pick, so a Desktop serving several viewers must not record it.
func remembersLastContext(cfg AppConfig) bool {
	return cfg.RestoreLastDesktopContext && !cfg.AuthConfig.Enabled() && !cfg.CloudTunnelConfigured
}

// startupContextPreference resolves which context this run starts on: the one
// the last session ended on, or an empty ref to keep the kubeconfig's
// current-context.
func startupContextPreference(cfg AppConfig) (k8s.ContextRef, error) {
	if !remembersLastContext(cfg) {
		return k8s.ContextRef{}, nil
	}
	current, err := settings.LoadChecked()
	if err != nil {
		return k8s.ContextRef{}, err
	}
	saved := current.LastDesktopContext
	if saved == nil {
		return k8s.ContextRef{}, nil
	}
	return k8s.ContextRef{
		Name:       saved.Name,
		SourceFile: saved.SourceFile,
		InFileName: saved.InFileName,
	}, nil
}

// RegisterLastContextMemory records the initially selected context and every
// successful switch so the next start comes back on the cluster the user was
// working in. Recording selections rather than the exit is deliberate — a
// force-quit or crash would otherwise lose the pick.
func RegisterLastContextMemory(cfg AppConfig) {
	if !remembersLastContext(cfg) {
		return
	}

	preferred, err := startupContextPreference(cfg)
	current := k8s.ContextSourceFor(k8s.GetContextName())
	matchedPreference := !preferred.Empty() && preferred.SourceFile == current.SourceFile && preferred.InFileName == current.InFileName
	knownMissingPreference := err == nil && !preferred.Empty() && !matchedPreference && k8s.ContextReferenceKnownMissing(preferred)
	if err == nil && (preferred.Empty() || knownMissingPreference) {
		persistLastContext(cfg, current.Name)
	}

	protected := k8s.ContextRef{}
	if err != nil || (!preferred.Empty() && !matchedPreference && !knownMissingPreference) {
		protected = current
	}
	k8s.OnContextSwitch(lastContextSwitchRecorder(cfg, protected))
}

func lastContextSwitchRecorder(cfg AppConfig, protected k8s.ContextRef) k8s.ContextSwitchCallback {
	return func(name string) {
		candidate := k8s.ContextSourceFor(name)
		if !protected.Empty() && candidate.SourceFile == protected.SourceFile && candidate.InFileName == protected.InFileName {
			return
		}
		if persistLastContext(cfg, name) {
			protected = k8s.ContextRef{}
		}
	}
}

// ForgetLastContext drops the remembered cluster, so turning the memory off and
// back on later doesn't reopen a cluster the user stopped using long ago.
func ForgetLastContext() {
	current, err := settings.LoadChecked()
	if err != nil {
		log.Printf("[context] failed to read settings before clearing the remembered context: %v", err)
		return
	}
	if current.LastDesktopContext == nil {
		return
	}
	if _, err := settings.UpdateChecked(func(st *settings.Settings) {
		st.LastDesktopContext = nil
	}); err != nil {
		log.Printf("[context] failed to clear the remembered context: %v", err)
	}
}

func persistLastContext(cfg AppConfig, name string) bool {
	if name == "" || !remembersLastContext(cfg) {
		return false
	}
	// A CAPI workload cluster lives in a temp kubeconfig that's gone next run,
	// so its context could never be restored.
	if k8s.IsEphemeralContext(name) {
		return false
	}
	// Record the file too: across multiple kubeconfigs the display name alone
	// can be reassigned to another file's context between runs.
	ref := k8s.ContextSourceFor(name)
	if ref.Empty() {
		log.Printf("[context] not remembering context %q because its kubeconfig source could not be resolved", name)
		return false
	}
	if _, err := settings.UpdateChecked(func(st *settings.Settings) {
		st.LastDesktopContext = &settings.LastContext{
			Name:       ref.Name,
			SourceFile: ref.SourceFile,
			InFileName: ref.InFileName,
		}
	}); err != nil {
		log.Printf("[context] failed to remember last used context %q: %v", name, err)
		return false
	}
	return true
}
