package rightsizing

import (
	"k8s.io/apimachinery/pkg/api/resource"
	"testing"
)

func TestCalculatedRequestUsesPracticalScaleAwareSteps(t *testing.T) {
	tests := []struct {
		name     string
		observed float64
		resKind  string
		want     string
	}{
		{"cpu tiny usage keeps a 10m floor", 0.0001, "cpu", "10m"},
		{"cpu 100m demand uses a 50m step", 0.100, "cpu", "150m"},
		{"cpu 1 core demand uses a 500m step", 1.0, "cpu", "1.5"},
		{"cpu crossing one core rounds to half a core", 0.870, "cpu", "1.5"},
		{"cpu above four cores uses whole cores", 3.56, "cpu", "5"},
		{"memory tiny usage keeps a 64Mi floor", 1024, "memory", "64Mi"},
		{"memory 100Mi demand uses the preferred ladder", 100 * 1024 * 1024, "memory", "128Mi"},
		{"memory 1Gi demand rounds to 1.5Gi", 1024 * 1024 * 1024, "memory", "1.5Gi"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := calculatedRequest(tc.observed, tc.resKind)
			if got != tc.want {
				t.Errorf("calculatedRequest(%g, %q) = %q, want %q", tc.observed, tc.resKind, got, tc.want)
			}
		})
	}
}

func TestRecommendRequestStagesLargeReductions(t *testing.T) {
	quantity := func(s string) *resource.Quantity { q := resource.MustParse(s); return &q }
	tests := []struct {
		name         string
		observed     float64
		current      *resource.Quantity
		resourceName string
		conservative bool
		want         string
		wantLimited  bool
	}{
		{"200m CPU reduces no lower than one quarter", 0.001, quantity("200m"), "cpu", false, "50m", true},
		{"one CPU reduces no lower than one half", 0.001, quantity("1"), "cpu", false, "500m", true},
		{"small CPU can reduce to the 10m minimum", 0.001, quantity("50m"), "cpu", false, "10m", false},
		{"bursty CPU uses the one-half floor", 0.001, quantity("200m"), "cpu", true, "100m", true},
		{"memory reduces no lower than one half", 1024, quantity("1Gi"), "memory", false, "512Mi", true},
		{"missing request uses the demand target", 0.1, nil, "cpu", false, "150m", false},
		{"increase uses the demand target", 0.2, quantity("100m"), "cpu", false, "250m", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, limited := recommendRequest(tc.observed, tc.current, tc.resourceName, tc.conservative)
			if got != tc.want || limited != tc.wantLimited {
				t.Errorf("recommendRequest() = %q, %t; want %q, %t", got, limited, tc.want, tc.wantLimited)
			}
		})
	}
}

func TestFormatRightsizingValue(t *testing.T) {
	tests := []struct {
		v       float64
		resKind string
		want    string
	}{
		{0.0005, "cpu", "10m"},
		{2.0, "cpu", "2"},
		{1.5, "cpu", "1.5"},
		{1024, "memory", "64Mi"},
		{0, "memory", "64Mi"},
		{float64(2 * 1024 * 1024 * 1024), "memory", "2Gi"},
		{1.0, "disk", ""},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			got := formatRightsizingValue(tc.v, tc.resKind)
			if got != tc.want {
				t.Errorf("formatRightsizingValue(%g, %q) = %q, want %q", tc.v, tc.resKind, got, tc.want)
			}
		})
	}
}

func TestMinimumDoesNotCreateAnObservedShortage(t *testing.T) {
	for _, tc := range []struct {
		resource string
		current  string
		observed float64
		minimum  string
	}{
		{"cpu", "5m", 0.001, "10m"},
		{"memory", "24Mi", 10 << 20, "64Mi"},
	} {
		t.Run(tc.resource, func(t *testing.T) {
			request := resource.MustParse(tc.current)
			row := Row{Resource: tc.resource}
			ClassifyRightsizingFit(&row, tc.observed, &request, nil, tc.resource)
			if row.Fit != FitBalanced || row.RecommendedReq != nil || row.RecommendationReason != "request_within_fit_range" {
				t.Fatalf("floor raised an existing request above measured demand: %+v", row)
			}
			row = Row{Resource: tc.resource}
			ClassifyRightsizingFit(&row, tc.observed, nil, nil, tc.resource)
			if row.Fit != FitMissingRequest || row.RecommendedReq == nil || *row.RecommendedReq != tc.minimum {
				t.Fatalf("absent request should still get the minimum: %+v", row)
			}
		})
	}
}

func TestCPUQuotaWithUnknownThrottlingUsesConservativeStep(t *testing.T) {
	request, limit := resource.MustParse("500m"), resource.MustParse("1")
	quiet, throttled := 0.0, 0.2
	for _, tc := range []struct {
		name     string
		limit    *resource.Quantity
		throttle *float64
		want     string
	}{
		{"unknown with quota", &limit, nil, "250m"},
		{"unknown without quota", nil, nil, "150m"},
		{"observed zero with quota", &limit, &quiet, "150m"},
		{"observed throttling with quota", &limit, &throttled, "250m"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := Row{Resource: "cpu", HPAEvidenceAvailable: true, ThrottleAvailable: tc.throttle != nil, ThrottleRatio: tc.throttle}
			ClassifyRightsizingFit(&row, 0.02, &request, tc.limit, "cpu")
			if row.RecommendedReq == nil || *row.RecommendedReq != tc.want {
				t.Fatalf("expected %s, got %+v", tc.want, row)
			}
		})
	}
}
