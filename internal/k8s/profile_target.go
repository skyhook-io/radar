package k8s

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/pkg/prom"
)

type ProfileTarget struct {
	Binding             string                       `json:"binding"`
	Context             string                       `json:"context"`
	Source              string                       `json:"source,omitempty"`
	InFileName          string                       `json:"inFileName,omitempty"`
	CAPI                *config.CAPIProfileReference `json:"capi,omitempty"`
	Identity            config.TargetIdentity        `json:"identity"`
	Fingerprint         string                       `json:"fingerprint"`
	ClientGeneration    uint64                       `json:"clientGeneration"`
	OperationGeneration uint64                       `json:"operationGeneration"`
}

func (p ProfileTarget) Same(other ProfileTarget) bool {
	return p.Binding != "" && p.Fingerprint != "" && p.Binding == other.Binding && p.Fingerprint == other.Fingerprint && p.ClientGeneration == other.ClientGeneration && p.OperationGeneration == other.OperationGeneration
}

func CurrentProfileTarget() (ProfileTarget, error) {
	clientMu.RLock()
	p := ProfileTarget{Binding: contextBinding, Context: contextName, Source: activeSourceFile, InFileName: activeSourceName, ClientGeneration: activeClientGeneration}
	var identity struct {
		Server, TLSName, User string
		CA                    []byte
		Insecure              bool
		Proxy                 string
	}
	caFile := ""
	if activeSourceConfig != nil && k8sConfig != nil {
		if ctx := activeSourceConfig.Contexts[activeSourceName]; ctx != nil {
			identity.User = ctx.AuthInfo
			if cluster := activeSourceConfig.Clusters[ctx.Cluster]; cluster != nil {
				identity.Proxy = cluster.ProxyURL
			}
		}
		identity.Server = k8sConfig.Host
		identity.TLSName = k8sConfig.ServerName
		identity.Insecure = k8sConfig.Insecure
		identity.CA = append([]byte(nil), k8sConfig.CAData...)
		caFile = k8sConfig.CAFile
	}
	for binding, path := range capiKubeconfigs {
		if path == p.Source {
			p.Source = "CAPI"
			if ref, exists := capiProfileReferences[binding]; exists {
				p.CAPI = &ref
			}
			break
		}
	}
	clientMu.RUnlock()
	p.OperationGeneration = currentOperationGen()
	if p.Binding == "" || identity.Server == "" {
		return p, errors.New("select a kubeconfig context before saving cluster settings")
	}
	u, err := url.Parse(identity.Server)
	if err != nil || u.Host == "" {
		return p, errors.New("Kubernetes target identity is unavailable")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	identity.Server = u.String()
	if caFile != "" && len(identity.CA) == 0 {
		f, err := os.Open(caFile)
		if err != nil {
			return p, errors.New("cannot read Kubernetes CA trust file")
		}
		identity.CA, err = io.ReadAll(io.LimitReader(f, 1024*1024+1))
		f.Close()
		if err != nil || len(identity.CA) > 1024*1024 {
			return p, errors.New("cannot read Kubernetes CA trust file")
		}
	}
	data, _ := json.Marshal(identity)
	digest := sha256.Sum256(data)
	p.Fingerprint = hex.EncodeToString(digest[:])
	trust := sha256.Sum256(identity.CA)
	p.Identity = config.TargetIdentity{Server: prom.SafeAddress(identity.Server), TLSName: identity.TLSName, User: identity.User, Trust: hex.EncodeToString(trust[:]), Proxy: prom.SafeAddress(identity.Proxy), InsecureTLS: identity.Insecure}
	return p, nil
}

var capiProfileReferences = map[string]config.CAPIProfileReference{}

func RegisterCAPIProfileReference(binding, managementBinding, namespace, name string) {
	clientMu.Lock()
	defer clientMu.Unlock()
	capiProfileReferences[binding] = config.CAPIProfileReference{ManagementBinding: managementBinding, Namespace: namespace, Name: name}
}

var ErrContextConfigurationBusy = errors.New("cluster connection is changing; reload Settings and try again")

// The callback may restart traffic through its already-locked helper, never
// through RestartTrafficSubsystem, which acquires the same operation lock.
func TryClusterConfiguration(fn func(restartTraffic func() error) error) error {
	if activeContextOperations.Load() != 0 || !contextOpMu.TryLock() {
		return ErrContextConfigurationBusy
	}
	defer contextOpMu.Unlock()
	if activeContextOperations.Load() != 0 {
		return ErrContextConfigurationBusy
	}
	return fn(restartTrafficSubsystemLocked)
}
