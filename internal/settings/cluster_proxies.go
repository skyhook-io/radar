package settings

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Cluster proxies live in their own file, not in Settings: /api/settings
// serializes Settings verbatim (including through a Cloud tunnel), and a proxy
// URL can carry credentials.
var proxiesMu sync.Mutex

func clusterProxiesPath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(homeDir, ".radar", "cluster-proxies.json")
}

// ClusterProxies returns the saved proxy URL per context binding. A missing
// file reads as no proxies; a file that can't be read or parsed is an error,
// so a context meant to go through a proxy never silently connects directly.
func ClusterProxies() (map[string]string, error) {
	proxiesMu.Lock()
	defer proxiesMu.Unlock()
	return loadClusterProxies()
}

func loadClusterProxies() (map[string]string, error) {
	proxies := map[string]string{}
	path := clusterProxiesPath()
	if path == "" {
		return proxies, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return proxies, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &proxies); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON; fix or remove it: %w", path, err)
	}
	return proxies, nil
}

// SetClusterProxy saves the proxy URL for a context binding; an empty URL removes it.
func SetClusterProxy(binding, proxyURL string) error {
	proxiesMu.Lock()
	defer proxiesMu.Unlock()
	path := clusterProxiesPath()
	if path == "" {
		return os.ErrNotExist
	}
	proxies, err := loadClusterProxies()
	if err != nil {
		return err
	}
	if proxyURL == "" {
		delete(proxies, binding)
	} else {
		proxies[binding] = proxyURL
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(proxies, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
