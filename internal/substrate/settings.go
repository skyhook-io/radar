package substrate

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/skyhook-io/radar/internal/config"
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
	// IdentitySharedAsCaller is IdentityShared minted with the requesting
	// user's own Kubernetes access, not Radar's. It is implicit, never saved.
	IdentitySharedAsCaller = "sharedAsCaller"
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
func (s Settings) Validate() error { return s.validate(true) }

func (s Settings) validate(files bool) error {
	switch s.Identity {
	case IdentityShared:
		if s.TokenFile != "" {
			return errors.New("a token file is used only with the tokenFile identity")
		}
	case IdentityTokenFile:
		if s.TokenFile == "" || !filepath.IsAbs(s.TokenFile) {
			return errors.New("the token file path must be absolute")
		}
		if files {
			if err := usableFile(s.TokenFile, "token file"); err != nil {
				return err
			}
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
	if net.ParseIP(host) == nil && len(validation.IsDNS1123Subdomain(strings.ToLower(host))) > 0 {
		return errors.New("endpoint host must be an IP address or DNS name")
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return errors.New("endpoint port must be between 1 and 65535")
	}
	if s.ServerName != "" && len(validation.IsDNS1123Subdomain(s.ServerName)) > 0 {
		return errors.New("serverName must be a DNS name")
	}
	if s.CAFile != "" && !filepath.IsAbs(s.CAFile) {
		return errors.New("the CA file path must be absolute")
	}
	if s.CAFile != "" && files {
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
	Target   string                `json:"target"`
	Identity config.TargetIdentity `json:"identity"`
	Settings Settings              `json:"settings"`
}

type storeFile struct {
	Version int `json:"version"`
	// Contexts is keyed by the context's binding (k8s.ProfileTarget.Binding),
	// the same key the integration-connections store uses.
	Contexts map[string]Saved `json:"contexts"`
}

// Store persists Substrate connection settings next to clusters.json. It is a
// sibling because older Radar binaries reject unknown integration types in
// clusters.json, which CLI and Desktop share.
type Store struct{ Path string }

func NewStore(radarConfigPath string) *Store {
	if radarConfigPath == "" {
		return &Store{}
	}
	return &Store{Path: filepath.Join(filepath.Dir(radarConfigPath), "substrate.json")}
}

var ErrConflict = errors.New("Substrate settings changed; reload Settings and try again")
var ErrBusy = errors.New("Substrate settings are busy; try again")

func (s *Store) file() config.RevisionFile {
	return config.RevisionFile{Path: s.Path, MaxBytes: 1 << 20, Label: "Substrate settings", TempPattern: ".substrate-*.json", Conflict: ErrConflict, Busy: ErrBusy}
}

func (s *Store) decode(data []byte) (storeFile, error) {
	out := storeFile{Version: 1, Contexts: map[string]Saved{}}
	if data == nil {
		return out, nil
	}
	out = storeFile{}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil || out.Version != 1 {
		return out, fmt.Errorf("%s is invalid; fix it or rename it to start over", s.Path)
	}
	if !json.Valid(data) {
		return out, fmt.Errorf("%s is invalid; fix it or rename it to start over", s.Path)
	}
	for _, saved := range out.Contexts {
		if err := saved.Settings.validate(false); err != nil {
			return out, fmt.Errorf("%s has invalid Substrate settings: %v", s.Path, err)
		}
	}
	if out.Contexts == nil {
		out.Contexts = map[string]Saved{}
	}
	return out, nil
}

func (s *Store) ReadSince(previous string) (storeFile, string, bool, error) {
	data, revision, changed, err := s.file().ReadSince(previous)
	if err != nil || !changed {
		return storeFile{}, revision, changed, err
	}
	out, err := s.decode(data)
	return out, revision, changed, err
}

func (s *Store) GetRevision(binding string) (Saved, bool, string, error) {
	f, revision, _, err := s.ReadSince("")
	saved, ok := f.Contexts[binding]
	return saved, ok, revision, err
}

func (s *Store) Get(binding string) (Saved, bool, error) {
	saved, ok, _, err := s.GetRevision(binding)
	return saved, ok, err
}

func (s *Store) Update(ctx context.Context, revision, binding string, saved *Saved) (string, error) {
	return s.file().Update(ctx, revision, func(data []byte) ([]byte, error) {
		f, err := s.decode(data)
		if err != nil {
			return nil, err
		}
		if saved == nil {
			delete(f.Contexts, binding)
		} else {
			if err := saved.Settings.validate(false); err != nil {
				return nil, err
			}
			f.Contexts[binding] = *saved
		}
		data, err = json.MarshalIndent(f, "", "  ")
		return append(data, '\n'), err
	})
}
