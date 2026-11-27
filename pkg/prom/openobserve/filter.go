package openobserve

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// FilterResponse removes every series from a query, query_range or series
// answer whose labels do not carry all matchers with exactly their value.
// The body is returned untouched when nothing was dropped, when the answer is
// not a success, and for shapes that hold no series.
func FilterResponse(body []byte, matchers []Matcher) ([]byte, int, error) {
	if len(matchers) == 0 {
		return nil, 0, errNoMatchers
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, 0, fmt.Errorf("openobserve: parse response: %w", err)
	}
	var status string
	if raw, ok := top["status"]; ok {
		_ = json.Unmarshal(raw, &status)
	}
	data, ok := top["data"]
	if status != "success" || !ok {
		return body, 0, nil
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return body, 0, nil
	}

	var newData json.RawMessage
	var dropped int
	var err error
	switch data[0] {
	case '[':
		newData, dropped, err = filterSeriesList(data, matchers)
	case '{':
		newData, dropped, err = filterQueryData(data, matchers)
	default:
		return body, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	if dropped == 0 {
		return body, 0, nil
	}
	top["data"] = newData
	out, err := json.Marshal(top)
	if err != nil {
		return nil, 0, fmt.Errorf("openobserve: encode response: %w", err)
	}
	return out, dropped, nil
}

func filterSeriesList(data json.RawMessage, matchers []Matcher) (json.RawMessage, int, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, 0, fmt.Errorf("openobserve: parse series list: %w", err)
	}
	if len(items) == 0 {
		return data, 0, nil
	}
	// /labels and /label/<n>/values answer a list of strings, not series.
	if first := bytes.TrimSpace(items[0]); len(first) == 0 || first[0] != '{' {
		return data, 0, nil
	}
	kept := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		var lbls map[string]string
		if err := json.Unmarshal(item, &lbls); err != nil {
			return nil, 0, fmt.Errorf("openobserve: parse series: %w", err)
		}
		if hasAll(lbls, matchers) {
			kept = append(kept, item)
		}
	}
	return marshalKept(data, items, kept)
}

func filterQueryData(data json.RawMessage, matchers []Matcher) (json.RawMessage, int, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, 0, fmt.Errorf("openobserve: parse response data: %w", err)
	}
	var resultType string
	if raw, ok := obj["resultType"]; ok {
		_ = json.Unmarshal(raw, &resultType)
	}
	if resultType != "vector" && resultType != "matrix" {
		return data, 0, nil
	}
	var items []json.RawMessage
	if raw, ok := obj["result"]; ok && !isNull(raw) {
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, 0, fmt.Errorf("openobserve: parse result: %w", err)
		}
	}
	kept := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		var s struct {
			Metric map[string]string `json:"metric"`
		}
		if err := json.Unmarshal(item, &s); err != nil {
			return nil, 0, fmt.Errorf("openobserve: parse result series: %w", err)
		}
		if hasAll(s.Metric, matchers) {
			kept = append(kept, item)
		}
	}
	dropped := len(items) - len(kept)
	if dropped == 0 {
		return data, 0, nil
	}
	result, err := json.Marshal(kept)
	if err != nil {
		return nil, 0, err
	}
	obj["result"] = result
	out, err := json.Marshal(obj)
	return out, dropped, err
}

func marshalKept(data json.RawMessage, items, kept []json.RawMessage) (json.RawMessage, int, error) {
	dropped := len(items) - len(kept)
	if dropped == 0 {
		return data, 0, nil
	}
	out, err := json.Marshal(kept)
	return out, dropped, err
}

func isNull(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == "null"
}

func hasAll(lbls map[string]string, matchers []Matcher) bool {
	for _, m := range matchers {
		want := strings.ToLower(m.Name)
		found := false
		for k, v := range lbls {
			if strings.ToLower(k) == want && v == m.Value {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
