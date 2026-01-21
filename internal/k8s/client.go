package k8s

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

var (
	k8sClient       *kubernetes.Clientset
	k8sConfig       *rest.Config
	discoveryClient *discovery.DiscoveryClient
	dynamicClient   dynamic.Interface
	initOnce        sync.Once
	initErr         error
	kubeconfigPath  string
	contextName     string
	clusterName     string
)

// InitOptions configures the K8s client initialization
type InitOptions struct {
	KubeconfigPath string
}

// Initialize initializes the K8s client with the given options
func Initialize(opts InitOptions) error {
	initOnce.Do(func() {
		initErr = doInit(opts)
	})
	return initErr
}

// MustInitialize is like Initialize but panics on error
func MustInitialize(opts InitOptions) {
	if err := Initialize(opts); err != nil {
		panic(fmt.Sprintf("failed to initialize k8s client: %v", err))
	}
}

func doInit(opts InitOptions) error {
	var config *rest.Config
	var err error

	// Try in-cluster config first (for when running inside a pod)
	config, err = rest.InClusterConfig()
	if err != nil {
		// Fall back to kubeconfig (for local development / CLI usage)
		kubeconfig := opts.KubeconfigPath
		if kubeconfig == "" {
			kubeconfig = os.Getenv("KUBECONFIG")
		}
		if kubeconfig == "" {
			if home := homedir.HomeDir(); home != "" {
				kubeconfig = filepath.Join(home, ".kube", "config")
			}
		}

		// Load kubeconfig to get context/cluster names
		loadingRules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig}
		configOverrides := &clientcmd.ConfigOverrides{}
		kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

		// Get raw config to extract context/cluster names
		rawConfig, err := kubeConfig.RawConfig()
		if err == nil {
			contextName = rawConfig.CurrentContext
			if ctx, ok := rawConfig.Contexts[contextName]; ok {
				clusterName = ctx.Cluster
			}
		}

		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return fmt.Errorf("failed to build kubeconfig from %s: %w", kubeconfig, err)
		}
		kubeconfigPath = kubeconfig
	} else {
		// In-cluster mode
		contextName = "in-cluster"
		clusterName = "in-cluster"
	}

	k8sConfig = config

	k8sClient, err = kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create k8s clientset: %w", err)
	}

	// Create discovery client for API resource discovery
	discoveryClient, err = discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create discovery client: %w", err)
	}

	// Create dynamic client for CRD access
	dynamicClient, err = dynamic.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create dynamic client: %w", err)
	}

	return nil
}

// GetClient returns the K8s clientset
func GetClient() *kubernetes.Clientset {
	return k8sClient
}

// GetConfig returns the K8s rest config
func GetConfig() *rest.Config {
	return k8sConfig
}

// GetDiscoveryClient returns the K8s discovery client for API resource discovery
func GetDiscoveryClient() *discovery.DiscoveryClient {
	return discoveryClient
}

// GetDynamicClient returns the K8s dynamic client for CRD access
func GetDynamicClient() dynamic.Interface {
	return dynamicClient
}

// GetKubeconfigPath returns the path to the kubeconfig file used
func GetKubeconfigPath() string {
	return kubeconfigPath
}

// GetContextName returns the current kubeconfig context name
func GetContextName() string {
	return contextName
}

// GetClusterName returns the current cluster name from kubeconfig
func GetClusterName() string {
	return clusterName
}

// IsInCluster returns true if running inside a Kubernetes cluster
func IsInCluster() bool {
	return kubeconfigPath == ""
}
