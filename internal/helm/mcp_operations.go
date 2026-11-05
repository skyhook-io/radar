package helm

import pkgauth "github.com/skyhook-io/radar/pkg/auth"

// InstallFreshAsUser refuses all existing release history and requires a locally
// configured source. Unlike the UI's recovery workflow, an agent's install never
// implicitly upgrades or replaces a failed/uninstalled release.
func (c *Client) InstallFreshAsUser(req *InstallRequest, user *pkgauth.User) (*HelmRelease, error) {
	cfg, err := c.getActionConfigForUser(req.Namespace, user)
	if err != nil {
		return nil, err
	}
	return c.installWithPolicy(cfg, req, true)
}

// UpgradeWithOverridesAsUser overlays values onto the stored user overrides on
// the server. The caller never has to round-trip redacted credentials.
func (c *Client) UpgradeWithOverridesAsUser(namespace, name, version, repository string, values map[string]any, user *pkgauth.User) error {
	cfg, err := c.getActionConfigForUser(namespace, user)
	if err != nil {
		return err
	}
	return c.upgradeWithValuesMode(cfg, name, version, repository, values, false, progressSender(nil))
}
