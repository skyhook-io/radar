package app

import (
	"reflect"
	"strings"
	"testing"
)

func TestResolvePrometheusHeaders(t *testing.T) {
	t.Setenv("PROM_TOKEN", "Bearer secret")
	t.Setenv("PROM_TENANT", "tenant-1")

	got, err := ResolvePrometheusHeaders(
		map[string]string{"X-Static": "literal"},
		map[string]string{
			"Authorization": "PROM_TOKEN",
			"X-Scope-OrgID": "PROM_TENANT",
		},
	)
	if err != nil {
		t.Fatalf("ResolvePrometheusHeaders failed: %v", err)
	}
	want := map[string]string{
		"X-Static":      "literal",
		"Authorization": "Bearer secret",
		"X-Scope-OrgID": "tenant-1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestResolvePrometheusHeadersFailures(t *testing.T) {
	t.Setenv("PROM_TOKEN", "Bearer secret")
	t.Setenv("BAD_VALUE", "safe\r\nInjected: bad")

	cases := []struct {
		name           string
		headers        map[string]string
		headersFromEnv map[string]string
		wantErr        string
	}{
		{
			name:           "unset env var",
			headersFromEnv: map[string]string{"Authorization": "MISSING_TOKEN"},
			wantErr:        "unset env var",
		},
		{
			name:           "invalid env var name",
			headersFromEnv: map[string]string{"Authorization": "1TOKEN"},
			wantErr:        "invalid env var name",
		},
		{
			name:           "duplicate source",
			headers:        map[string]string{"Authorization": "literal"},
			headersFromEnv: map[string]string{"Authorization": "PROM_TOKEN"},
			wantErr:        "configured both",
		},
		{
			name:           "invalid resolved value",
			headersFromEnv: map[string]string{"Authorization": "BAD_VALUE"},
			wantErr:        "control characters",
		},
		{
			name:           "invalid header name",
			headersFromEnv: map[string]string{"Bad Header": "PROM_TOKEN"},
			wantErr:        "invalid prometheus header name",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ResolvePrometheusHeaders(tc.headers, tc.headersFromEnv)
			if err == nil {
				t.Fatalf("expected error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("got error %q, want substring %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestValidEnvVarName(t *testing.T) {
	valid := []string{"PROM_TOKEN", "_PROM_TOKEN", "PROM_TOKEN_1"}
	for _, name := range valid {
		if !ValidEnvVarName(name) {
			t.Errorf("%q should be valid", name)
		}
	}
	invalid := []string{"", "1PROM_TOKEN", "PROM-TOKEN", "PROM.TOKEN"}
	for _, name := range invalid {
		if ValidEnvVarName(name) {
			t.Errorf("%q should be invalid", name)
		}
	}
}
