package server

import (
	"testing"
	"time"
)

func TestDrainOptionsFromRequestDefaults(t *testing.T) {
	no := false
	yes := true
	grace := int64(5)

	tests := []struct {
		name            string
		req             DrainRequest
		emptyDirDefault bool
		wantEmptyDir    bool
		wantForce       bool
		wantTimeout     time.Duration
	}{
		{name: "drain keeps its historical emptyDir default", req: DrainRequest{}, emptyDirDefault: true, wantEmptyDir: true, wantTimeout: 60 * time.Second},
		{name: "plan never includes emptyDir data unless asked", req: DrainRequest{}, emptyDirDefault: false, wantEmptyDir: false, wantTimeout: 60 * time.Second},
		{name: "explicit false overrides the drain default", req: DrainRequest{DeleteEmptyDirData: &no}, emptyDirDefault: true, wantEmptyDir: false, wantTimeout: 60 * time.Second},
		{name: "explicit true overrides the plan default", req: DrainRequest{DeleteEmptyDirData: &yes, Force: true, Timeout: 120}, emptyDirDefault: false, wantEmptyDir: true, wantForce: true, wantTimeout: 120 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := drainOptionsFromRequest(tt.req, tt.emptyDirDefault)
			if !got.IgnoreDaemonSets {
				t.Fatalf("DaemonSets must always be ignored")
			}
			if got.DeleteEmptyDirData != tt.wantEmptyDir || got.Force != tt.wantForce || got.Timeout != tt.wantTimeout {
				t.Fatalf("got %+v", got)
			}
		})
	}

	got := drainOptionsFromRequest(DrainRequest{GracePeriodSeconds: &grace}, false)
	if got.GracePeriodSeconds == nil || *got.GracePeriodSeconds != 5 {
		t.Fatalf("grace period must pass through, got %+v", got.GracePeriodSeconds)
	}
}
