package prom

import (
	"errors"
	"maps"
	"net/http"
	"strings"
)

type HeaderOperation struct {
	Key    string `json:"key"`
	Action string `json:"action"`
	Value  string `json:"value,omitempty"`
}

func ApplyHeaderOperations(original Connection, targetURL string, operations []HeaderOperation) (Connection, error) {
	result := Connection{URL: targetURL, Headers: maps.Clone(original.Headers), HeadersFromEnv: maps.Clone(original.HeadersFromEnv)}
	if result.Headers == nil {
		result.Headers = map[string]string{}
	}
	seen := map[string]bool{}
	preserved := map[string]bool{}
	for key := range original.Headers {
		preserved[strings.ToLower(key)] = true
	}
	for key := range original.HeadersFromEnv {
		preserved[strings.ToLower(key)] = true
	}
	for _, op := range operations {
		key := strings.TrimSpace(op.Key)
		lower := strings.ToLower(key)
		if key == "" || seen[lower] {
			return Connection{}, errors.New("header operations require unique header names")
		}
		seen[lower] = true
		if err := ValidateHeaders(map[string]string{key: ""}); err != nil {
			return Connection{}, err
		}
		for envKey := range original.HeadersFromEnv {
			if strings.EqualFold(envKey, key) && op.Action != "keep" {
				return Connection{}, errors.New("environment-backed headers must be edited in clusters.json")
			}
		}
		switch op.Action {
		case "keep":
			if !preserved[lower] || op.Value != "" {
				return Connection{}, errors.New("keep requires a saved header and no value")
			}
		case "set", "clear":
			if op.Action == "clear" && op.Value != "" {
				return Connection{}, errors.New("clear must not include a header value")
			}
			delete(preserved, lower)
			for oldKey := range result.Headers {
				if strings.EqualFold(oldKey, key) {
					delete(result.Headers, oldKey)
				}
			}
			if op.Action == "set" {
				result.Headers[http.CanonicalHeaderKey(key)] = op.Value
			}
		default:
			return Connection{}, errors.New("header action must be keep, set, or clear")
		}
	}
	if len(preserved) > 0 {
		before, ok := NormalizeOrigin(original.URL)
		after, valid := NormalizeOrigin(targetURL)
		if !ok || !valid || before != after {
			message := "changing servers requires replacing or clearing every saved header"
			if len(original.HeadersFromEnv) > 0 {
				message += "; edit environment references in clusters.json"
			}
			return Connection{}, errors.New(message)
		}
	}
	if err := result.Validate(); err != nil {
		return Connection{}, err
	}
	return result, nil
}
