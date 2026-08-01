package k8s

import (
	"context"
	"errors"
	"testing"
)

func TestInitAllSubsystemsPropagatesTimelineInitFailure(t *testing.T) {
	timelineErr := errors.New("PostgreSQL timeline store is not implemented")
	RegisterTimelineFuncs(nil, func() error {
		return timelineErr
	})
	t.Cleanup(func() {
		RegisterTimelineFuncs(nil, nil)
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := InitAllSubsystems(ctx, func(string) {})
	if !errors.Is(err, timelineErr) {
		t.Fatalf("InitAllSubsystems error = %v, want timeline error %v", err, timelineErr)
	}
}
