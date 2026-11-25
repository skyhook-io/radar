package substrate

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// InstallationConfig is how an in-cluster Radar or Radar Hub reads
// Substrate's API, set by the operator through environment variables (Helm
// substrate.api). Err holds an invalid explicit value: the API then stays
// visibly failed rather than falling back to defaults.
type InstallationConfig struct {
	Enabled  bool
	Settings Settings
	Err      error
}

func InstallationFromEnv() InstallationConfig { return ParseInstallation(os.LookupEnv) }

// ParseInstallation reads RADAR_SUBSTRATE_API, _ENDPOINT, _SERVER_NAME,
// _CA_FILE and _ATESPACES. Error text names the variable, never the CA path.
func ParseInstallation(lookup func(string) (string, bool)) InstallationConfig {
	c := InstallationConfig{Enabled: true, Settings: DefaultInstallationSettings()}
	if v, ok := lookup("RADAR_SUBSTRATE_API"); ok {
		switch v {
		case "enabled":
		case "disabled":
			c.Enabled = false
		default:
			c.Err = fmt.Errorf("RADAR_SUBSTRATE_API must be enabled or disabled")
		}
	}
	for _, field := range []struct {
		name string
		dst  *string
	}{
		{"RADAR_SUBSTRATE_ENDPOINT", &c.Settings.Endpoint}, {"RADAR_SUBSTRATE_SERVER_NAME", &c.Settings.ServerName}, {"RADAR_SUBSTRATE_CA_FILE", &c.Settings.CAFile},
	} {
		if v, ok := lookup(field.name); ok {
			*field.dst = v
			if v == "" {
				c.Err = fmt.Errorf("%s must not be empty", field.name)
			}
		}
	}
	if v, ok := lookup("RADAR_SUBSTRATE_ATESPACES"); ok && v != "" {
		for _, a := range strings.Split(v, ",") {
			c.Settings.Atespaces = append(c.Settings.Atespaces, strings.TrimSpace(a))
		}
	}
	if err := c.Settings.Validate(); err != nil && c.Err == nil {
		reason := err.Error()
		if c.Settings.CAFile != "" {
			reason = strings.ReplaceAll(reason, c.Settings.CAFile, "(RADAR_SUBSTRATE_CA_FILE)")
		}
		c.Err = fmt.Errorf("Substrate installation settings: %s", reason)
	}
	return c
}

func (c InstallationConfig) Trust() string {
	if c.Settings.CAFile != "" {
		return "caFile"
	}
	return "clusterTrustBundle"
}

func DefaultInstallationSettings() Settings {
	return Settings{Identity: IdentityShared, Endpoint: net.JoinHostPort(APIServerName, strconv.Itoa(APIPort)), ServerName: APIServerName}
}
