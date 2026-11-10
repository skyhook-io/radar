package config

import (
	"strings"
	"testing"
)

func TestPprofOverride(t *testing.T) {
	for _, tt := range []struct {
		name string
		env  string
		want string
	}{
		{name: "automatic", want: "auto"},
		{name: "env true", env: "true", want: "true"},
		{name: "env false", env: "false", want: "false"},
		{name: "env on", env: "1", want: "true"},
		{name: "env off", env: "0", want: "false"},
		{name: "env mixed case", env: "True", want: "true"},
		{name: "invalid env", env: "yes", want: "error"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("RADAR_PPROF", tt.env)
			got, err := PprofOverrideFromEnv()
			if tt.want == "error" {
				if err == nil || !strings.Contains(err.Error(), "RADAR_PPROF must be a boolean") {
					t.Fatalf("expected invalid environment error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tt.want == "auto" {
				if got != nil {
					t.Fatalf("expected automatic default, got %v", *got)
				}
			} else if got == nil || *got != (tt.want == "true") {
				t.Fatalf("override = %v, want %s", got, tt.want)
			}
		})
	}
}
