package openobserve

import (
	"encoding/json"
	"testing"
)

var clusterB = []Matcher{{Name: "cluster", Value: "oo-b"}}

func TestFilterResponse(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		matchers    []Matcher
		wantDropped int
		wantSeries  int
		unchanged   bool
	}{
		{
			name: "vector keeps matching drops others",
			body: `{"status":"success","data":{"resultType":"vector","result":[
				{"metric":{"__name__":"up","cluster":"oo-b"},"value":[1,"1"]},
				{"metric":{"__name__":"up","cluster":"oo-a"},"value":[1,"1"]},
				{"metric":{"__name__":"up"},"value":[1,"1"]}]},"warnings":["w"]}`,
			matchers: clusterB, wantDropped: 2, wantSeries: 1,
		},
		{
			name: "matrix",
			body: `{"status":"success","data":{"resultType":"matrix","result":[
				{"metric":{"cluster":"oo-b"},"values":[[1,"1"],[2,"2"]]},
				{"metric":{"cluster":"oo-b2"},"values":[[1,"1"]]}]}}`,
			matchers: clusterB, wantDropped: 1, wantSeries: 1,
		},
		{
			name:     "all match is unchanged",
			body:     `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"cluster":"oo-b"},"value":[1,"1"]}]}}`,
			matchers: clusterB, wantSeries: 1, unchanged: true,
		},
		{
			name:     "label name compared lowercase",
			body:     `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"Cluster":"oo-b"},"value":[1,"1"]}]}}`,
			matchers: []Matcher{{Name: "CLUSTER", Value: "oo-b"}}, wantSeries: 1, unchanged: true,
		},
		{
			name:     "value compared exactly",
			body:     `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"cluster":"OO-B"},"value":[1,"1"]}]}}`,
			matchers: clusterB, wantDropped: 1,
		},
		{
			name: "every matcher required",
			body: `{"status":"success","data":{"resultType":"vector","result":[
				{"metric":{"cluster":"oo-b","radar_cluster_uid":"u"},"value":[1,"1"]},
				{"metric":{"cluster":"oo-b"},"value":[1,"1"]}]}}`,
			matchers: []Matcher{{Name: "cluster", Value: "oo-b"}, {Name: "radar_cluster_uid", Value: "u"}}, wantDropped: 1, wantSeries: 1,
		},
		{
			name:     "series list",
			body:     `{"status":"success","data":[{"__name__":"up","cluster":"oo-b"},{"__name__":"up","cluster":"oo-c"},{"__name__":"up"}]}`,
			matchers: clusterB, wantDropped: 2, wantSeries: 1,
		},
		{
			name:     "scalar passes through",
			body:     `{"status":"success","data":{"resultType":"scalar","result":[1,"1"]}}`,
			matchers: clusterB, unchanged: true,
		},
		{
			name:     "string passes through",
			body:     `{"status":"success","data":{"resultType":"string","result":[1,"x"]}}`,
			matchers: clusterB, unchanged: true,
		},
		{
			name:     "error status passes through",
			body:     `{"status":"error","errorType":"bad_data","error":"boom"}`,
			matchers: clusterB, unchanged: true,
		},
		{
			name:     "label values list passes through",
			body:     `{"status":"success","data":["a","b"]}`,
			matchers: clusterB, unchanged: true,
		},
		{
			name:     "null result",
			body:     `{"status":"success","data":{"resultType":"vector","result":null}}`,
			matchers: clusterB, unchanged: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, dropped, err := FilterResponse([]byte(tc.body), tc.matchers)
			if err != nil {
				t.Fatal(err)
			}
			if dropped != tc.wantDropped {
				t.Fatalf("dropped = %d, want %d", dropped, tc.wantDropped)
			}
			if tc.unchanged {
				if string(out) != tc.body {
					t.Fatalf("body changed: %s", out)
				}
				return
			}
			if n := countSeries(t, out); n != tc.wantSeries {
				t.Fatalf("series = %d, want %d: %s", n, tc.wantSeries, out)
			}
		})
	}
}

func TestFilterResponseKeepsOtherFields(t *testing.T) {
	body := `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"cluster":"x"},"value":[1,"1"]}]},"warnings":["w"],"isPartial":true}`
	out, dropped, err := FilterResponse([]byte(body), clusterB)
	if err != nil || dropped != 1 {
		t.Fatalf("dropped=%d err=%v", dropped, err)
	}
	var got struct {
		Warnings  []string `json:"warnings"`
		IsPartial bool     `json:"isPartial"`
		Data      struct {
			ResultType string            `json:"resultType"`
			Result     []json.RawMessage `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Warnings) != 1 || !got.IsPartial || got.Data.ResultType != "vector" || got.Data.Result == nil || len(got.Data.Result) != 0 {
		t.Fatalf("unexpected body %s", out)
	}
}

func TestFilterResponseErrors(t *testing.T) {
	if _, _, err := FilterResponse([]byte(`not json`), clusterB); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	if _, _, err := FilterResponse([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":"x"}]}}`), clusterB); err == nil {
		t.Fatal("malformed series accepted")
	}
	if _, _, err := FilterResponse([]byte(`{"status":"success","data":[]}`), nil); err == nil {
		t.Fatal("empty matchers accepted")
	}
}

func countSeries(t *testing.T, body []byte) int {
	t.Helper()
	var top struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &top); err != nil {
		t.Fatal(err)
	}
	var list []json.RawMessage
	if json.Unmarshal(top.Data, &list) == nil {
		return len(list)
	}
	var data struct {
		Result []json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(top.Data, &data); err != nil {
		t.Fatal(err)
	}
	return len(data.Result)
}
