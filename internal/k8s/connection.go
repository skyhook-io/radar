package k8s

import (
	"context"
	"sync/atomic"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Connection owns the clients, config, discovery, permissions and typed
// informer cache for one cluster context. A switch installs a new connection,
// so a request that captures one keeps the same clients and resource mapping
// even if a switch lands mid-request. A rebuild on the same clients (rescope, recovery)
// also starts from a new one, so state built for the old admission stays there.
type Connection struct {
	config                                   *rest.Config
	clients                                  *sharedKubernetesClients
	contextName, contextBinding, clusterName string
	contextNamespace                         string
	perms                                    atomic.Pointer[permissionState]
	cache                                    atomic.Pointer[connectionCache]
	discovery                                atomic.Pointer[ResourceDiscovery]
	// id is written once, when the connection is admitted, under
	// connectionStatusMu; it stays empty for a connection never admitted.
	id string
}

// Current returns the installed connection, or nil before the first install.
// A caller that needs more than one of its clients or fields takes it once, so
// they all come from the same install.
func Current() *Connection {
	return installedConn.Load()
}

func boundOrCurrent(ctx context.Context) *Connection {
	if c := ConnectionFrom(ctx); c != nil {
		return c
	}
	return Current()
}

func installedConnectionLocked(config *rest.Config, clients *sharedKubernetesClients) *Connection {
	return &Connection{config: config, clients: clients, contextName: contextName, contextBinding: contextBinding, clusterName: clusterName, contextNamespace: contextNamespace}
}

// fresh returns a new, unadmitted connection on the same clients, with no
// discovery, permissions or informer cache yet.
func (c *Connection) fresh() *Connection {
	if c == nil {
		return &Connection{}
	}
	return &Connection{config: c.config, clients: c.clients, contextName: c.contextName, contextBinding: c.contextBinding, clusterName: c.clusterName, contextNamespace: c.contextNamespace}
}

func (c *Connection) Client() *kubernetes.Clientset {
	if c == nil || c.clients == nil {
		return nil
	}
	return c.clients.clientset
}

// ClientInterface returns the clientset as kubernetes.Interface, or an untyped
// nil when there is none. Store this rather than Client() in an interface: a
// nil *Clientset wrapped in one is non-nil, so downstream "client == nil"
// guards never fire and the first API call panics.
func (c *Connection) ClientInterface() kubernetes.Interface {
	if client := c.Client(); client != nil {
		return client
	}
	return nil
}

// Config returns the connection's REST config, shared by every holder: copy it
// (ConfigCopy) before changing anything.
func (c *Connection) Config() *rest.Config {
	if c == nil {
		return nil
	}
	return c.config
}

func (c *Connection) ConfigCopy() *rest.Config {
	if c.Config() == nil {
		return nil
	}
	return rest.CopyConfig(c.config)
}

// Dynamic returns the dynamic client, or an untyped nil (see ClientInterface).
func (c *Connection) Dynamic() dynamic.Interface {
	if c == nil || c.clients == nil {
		return nil
	}
	return c.clients.dynamic
}

func (c *Connection) DiscoveryClient() *discovery.DiscoveryClient {
	if c == nil || c.clients == nil {
		return nil
	}
	return c.clients.discovery
}

// InformerClient returns the clientset the informer cache lists and watches
// through. It shares Client()'s transport (auth observer, request deadlines)
// but has its own rate limiter, so informer LIST/WATCH bursts never queue
// request handlers. Falls back to Client() when there is none (tests that
// install only a request client).
func (c *Connection) InformerClient() kubernetes.Interface {
	if c.Client() != nil && c.clients.informerClientset != nil {
		return c.clients.informerClientset
	}
	return c.ClientInterface()
}

// InformerDynamicClient is InformerClient for the dynamic cache and the
// startup list probes.
func (c *Connection) InformerDynamicClient() dynamic.Interface {
	if c.Dynamic() != nil && c.clients.informerDynamic != nil {
		return c.clients.informerDynamic
	}
	return c.Dynamic()
}

// ClientGeneration identifies the client bundle, which a rebuild on the same
// clients keeps: the auth transport embeds it, and a reconnect under the same
// context name, possibly to another cluster, gets a new one.
func (c *Connection) ClientGeneration() uint64 {
	if c == nil || c.clients == nil {
		return 0
	}
	return c.clients.generation
}

func (c *Connection) ContextName() string {
	if c == nil {
		return ""
	}
	return c.contextName
}

func (c *Connection) ContextBinding() string {
	if c == nil {
		return ""
	}
	return c.contextBinding
}

func (c *Connection) ClusterName() string {
	if c == nil {
		return ""
	}
	return c.clusterName
}

// Discovery returns the API discovery built for this connection, or nil until
// its first build finishes.
func (c *Connection) Discovery() *ResourceDiscovery {
	if c == nil {
		return nil
	}
	return c.discovery.Load()
}

// ID returns the connection ID it was admitted with, or "" if never admitted.
func (c *Connection) ID() string {
	if c == nil {
		return ""
	}
	connectionStatusMu.RLock()
	defer connectionStatusMu.RUnlock()
	return c.id
}
