package connectionruntime

import (
	"errors"
	"testing"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/connections"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/loki"
	"github.com/skyhook-io/radar/pkg/prom"
)

func TestActivateConfiguresLoki(t *testing.T) {
	t.Cleanup(func() { loki.Retire(nil) })
	r := New(nil, nil)
	target := k8s.ProfileTarget{Binding: "a", Fingerprint: "f"}
	// Only Loki changes; the other integrations stay as they are.
	r.target = target
	for _, kind := range config.IntegrationKinds {
		if kind != config.IntegrationLoki {
			r.active[kind] = connections.Selection{}
		}
	}
	r.activate(target, map[config.Integration]connections.Selection{
		config.IntegrationLoki: {Bundle: connections.Bundle{Settings: config.IntegrationSettings{Loki: &prom.Connection{URL: "http://loki.example:3100"}}}},
	}, true)
	if st := loki.CurrentStatus(); !st.Configured || st.Address != "http://loki.example:3100" {
		t.Fatalf("status = %+v", st)
	}
	r.activate(target, map[config.Integration]connections.Selection{
		config.IntegrationLoki: {Bundle: connections.Bundle{Err: errors.New("bad settings")}},
	}, true)
	if st := loki.CurrentStatus(); st.Configured || st.Error != "bad settings" {
		t.Fatalf("status = %+v", st)
	}
}
