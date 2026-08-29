package opencost

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestComputeKubecostTrendUsesRetainedBucketsAndFiltersNamespaces(t *testing.T) {
	transport := &fakeKubecostTransport{responses: []string{
		`{"code":200,"data":[{"cluster-a/allowed":{"properties":{"cluster":"cluster-a","namespace":"allowed"},"start":"2026-08-29T00:00:00Z","end":"2026-08-29T01:00:00Z","minutes":60,"totalCost":2},"cluster-a/private":{"properties":{"cluster":"cluster-a","namespace":"private"},"start":"2026-08-29T00:00:00Z","end":"2026-08-29T01:30:00Z","minutes":90,"totalCost":20},"__idle__":{"properties":{"cluster":"__idle__","namespace":"__idle__"},"start":"2026-08-29T00:00:00Z","end":"2026-08-29T01:15:00Z","minutes":75,"totalCost":100},"cluster-a/__unallocated__":{"properties":{"cluster":"cluster-a","namespace":"__unallocated__"},"start":"2026-08-29T00:00:00Z","end":"2026-08-29T01:00:00Z","minutes":60,"totalCost":100}},null,{"cluster-a/allowed":{"properties":{"cluster":"cluster-a","namespace":"allowed"},"start":"2026-08-29T01:00:00Z","end":"2026-08-29T03:00:00Z","minutes":120,"cpuCost":4,"ramCost":4}}]}`,
	}}
	response, err := ComputeKubecostTrend(context.Background(), NewKubecostClient(transport), KubecostTrendOptions{
		Range:      "7d",
		Namespaces: []string{"allowed"},
		Currency:   "EUR",
		ClusterID:  "cluster-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !response.Available || response.Source != "kubecost" || response.Currency != "EUR" || response.Range != "7d" {
		t.Fatalf("unexpected response metadata: %#v", response)
	}
	if len(response.Series) != 1 || response.Series[0].Namespace != "allowed" {
		t.Fatalf("unexpected series: %#v", response.Series)
	}
	points := response.Series[0].DataPoints
	if len(points) != 2 || points[0].Value != 2 || points[1].Value != 4 {
		t.Fatalf("unexpected normalized points: %#v", points)
	}
	wantFirstTimestamp := time.Date(2026, 8, 29, 1, 30, 0, 0, time.UTC).Unix()
	if points[0].Timestamp != wantFirstTimestamp {
		t.Fatalf("first timestamp = %d, want bucket boundary %d", points[0].Timestamp, wantFirstTimestamp)
	}
	wantTimestamp := time.Date(2026, 8, 29, 3, 0, 0, 0, time.UTC).Unix()
	if points[1].Timestamp != wantTimestamp {
		t.Fatalf("last timestamp = %d, want %d", points[1].Timestamp, wantTimestamp)
	}
	if response.WindowEnd-response.WindowStart != int64((7 * 24 * time.Hour).Seconds()) {
		t.Fatalf("window = %d..%d, want seven days", response.WindowStart, response.WindowEnd)
	}
	request := transport.requests[0].params
	if request.Get("window") != "7d" || request.Get("aggregate") != "cluster,namespace" || request.Get("accumulate") != "false" || request.Get("idle") != "false" || request.Get("shareIdle") != "false" {
		t.Fatalf("unexpected query: %v", request)
	}
	if request.Get("filter") != `cluster:"cluster-a"` {
		t.Fatalf("filter = %q", request.Get("filter"))
	}
}

func TestComputeKubecostTrendRanksAtLatestGlobalBucketAndAggregatesOther(t *testing.T) {
	transport := &fakeKubecostTransport{responses: []string{
		`{"code":200,"data":[{"a":{"properties":{"cluster":"cluster-a","namespace":"a"},"start":"2026-08-29T00:00:00Z","end":"2026-08-29T01:00:00Z","minutes":60,"totalCost":1},"b":{"properties":{"cluster":"cluster-a","namespace":"b"},"start":"2026-08-29T00:00:00Z","end":"2026-08-29T01:00:00Z","minutes":60,"totalCost":2},"c":{"properties":{"cluster":"cluster-a","namespace":"c"},"start":"2026-08-29T00:00:00Z","end":"2026-08-29T01:00:00Z","minutes":60,"totalCost":100}},{"a":{"properties":{"cluster":"cluster-a","namespace":"a"},"start":"2026-08-29T01:00:00Z","end":"2026-08-29T02:00:00Z","minutes":60,"totalCost":10},"b":{"properties":{"cluster":"cluster-a","namespace":"b"},"start":"2026-08-29T01:00:00Z","end":"2026-08-29T02:00:00Z","minutes":60,"totalCost":8}}]}`,
	}}
	response, err := ComputeKubecostTrend(context.Background(), NewKubecostClient(transport), KubecostTrendOptions{
		Range: "24h", MaxSeries: 1, ClusterID: "cluster-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Series) != 2 || response.Series[0].Namespace != "a" || response.Series[1].Namespace != "other" {
		t.Fatalf("unexpected ranked series: %#v", response.Series)
	}
	other := response.Series[1].DataPoints
	if len(other) != 2 || other[0].Value != 102 || other[1].Value != 8 {
		t.Fatalf("unexpected other points: %#v", other)
	}
}

func TestComputeKubecostTrendReportsNoRetainedMetrics(t *testing.T) {
	transport := &fakeKubecostTransport{responses: []string{`{"code":200,"data":[null,null]}`}}
	response, err := ComputeKubecostTrend(context.Background(), NewKubecostClient(transport), KubecostTrendOptions{Range: "bogus", ClusterID: "cluster-a"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Available || response.Reason != ReasonNoMetrics || response.Range != "24h" || response.Currency != DefaultCurrency {
		t.Fatalf("unexpected empty response: %#v", response)
	}
}

func TestComputeKubecostTrendReportsInsufficientHistoryForOneBucket(t *testing.T) {
	transport := &fakeKubecostTransport{responses: []string{
		`{"code":200,"data":[{"row":{"properties":{"cluster":"cluster-a","namespace":"demo"},"start":"2026-08-29T00:00:00Z","end":"2026-08-29T01:00:00Z","minutes":60,"totalCost":1}}]}`,
	}}
	response, err := ComputeKubecostTrend(context.Background(), NewKubecostClient(transport), KubecostTrendOptions{ClusterID: "cluster-a"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Available || response.Reason != ReasonInsufficientHistory || len(response.Series) != 0 {
		t.Fatalf("unexpected one-bucket response: %#v", response)
	}
}

func TestComputeKubecostTrendNormalizesPartialBucketsAndPreservesZeroValues(t *testing.T) {
	transport := &fakeKubecostTransport{responses: []string{
		`{"code":200,"data":[{"paid":{"properties":{"cluster":"cluster-a","namespace":"paid"},"start":"2026-08-29T00:00:00Z","end":"2026-08-29T01:00:00Z","minutes":60,"cpuCost":1,"ramCost":1},"free":{"properties":{"cluster":"cluster-a","namespace":"free"},"start":"2026-08-29T00:00:00Z","end":"2026-08-29T01:00:00Z","minutes":60}},{"paid":{"properties":{"cluster":"cluster-a","namespace":"paid"},"start":"2026-08-29T01:00:00Z","end":"2026-08-29T01:30:00Z","minutes":30,"cpuCost":0.5,"ramCost":0.5},"free":{"properties":{"cluster":"cluster-a","namespace":"free"},"start":"2026-08-29T01:00:00Z","end":"2026-08-29T01:30:00Z","minutes":30}}]}`,
	}}
	response, err := ComputeKubecostTrend(context.Background(), NewKubecostClient(transport), KubecostTrendOptions{ClusterID: "cluster-a"})
	if err != nil {
		t.Fatal(err)
	}
	if !response.Available || len(response.Series) != 2 {
		t.Fatalf("unexpected response: %#v", response)
	}
	if response.Series[0].Namespace != "paid" || response.Series[0].DataPoints[0].Value != 2 || response.Series[0].DataPoints[1].Value != 2 {
		t.Fatalf("unexpected component fallback or duration normalization: %#v", response.Series[0])
	}
	if response.Series[1].Namespace != "free" || response.Series[1].DataPoints[0].Value != 0 || response.Series[1].DataPoints[1].Value != 0 {
		t.Fatalf("unexpected zero-cost series: %#v", response.Series[1])
	}
}

func TestComputeKubecostTrendPropagatesAllocationFailures(t *testing.T) {
	tests := []struct {
		name      string
		transport *fakeKubecostTransport
		want      string
	}{
		{name: "transport", transport: &fakeKubecostTransport{errors: []error{errors.New("unreachable")}}, want: "unreachable"},
		{name: "api code", transport: &fakeKubecostTransport{responses: []string{`{"code":500,"message":"query failed"}`}}, want: "code 500"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ComputeKubecostTrend(context.Background(), NewKubecostClient(tt.transport), KubecostTrendOptions{ClusterID: "cluster-a"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestComputeKubecostTrendRejectsMalformedRetainedRows(t *testing.T) {
	transport := &fakeKubecostTransport{responses: []string{
		`{"code":200,"data":[{"row":{"properties":{"cluster":"cluster-a","namespace":"demo"},"start":"2026-08-29T00:00:00Z","end":"not-a-time","minutes":60,"totalCost":1}}]}`,
	}}
	_, err := ComputeKubecostTrend(context.Background(), NewKubecostClient(transport), KubecostTrendOptions{ClusterID: "cluster-a"})
	if err == nil || !errors.Is(err, ErrKubecostMalformedResponse) {
		t.Fatalf("error = %v, want invalid timestamp", err)
	}
}

func TestComputeKubecostTrendRequiresExactClusterIdentity(t *testing.T) {
	transport := &fakeKubecostTransport{responses: []string{
		`{"code":200,"data":[{"row":{"properties":{"cluster":"cluster-b","namespace":"demo"},"start":"2026-08-29T00:00:00Z","end":"2026-08-29T01:00:00Z","minutes":60,"totalCost":1}}]}`,
	}}
	_, err := ComputeKubecostTrend(context.Background(), NewKubecostClient(transport), KubecostTrendOptions{ClusterID: "cluster-a"})
	if err == nil || !errors.Is(err, ErrKubecostClusterMismatch) || !strings.Contains(err.Error(), "cluster-b") {
		t.Fatalf("error = %v, want cluster mismatch", err)
	}
}
