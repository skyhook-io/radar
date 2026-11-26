package argocd

import (
	"context"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/argoapi"
)

func PrepareCandidate(connection argoapi.Connection) func(context.Context) error {
	return PrepareCandidateFor(k8s.Current(), k8s.IsInCluster(), k8s.ClusterSafetyBinding(context.Background()), connection)
}

func PrepareCandidateFor(conn *k8s.Connection, inCluster bool, binding string, connection argoapi.Connection) func(context.Context) error {
	m := NewConnectionManager(conn, inCluster, binding)
	m.SetConfig(connection.URL, connection.Token, connection.InsecureTLS, true)
	return func(ctx context.Context) error {
		defer m.Reset()
		return m.Probe(ctx)
	}
}
