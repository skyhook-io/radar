package server

import (
	"testing"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/pkg/k8score"
)

func TestResolveDebugImage(t *testing.T) {
	tests := []struct {
		name      string
		configImg string
		requested string
		want      string
	}{
		{"request override wins over config", "mirror/busybox:1.36", "custom/alpine:3", "custom/alpine:3"},
		{"config used when no request override", "mirror/busybox:1.36", "", "mirror/busybox:1.36"},
		{"request override wins over default", "", "custom/alpine:3", "custom/alpine:3"},
		{"falls back to busybox default", "", "", k8score.DefaultDebugImage},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Server{effectiveConfig: &config.Config{DebugImage: tt.configImg}}
			if got := s.resolveDebugImage(tt.requested); got != tt.want {
				t.Errorf("resolveDebugImage(%q) with config %q = %q, want %q", tt.requested, tt.configImg, got, tt.want)
			}
		})
	}

	// A nil effectiveConfig must not panic and falls back to the built-in default.
	s := &Server{}
	if got := s.resolveDebugImage(""); got != k8score.DefaultDebugImage {
		t.Errorf("resolveDebugImage with nil config = %q, want %q", got, k8score.DefaultDebugImage)
	}
}
