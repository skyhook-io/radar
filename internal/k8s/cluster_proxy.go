package k8s

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"

	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/settings"
	"github.com/skyhook-io/radar/pkg/prom"
)

// savedClusterProxy is the proxy URL saved for a context binding, "" when none.
// Proxies are keyed by binding (kubeconfig file + the context's name in it),
// not the display name, which gains a source suffix when another kubeconfig
// adds a context of the same name.
var savedClusterProxy = func(binding string) (string, error) {
	if binding == "" {
		return "", nil
	}
	proxies, err := settings.ClusterProxies()
	return proxies[binding], err
}

// ContextBindingFor returns the binding of a context by its display name, ""
// when Radar doesn't know the context.
func ContextBindingFor(name string) string {
	clientMu.RLock()
	registry, singlePath := contextRegistry, kubeconfigPath
	clientMu.RUnlock()
	if registry != nil {
		entry, ok := registry[name]
		if !ok {
			return ""
		}
		return sourceContextBinding(entry.SourceFile, entry.InFileName)
	}
	return sourceContextBinding(singlePath, name)
}

// ParseClusterProxyURL accepts the URLs client-go accepts for a kubeconfig
// proxy-url: http, https and socks5.
func ParseClusterProxyURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, errors.New("proxy URL is not a valid URL")
	}
	switch u.Scheme {
	case "http", "https", "socks5":
	default:
		return nil, errors.New("proxy URL must start with http://, https:// or socks5://")
	}
	if u.Hostname() == "" {
		return nil, errors.New("proxy URL must include a host")
	}
	return u, nil
}

// applyClusterProxy routes config through the proxy saved for binding and
// returns it ("" when none). It takes precedence over the kubeconfig's
// proxy-url and HTTPS_PROXY, as rest.Config.Proxy does; client-go's exec,
// port-forward and websocket transports read the same field.
func applyClusterProxy(config *rest.Config, binding, contextName string) (string, error) {
	raw, err := savedClusterProxy(binding)
	if err != nil {
		return "", fmt.Errorf("can't read the proxies saved in Radar: %w", err)
	}
	if raw == "" {
		return "", nil
	}
	u, err := ParseClusterProxyURL(raw)
	if err != nil {
		return "", fmt.Errorf("the proxy saved in Radar for context %q is invalid: %w", contextName, err)
	}
	config.Proxy = http.ProxyURL(u)
	log.Printf("[k8s] Using the proxy saved in Radar for context %q: %s", contextName, prom.SafeAddress(raw))
	return raw, nil
}

// TestContextProxy checks that a context's API server answers through
// proxyURL, without touching the active connection.
func TestContextProxy(ctx context.Context, name, proxyURL string) error {
	if IsInCluster() {
		return errors.New("a cluster proxy can't be set in-cluster")
	}
	u, err := ParseClusterProxyURL(proxyURL)
	if err != nil {
		return err
	}
	_, _, kubeConfig, rawConfig, kubeContext, err := loadContextSource(name)
	if err != nil {
		return err
	}
	config, err := kubeConfig.ClientConfig()
	if err != nil {
		return fmt.Errorf("%w for %q: %s", errKubeconfigClientSetupFailed, name, kubeconfigDiagnosticError(err))
	}
	config.Proxy = http.ProxyURL(u)
	usesExec := false
	if ai, ok := rawConfig.AuthInfos[kubeContext.AuthInfo]; ok && ai.Exec != nil {
		usesExec = true
	}
	probeCtx, cancel := context.WithTimeout(ctx, connectionTestTimeoutFor(usesExec))
	defer cancel()
	return testConnection(probeCtx, config, usesExec)
}

// ContextExists reports whether a kubeconfig context of this display name exists.
func ContextExists(name string) bool {
	_, _, _, _, _, err := loadContextSource(name)
	return err == nil
}

// IsContextNotFound reports whether err says the named kubeconfig context doesn't exist.
func IsContextNotFound(err error) bool {
	return errors.Is(err, errKubeconfigContextNotFound)
}

// IsClientSetupFailed reports whether err says the kubeconfig couldn't be turned into a client.
func IsClientSetupFailed(err error) bool {
	return errors.Is(err, errKubeconfigClientSetupFailed)
}
