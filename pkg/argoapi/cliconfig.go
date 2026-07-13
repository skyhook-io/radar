package argoapi

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"
)

type cliConfig struct {
	Contexts       []cliContext `json:"contexts"`
	CurrentContext string       `json:"current-context"`
	Users          []cliUser    `json:"users"`
	Servers        []cliServer  `json:"servers"`
}

type cliContext struct {
	Name   string `json:"name"`
	Server string `json:"server"`
	User   string `json:"user"`
}

type cliUser struct {
	Name      string `json:"name"`
	AuthToken string `json:"auth-token"`
}

type cliServer struct {
	Server   string `json:"server"`
	Insecure bool   `json:"insecure"`
}

// TokenFromCLIConfig reads the auth token from an Argo CD CLI config file
// (default ~/.config/argocd/config when configPath is empty). The context is
// selected by matching serverURL's host[:port] against each context's server
// — schemes and paths are ignored on both sides. When serverURL is empty, the
// file's current-context is used instead.
func TokenFromCLIConfig(configPath, serverURL string) (string, error) {
	if configPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("argoapi: resolve home dir: %w", err)
		}
		configPath = filepath.Join(home, ".config", "argocd", "config")
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		return "", fmt.Errorf("argoapi: read argocd CLI config: %w", err)
	}
	var cfg cliConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return "", fmt.Errorf("argoapi: parse argocd CLI config %s: %w", configPath, err)
	}

	var match *cliContext
	if serverURL == "" {
		if cfg.CurrentContext == "" {
			return "", fmt.Errorf("argoapi: no serverURL given and no current-context in %s", configPath)
		}
		for i := range cfg.Contexts {
			if cfg.Contexts[i].Name == cfg.CurrentContext {
				match = &cfg.Contexts[i]
				break
			}
		}
		if match == nil {
			return "", fmt.Errorf("argoapi: current-context %q not found in %s", cfg.CurrentContext, configPath)
		}
	} else {
		want := hostPort(serverURL)
		for i := range cfg.Contexts {
			if hostPort(cfg.Contexts[i].Server) == want {
				match = &cfg.Contexts[i]
				break
			}
		}
		if match == nil {
			return "", fmt.Errorf("argoapi: no context for server %q in %s", want, configPath)
		}
	}

	for _, u := range cfg.Users {
		if u.Name == match.User {
			if u.AuthToken == "" {
				return "", fmt.Errorf("argoapi: user %q in %s has no auth-token", u.Name, configPath)
			}
			return u.AuthToken, nil
		}
	}
	return "", fmt.Errorf("argoapi: user %q for context %q not found in %s", match.User, match.Name, configPath)
}

// CLISession is a detected Argo CD CLI login — the server and user of the
// config's current-context, plus whether that server was registered with TLS
// verification off. The token is intentionally omitted: it stays server-side
// and is only materialized on an explicit connect.
type CLISession struct {
	Server   string `json:"server"`
	User     string `json:"user"`
	Insecure bool   `json:"insecure"`
}

// CLISessionFromConfig returns the current-context session (server + user) from
// the Argo CD CLI config, or (nil, nil) when there is no usable session — no
// file, no current-context, or the context has no token. Only a malformed file
// is an error. Token-free: for surfacing "you're logged in as X" without
// exposing the credential.
func CLISessionFromConfig(configPath string) (*CLISession, error) {
	if configPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("argoapi: resolve home dir: %w", err)
		}
		configPath = filepath.Join(home, ".config", "argocd", "config")
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no CLI config → no session, not an error
		}
		return nil, fmt.Errorf("argoapi: read argocd CLI config: %w", err)
	}
	var cfg cliConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("argoapi: parse argocd CLI config %s: %w", configPath, err)
	}
	if cfg.CurrentContext == "" {
		return nil, nil
	}
	var match *cliContext
	for i := range cfg.Contexts {
		if cfg.Contexts[i].Name == cfg.CurrentContext {
			match = &cfg.Contexts[i]
			break
		}
	}
	if match == nil {
		return nil, nil
	}
	// A loopback server is a port-forward artifact: the address is only valid
	// while that specific forward runs, which is almost never true when Radar
	// reads the config later. Offering it would just fail "unreachable" on
	// click, so don't — the user connects via the URL field instead.
	if isLoopbackHost(match.Server) {
		return nil, nil
	}
	// Only offer a session the user can actually connect with.
	hasToken := false
	for _, u := range cfg.Users {
		if u.Name == match.User && u.AuthToken != "" {
			hasToken = true
			break
		}
	}
	if !hasToken {
		return nil, nil
	}
	insecure := false
	for _, s := range cfg.Servers {
		if hostPort(s.Server) == hostPort(match.Server) {
			insecure = s.Insecure
			break
		}
	}
	return &CLISession{Server: match.Server, User: match.User, Insecure: insecure}, nil
}

// isLoopbackHost reports whether a server reference points at localhost /
// 127.x — i.e. a transient port-forward rather than a stable endpoint.
func isLoopbackHost(server string) bool {
	h := hostPort(server)
	// Strip the port. IPv6 literals are bracketed ("[::1]:8080"); a plain host or
	// IPv4 uses a single trailing colon. A bare IPv6 ("::1") has 2+ colons and no
	// brackets, so it's left intact for ParseIP.
	switch {
	case strings.HasPrefix(h, "["):
		if end := strings.Index(h, "]"); end >= 0 {
			h = h[1:end]
		}
	case strings.Count(h, ":") == 1:
		h = h[:strings.LastIndex(h, ":")]
	}
	if h == "localhost" {
		return true
	}
	// net.ParseIP covers IPv4 loopback (127.0.0.0/8), IPv6 ::1, and mapped forms.
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// hostPort reduces a server reference to lowercase host[:port]. The CLI
// config stores servers without a scheme while callers usually pass full
// URLs, so both sides are normalized before comparing. The HTTP(S) default
// ports are dropped so "argocd.example.com:443" and "argocd.example.com" —
// the same origin written two ways — compare equal, matching normalizeOrigin.
func hostPort(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	s = strings.ToLower(s)
	s = strings.TrimSuffix(s, ":443")
	s = strings.TrimSuffix(s, ":80")
	return s
}
