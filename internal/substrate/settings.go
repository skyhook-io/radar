package substrate

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"k8s.io/apimachinery/pkg/util/validation"
)

// Identity modes: who Radar reads Substrate's API as.
const (
	// IdentityShared mints tokens for Substrate's ate-system/ate-client
	// ServiceAccount, as kubectl ate does without --token-file. Everyone who
	// can create a token for it is the same principal.
	IdentityShared = "shared"
	// IdentityTokenFile sends the bearer token in a local file.
	IdentityTokenFile = "tokenFile"
)

// Settings is how Radar connects to Substrate's API for one kube context. It
// holds paths, never a token.
type Settings struct {
	Identity  string `json:"identity"`
	TokenFile string `json:"tokenFile,omitempty"`
	// Endpoint (host:port) dials the API directly instead of port-forwarding
	// to Service ate-system/api.
	Endpoint string `json:"endpoint,omitempty"`
	// CAFile verifies Endpoint instead of the cluster's ClusterTrustBundles.
	CAFile string `json:"caFile,omitempty"`
	// ServerName overrides the TLS name checked on Endpoint's certificate.
	ServerName string `json:"serverName,omitempty"`
	// Atespaces are the atespaces to read when the identity may not list
	// atespaces, as a token scoped to some atespaces may not. Ignored when
	// Substrate lists them; other atespaces then stay unknown.
	Atespaces []string `json:"atespaces,omitempty"`
}

// MaxScopedAtespaces bounds Settings.Atespaces.
const MaxScopedAtespaces = 64

// Validate checks the settings before they are saved. File checks read the
// file only to prove it is usable; nothing read is returned.
func (s Settings) Validate() error {
	switch s.Identity {
	case IdentityShared:
		if s.TokenFile != "" {
			return errors.New("a token file is used only with the tokenFile identity")
		}
	case IdentityTokenFile:
		if err := usableFile(s.TokenFile, "token file"); err != nil {
			return err
		}
	default:
		return errors.New("identity must be shared or tokenFile")
	}
	if len(s.Atespaces) > MaxScopedAtespaces {
		return fmt.Errorf("name at most %d atespaces", MaxScopedAtespaces)
	}
	seen := map[string]bool{}
	for _, a := range s.Atespaces {
		if len(validation.IsDNS1123Label(a)) > 0 {
			return fmt.Errorf("atespace %q is not a valid atespace name", a)
		}
		if seen[a] {
			return fmt.Errorf("atespace %s is named twice", a)
		}
		seen[a] = true
	}
	if s.Endpoint == "" {
		if s.CAFile != "" || s.ServerName != "" {
			return errors.New("caFile and serverName apply only with an endpoint")
		}
		return nil
	}
	host, port, err := net.SplitHostPort(s.Endpoint)
	if err != nil || host == "" || port == "" || strings.ContainsAny(s.Endpoint, "/ \t\r\n") {
		return errors.New("endpoint must be host:port")
	}
	if s.ServerName != "" && len(validation.IsDNS1123Subdomain(s.ServerName)) > 0 {
		return errors.New("serverName must be a DNS name")
	}
	if s.CAFile != "" {
		if err := usableFile(s.CAFile, "CA file"); err != nil {
			return err
		}
		data, err := os.ReadFile(s.CAFile)
		if err != nil {
			return fmt.Errorf("cannot read CA file %s", s.CAFile)
		}
		if !x509.NewCertPool().AppendCertsFromPEM(data) {
			return fmt.Errorf("CA file %s holds no PEM certificate", s.CAFile)
		}
	}
	return nil
}

func usableFile(path, what string) error {
	if path == "" {
		return fmt.Errorf("a %s path is required", what)
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("the %s path must be absolute", what)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("cannot read %s %s", what, path)
	}
	if info.IsDir() {
		return fmt.Errorf("%s %s is a directory", what, path)
	}
	if info.Size() > 1<<20 {
		return fmt.Errorf("%s %s is larger than 1 MiB", what, path)
	}
	return nil
}

// Saved is one context's settings as stored.
type Saved struct {
	Context string `json:"context"`
	// Target fingerprints the cluster the settings were saved for, so a
	// context that now points elsewhere does not send them to it.
	Target   string   `json:"target"`
	Settings Settings `json:"settings"`
}

type storeFile struct {
	Version int `json:"version"`
	// Contexts is keyed by the context's binding (k8s.ProfileTarget.Binding),
	// the same key the integration-connections store uses.
	Contexts map[string]Saved `json:"contexts"`
}

// Store persists Substrate connection settings next to clusters.json. It is a
// sibling of that store rather than an integration in it: clusters.json's
// integrations are URL-plus-credentials connections that the Settings UI
// lists, copies, imports from legacy config and probes, and Substrate's
// connection fits none of those flows.
type Store struct{ Path string }

func NewStore(radarConfigPath string) *Store {
	if radarConfigPath == "" {
		return &Store{}
	}
	return &Store{Path: filepath.Join(filepath.Dir(radarConfigPath), "substrate.json")}
}

func (s *Store) read() (storeFile, error) {
	out := storeFile{Version: 1, Contexts: map[string]Saved{}}
	if s.Path == "" {
		return out, errors.New("Radar's settings directory is unavailable")
	}
	f, err := os.Open(s.Path)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return out, fmt.Errorf("cannot read %s", s.Path)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil || len(data) > 1<<20 {
		return out, fmt.Errorf("cannot read %s", s.Path)
	}
	if err := json.Unmarshal(data, &out); err != nil || out.Version != 1 {
		return storeFile{Version: 1, Contexts: map[string]Saved{}}, fmt.Errorf("%s is invalid; fix it or rename it to start over", s.Path)
	}
	if out.Contexts == nil {
		out.Contexts = map[string]Saved{}
	}
	return out, nil
}

// Get returns the saved settings for a binding.
func (s *Store) Get(binding string) (Saved, bool, error) {
	f, err := s.read()
	if err != nil {
		return Saved{}, false, err
	}
	saved, ok := f.Contexts[binding]
	return saved, ok, nil
}

// Put saves (or, with nil, removes) one binding's settings.
func (s *Store) Put(ctx context.Context, binding string, saved *Saved) error {
	if s.Path == "" {
		return errors.New("Radar's settings directory is unavailable")
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0700); err != nil {
		return err
	}
	lock := flock.New(s.Path+".lock", flock.SetPermissions(0600))
	defer lock.Close()
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if locked, err := lock.TryLockContext(ctx, 10*time.Millisecond); err != nil || !locked {
		return errors.New("Substrate settings are busy; try again")
	}
	defer lock.Unlock()
	f, err := s.read()
	if err != nil {
		return err
	}
	if saved == nil {
		delete(f.Contexts, binding)
	} else {
		f.Contexts[binding] = *saved
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.Path), ".substrate-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.Path)
}
