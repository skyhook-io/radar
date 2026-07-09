package argoapi

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"
)

type cliConfig struct {
	Contexts       []cliContext `json:"contexts"`
	CurrentContext string       `json:"current-context"`
	Users          []cliUser    `json:"users"`
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

// hostPort reduces a server reference to lowercase host[:port]. The CLI
// config stores servers without a scheme while callers usually pass full
// URLs, so both sides are normalized before comparing.
func hostPort(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	return strings.ToLower(s)
}
