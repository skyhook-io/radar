package cnpg

import (
	"context"
	"time"

	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/pkg/prom"
)

type Metrics struct {
	Connection func(context.Context) (bool, error)
	PVCUsage   func(context.Context, string, []string, []prom.WorkloadPodIdentity) prometheuspkg.PVCUsageBatch
	CNPGScope  func(context.Context, string, string, []prom.WorkloadPodIdentity, time.Duration) (string, prometheuspkg.SeriesIsolation, error)
	PVCScope   func(context.Context, string, []string, []prom.WorkloadPodIdentity, time.Duration) (string, prometheuspkg.SeriesIsolation, error)
	History    func(context.Context, prometheuspkg.CNPGHistoryRequest) ([]prometheuspkg.CNPGHistoryChart, error)
	FleetLag   func(context.Context, string, []string, string) (prometheuspkg.CNPGFleetLag, error)
	FleetSlots func(context.Context, string, []string, string) (prometheuspkg.CNPGFleetSlots, error)
	DiskGrowth func(context.Context, string, []string, time.Duration, string) (map[string]float64, error)
}

func (s *Reader) prometheusUnavailable(ctx context.Context) string {
	connected, err := s.Metrics.Connection(ctx)
	if !connected {
		return cnpgNoPrometheusReason("")
	}
	if err != nil {
		return cnpgNoPrometheusReason(err.Error())
	}
	return ""
}
