package substrate

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/portforward"
	"github.com/skyhook-io/radar/internal/substrate/ateapi"
	model "github.com/skyhook-io/radar/pkg/substrate"
)

// Freshness is how long after its completion one snapshot serves requests
// without a refresh. MaxStale is how long after its completion the last good
// snapshot is still served, marked refreshing, while a refresh runs or after
// one failed; past it a failure is the answer.
const (
	Freshness = 5 * time.Second
	MaxStale  = 2 * time.Minute
)

// API tier states.
const (
	// TierConnected: the snapshot served was read; it may be stale while a
	// refresh runs or after one failed (APITier.Refreshing, LastError).
	TierConnected = "connected"
	// TierConnecting: settings are saved and the first read is running.
	TierConnecting = "connecting"
	// TierNotConfigured: no identity is saved for this context.
	TierNotConfigured = "notConfigured"
	// TierDisabled: this Radar does not read Substrate's API for this
	// request: it has no sign-in (sharedRadar), or the operator disabled it
	// (apiDisabled).
	TierDisabled = "disabled"
	// TierFailed: settings exist but no usable snapshot could be read.
	TierFailed = "failed"
)

// Identity is who Radar reads Substrate as.
type Identity struct {
	// Mode is shared, tokenFile, or sharedAsCaller: the shared identity,
	// minted with the requesting user's Kubernetes access.
	Mode string `json:"mode"`
	// Principal is who Substrate sees. Absent for tokenFile: Substrate does
	// not tell a client who its token authenticates as.
	Principal string `json:"principal,omitempty"`
}

// APITier is the state of Radar's connection to Substrate's API.
type APITier struct {
	// State is connected, connecting, notConfigured, disabled or failed.
	State string `json:"state"`
	// Reason says why, in a sentence; absent when connected.
	Reason string `json:"reason,omitempty"`
	// Code is set with notConfigured (notConfigured), disabled (sharedRadar,
	// apiDisabled) and failed (targetChanged, contextChanged, notInstalled,
	// tls, unreachable, unauthenticated, mintDenied, settingsFile or failed);
	// absent when connected or connecting.
	Code string `json:"code,omitempty"`
	// Identity and Endpoint are set once settings are saved.
	Identity *Identity `json:"identity,omitempty"`
	Endpoint string    `json:"endpoint,omitempty"`
	// ReadStartedAt and ReadAt bound the read that produced the snapshot
	// served: its counts were observed over that interval. ReadAt is the
	// completion. Both absent when no read has completed.
	ReadStartedAt time.Time `json:"readStartedAt,omitzero"`
	ReadAt        time.Time `json:"readAt,omitzero"`
	// Refreshing: the snapshot served is older than Freshness and a newer
	// read is running, or the last one failed (LastError).
	Refreshing bool `json:"refreshing,omitempty"`
	// LastError is the newest read's failure while an older snapshot is
	// served; absent when the newest read succeeded.
	LastError *ReadError `json:"lastError,omitempty"`
}

// ReadError is a failed read.
type ReadError struct {
	Code    string    `json:"code"`
	Message string    `json:"message"`
	At      time.Time `json:"at"`
}

func identityOf(s Settings) *Identity {
	id := &Identity{Mode: s.Identity}
	if s.Identity == IdentityShared || s.Identity == IdentitySharedAsCaller {
		id.Principal = SharedPrincipal
	}
	return id
}

// Manager owns Radar's one client per (context, settings) and its snapshot.
//
// Work runs under a generation. Reset (a context switch, a settings save)
// starts a new one and cancels the old one's dials and reads; a result from
// a retired generation is discarded, never published or served.
type Manager struct {
	Store *Store
	// Kube captures the current cluster connection: its identity and its
	// clients together, consistent with context switches.
	Kube func() (Kube, error)
	Now  func() time.Time
	// CallerDial is where IdentitySharedAsCaller connects: endpoint, CA file
	// and server name. Zero dials the API's Service in the cluster.
	CallerDial  Settings
	selectionMu sync.Mutex
	selection   string
	callerTok   callerToken

	// dialMu serializes dials: every conn shares Radar's one Substrate
	// port-forward, so two racing dials would tear down each other's tunnel.
	dialMu sync.Mutex
	mu     sync.Mutex
	gen    uint64
	genCtx context.Context
	cancel context.CancelFunc
	conn   *conn
	// good is the newest successful read of this generation; failed is the
	// newest read when it failed after good.
	good   *cachedRead
	failed *cachedRead
	flight singleflight.Group
	// scoped caches per-atespace actor reads, by generation, key and atespace.
	scoped map[string]*scopedRead
}

type cachedRead struct {
	gen  uint64
	key  string
	at   time.Time
	snap *Snapshot
	err  *TierError
}

type scopedRead struct {
	at     time.Time
	result kindResult[model.Actor]
}

type readResult struct {
	snap *Snapshot
	err  *TierError
}

func NewManager(store *Store, kube func() (Kube, error)) *Manager {
	m := &Manager{Store: store, Kube: kube, Now: time.Now, CallerDial: DefaultInstallationSettings()}
	m.genCtx, m.cancel = context.WithCancel(context.Background())
	return m
}

var errRetired = &TierError{Code: CodeContextChanged, Message: "The cluster connection or Substrate's settings changed during the read; try again."}

// Reset retires the current generation: its client, its cached snapshot and
// any dial or read still running. A context switch and a settings save call it.
func (m *Manager) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gen++
	m.cancel()
	m.genCtx, m.cancel = context.WithCancel(context.Background())
	// Closed under mu, so no dial of the new generation can have started a
	// port-forward that this close would stop.
	m.conn.close()
	m.conn, m.good, m.failed, m.scoped = nil, nil, nil, nil
	m.callerTok.mu.Lock()
	m.callerTok.clearLocked("")
	m.callerTok.mu.Unlock()
}

func (m *Manager) generation() (uint64, context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.gen, m.genCtx
}

func (m *Manager) current(gen uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.gen == gen
}

// stillCaptured reports whether the cluster connection is the one k captured.
func (m *Manager) stillCaptured(k Kube) bool {
	if k.Operation != nil && k.Operation.Err() != nil {
		return false
	}
	now, err := m.Kube()
	return err == nil && now.Binding == k.Binding && now.Fingerprint == k.Fingerprint && now.Generation == k.Generation
}

// settings returns the settings to read with, or a tier state that says why
// there are none: for a request served as IdentitySharedAsCaller (WithCaller)
// the implicit ones, with the token minted for it; otherwise the current
// context's saved settings.
func (m *Manager) settings(ctx context.Context) (Kube, *Settings, *APITier) {
	c, isCaller := callerFrom(ctx)
	// A caller's tier names its identity in every state, failures included,
	// so the UI can tell it has no settings to change.
	var callerTier APITier
	if isCaller {
		s := m.callerSettings()
		callerTier = APITier{Identity: identityOf(s), Endpoint: endpointLabel(s)}
	}
	k, err := m.Kube()
	if errors.Is(err, ErrClusterChanging) {
		tier := callerTier
		tier.State, tier.Code, tier.Reason = TierFailed, CodeContextChanged, err.Error()
		return k, nil, &tier
	}
	if err != nil {
		m.Reset()
		tier := callerTier
		tier.State, tier.Code, tier.Reason = TierFailed, CodeUnreachable, "Not connected to Kubernetes: "+err.Error()
		return k, nil, &tier
	}
	if isCaller {
		s := m.callerSettings()
		tier := &callerTier
		if tierErr := m.ensureCallerToken(ctx, k, c); tierErr != nil {
			tier.State, tier.Code, tier.Reason = TierFailed, tierErr.Code, tierErr.Message
			return k, nil, tier
		}
		return k, &s, tier
	}
	saved, ok, settingsErr := m.selectLocal(k)
	if settingsErr != nil {
		return k, nil, &APITier{State: TierFailed, Code: CodeSettingsFile, Reason: settingsErr.Error()}
	}
	if !ok {
		return k, nil, &APITier{State: TierNotConfigured, Code: CodeNotConfigured, Reason: "Choose who Radar reads Substrate's API as."}
	}
	tier := &APITier{Identity: identityOf(saved.Settings), Endpoint: endpointLabel(saved.Settings)}
	if saved.Target != k.Fingerprint {
		tier.State, tier.Code = TierFailed, CodeTargetChanged
		tier.Reason = "This context now points at a different cluster than the one these settings were saved for. Save them again to use them here."
		return k, nil, tier
	}
	s := saved.Settings
	return k, &s, tier
}

// ErrClusterChanging is returned by a Kube capture taken while a context
// operation runs.
var ErrClusterChanging = errors.New("the cluster connection is changing; try again in a moment")

// client returns the conn for k and s under generation gen, dialing a new one
// when either changed or its port-forward dropped. A dial that finishes after
// its generation retired, or after the cluster connection moved, is closed
// rather than published.
func (m *Manager) client(ctx context.Context, k Kube, s Settings, gen uint64) (*conn, error) {
	key := connKey(k, s)
	m.dialMu.Lock()
	defer m.dialMu.Unlock()
	m.mu.Lock()
	if m.gen != gen {
		m.mu.Unlock()
		return nil, errRetired
	}
	if m.conn != nil && m.conn.key == key && m.conn.alive() {
		c := m.conn
		m.mu.Unlock()
		return c, nil
	}
	old := m.conn
	m.conn = nil
	old.close()
	m.mu.Unlock()

	var caller tokenSource
	if s.Identity == IdentitySharedAsCaller {
		caller = callerSource{m: m, key: callerTokenKey(k)}
	}
	c, err := dial(ctx, k, s, caller)
	if err != nil {
		if ctx.Err() != nil && !m.current(gen) {
			return nil, errRetired
		}
		return nil, err
	}
	if !m.stillCaptured(k) {
		c.close()
		return nil, errRetired
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.gen != gen {
		c.close()
		return nil, errRetired
	}
	m.conn = c
	return c, nil
}

func (m *Manager) dropIf(c *conn) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conn == c {
		m.conn.close()
		m.conn = nil
	}
}

// Snapshot returns the snapshot to serve. A read under Freshness old is
// served as it is. An older one, up to MaxStale, is served marked
// refreshing while a refresh runs in the background, and after that refresh
// fails, with its error. With nothing usable the caller waits for the read.
// Concurrent callers of one generation and settings share one read, and each
// gets that read's own result. The snapshot is nil unless the tier is
// connected.
func (m *Manager) Snapshot(ctx context.Context) (*Snapshot, APITier) {
	return m.snapshot(ctx, true)
}

// Peek is Snapshot that never waits: with nothing usable it starts the read
// and answers connecting. Status uses it, so it never waits on an actor walk.
func (m *Manager) Peek(ctx context.Context) (*Snapshot, APITier) {
	return m.snapshot(ctx, false)
}

func (m *Manager) snapshot(ctx context.Context, wait bool) (*Snapshot, APITier) {
	k, s, tier := m.settings(ctx)
	if s == nil {
		return nil, *tier
	}
	key := connKey(k, *s)
	m.mu.Lock()
	gen, genCtx := m.gen, m.genCtx
	now := m.Now()
	good, failed := m.good, m.failed
	if good != nil && (good.gen != gen || good.key != key) {
		good = nil
	}
	if failed != nil && (failed.gen != gen || failed.key != key) {
		failed = nil
	}
	m.mu.Unlock()

	if good != nil && now.Sub(good.at) < Freshness && failed == nil {
		return good.snap, withRead(*tier, good.snap, nil)
	}
	recentFailure := failed != nil && now.Sub(failed.at) < Freshness
	result := m.startRead(k, *s, key, gen, genCtx, recentFailure)
	if good != nil && now.Sub(good.at) < MaxStale {
		served := withRead(*tier, good.snap, nil)
		served.Refreshing = true
		if failed != nil {
			served.LastError = &ReadError{Code: failed.err.Code, Message: failed.err.Message, At: failed.at}
		}
		return good.snap, served
	}
	if recentFailure {
		return nil, withRead(*tier, nil, failed.err)
	}
	// A retry after a failure is not a first read: keep saying why the last
	// one failed while it runs, so a failing connection doesn't flicker
	// between failed and connecting.
	if failed != nil && !wait {
		served := withRead(*tier, nil, failed.err)
		served.Refreshing = true
		return nil, served
	}
	if !wait {
		tier.State, tier.Code, tier.Reason = TierConnecting, "", "Radar is reading Substrate's API for the first time."
		return nil, *tier
	}
	select {
	case r := <-result:
		res := r.Val.(readResult)
		return res.snap, withRead(*tier, res.snap, res.err)
	case <-ctx.Done():
		return nil, APITier{State: TierFailed, Code: CodeUnreachable, Reason: "The request ended before Substrate's API answered.", Identity: tier.Identity, Endpoint: tier.Endpoint}
	}
}

// startRead joins or starts the read for (gen, key); with skip, a read that
// failed under Freshness ago is not retried yet and nothing starts.
func (m *Manager) startRead(k Kube, s Settings, key string, gen uint64, genCtx context.Context, skip bool) <-chan singleflight.Result {
	if skip {
		return nil
	}
	// The generation is part of the key, so a caller after Reset never joins
	// a retired flight.
	return m.flight.DoChan(strconv.FormatUint(gen, 10)+"\x00"+key, func() (any, error) {
		// Detached from any one request, so a caller that goes away does not
		// cancel the read others wait on; Reset cancels it through genCtx.
		readCtx, cancel := context.WithTimeout(genCtx, readTimeout+5*time.Second)
		defer cancel()
		snap, tierErr := m.read(readCtx, k, s, gen)
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.gen != gen {
			return readResult{err: errRetired}, nil
		}
		entry := &cachedRead{gen: gen, key: key, at: m.Now(), snap: snap, err: tierErr}
		if tierErr != nil {
			m.failed = entry
		} else {
			m.good, m.failed = entry, nil
		}
		return readResult{snap: snap, err: tierErr}, nil
	})
}

func withRead(tier APITier, snap *Snapshot, err *TierError) APITier {
	if err != nil {
		tier.State, tier.Code, tier.Reason = TierFailed, err.Code, err.Message
		return tier
	}
	tier.State = TierConnected
	if snap != nil {
		tier.ReadStartedAt, tier.ReadAt = snap.ReadStartedAt, snap.ReadAt
	}
	return tier
}

func (m *Manager) read(ctx context.Context, k Kube, s Settings, gen uint64) (*Snapshot, *TierError) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if k.Operation != nil {
		defer context.AfterFunc(k.Operation, cancel)()
	}
	c, err := m.client(ctx, k, s, gen)
	if err != nil {
		code, msg := classify(err, nil)
		return nil, &TierError{Code: code, Message: msg}
	}
	snap, tierErr := readSnapshot(ctx, c, m.Now(), s.Atespaces)
	if !m.current(gen) || !m.stillCaptured(k) {
		return nil, errRetired
	}
	if snap != nil {
		snap.ReadAt = m.Now()
	}
	if tierErr != nil {
		// The next read dials again: a fresh token, a new port-forward.
		m.dropIf(c)
		log.Printf("[substrate] Read failed (%s)", tierErr.Code)
	}
	return snap, tierErr
}

// MaxScopedReads bounds the per-atespace actor reads kept.
const MaxScopedReads = 16

// AtespaceActors reads one atespace's actors directly, for a query the shared
// snapshot cannot answer in full. A read under Freshness old is reused.
func (m *Manager) AtespaceActors(ctx context.Context, atespace string) ([]model.Actor, Coverage, *APITier) {
	k, s, tier := m.settings(ctx)
	if s == nil {
		return nil, Coverage{}, tier
	}
	key := connKey(k, *s)
	gen, genCtx := m.generation()
	cacheKey := strconv.FormatUint(gen, 10) + "\x00" + key + "\x00" + atespace
	m.mu.Lock()
	if e := m.scoped[cacheKey]; e != nil && m.Now().Sub(e.at) < Freshness {
		m.mu.Unlock()
		return e.result.items, e.result.coverage, nil
	}
	m.mu.Unlock()
	ch := m.flight.DoChan("atespace\x00"+cacheKey, func() (any, error) {
		readCtx, cancel := context.WithTimeout(genCtx, readTimeout+5*time.Second)
		defer cancel()
		c, err := m.client(readCtx, k, *s, gen)
		if err != nil {
			code, msg := classify(err, nil)
			return kindResult[model.Actor]{tierErr: &TierError{Code: code, Message: msg}}, nil
		}
		r := readAtespaceActors(readCtx, c, atespace)
		if !m.current(gen) || !m.stillCaptured(k) {
			return kindResult[model.Actor]{tierErr: errRetired}, nil
		}
		if r.tierErr != nil {
			m.dropIf(c)
			return r, nil
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.gen == gen {
			if m.scoped == nil {
				m.scoped = map[string]*scopedRead{}
			}
			if len(m.scoped) >= MaxScopedReads {
				var oldest string
				for k, e := range m.scoped {
					if oldest == "" || e.at.Before(m.scoped[oldest].at) {
						oldest = k
					}
				}
				delete(m.scoped, oldest)
			}
			m.scoped[cacheKey] = &scopedRead{at: m.Now(), result: r}
		}
		return r, nil
	})
	select {
	case res := <-ch:
		r := res.Val.(kindResult[model.Actor])
		if r.tierErr != nil {
			tier.State, tier.Code, tier.Reason = TierFailed, r.tierErr.Code, r.tierErr.Message
			return nil, Coverage{}, tier
		}
		return r.items, r.coverage, nil
	case <-ctx.Done():
		tier.State, tier.Code, tier.Reason = TierFailed, CodeUnreachable, "The request ended before Substrate's API answered."
		return nil, Coverage{}, tier
	}
}

// Call runs one RPC on the current client, for reads outside the snapshot
// (an actor's detail). It returns the tier when the call could not be made,
// and maps a failed RPC to a code and a scrubbed message. Reset cancels it.
func (m *Manager) Call(ctx context.Context, fn func(ctx context.Context, client ateapi.ControlClient) error) (*APITier, string, string) {
	k, s, tier := m.settings(ctx)
	if s == nil {
		return tier, "", ""
	}
	gen, genCtx := m.generation()
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	stop := context.AfterFunc(genCtx, cancel)
	defer stop()
	if k.Operation != nil {
		defer context.AfterFunc(k.Operation, cancel)()
	}
	retired := func() (*APITier, string, string) {
		tier.State, tier.Code, tier.Reason = TierFailed, errRetired.Code, errRetired.Message
		return tier, "", ""
	}
	c, err := m.client(ctx, k, *s, gen)
	if err != nil {
		code, msg := classify(err, nil)
		tier.State, tier.Code, tier.Reason = TierFailed, code, msg
		return tier, "", ""
	}
	err = fn(ctx, c.client)
	if !m.current(gen) || !m.stillCaptured(k) {
		return retired()
	}
	if err != nil {
		code, msg := classify(err, c.creds)
		if connectionLevel(code) {
			m.dropIf(c)
			tier.State, tier.Code, tier.Reason = TierFailed, code, msg
			return tier, "", ""
		}
		return nil, code, msg
	}
	return nil, "", ""
}

// ProbeTimeout bounds the check a save makes before it persists.
const ProbeTimeout = 10 * time.Second

// Probe dials k with s, on a client of its own that is never published, and
// makes one cheap read: the first page of atespaces. PermissionDenied counts
// as success: Substrate authenticated the identity and reached its policy,
// as it does for a token scoped to some atespaces. It returns nil, or a
// TierError whose code says what failed.
//
// It holds dialMu throughout: the probe's port-forward is Radar's one
// Substrate forward, so a dial running beside it could have the forward it
// is starting replaced, or stopped by the probe's close. It ends with k's
// context operation, so a probe for a context being left cannot replace the
// next context's forward.
func (m *Manager) Probe(ctx context.Context, k Kube, s Settings) *TierError {
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	if k.Operation != nil {
		defer context.AfterFunc(k.Operation, cancel)()
	}
	m.dialMu.Lock()
	defer m.dialMu.Unlock()
	if !m.stillCaptured(k) {
		return &TierError{Code: CodeContextChanged, Message: ErrClusterChanging.Error()}
	}
	c, err := dial(ctx, k, s, nil)
	if err != nil {
		code, msg := classify(err, nil)
		return &TierError{Code: code, Message: msg}
	}
	defer m.closeProbe(c)
	_, err = c.client.ListAtespaces(ctx, &ateapi.ListAtespacesRequest{Options: &ateapi.ListOptions{PageSize: 1}})
	if err == nil {
		return nil
	}
	code, msg := classify(err, c.creds)
	if code == CodeDenied {
		return nil
	}
	return &TierError{Code: code, Message: msg}
}

// closeProbe closes a probe's client. Its port-forward is Radar's one
// Substrate forward, which the published client may share, so it is stopped
// only when no published client uses it.
func (m *Manager) closeProbe(c *conn) {
	if c.cc != nil {
		c.cc.Close()
	}
	if c.forwardPort == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conn == nil || m.conn.forwardPort != c.forwardPort {
		portforward.StopIfAddress(portforward.OwnerSubstrate, forwardAddress(c.forwardPort))
	}
}

// Target is the context a connection grant is saved for: the binding and
// fingerprint of k8s.ProfileTarget, captured by the caller under the
// cluster-configuration guard after checking it is the target the user
// reviewed.
type Target struct {
	Binding     string
	Context     string
	Fingerprint string
	Identity    config.TargetIdentity
}

// SaveRevision saves (nil removes) target's settings and retires the current
// generation, so the next read uses them. It does not capture the cluster
// itself: the caller holds the configuration guard, under which a capture
// would be refused.
func (m *Manager) SaveRevision(ctx context.Context, target Target, revision string, s *Settings) error {
	var saved *Saved
	if s != nil {
		if err := s.Validate(); err != nil {
			return &TierError{Code: CodeInvalidSettings, Message: err.Error()}
		}
		saved = &Saved{Context: target.Context, Target: target.Fingerprint, Identity: target.Identity, Settings: *s}
	}
	if _, err := m.Store.Update(ctx, revision, target.Binding, saved); err != nil {
		return err
	}
	m.Reset()
	return nil
}

// SavedSettings returns the current context's saved settings, if any, and
// whether they were saved for the cluster the context points at now.
func (m *Manager) SavedSettings() (*Saved, bool, error) {
	k, err := m.Kube()
	if err != nil {
		return nil, false, err
	}
	saved, ok, err := m.Store.Get(k.Binding)
	if err != nil || !ok {
		return nil, false, err
	}
	return &saved, saved.Target == k.Fingerprint, nil
}

// Select retires work when the selected file or Kubernetes target changes.
// It never calls the runtime refresh hook: activation may hold its lock.
func (m *Manager) Select(k Kube) { m.selectLocal(k) }

func (m *Manager) selectLocal(k Kube) (Saved, bool, error) {
	m.selectionMu.Lock()
	defer m.selectionMu.Unlock()
	saved, exists, revision, err := m.Store.GetRevision(k.Binding)
	errorText := ""
	if err != nil {
		errorText = err.Error()
	}
	data, _ := json.Marshal([]string{k.Binding, k.Fingerprint, k.Generation, revision, errorText})
	selection := string(data)
	if m.selection != selection {
		m.Reset()
		m.selection = selection
	}
	return saved, exists, err
}
