package cnpg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"

	"github.com/skyhook-io/radar/internal/integration"
	declarations "github.com/skyhook-io/radar/pkg/cnpg"
)

type ArchivingParams struct {
	ObjectStore        string `json:"objectStore"`
	ServerName         string `json:"serverName"`
	AcknowledgeArchive bool   `json:"acknowledgeArchive"`
}

// Digests bind configuration without returning credential-bearing provider configuration.
type ArchivingFacts struct {
	ClusterConfig     string `json:"clusterConfig"`
	ObjectStoreUID    string `json:"objectStoreUID"`
	ObjectStoreConfig string `json:"objectStoreConfig"`
	ObjectStore       string `json:"objectStore"`
	ServerName        string `json:"serverName"`
}

type ArchivingPreview struct {
	Context     string         `json:"context"`
	UID         string         `json:"uid"`
	Facts       ArchivingFacts `json:"facts"`
	Destination string         `json:"destination"`
	Endpoint    string         `json:"endpoint,omitempty"`
	Plugin      map[string]any `json:"plugin"`
	Warnings    []string       `json:"warnings"`
	Unchanged   bool           `json:"unchanged"`
}

func configDigest(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func protectionConfig(cluster *unstructured.Unstructured) map[string]any {
	spec, _, _ := unstructured.NestedMap(cluster.Object, "spec")
	return map[string]any{"plugins": spec["plugins"], "backup": spec["backup"], "bootstrap": spec["bootstrap"], "externalClusters": spec["externalClusters"], "replica": spec["replica"], "deleting": !cluster.GetDeletionTimestamp().IsZero()}
}

func archivingPlugin(cluster *unstructured.Unstructured, p ArchivingParams) ([]any, map[string]any, error) {
	if !cluster.GetDeletionTimestamp().IsZero() {
		return nil, nil, integration.BlockedAction("The Cluster is being deleted")
	}
	d := declarations.ParseBackupDeclaration(cluster)
	if d.InTreeConfigured || d.SnapshotsConfigured {
		return nil, nil, integration.BlockedAction("This Cluster declares in-tree or snapshot backups. Review a method migration in Cluster YAML instead of attaching a plugin here")
	}
	plugins, _, err := unstructured.NestedSlice(cluster.Object, "spec", "plugins")
	if err != nil {
		return nil, nil, err
	}
	index := -1
	for i, raw := range plugins {
		entry, ok := raw.(map[string]any)
		if !ok {
			return nil, nil, integration.BlockedAction("The plugin configuration needs review in Cluster YAML")
		}
		name, _ := entry["name"].(string)
		if name == declarations.BarmanPluginName {
			if index >= 0 {
				return nil, nil, integration.BlockedAction("More than one Barman plugin entry is declared; review Cluster YAML")
			}
			index = i
		} else if entry["enabled"] != false && entry["isWALArchiver"] == true {
			return nil, nil, integration.BlockedAction("Another WAL archiver is declared; this guide does not replace it")
		}
	}
	plugin := map[string]any{"name": declarations.BarmanPluginName}
	if index >= 0 {
		plugin = plugins[index].(map[string]any)
	}
	params, _ := plugin["parameters"].(map[string]any)
	if params == nil {
		params = map[string]any{}
	}
	oldStore, _ := params["barmanObjectName"].(string)
	oldServer, _ := params["serverName"].(string)
	if oldServer == "" {
		oldServer = cluster.GetName()
	}
	if oldStore != "" && (oldStore != p.ObjectStore || oldServer != p.ServerName) {
		return nil, nil, integration.BlockedAction("An archive destination is already declared. Changing that identity needs a migration reviewed in Cluster YAML")
	}
	params["barmanObjectName"], params["serverName"] = p.ObjectStore, p.ServerName
	plugin["parameters"], plugin["isWALArchiver"], plugin["enabled"] = params, true, true
	if index < 0 {
		plugins = append(plugins, plugin)
	} else {
		plugins[index] = plugin
	}
	return plugins, plugin, nil
}

type archiveLocation struct{ path, endpoint string }

func locationOf(configuration map[string]any, server string) (archiveLocation, error) {
	destination, _ := configuration["destinationPath"].(string)
	u, err := url.Parse(destination)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return archiveLocation{}, integration.BlockedAction("The ObjectStore needs a valid destinationPath without embedded credentials or query parameters")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + server
	u.RawPath = ""
	endpoint, _ := configuration["endpointURL"].(string)
	if endpoint != "" {
		e, err := url.Parse(endpoint)
		if err != nil || e.Host == "" || e.User != nil || e.RawQuery != "" || e.Fragment != "" {
			return archiveLocation{}, integration.BlockedAction("The ObjectStore endpoint needs review in its YAML")
		}
		endpoint = strings.TrimRight(e.String(), "/")
	}
	return archiveLocation{path: u.String(), endpoint: endpoint}, nil
}

func readStore(ctx context.Context, dyn dynamic.Interface, namespace, name string) (*unstructured.Unstructured, map[string]any, error) {
	store, err := dyn.Resource(cnpgObjectStoreGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, nil, err
	}
	config, _, err := unstructured.NestedMap(store.Object, "spec", "configuration")
	if err != nil {
		return nil, nil, err
	}
	return store, config, nil
}

func checkArchiveIdentity(ctx context.Context, dyn dynamic.Interface, cluster, store *unstructured.Unstructured, chosen archiveLocation, p ArchivingParams) ([]string, error) {
	warnings := []string{"Radar checks visible declarations, not remote archive contents or credential validity. Confirm this archive identity is reserved for this Cluster.", "Enabling the plugin can reconcile instance Pods and sidecars; inspect the operator and instance progress afterward."}
	namespace := cluster.GetNamespace()
	location := func(ns, name, server string) (archiveLocation, error) {
		if ns == namespace && name == store.GetName() {
			config, _, err := unstructured.NestedMap(store.Object, "spec", "configuration")
			if err != nil {
				return archiveLocation{}, err
			}
			return locationOf(config, server)
		}
		_, config, err := readStore(ctx, dyn, ns, name)
		if err != nil {
			return archiveLocation{}, err
		}
		return locationOf(config, server)
	}
	source, _, _ := unstructured.NestedString(cluster.Object, "spec", "bootstrap", "recovery", "source")
	replicaSource, _, _ := unstructured.NestedString(cluster.Object, "spec", "replica", "source")
	sources := []string{}
	if source != "" {
		sources = append(sources, source)
	}
	if replicaSource != "" && replicaSource != source {
		sources = append(sources, replicaSource)
	}
	backupName, _, _ := unstructured.NestedString(cluster.Object, "spec", "bootstrap", "recovery", "backup", "name")
	if backupName != "" {
		backup, err := dyn.Resource(cnpgBackupGVR).Namespace(namespace).Get(ctx, backupName, metav1.GetOptions{})
		if err != nil {
			return nil, integration.BlockedAction("The recovery-source Backup could not be read to compare archive identities")
		}
		method, _, _ := unstructured.NestedString(backup.Object, "status", "method")
		if method == "" {
			method, _, _ = unstructured.NestedString(backup.Object, "spec", "method")
		}
		if method != "volumeSnapshot" {
			configuration, _, _ := unstructured.NestedMap(backup.Object, "status")
			server, _ := configuration["serverName"].(string)
			if server == "" {
				return nil, integration.BlockedAction("The Backup does not report its source archive identity; review archive isolation in Cluster YAML")
			}
			backupLocation, err := locationOf(configuration, server)
			if err != nil {
				return nil, integration.BlockedAction("The Backup does not report a comparable source archive location; review archive isolation in Cluster YAML")
			}
			if backupLocation == chosen {
				return nil, integration.BlockedAction("This is the recovery-source archive. Choose a new server name")
			}
		}
	}
	external, _, _ := unstructured.NestedSlice(cluster.Object, "spec", "externalClusters")
	matched := []string{}
	for _, raw := range external {
		e, ok := raw.(map[string]any)
		name, _ := e["name"].(string)
		if !ok || !slices.Contains(sources, name) {
			continue
		}
		matched = append(matched, name)
		plugin, _ := e["plugin"].(map[string]any)
		if plugin["name"] != declarations.BarmanPluginName {
			configuration, _ := e["barmanObjectStore"].(map[string]any)
			if configuration == nil {
				return nil, integration.BlockedAction("The external recovery source has no comparable archive location; review archive isolation in Cluster YAML")
			}
			server, _ := configuration["serverName"].(string)
			if server == "" {
				server = name
			}
			inTree, err := locationOf(configuration, server)
			if err != nil {
				return nil, err
			}
			if inTree == chosen {
				return nil, integration.BlockedAction("This is the recovery-source archive. Choose a new server name")
			}
			continue
		}
		params, _ := plugin["parameters"].(map[string]any)
		sourceStore, _ := params["barmanObjectName"].(string)
		server, _ := params["serverName"].(string)
		if server == "" {
			server = name
		}
		sourceLocation, err := location(namespace, sourceStore, server)
		if err != nil {
			return nil, integration.BlockedAction("The recovery-source ObjectStore could not be read to compare archive identities; review its access and configuration first")
		}
		if sourceLocation == chosen {
			return nil, integration.BlockedAction("This is the recovery-source archive. Choose a new server name so this Cluster cannot write into the archive it restores from")
		}
	}
	for _, source := range sources {
		if !slices.Contains(matched, source) {
			return nil, integration.BlockedAction("The declared recovery source is unresolved; review archive isolation in Cluster YAML")
		}
	}
	windows, _, _ := unstructured.NestedMap(store.Object, "status", "serverRecoveryWindow")
	if _, exists := windows[p.ServerName]; exists {
		current, ok := declarations.ParseBackupDeclaration(cluster).BarmanPlugin()
		if !ok || current.ObjectStore != p.ObjectStore || current.ServerName != p.ServerName {
			return nil, integration.BlockedAction("The ObjectStore reports backups for this server name already. Choose a new archive identity")
		}
	}
	clusters, err := dyn.Resource(ClusterGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		if !apierrors.IsForbidden(err) {
			return nil, err
		}
		warnings = append(warnings, "Cluster inventory is not readable across every namespace; other archive users may be out of view.")
		clusters, err = dyn.Resource(ClusterGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			if !apierrors.IsForbidden(err) {
				return nil, err
			}
			return append(warnings, "Cluster inventory in this namespace is also unreadable; collision checks are incomplete."), nil
		}
	}
	for i := range clusters.Items {
		other := &clusters.Items[i]
		if other.GetUID() == cluster.GetUID() {
			continue
		}
		declared := declarations.ParseBackupDeclaration(other)
		plugin, ok := declared.BarmanPlugin()
		var otherLocation archiveLocation
		var err error
		if ok && plugin.ObjectStore != "" {
			otherLocation, err = location(other.GetNamespace(), plugin.ObjectStore, plugin.ServerName)
		} else if declared.InTreeDestination != "" {
			configuration, _, _ := unstructured.NestedMap(other.Object, "spec", "backup", "barmanObjectStore")
			server, _ := configuration["serverName"].(string)
			if server == "" {
				server = other.GetName()
			}
			otherLocation, err = locationOf(configuration, server)
		} else {
			continue
		}
		if err != nil {
			if apierrors.IsForbidden(err) || apierrors.IsNotFound(err) {
				warnings = append(warnings, fmt.Sprintf("Archive of Cluster %s/%s cannot be compared; inventory is incomplete.", other.GetNamespace(), other.GetName()))
				continue
			}
			return nil, err
		}
		if otherLocation == chosen {
			return nil, integration.BlockedAction("Cluster " + other.GetNamespace() + "/" + other.GetName() + " already archives to this destination and server name")
		}
	}
	return warnings, nil
}

func prepareArchiving(ctx context.Context, c ActionClients, namespace, name string, p ArchivingParams) (*unstructured.Unstructured, map[string]any, ArchivingPreview, error) {
	var out ArchivingPreview
	if len(validation.IsDNS1123Subdomain(p.ObjectStore)) > 0 || len(validation.IsDNS1123Label(p.ServerName)) > 0 {
		return nil, nil, out, integration.RefuseAction(http.StatusBadRequest, "", "Choose an ObjectStore name and a server name (a lowercase DNS label, up to 63 characters)")
	}
	cluster, err := c.Dynamic.Resource(ClusterGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, nil, out, err
	}
	clusterDigest, err := configDigest(protectionConfig(cluster))
	if err != nil {
		return nil, nil, out, err
	}
	plugins, plugin, err := archivingPlugin(cluster, p)
	if err != nil {
		return nil, nil, out, err
	}
	store, configuration, err := readStore(ctx, c.Dynamic, namespace, p.ObjectStore)
	if err != nil {
		return nil, nil, out, err
	}
	if !store.GetDeletionTimestamp().IsZero() {
		return nil, nil, out, integration.BlockedAction("The ObjectStore is being deleted")
	}
	if server, _ := configuration["serverName"].(string); server != "" {
		return nil, nil, out, integration.BlockedAction("ObjectStore configuration.serverName must be empty; use the Cluster plugin parameter instead")
	}
	storeDigest, err := configDigest(store.Object["spec"])
	if err != nil {
		return nil, nil, out, err
	}
	chosen, err := locationOf(configuration, p.ServerName)
	if err != nil {
		return nil, nil, out, err
	}
	warnings, err := checkArchiveIdentity(ctx, c.Dynamic, cluster, store, chosen, p)
	if err != nil {
		return nil, nil, out, err
	}
	old, _, _ := unstructured.NestedSlice(cluster.Object, "spec", "plugins")
	out = ArchivingPreview{UID: string(cluster.GetUID()), Facts: ArchivingFacts{ClusterConfig: clusterDigest, ObjectStoreUID: string(store.GetUID()), ObjectStoreConfig: storeDigest, ObjectStore: p.ObjectStore, ServerName: p.ServerName}, Destination: chosen.path, Endpoint: chosen.endpoint, Plugin: plugin, Warnings: warnings, Unchanged: reflect.DeepEqual(old, plugins)}
	return cluster, map[string]any{"spec": map[string]any{"plugins": plugins}}, out, nil
}

func (s *Reader) PreviewArchiving(ctx context.Context, c ActionClients, contextName, namespace, name string, p ArchivingParams) (*ArchivingPreview, error) {
	cluster, patch, out, err := prepareArchiving(ctx, c, namespace, name, p)
	if err != nil {
		return nil, err
	}
	if !out.Unchanged {
		patch["metadata"] = map[string]any{"resourceVersion": cluster.GetResourceVersion()}
		data, err := json.Marshal(patch)
		if err != nil {
			return nil, err
		}
		_, err = c.Dynamic.Resource(ClusterGVR).Namespace(namespace).Patch(ctx, name, types.MergePatchType, data, metav1.PatchOptions{DryRun: []string{metav1.DryRunAll}})
		if err != nil {
			return nil, err
		}
	}
	out.Context = contextName
	return &out, nil
}

func runConfigureArchiving(ctx context.Context, c ActionClients, namespace, name string, req integration.ActionRequest) (*CNPGActionResult, error) {
	var params ArchivingParams
	if err := integration.DecodeActionParams(req.Params, &params); err != nil {
		return nil, err
	}
	if !params.AcknowledgeArchive {
		return nil, integration.RefuseAction(http.StatusBadRequest, "", "Confirm the archive identity is reserved for this Cluster before enabling archiving")
	}
	var reviewed ArchivingFacts
	if err := integration.DecodeActionParams(req.Facts, &reviewed); err != nil {
		return nil, err
	}
	if reviewed.ClusterConfig == "" || reviewed.ObjectStoreUID == "" || reviewed.ObjectStoreConfig == "" {
		return nil, integration.RefuseAction(http.StatusBadRequest, "", "Reviewed archiving facts are required")
	}
	cluster, patch, current, err := prepareArchiving(ctx, c, namespace, name, params)
	if err != nil {
		return nil, err
	}
	if string(cluster.GetUID()) != req.UID || current.Facts != reviewed {
		return nil, integration.ChangedAction(current, "Cluster or ObjectStore configuration changed since review; review the attachment again")
	}
	if current.Unchanged {
		return &CNPGActionResult{Action: "configureArchiving", Message: "Archiving configuration is unchanged"}, nil
	}
	if err := integration.MergePatchAtVersion(ctx, c.Dynamic, ClusterGVR, cluster, patch); err != nil {
		if apierrors.IsConflict(err) {
			return nil, integration.ChangedAction(current, "The Cluster changed while the patch was sent; review it again")
		}
		return nil, err
	}
	return &CNPGActionResult{Action: "configureArchiving", Message: "Archiving configuration saved; verify WAL uploads and a successful base backup"}, nil
}
