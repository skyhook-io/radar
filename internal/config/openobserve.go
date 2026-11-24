package config

import (
	"errors"
	"maps"
	"os"
	"strings"
)

// OpenObserveConfig is the shared OpenObserve connection written by
// `radar openobserve connect`.
type OpenObserveConfig struct {
	BaseURL string `json:"baseUrl"`
	Org     string `json:"org"`
	User    string `json:"user"`
	// Token is stored in plain text; Save keeps the file 0600. TokenEnv, when
	// set to a non-empty variable, wins.
	Token      string `json:"token,omitempty"`
	TokenEnv   string `json:"tokenEnv,omitempty"`
	CACertFile string `json:"caCertFile,omitempty"`
	// ClusterLabel overrides the label that names clusters in OpenObserve.
	ClusterLabel string `json:"clusterLabel,omitempty"`
	// Matches is keyed by the cluster's kube-system namespace UID.
	Matches map[string]OpenObserveMatch `json:"matches,omitempty"`
}

// OpenObserveMatch records how one cluster's series are found. "name"
// entries are names the user confirmed and are the only way a name is ever
// used as a filter. "id" entries are a cache of a cluster ID match, which is
// rediscovered on every activation; the entry only tells Radar that the
// cluster lives in OpenObserve while OpenObserve can't be reached.
type OpenObserveMatch struct {
	Label       string `json:"label"`
	Value       string `json:"value"`
	By          string `json:"by"`
	ConfirmedAt string `json:"confirmedAt,omitempty"`
}

const (
	OpenObserveMatchByID   = "id"
	OpenObserveMatchByName = "name"
)

// ResolveToken returns the token from TokenEnv when that variable is set and
// non-empty, else the stored token.
func (c OpenObserveConfig) ResolveToken() (string, error) {
	if env := strings.TrimSpace(c.TokenEnv); env != "" {
		if v := os.Getenv(env); v != "" {
			return v, nil
		}
	}
	if c.Token != "" {
		return c.Token, nil
	}
	if c.TokenEnv != "" {
		return "", errors.New("OpenObserve token: environment variable " + c.TokenEnv + " is empty and no token is saved")
	}
	return "", errors.New("OpenObserve token is not set")
}

func (c OpenObserveConfig) Clone() OpenObserveConfig {
	c.Matches = maps.Clone(c.Matches)
	return c
}
