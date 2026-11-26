package helm

import (
	"fmt"
	"sync"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/release"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

// Reopening kubeconfig here could retarget a Helm action after its connection
// was captured. Discovery and mapping use the same config as release storage.
type restConfigGetter struct {
	config     *rest.Config
	namespace  string
	user       *pkgauth.User
	connection *k8s.Connection

	mu        sync.Mutex
	discovery discovery.CachedDiscoveryInterface
	mapper    meta.RESTMapper
}

func newRESTConfigGetter(cfg *rest.Config, namespace string, user *pkgauth.User) *restConfigGetter {
	if user != nil {
		// Copy so a future Helm/middleware mutation can't bleed into a
		// different user's request via the captured slices.
		u := *user
		u.Groups = append([]string(nil), user.Groups...)
		user = &u
	}
	return &restConfigGetter{
		config:    cfg,
		namespace: namespace,
		user:      user,
	}
}

func (g *restConfigGetter) ToRESTConfig() (*rest.Config, error) {
	if g.config == nil {
		return nil, fmt.Errorf("no in-cluster rest config available")
	}
	if g.user != nil {
		return k8s.UserRESTConfig(g.config, g.user), nil
	}
	return rest.CopyConfig(g.config), nil
}

func (g *restConfigGetter) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	if dc, _ := g.connection.CachedRESTDiscovery(); dc != nil {
		return dc, nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.discovery != nil {
		return g.discovery, nil
	}
	cfg, err := g.ToRESTConfig()
	if err != nil {
		return nil, err
	}
	// Match kubectl's QPS/Burst defaults (50/100). The client-go default
	// (5/10) is too low for Helm operations that walk many resources.
	cfg.QPS = 50
	cfg.Burst = 100
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, err
	}
	g.discovery = memory.NewMemCacheClient(dc)
	return g.discovery, nil
}

func (g *restConfigGetter) ToRESTMapper() (meta.RESTMapper, error) {
	if _, mapper := g.connection.CachedRESTDiscovery(); mapper != nil {
		return mapper, nil
	}
	// ToDiscoveryClient does its own locking; calling it under g.mu would
	// deadlock since sync.Mutex is non-reentrant. Take the discovery
	// client first (idempotent), then lock for the mapper cache.
	dc, err := g.ToDiscoveryClient()
	if err != nil {
		return nil, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.mapper != nil {
		return g.mapper, nil
	}
	deferred := restmapper.NewDeferredDiscoveryRESTMapper(dc)
	g.mapper = restmapper.NewShortcutExpander(deferred, dc, func(string) {})
	return g.mapper, nil
}

func (g *restConfigGetter) ToRawKubeConfigLoader() clientcmd.ClientConfig {
	// Helm calls Namespace() on this loader (in EnvSettings + the kube
	// client) to resolve the default namespace. We have no real kubeconfig
	// to surface, so return an empty Config with just the namespace
	// override.
	return clientcmd.NewDefaultClientConfig(
		clientcmdapi.Config{},
		&clientcmd.ConfigOverrides{
			Context: clientcmdapi.Context{Namespace: g.namespace},
		},
	)
}

func runUpgradeWithCachedDiscovery(cfg *action.Configuration, upgrade *action.Upgrade, name string, ch *chart.Chart, values map[string]any) (*release.Release, error) {
	if err := loadCachedCapabilities(cfg); err != nil {
		return nil, err
	}
	return upgrade.Run(name, ch, values)
}

// Helm otherwise invalidates the discovery cache every time it loads chart capabilities.
func loadCachedCapabilities(cfg *action.Configuration) error {
	getter, ok := cfg.RESTClientGetter.(*restConfigGetter)
	if !ok || cfg.Capabilities != nil {
		return nil
	}
	dc, _ := getter.connection.CachedRESTDiscovery()
	if dc == nil {
		return nil
	}
	version, err := dc.ServerVersion()
	if err != nil {
		return fmt.Errorf("could not get server version from Kubernetes: %w", err)
	}
	apiVersions, err := action.GetVersionSet(dc)
	if err != nil {
		if !discovery.IsGroupDiscoveryFailedError(err) {
			return err
		}
		cfg.Log("WARNING: The Kubernetes server has an orphaned API service. Server reports: %s", err)
	}
	cfg.Capabilities = &chartutil.Capabilities{
		APIVersions: apiVersions,
		KubeVersion: chartutil.KubeVersion{Version: version.GitVersion, Major: version.Major, Minor: version.Minor},
		HelmVersion: chartutil.DefaultCapabilities.HelmVersion,
	}
	return nil
}
