package argocd

import (
	"context"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/argoapi"
)

func PrepareCandidate(connection argoapi.Connection) func(context.Context) error {
	m := NewManager()
	conn := k8s.Current()
	name, binding, inCluster := conn.ContextName(), k8s.ClusterSafetyBinding(context.Background()), k8s.IsInCluster()
	m.clusterClients = func() probeCluster { return probeCluster{conn.ClientInterface(), conn.PortForwards()} }
	m.contextName = func() string { return name }
	m.contextBinding = func(context.Context) string { return binding }
	m.inCluster = func() bool { return inCluster }
	m.SetConfig(connection.URL, connection.Token, connection.InsecureTLS, true)
	return func(ctx context.Context) error {
		defer m.Reset()
		return m.Probe(ctx)
	}
}
