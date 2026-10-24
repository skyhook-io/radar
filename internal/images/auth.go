package images

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log"
	"maps"
	"strings"
	"sync"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/v1/google"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/internal/k8s"
)

// DockerConfigJSON represents the structure of a docker config.json
type DockerConfigJSON struct {
	Auths map[string]DockerConfigEntry `json:"auths"`
}

// DockerConfigEntry represents a single registry entry in docker config
type DockerConfigEntry struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Auth     string `json:"auth"`
}

// GetPullSecretsFromPod discovers ImagePullSecrets from a pod's spec
// Returns a list of secret names that can be used for authentication
func GetPullSecretsFromPod(namespace, podName string) []string {
	if namespace == "" || podName == "" {
		return nil
	}

	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil
	}

	podLister := cache.Pods()
	if podLister == nil {
		return nil
	}

	pod, err := podLister.Pods(namespace).Get(podName)
	if err != nil {
		log.Printf("Warning: could not find pod %s/%s: %v", namespace, podName, err)
		return nil
	}

	var secretNames []string

	// Get imagePullSecrets from pod spec
	for _, ref := range pod.Spec.ImagePullSecrets {
		if ref.Name != "" {
			secretNames = append(secretNames, ref.Name)
		}
	}

	// Get service account name (default to "default" if not specified)
	saName := pod.Spec.ServiceAccountName
	if saName == "" {
		saName = "default"
	}

	// Try to get additional pull secrets from the service account
	// ServiceAccounts are not in our typed cache, so we use dynamic cache
	saSecrets := getServiceAccountPullSecrets(namespace, saName)
	secretNames = append(secretNames, saSecrets...)

	log.Printf("Discovered %d pull secrets for pod %s/%s: %v", len(secretNames), namespace, podName, secretNames)
	return secretNames
}

// getServiceAccountPullSecrets gets imagePullSecrets from a service account
func getServiceAccountPullSecrets(namespace, saName string) []string {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil
	}

	// Use dynamic cache to get ServiceAccount
	ctx := context.Background()
	sa, err := cache.GetDynamic(ctx, "ServiceAccount", namespace, saName)
	if err != nil {
		// ServiceAccount not found or not accessible - that's ok
		return nil
	}

	// Extract imagePullSecrets from unstructured object
	pullSecrets, found, _ := unstructured.NestedSlice(sa.Object, "imagePullSecrets")
	if !found {
		return nil
	}

	var secrets []string
	for _, ps := range pullSecrets {
		if psMap, ok := ps.(map[string]any); ok {
			if name, ok := psMap["name"].(string); ok && name != "" {
				secrets = append(secrets, name)
			}
		}
	}

	return secrets
}

// RegistryType represents the type of container registry
type RegistryType string

const (
	RegistryDocker  RegistryType = "docker"  // Docker Hub
	RegistryGoogle  RegistryType = "google"  // GCR, Artifact Registry
	RegistryAWS     RegistryType = "aws"     // ECR
	RegistryAzure   RegistryType = "azure"   // ACR
	RegistryGitHub  RegistryType = "github"  // GHCR
	RegistryQuay    RegistryType = "quay"    // Quay.io
	RegistryGitLab  RegistryType = "gitlab"  // GitLab Container Registry
	RegistryGeneric RegistryType = "generic" // Unknown/other registries
)

// DetectRegistryType determines the registry type from an image reference
func DetectRegistryType(imageRef string) RegistryType {
	ref := strings.ToLower(imageRef)

	// Google Cloud (GCR, Artifact Registry)
	if strings.Contains(ref, "gcr.io") || strings.Contains(ref, "pkg.dev") {
		return RegistryGoogle
	}

	// AWS ECR
	if strings.Contains(ref, ".dkr.ecr.") && strings.Contains(ref, ".amazonaws.com") {
		return RegistryAWS
	}

	// Azure ACR
	if strings.Contains(ref, ".azurecr.io") {
		return RegistryAzure
	}

	// GitHub Container Registry
	if strings.Contains(ref, "ghcr.io") {
		return RegistryGitHub
	}

	// Quay.io
	if strings.Contains(ref, "quay.io") {
		return RegistryQuay
	}

	// GitLab Container Registry
	if strings.Contains(ref, "registry.gitlab.com") {
		return RegistryGitLab
	}

	// Docker Hub (no registry prefix or docker.io)
	if !strings.Contains(ref, "/") || strings.HasPrefix(ref, "docker.io") || strings.HasPrefix(ref, "index.docker.io") {
		return RegistryDocker
	}

	// Check for library images (no slash = Docker Hub official image)
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) == 1 || !strings.Contains(parts[0], ".") {
		return RegistryDocker
	}

	return RegistryGeneric
}

// GetAnonymousKeychain returns a keychain that only uses anonymous auth
func GetAnonymousKeychain() authn.Keychain {
	return authn.NewMultiKeychain(authn.DefaultKeychain)
}

// GetAuthenticatedKeychain creates a keychain with all available credentials
// for the given image. This includes:
// 1. ImagePullSecrets from the cluster
// 2. Registry-specific authentication (Google ADC, etc.)
// 3. Default keychain (docker config.json)
func GetAuthenticatedKeychain(imageRef string, namespace string, secretNames []string) authn.Keychain {
	var keychains []authn.Keychain
	registryType := DetectRegistryType(imageRef)

	// 1. Try ImagePullSecrets from cluster
	if len(secretNames) > 0 {
		psKeychain := getKeychainFromSecrets(namespace, secretNames)
		if psKeychain != nil {
			keychains = append(keychains, psKeychain)
		}
	}

	// 2. Add registry-specific keychains
	switch registryType {
	case RegistryGoogle:
		log.Printf("Adding Google keychain for registry: %s", imageRef)
		keychains = append(keychains, google.Keychain)
		// AWS, Azure, GitHub, Quay, GitLab all use docker config.json credentials
		// which are handled by the default keychain
	}

	// 3. Add default keychain as fallback (reads ~/.docker/config.json)
	keychains = append(keychains, authn.DefaultKeychain)

	return authn.NewMultiKeychain(keychains...)
}

// getPodKeychain scopes an authenticated caller's registry credentials to the
// Pod they can read: the Pod's pull secrets the caller may use, plus, with
// useAmbient (an exec holder fetching the digest the kubelet pulled), Radar's
// own credentials, which node-identity pulls such as Artifact Registry on GKE
// need.
func getPodKeychain(namespace string, pod *corev1.Pod, useAmbient bool, canUseSecret func(string) bool) authn.Keychain {
	return &podKeychain{namespace: namespace, pod: pod, useAmbient: useAmbient, canUseSecret: canUseSecret}
}

type podKeychain struct {
	namespace    string
	pod          *corev1.Pod
	useAmbient   bool
	canUseSecret func(string) bool
	once         sync.Once
	keychain     authn.Keychain
}

func (k *podKeychain) Resolve(target authn.Resource) (authn.Authenticator, error) {
	k.once.Do(func() {
		var secretNames []string
		for _, ref := range k.pod.Spec.ImagePullSecrets {
			if ref.Name != "" && k.canUseSecret(ref.Name) {
				secretNames = append(secretNames, ref.Name)
			}
		}
		if k.useAmbient {
			k.keychain = GetAuthenticatedKeychain(target.String(), k.namespace, secretNames)
			return
		}
		k.keychain = getKeychainFromSecrets(k.namespace, secretNames)
		if k.keychain == nil {
			k.keychain = &pullSecretKeychain{}
		}
	})
	return k.keychain.Resolve(target)
}

// GetKeychainForImage creates an authn.Keychain for fetching an image
// Deprecated: Use GetAuthenticatedKeychain for brute-force auth flow
func GetKeychainForImage(imageRef string, namespace string, secretNames []string) authn.Keychain {
	return GetAuthenticatedKeychain(imageRef, namespace, secretNames)
}

// getKeychainFromSecrets creates a keychain from ImagePullSecrets
func getKeychainFromSecrets(namespace string, secretNames []string) authn.Keychain {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil
	}

	secretLister := cache.Secrets()
	if secretLister == nil {
		return nil
	}

	// Collect credentials from all pull secrets
	auths := make(map[string]DockerConfigEntry)

	for _, secretName := range secretNames {
		secret, err := secretLister.Secrets(namespace).Get(secretName)
		if err != nil {
			continue // Skip missing secrets
		}

		if secret.Type != corev1.SecretTypeDockerConfigJson {
			continue
		}

		configData, ok := secret.Data[".dockerconfigjson"]
		if !ok {
			continue
		}

		var config DockerConfigJSON
		if err := json.Unmarshal(configData, &config); err != nil {
			continue
		}

		maps.Copy(auths, config.Auths)
	}

	if len(auths) == 0 {
		return nil
	}

	return &pullSecretKeychain{auths: auths}
}

// GetKeychainFromPullSecrets creates an authn.Keychain from pod ImagePullSecrets
// Deprecated: Use GetKeychainForImage instead which supports multiple auth methods
func GetKeychainFromPullSecrets(namespace string, secretNames []string) authn.Keychain {
	if len(secretNames) == 0 {
		return authn.DefaultKeychain
	}
	keychain := getKeychainFromSecrets(namespace, secretNames)
	if keychain == nil {
		return authn.DefaultKeychain
	}
	return keychain
}

// pullSecretKeychain implements authn.Keychain using ImagePullSecrets
type pullSecretKeychain struct {
	auths map[string]DockerConfigEntry
}

func (k *pullSecretKeychain) Resolve(target authn.Resource) (authn.Authenticator, error) {
	registry := target.RegistryStr()

	// Try exact match first
	if entry, ok := k.auths[registry]; ok {
		return entryToAuthenticator(entry), nil
	}

	// Try with https:// prefix
	if entry, ok := k.auths["https://"+registry]; ok {
		return entryToAuthenticator(entry), nil
	}

	// Try docker.io variants
	if registry == "index.docker.io" || registry == "docker.io" {
		for _, variant := range []string{"https://index.docker.io/v1/", "https://index.docker.io/v2/", "docker.io", "index.docker.io"} {
			if entry, ok := k.auths[variant]; ok {
				return entryToAuthenticator(entry), nil
			}
		}
	}

	return authn.Anonymous, nil
}

func entryToAuthenticator(entry DockerConfigEntry) authn.Authenticator {
	// Handle base64 encoded auth
	if entry.Auth != "" {
		decoded, err := base64.StdEncoding.DecodeString(entry.Auth)
		if err == nil {
			parts := strings.SplitN(string(decoded), ":", 2)
			if len(parts) == 2 {
				return authn.FromConfig(authn.AuthConfig{
					Username: parts[0],
					Password: parts[1],
				})
			}
		}
	}

	// Use direct username/password
	if entry.Username != "" {
		return authn.FromConfig(authn.AuthConfig{
			Username: entry.Username,
			Password: entry.Password,
		})
	}

	return authn.Anonymous
}

// pullSecretIdentity names a pull secret's exact contents: its UID and
// resourceVersion change when it is recreated or rotated.
func pullSecretIdentity(namespace, name string) (string, bool) {
	cache := k8s.GetResourceCache()
	if cache == nil || cache.Secrets() == nil {
		return "", false
	}
	secret, err := cache.Secrets().Secrets(namespace).Get(name)
	if err != nil {
		return "", false
	}
	return string(secret.UID) + "/" + secret.ResourceVersion, true
}
