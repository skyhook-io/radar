package prom

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestDataPoint_MarshalJSON_SpecialFloats(t *testing.T) {
	cases := []struct {
		name string
		dp   DataPoint
		want string
	}{
		{"finite", DataPoint{Timestamp: 1700000000, Value: 0.5}, `{"timestamp":1700000000,"value":0.5}`},
		{"integer-valued", DataPoint{Timestamp: 1, Value: 42}, `{"timestamp":1,"value":42}`},
		{"negative", DataPoint{Timestamp: 2, Value: -3.5}, `{"timestamp":2,"value":-3.5}`},
		{"NaN", DataPoint{Timestamp: 3, Value: math.NaN()}, `{"timestamp":3,"value":null}`},
		{"+Inf", DataPoint{Timestamp: 4, Value: math.Inf(1)}, `{"timestamp":4,"value":null}`},
		{"-Inf", DataPoint{Timestamp: 5, Value: math.Inf(-1)}, `{"timestamp":5,"value":null}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.dp)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(b) != tc.want {
				t.Errorf("got %s, want %s", b, tc.want)
			}
		})
	}
}

// A single NaN/Inf value must not fail json.Marshal of the whole series
// ("json: unsupported value: NaN") — that would break the entire query response
// for both REST and MCP. The series must marshal cleanly, with null gaps.
func TestSeries_MarshalJSON_WithNaNDoesNotFail(t *testing.T) {
	s := Series{
		Labels: map[string]string{"pod": "web-1"},
		DataPoints: []DataPoint{
			{Timestamp: 1, Value: 0.5},
			{Timestamp: 2, Value: math.NaN()},
			{Timestamp: 3, Value: math.Inf(1)},
		},
	}
	b, err := json.Marshal([]Series{s})
	if err != nil {
		t.Fatalf("marshalling a series containing NaN/Inf must not fail: %v", err)
	}
	got := string(b)
	if strings.Count(got, "null") != 2 {
		t.Errorf("expected 2 null gaps, got %s", got)
	}
	if !strings.Contains(got, `"value":0.5`) {
		t.Errorf("finite value should be preserved, got %s", got)
	}
}

// A whole series serializes with non-finite points as explicit null gaps, so
// the payload stays valid JSON, the point shape stays uniform, and clients read
// null (never a fabricated 0).
func TestSeries_MarshalJSON_NullForNonFiniteValues(t *testing.T) {
	s := Series{
		Labels: map[string]string{"pod": "a"},
		DataPoints: []DataPoint{
			{Timestamp: 1, Value: 1.5},
			{Timestamp: 2, Value: math.NaN()},
		},
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal series: %v", err)
	}
	got := string(b)
	if !strings.Contains(got, `{"timestamp":2,"value":null}`) {
		t.Errorf("NaN point should serialize as value:null, got %s", got)
	}
	if strings.Contains(got, `"value":0`) {
		t.Errorf("must not fabricate a 0 for the gap, got %s", got)
	}
}
