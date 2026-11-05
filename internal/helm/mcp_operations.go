package helm

import (
	"bytes"
	"encoding/json"
	"fmt"
	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

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

// validateOverrideReplacement prevents a partial override from replacing a
// container whose current contents are hidden from an MCP client. Maps merge,
// but Helm replaces lists and null/type-changing values as whole units.
func validateOverrideReplacement(stored, overrides map[string]any) error {
	encoded, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	var redacted map[string]any
	if err := json.Unmarshal(encoded, &redacted); err != nil {
		return err
	}
	aicontext.RedactHelmValues(redacted)
	var check func(any, any) error
	check = func(old, next any) error {
		oldMap, oldIsMap := old.(map[string]any)
		nextMap, nextIsMap := next.(map[string]any)
		if oldIsMap && nextIsMap {
			for key, value := range nextMap {
				if err := check(oldMap[key], value); err != nil {
					return err
				}
			}
			return nil
		}
		_, oldIsList := old.([]any)
		if oldIsMap || oldIsList {
			serialized, err := json.Marshal(old)
			if err != nil {
				return err
			}
			if bytes.Contains(serialized, []byte("[REDACTED]")) {
				return fmt.Errorf("override would replace a list or object containing hidden credentials; omit that container or use replace_values=true with a complete reviewed configuration")
			}
		}
		return nil
	}
	return check(redacted, overrides)
}
