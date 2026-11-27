package helm

import (
	"path/filepath"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
)

// Rendering is offline. No kubeconfig, cluster or release state is read.
func TestCertificateExpiryChartCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name      string
		values    map[string]interface{}
		oldValues bool
		wantError bool
		wantFlags []string
	}{
		{name: "defaults preserve old arguments"},
		{name: "reuse old release values", oldValues: true},
		{name: "null parent", values: map[string]interface{}{"certificateExpiry": nil}},
		{name: "custom windows", values: map[string]interface{}{"certificateExpiry": map[string]interface{}{"warningPercent": 20}}, wantFlags: []string{"--certificate-warning-percent=20"}},
		{name: "explicit auto", values: map[string]interface{}{"certificateExpiry": map[string]interface{}{"warningPercent": "auto"}}, wantFlags: []string{"--certificate-warning-percent=auto"}},
		{name: "cloud agent", values: map[string]interface{}{"cloud": map[string]interface{}{"enabled": true, "token": "test", "url": "wss://hub.example.test", "clusterName": "test"}, "certificateExpiry": map[string]interface{}{"warningPercent": 20}}, wantFlags: []string{"--certificate-warning-percent=20"}},
		{name: "reject duration syntax", values: map[string]interface{}{"certificateExpiry": map[string]interface{}{"warningPercent": "30d"}}, wantError: true},
		{name: "reject negative", values: map[string]interface{}{"certificateExpiry": map[string]interface{}{"warningPercent": "-1"}}, wantError: true},
		{name: "quoted percentage", values: map[string]interface{}{"certificateExpiry": map[string]interface{}{"warningPercent": "20"}}, wantFlags: []string{"--certificate-warning-percent=20"}},
		{name: "minimum percentage", values: map[string]interface{}{"certificateExpiry": map[string]interface{}{"warningPercent": 1}}, wantFlags: []string{"--certificate-warning-percent=1"}},
		{name: "maximum percentage", values: map[string]interface{}{"certificateExpiry": map[string]interface{}{"warningPercent": 100}}, wantFlags: []string{"--certificate-warning-percent=100"}},
		{name: "reject zero", values: map[string]interface{}{"certificateExpiry": map[string]interface{}{"warningPercent": 0}}, wantError: true},
		{name: "reject above 100", values: map[string]interface{}{"certificateExpiry": map[string]interface{}{"warningPercent": 101}}, wantError: true},
		{name: "reject fractional percentage", values: map[string]interface{}{"certificateExpiry": map[string]interface{}{"warningPercent": 20.5}}, wantError: true},
		{name: "reject removed duration setting", values: map[string]interface{}{"certificateExpiry": map[string]interface{}{"warningWindow": "10m"}}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chart, err := loader.Load(filepath.Join("..", "..", "deploy", "helm", "radar"))
			if err != nil {
				t.Fatal(err)
			}
			if tc.oldValues {
				delete(chart.Values, "certificateExpiry")
			}
			values, err := chartutil.ToRenderValues(chart, tc.values, chartutil.ReleaseOptions{Name: "radar", Namespace: "test", IsUpgrade: true}, chartutil.DefaultCapabilities)
			if tc.wantError {
				if err == nil {
					t.Fatal("invalid duration accepted by chart schema")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			rendered, err := engine.Render(chart, values)
			if err != nil {
				t.Fatal(err)
			}
			deployment := rendered["radar/templates/deployment.yaml"]
			if deployment == "" {
				t.Fatal("deployment was not rendered")
			}
			if strings.Count(deployment, "--certificate-") != len(tc.wantFlags) {
				t.Fatalf("unexpected certificate arguments in deployment: %s", deployment)
			}
			for _, flag := range tc.wantFlags {
				if !strings.Contains(deployment, flag) {
					t.Fatalf("missing %s", flag)
				}
			}
		})
	}
}
