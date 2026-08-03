package cloud

import (
	"fmt"
	"os"
	"strings"
)

// ResolveHubOriginsFromEnv resolves the self-hosted Hub override for every
// entrypoint that starts the shared server — the CLI and Desktop must agree,
// or Cloud Connect on one of them silently targets the hosted control plane.
// RADAR_HUB_URL points the device flow and tunnel at a self-hosted Hub;
// RADAR_HUB_APP_URL overrides the user-facing origin when it differs (a
// single-origin stack needs only the first). Empty results mean "use the
// hosted defaults". Both come back canonicalized (no trailing slash) so
// consumers can concatenate paths safely.
func ResolveHubOriginsFromEnv() (apiURL, appURL string, err error) {
	apiURL = strings.TrimSpace(os.Getenv("RADAR_HUB_URL"))
	appURL = strings.TrimSpace(os.Getenv("RADAR_HUB_APP_URL"))
	if apiURL != "" {
		if apiURL, err = NormalizeHubOrigin(apiURL); err != nil {
			return "", "", fmt.Errorf("invalid RADAR_HUB_URL: %w", err)
		}
		if appURL == "" {
			appURL = apiURL
		}
	}
	if appURL != "" {
		if appURL, err = NormalizeHubOrigin(appURL); err != nil {
			return "", "", fmt.Errorf("invalid RADAR_HUB_APP_URL: %w", err)
		}
	}
	return apiURL, appURL, nil
}
