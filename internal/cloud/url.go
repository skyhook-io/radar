package cloud

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// IsLoopbackHostname reports whether hostname is an explicit local-only host.
// It intentionally does not resolve arbitrary DNS names: allowing plaintext
// based on DNS resolution would make the transport policy vulnerable to DNS
// rebinding.
func IsLoopbackHostname(hostname string) bool {
	if strings.EqualFold(hostname, "localhost") {
		return true
	}
	ip := net.ParseIP(hostname)
	return ip != nil && ip.IsLoopback()
}

// FrontendOrigin derives the Hub's user-facing origin from a Hub-provided URL
// (typically connect_url). Presenters build cluster links from Hub responses
// rather than hardcoding a hosted domain, so self-hosted hubs link correctly.
func FrontendOrigin(hubProvidedURL string) string {
	u, err := url.Parse(hubProvidedURL)
	if err != nil {
		return ""
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
}

// ClusterURL is the Hub frontend page for one connected cluster.
func ClusterURL(hubProvidedURL, clusterID string) string {
	return FrontendOrigin(hubProvidedURL) + "/c/" + url.PathEscape(clusterID)
}

// ClustersURL is the Hub frontend clusters list.
func ClustersURL(hubProvidedURL string) string {
	return FrontendOrigin(hubProvidedURL) + "/clusters"
}

// NormalizeHubOrigin validates raw as a Hub origin and returns it with any
// trailing slash removed, so consumers can concatenate paths onto it safely.
func NormalizeHubOrigin(raw string) (string, error) {
	if err := ValidateHubOrigin(raw); err != nil {
		return "", err
	}
	u, _ := url.Parse(raw) // ValidateHubOrigin already parsed and validated it.
	u.Path = ""
	u.RawPath = ""
	return u.String(), nil
}

// ValidateHubOrigin validates the API origin used by the device-flow client.
// The client sends its device secret in an Authorization header, so plaintext
// is restricted to explicit loopback hosts.
func ValidateHubOrigin(raw string) error {
	if raw == "" {
		return errors.New("Hub API origin is required")
	}
	if raw != strings.TrimSpace(raw) {
		return errors.New("Hub API origin must not contain surrounding whitespace")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("Hub API origin is invalid")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("Hub API origin must use http:// or https://")
	}
	if u.Host == "" || u.Hostname() == "" || strings.HasSuffix(u.Host, ":") {
		return errors.New("Hub API origin must include a host")
	}
	if u.User != nil {
		return errors.New("Hub API origin must not include credentials")
	}
	if path := u.EscapedPath(); path != "" && path != "/" {
		return errors.New("Hub API origin must not include a path")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return errors.New("Hub API origin must not include a query string or fragment")
	}
	if err := validateURLPort(u); err != nil {
		return err
	}
	if u.Scheme == "http" && !IsLoopbackHostname(u.Hostname()) {
		return errors.New("Hub API origin must use https:// unless the host is localhost or a loopback address")
	}
	return nil
}

// ValidateWebSocketURL validates a Cloud agent endpoint. Plaintext WebSockets
// are useful for local development, but cluster credentials must never cross a
// non-loopback network without TLS.
func ValidateWebSocketURL(raw string) error {
	if raw == "" {
		return errors.New("cloud WebSocket URL is required")
	}
	if raw != strings.TrimSpace(raw) {
		return errors.New("cloud WebSocket URL must not contain surrounding whitespace")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("cloud WebSocket URL is invalid")
	}
	if u.Scheme != "ws" && u.Scheme != "wss" {
		return errors.New("cloud WebSocket URL must use ws:// or wss://")
	}
	if u.Host == "" || u.Hostname() == "" || strings.HasSuffix(u.Host, ":") {
		return errors.New("cloud WebSocket URL must include a host")
	}
	if u.User != nil {
		return errors.New("cloud WebSocket URL must not include credentials")
	}
	if u.Fragment != "" || strings.Contains(raw, "#") {
		return errors.New("cloud WebSocket URL must not include a fragment")
	}
	if u.Scheme == "ws" && !IsLoopbackHostname(u.Hostname()) {
		return errors.New("cloud WebSocket URL must use wss:// unless the host is localhost or a loopback address")
	}
	if err := validateURLPort(u); err != nil {
		return err
	}
	return nil
}

// HubOriginFromWebSocketURL derives the Hub API origin from an agent endpoint.
// Agent URLs are issued as ws(s)://host/agent; status calls use the same
// authority over http(s) and never carry path/query data across protocols.
func HubOriginFromWebSocketURL(raw string) (string, error) {
	if err := ValidateWebSocketURL(raw); err != nil {
		return "", err
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("cloud WebSocket URL is invalid")
	}
	if u.Scheme == "wss" {
		u.Scheme = "https"
	} else {
		u.Scheme = "http"
	}
	u.Path = ""
	u.RawPath = ""
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	return u.String(), nil
}

func validateURLPort(u *url.URL) error {
	port := u.Port()
	if port == "" {
		return nil
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return errors.New("URL port must be between 1 and 65535")
	}
	return nil
}
