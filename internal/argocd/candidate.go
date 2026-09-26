package argocd

import (
	"context"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/argoapi"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func ProbeCandidate(ctx context.Context, connection argoapi.Connection) error {
	return PrepareCandidate(connection)(ctx)
}

func PrepareCandidate(connection argoapi.Connection) func(context.Context) error {
	m := NewManager()
	client, cfg := k8s.GetClientInterface(), k8s.GetConfig()
	name, binding, inCluster := k8s.GetContextName(), k8s.ClusterSafetyBinding(context.Background()), k8s.IsInCluster()
	m.k8sClient = func() kubernetes.Interface { return client }
	m.k8sConfig = func() *rest.Config { return cfg }
	m.contextName = func() string { return name }
	m.contextBinding = func(context.Context) string { return binding }
	m.inCluster = func() bool { return inCluster }
	m.SetConfig(connection.URL, connection.Token, connection.InsecureTLS, true)
	return func(ctx context.Context) error {
		defer m.Reset()
		return m.Probe(ctx)
	}
}
