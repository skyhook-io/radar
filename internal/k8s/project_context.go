package k8s

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/skyhook-io/radar/pkg/datum"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

type projectContext struct {
	Source, SourceName, Parent, Binding, Project, UID string
	Prepared                                          *clientcmdapi.Config
}

var projectContexts = map[string]projectContext{}

func runtimeProjectContext(name string) (projectContext, bool) {
	clientMu.RLock()
	defer clientMu.RUnlock()
	target, ok := projectContexts[name]
	return target, ok
}

func projectServer(server, project string) (string, error) {
	u, err := url.Parse(server)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("project connections require an HTTPS API server without URL credentials or query parameters")
	}
	if strings.ContainsAny(project, "/\\") || project == "" || project == "." || project == ".." {
		return "", fmt.Errorf("invalid project name")
	}
	path := strings.TrimSuffix(u.Path, "/")
	marker := "/apis/" + datum.ResourceManagerGroup + "/v1alpha1/"
	if i := strings.LastIndex(path, marker); i >= 0 {
		suffix := path[i+len(marker):]
		parts := strings.Split(suffix, "/")
		if len(parts) == 3 && (parts[0] == "organizations" || parts[0] == "projects") && parts[2] == "control-plane" {
			path = path[:i]
		}
	}
	u.Path = path + marker + "projects/" + project + "/control-plane"
	u.RawPath = ""
	return u.String(), nil
}

// Reload the source auth configuration, not a bearer token produced by an exec plugin.
func (p projectContext) loadAndVerify(ctx context.Context) (*clientcmdapi.Config, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	type result struct {
		config *clientcmdapi.Config
		err    error
	}
	done := make(chan result, 1)
	go func() { config, err := p.loadAndVerifyRequest(ctx); done <- result{config, err} }()
	select {
	case r := <-done:
		return r.config, r.err
	case <-ctx.Done():
		return nil, fmt.Errorf("project verification timed out or was canceled")
	}
}

func (p projectContext) loadAndVerifyRequest(ctx context.Context) (*clientcmdapi.Config, error) {
	rules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: p.Source}
	loaded := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: p.SourceName})
	raw, err := loaded.RawConfig()
	if err != nil {
		return nil, fmt.Errorf("parent kubeconfig could not be loaded")
	}
	parent, ok := raw.Contexts[p.SourceName]
	if !ok || parent == nil {
		return nil, fmt.Errorf("parent context no longer exists")
	}
	config, err := loaded.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("parent client configuration could not be loaded")
	}
	clients, err := newSharedKubernetesClients(config)
	if err != nil {
		return nil, fmt.Errorf("parent client configuration is invalid")
	}
	probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	obj, err := clients.dynamic.Resource(schema.GroupVersionResource{Group: datum.ResourceManagerGroup, Version: "v1alpha1", Resource: "projects"}).Get(probeCtx, p.Project, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("project could not be read from its parent control plane")
	}
	if string(obj.GetUID()) != p.UID || obj.GetDeletionTimestamp() != nil {
		return nil, fmt.Errorf("project identity changed or project is terminating")
	}
	host, err := projectServer(config.Host, p.Project)
	if err != nil {
		return nil, err
	}
	scoped := rest.CopyConfig(config)
	scoped.Host = host
	target, err := newSharedKubernetesClients(scoped)
	if err != nil {
		return nil, fmt.Errorf("project client configuration is invalid")
	}
	if _, err = target.clientset.CoreV1().Namespaces().List(probeCtx, metav1.ListOptions{Limit: 1}); err != nil {
		return nil, fmt.Errorf("project control plane is not reachable or access was denied")
	}
	raw = *raw.DeepCopy()
	cluster := raw.Clusters[parent.Cluster]
	if cluster == nil {
		return nil, fmt.Errorf("parent cluster configuration is missing")
	}
	cluster.Server = host
	raw.Contexts[p.SourceName].Namespace = ""
	raw.CurrentContext = p.SourceName
	return &raw, nil
}

func RegisterProjectContext(ctx context.Context, project, uid, expectedBinding string) (string, error) {
	if IsInCluster() {
		return "", fmt.Errorf("project navigation is local-only")
	}
	clientMu.RLock()
	source, sourceName, parent, binding := activeSourceFile, activeSourceName, contextName, contextBinding
	if current, ok := projectContexts[parent]; ok {
		source, sourceName, parent, binding = current.Source, current.SourceName, current.Parent, current.Binding
	}
	activeBinding := contextBinding
	clientMu.RUnlock()
	if activeBinding != expectedBinding {
		return "", fmt.Errorf("%w: connection changed; reopen the project", ErrContextSwitchPreflight)
	}
	target := projectContext{Source: source, SourceName: sourceName, Parent: parent, Binding: binding, Project: project, UID: uid}
	if _, err := target.loadAndVerify(ctx); err != nil {
		return "", err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(binding+"\x00"+uid)))
	name := parent + " / project " + project + " [" + digest[:10] + "]"
	clientMu.Lock()
	defer clientMu.Unlock()
	if contextBinding != expectedBinding {
		return "", fmt.Errorf("%w: connection changed; reopen the project", ErrContextSwitchPreflight)
	}
	projectContexts[name] = target
	return name, nil
}
func activateProjectContext(name string, target projectContext) error {
	raw := target.Prepared
	var err error
	if raw == nil {
		raw, err = target.loadAndVerify(context.Background())
		if err != nil {
			return err
		}
	}
	config, err := clientcmd.NewNonInteractiveClientConfig(*raw, target.SourceName, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
	if err != nil {
		return err
	}
	config.QPS = 50
	config.Burst = 100
	clients, err := newSharedKubernetesClients(config)
	if err != nil {
		return err
	}
	ctx := raw.Contexts[target.SourceName]
	clientMu.Lock()
	defer clientMu.Unlock()
	target.Prepared = nil
	projectContexts[name] = target
	k8sConfig = config
	k8sClient = clients.clientset
	discoveryClient = clients.discovery
	dynamicClient = clients.dynamic
	activeClientGeneration = clients.generation
	contextName = name
	contextBinding = target.Binding + "/project/" + target.UID
	activeSourceFile = "runtime:" + name
	activeSourceName = target.SourceName
	activeSourceConfig = raw
	clusterName = ctx.Cluster + " / " + target.Project
	contextNamespace = ctx.Namespace
	contextUsesExec = raw.AuthInfos[ctx.AuthInfo] != nil && raw.AuthInfos[ctx.AuthInfo].Exec != nil
	return nil
}
func GetAvailableContexts() ([]ContextInfo, error) {
	contexts, err := getFileContexts()
	if err != nil {
		return nil, err
	}
	clientMu.RLock()
	defer clientMu.RUnlock()
	for name, target := range projectContexts {
		contexts = append(contexts, ContextInfo{Name: name, OriginalName: target.Project, Cluster: target.Project, Source: "Project of " + target.Parent, IsCurrent: name == contextName})
	}
	return contexts, nil
}
