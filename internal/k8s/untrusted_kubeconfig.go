package k8s

import (
	"fmt"
	"maps"
	"slices"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// UntrustedKubeconfigReasons lists the settings that would make client-go run
// a local command or read a local file, for a kubeconfig that came from
// someone other than the operator (such as a cluster Secret) but is used on
// the operator's machine with the operator's access. Empty means safe to use.
//   - exec runs an arbitrary command to get credentials;
//   - auth-provider runs a client-side plugin that refreshes tokens and writes
//     them back to the kubeconfig;
//   - tokenFile, client-certificate, client-key and certificate-authority read
//     local files; a token file's content is sent to the cluster's server.
//
// Inline credentials (token, client-certificate-data, client-key-data,
// certificate-authority-data, username/password) stay allowed.
//
// Every user and cluster entry is checked, not only the selected context's,
// because all contexts in the file are registered and can be selected later.
func UntrustedKubeconfigReasons(cfg *clientcmdapi.Config) []string {
	if cfg == nil {
		return nil
	}
	var reasons []string
	for _, name := range slices.Sorted(maps.Keys(cfg.AuthInfos)) {
		ai := cfg.AuthInfos[name]
		if ai == nil {
			continue
		}
		if ai.Exec != nil {
			reasons = append(reasons, fmt.Sprintf("user %q runs the command %q to log in", name, ai.Exec.Command))
		}
		if ai.AuthProvider != nil {
			reasons = append(reasons, fmt.Sprintf("user %q uses the %q auth-provider plugin", name, ai.AuthProvider.Name))
		}
		if ai.TokenFile != "" {
			reasons = append(reasons, fmt.Sprintf("user %q reads its token from the local file %q", name, ai.TokenFile))
		}
		if ai.ClientCertificate != "" {
			reasons = append(reasons, fmt.Sprintf("user %q reads its client certificate from the local file %q", name, ai.ClientCertificate))
		}
		if ai.ClientKey != "" {
			reasons = append(reasons, fmt.Sprintf("user %q reads its client key from the local file %q", name, ai.ClientKey))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(cfg.Clusters)) {
		c := cfg.Clusters[name]
		if c == nil {
			continue
		}
		if c.CertificateAuthority != "" {
			reasons = append(reasons, fmt.Sprintf("cluster %q reads its certificate authority from the local file %q", name, c.CertificateAuthority))
		}
	}
	return reasons
}
