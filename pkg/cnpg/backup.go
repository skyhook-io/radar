// Package cnpg interprets CloudNativePG declarations without I/O or caller permissions.
package cnpg

import "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

const BarmanPluginName = "barman-cloud.cloudnative-pg.io"

type BackupPlugin struct {
	Name        string
	Enabled     bool
	WALArchiver bool
	ObjectStore string
	ServerName  string
}

type BackupDeclaration struct {
	Plugins             []BackupPlugin
	InTreeConfigured    bool
	InTreeDestination   string
	SnapshotsConfigured bool
}

func ParseBackupDeclaration(cluster *unstructured.Unstructured) BackupDeclaration {
	var out BackupDeclaration
	backup, _, _ := unstructured.NestedMap(cluster.Object, "spec", "backup")
	if store, ok := backup["barmanObjectStore"].(map[string]any); ok {
		out.InTreeConfigured = true
		out.InTreeDestination, _ = store["destinationPath"].(string)
	}
	out.SnapshotsConfigured = backup["volumeSnapshot"] != nil
	plugins, _, _ := unstructured.NestedSlice(cluster.Object, "spec", "plugins")
	for _, raw := range plugins {
		p, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _ := p["name"].(string)
		if name == "" {
			continue
		}
		archiver, _ := p["isWALArchiver"].(bool)
		params, _ := p["parameters"].(map[string]any)
		store, _ := params["barmanObjectName"].(string)
		server, _ := params["serverName"].(string)
		if server == "" {
			server = cluster.GetName()
		}
		out.Plugins = append(out.Plugins, BackupPlugin{Name: name, Enabled: p["enabled"] != false, WALArchiver: archiver, ObjectStore: store, ServerName: server})
	}
	return out
}

func (d BackupDeclaration) BarmanPlugin() (BackupPlugin, bool) {
	for _, p := range d.Plugins {
		if p.Name == BarmanPluginName && p.Enabled {
			return p, true
		}
	}
	return BackupPlugin{}, false
}

func (d BackupDeclaration) HasDestination() bool {
	return d.InTreeDestination != "" || d.SnapshotsConfigured || d.DestinationPlugin() != ""
}

func (d BackupDeclaration) DestinationPlugin() string {
	for _, p := range d.Plugins {
		if p.Enabled && (p.Name != BarmanPluginName || p.ObjectStore != "") {
			return p.Name
		}
	}
	return ""
}

type BackupBlocker struct {
	Code   string `json:"code"`
	Method string `json:"method"`
	Plugin string `json:"plugin,omitempty"`
}

func (d BackupDeclaration) DestinationBlocker(method, plugin string) *BackupBlocker {
	if method == "" {
		method = "barmanObjectStore"
	}
	blocked := false
	switch method {
	case "barmanObjectStore":
		blocked = d.InTreeDestination == ""
	case "volumeSnapshot":
		blocked = !d.SnapshotsConfigured
	case "plugin":
		blocked = true
		for _, p := range d.Plugins {
			if p.Name == plugin && p.Enabled && (p.Name != BarmanPluginName || p.ObjectStore != "") {
				blocked = false
				break
			}
		}
	}
	if !blocked {
		return nil
	}
	code := "no_destination"
	if d.HasDestination() {
		code = "method_destination_missing"
	}
	return &BackupBlocker{Code: code, Method: method, Plugin: d.DestinationPlugin()}
}
