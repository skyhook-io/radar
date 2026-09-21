package prom

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"strings"
)

type Connection struct {
	URL            string            `json:"url"`
	Headers        map[string]string `json:"headers,omitempty"`
	HeadersFromEnv map[string]string `json:"headersFromEnv,omitempty"`
}

func ValidateBaseURL(raw string) error {
	if err := ValidateHTTPBaseURL(raw); err != nil {
		return fmt.Errorf("Prometheus URL %w", err)
	}
	return nil
}

func ValidateHTTPBaseURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("must be an HTTP(S) base URL without credentials, query parameters or fragments")
	}
	if _, valid := NormalizeOrigin(raw); !valid {
		return errors.New("has an invalid port")
	}
	return nil
}

func (c Connection) Validate() error {
	if err := ValidateBaseURL(c.URL); err != nil {
		return err
	}
	if c.URL == "" && len(c.Headers)+len(c.HeadersFromEnv) > 0 {
		return ErrHeadersRequireURL
	}
	all := make(map[string]string, len(c.Headers)+len(c.HeadersFromEnv))
	for k, v := range c.Headers {
		all[k] = v
	}
	for k, v := range c.HeadersFromEnv {
		if !ValidEnvVarName(v) {
			return errors.New("Prometheus header references an invalid environment variable name")
		}
		for existing := range all {
			if strings.EqualFold(existing, k) {
				return errors.New("Prometheus header is configured from multiple sources")
			}
		}
		all[k] = ""
	}
	return ValidateHeaders(all)
}

func ValidEnvVarName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' {
			continue
		}
		if i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

func ResolveHeaders(headers, refs map[string]string) (map[string]string, error) {
	if err := ValidateHeaders(headers); err != nil {
		return nil, err
	}
	out := maps.Clone(headers)
	if out == nil {
		out = map[string]string{}
	}
	for key, name := range refs {
		key = strings.TrimSpace(key)
		name = strings.TrimSpace(name)
		if !ValidEnvVarName(name) {
			return nil, fmt.Errorf("invalid env var name for prometheus header %q", key)
		}
		for existing := range out {
			if strings.EqualFold(existing, key) {
				return nil, fmt.Errorf("prometheus header %q is configured from multiple sources", key)
			}
		}
		value, ok := os.LookupEnv(name)
		if !ok {
			return nil, fmt.Errorf("prometheus header %q references unset env var %q", key, name)
		}
		out[key] = value
	}
	if err := ValidateHeaders(out); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}
