package helm

import (
	"fmt"
	"sync"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// restConfigGetter implements genericclioptions.RESTClientGetter by
// reusing a *rest.Config Radar already resolved at boot, instead of
// asking Helm to re-resolve from kubeconfig.
//
// Used whenever no single kubeconfig file path is available to hand
// Helm. Two cases trigger this:
//
//  1. In-cluster (Hub mode, OSS Helm-chart deploy): no ~/.kube/config in
//     the pod. Helm's default ConfigFlags would fall through to
//     localhost:8080.
//  2. Multi-source kubeconfig on a laptop (--kubeconfig-dir or a
//     KUBECONFIG env var with multiple paths): Radar uses isolated
//     per-file loading, so there isn't one path Helm can re-parse —
//     it would silently pick a different file from its own precedence.
//
// In both cases we hand Helm the exact rest.Config Radar's K8s client
// is already using, so Helm can't drift to a different cluster or
// fabricate a localhost target.
type restConfigGetter struct {
	config         *rest.Config
	namespace      string
	impersonate    string
	impersonateGrp []string

	mu        sync.Mutex
	discovery discovery.CachedDiscoveryInterface
	mapper    meta.RESTMapper
}

func newRESTConfigGetter(cfg *rest.Config, namespace, user string, groups []string) *restConfigGetter {
	return &restConfigGetter{
		config:         cfg,
		namespace:      namespace,
		impersonate:    user,
		impersonateGrp: groups,
	}
}

func (g *restConfigGetter) ToRESTConfig() (*rest.Config, error) {
	if g.config == nil {
		return nil, fmt.Errorf("no in-cluster rest config available")
	}
	cfg := rest.CopyConfig(g.config)
	if g.impersonate != "" {
		cfg.Impersonate = rest.ImpersonationConfig{
			UserName: g.impersonate,
			Groups:   g.impersonateGrp,
		}
	}
	return cfg, nil
}

func (g *restConfigGetter) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.discovery != nil {
		return g.discovery, nil
	}
	cfg, err := g.ToRESTConfig()
	if err != nil {
		return nil, err
	}
	// Helm fans out discovery calls during chart rendering / list ops.
	cfg.Burst = 100
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, err
	}
	g.discovery = memory.NewMemCacheClient(dc)
	return g.discovery, nil
}

func (g *restConfigGetter) ToRESTMapper() (meta.RESTMapper, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.mapper != nil {
		return g.mapper, nil
	}
	dc, err := g.ToDiscoveryClient()
	if err != nil {
		return nil, err
	}
	deferred := restmapper.NewDeferredDiscoveryRESTMapper(dc)
	g.mapper = restmapper.NewShortcutExpander(deferred, dc, func(string) {})
	return g.mapper, nil
}

func (g *restConfigGetter) ToRawKubeConfigLoader() clientcmd.ClientConfig {
	// Helm uses this primarily for namespace resolution. We have no real
	// kubeconfig to surface; return an empty Config with the namespace
	// override so action.Configuration.Init's fallback path picks it up.
	return clientcmd.NewDefaultClientConfig(
		clientcmdapi.Config{},
		&clientcmd.ConfigOverrides{
			Context: clientcmdapi.Context{Namespace: g.namespace},
		},
	)
}
