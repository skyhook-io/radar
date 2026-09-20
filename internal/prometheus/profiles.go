package prometheus

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"sort"
	"sync"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/prom"
)

type LegacyProfileOffer struct {
	URL        string   `json:"url"`
	HeaderKeys []string `json:"headerKeys"`
	Revision   string   `json:"revision"`
	Error      string   `json:"error,omitempty"`
}

type ProfileView struct {
	Target         k8s.ProfileTarget   `json:"target"`
	Revision       string              `json:"revision"`
	State          string              `json:"state"`
	URL            string              `json:"url"`
	HeaderKeys     []string            `json:"headerKeys"`
	HeadersManaged bool                `json:"headersManaged"`
	Error          string              `json:"error,omitempty"`
	Legacy         *LegacyProfileOffer `json:"legacy,omitempty"`
}

type ProfileSelection struct {
	View         ProfileView
	Connection   prom.Connection
	Err          error
	fileRevision string
}

type ProfileResolver struct {
	Store        *config.ProfileStore
	mu           sync.Mutex
	launchTarget k8s.ProfileTarget
	launch       *prom.Connection
	selected     *ProfileSelection
	revisionKey  [32]byte
}

func NewProfileResolver(store *config.ProfileStore, target k8s.ProfileTarget, launch *prom.Connection) *ProfileResolver {
	p := &ProfileResolver{Store: store, launchTarget: target, launch: launch}
	rand.Read(p.revisionKey[:])
	return p
}

func (p *ProfileResolver) revision(digest string) string {
	mac := hmac.New(sha256.New, p.revisionKey[:])
	mac.Write([]byte(digest))
	return hex.EncodeToString(mac.Sum(nil))
}

func (p *ProfileResolver) Save(ctx context.Context, selection ProfileSelection, expected string, mutate func(*config.ClusterProfiles) error) error {
	if expected != selection.View.Revision {
		return config.ErrProfileConflict
	}
	_, err := p.Store.Update(ctx, selection.fileRevision, mutate)
	return err
}

func (p *ProfileResolver) IsCurrent(selection ProfileSelection) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.selected != nil && p.selected.View.Target.Same(selection.View.Target) && p.selected.View.Revision == selection.View.Revision
}

func (p *ProfileResolver) LegacyForAdoption(expected string) (prom.Connection, error) {
	legacy, digest := LegacyPrometheus()
	if expected == "" || expected != p.revision(digest) {
		return prom.Connection{}, config.ErrProfileConflict
	}
	return legacy, nil
}

func LegacyPrometheus() (prom.Connection, string) {
	c := config.Load()
	connection := prom.Connection{URL: c.PrometheusURL, Headers: c.PrometheusHeaders, HeadersFromEnv: c.PrometheusHeadersFromEnv}
	data, _ := json.Marshal(connection)
	digest := sha256.Sum256(data)
	return connection, hex.EncodeToString(digest[:])
}

func headerKeys(c prom.Connection) []string {
	keys := make([]string, 0, len(c.Headers)+len(c.HeadersFromEnv))
	for key := range c.Headers {
		keys = append(keys, key)
	}
	for key := range c.HeadersFromEnv {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func cloneSelection(s ProfileSelection) ProfileSelection {
	s.Connection.Headers = maps.Clone(s.Connection.Headers)
	s.Connection.HeadersFromEnv = maps.Clone(s.Connection.HeadersFromEnv)
	s.View.HeaderKeys = append([]string{}, s.View.HeaderKeys...)
	if s.View.Legacy != nil {
		offer := *s.View.Legacy
		offer.HeaderKeys = append([]string{}, offer.HeaderKeys...)
		s.View.Legacy = &offer
	}
	return s
}

func (p *ProfileResolver) Resolve(target k8s.ProfileTarget, refresh bool) ProfileSelection {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !refresh && p.selected != nil && p.selected.View.Target.Same(target) {
		return cloneSelection(*p.selected)
	}
	selection := ProfileSelection{View: ProfileView{Target: target, State: "auto", HeaderKeys: []string{}}}
	file, revision, err := p.Store.Read()
	selection.fileRevision = revision
	selection.View.Revision = p.revision(revision)
	if err != nil {
		selection.Err = err
		selection.View.State = "error"
		selection.View.Error = err.Error()
	} else {
		if saved, ok := file.Profiles[target.Binding]; ok {
			selection.Connection = saved.Prometheus
			selection.View.State = "saved"
			if saved.Target != target.Fingerprint {
				selection.View.State = "target_changed"
			}
		}
		if !file.PrometheusMigrationComplete && !file.PrometheusAdopted[target.Binding] && selection.View.State == "auto" {
			legacy, stamp := LegacyPrometheus()
			if legacy.URL != "" || len(legacy.Headers)+len(legacy.HeadersFromEnv) > 0 {
				selection.View.Legacy = &LegacyProfileOffer{URL: prom.SafeAddress(legacy.URL), HeaderKeys: headerKeys(legacy), Revision: p.revision(stamp)}
				if err := legacy.Validate(); err != nil {
					selection.View.Legacy.Error = err.Error()
				}
			}
		}
	}
	if p.launch != nil && target.Binding == p.launchTarget.Binding && target.Fingerprint == p.launchTarget.Fingerprint {
		selection.Connection = *p.launch
		selection.View.State = "launch"
		selection.Err = nil
		selection.View.Error = ""
	}
	selection.View.URL = selection.Connection.URL
	selection.View.HeaderKeys = headerKeys(selection.Connection)
	selection.View.HeadersManaged = len(selection.Connection.HeadersFromEnv) > 0
	if selection.Err == nil && selection.View.State != "target_changed" {
		headers, err := prom.ResolveHeaders(selection.Connection.Headers, selection.Connection.HeadersFromEnv)
		if err != nil {
			selection.Err = err
			selection.View.State = "error"
			selection.View.Error = err.Error()
		} else {
			selection.Connection.Headers = headers
			selection.Connection.HeadersFromEnv = nil
		}
	}
	p.selected = &selection
	return cloneSelection(selection)
}
