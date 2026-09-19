package prometheus

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
)

const (
	rightsizingScanDuration    = 3 * time.Minute
	rightsizingScanRetention   = 15 * time.Minute
	rightsizingScanMaxRows     = 40000
	rightsizingScanMaxRetained = 8
	rightsizingScanMaxActive   = 2
)

var (
	ErrRightsizingScanNotFound     = errors.New("scan no longer available; run a new scan")
	ErrRightsizingScanScopeChanged = errors.New("scan scope or cluster changed; run a new scan")
	ErrRightsizingScanBusy         = errors.New("two rightsizing scans are already running; wait for one to finish, then retry")
)

type RightsizingScanProgress struct {
	ScanID           string     `json:"scanId,omitempty"`
	ScanStatus       string     `json:"scanStatus,omitempty"`
	DeadlineAt       *time.Time `json:"deadlineAt,omitempty"`
	ExpiresAt        *time.Time `json:"expiresAt,omitempty"`
	PollAfterSeconds int        `json:"pollAfterSeconds,omitempty"`
}

type RightsizingScanRequest struct {
	Generation uint64
	Namespaces []string
	Scope      RightsizingScanScope
	ID         string
	Start      bool
	Refresh    bool
	Cancel     bool
	Wait       time.Duration
}

type scanRunner func(context.Context, RightsizingScanScope, func(RightsizingScanResponse)) RightsizingScanResponse

type rightsizingScanJob struct {
	owner      string
	key        string
	namespaces []string
	result     RightsizingScanResponse
	cancel     context.CancelFunc
	changed    chan struct{}
	valid      func() bool
	active     bool
}

type RightsizingScanManager struct {
	mu         sync.Mutex
	generation uint64
	jobs       map[string]*rightsizingScanJob
	active     int
	duration   time.Duration
	retention  time.Duration
	prepare    func() (scanRunner, func() bool)
}

var scanManager = sync.OnceValue(func() *RightsizingScanManager {
	m := newRightsizingScanManager()
	k8s.OnBeforeContextSwitch(func(string) { m.Invalidate() })
	k8s.OnContextSwitch(func(string) { m.Invalidate() })
	k8s.OnNamespaceRescope(func(string) { m.Invalidate() })
	return m
})

func RightsizingScans() *RightsizingScanManager { return scanManager() }

func newRightsizingScanManager() *RightsizingScanManager {
	return &RightsizingScanManager{
		jobs: map[string]*rightsizingScanJob{}, duration: rightsizingScanDuration, retention: rightsizingScanRetention,
		prepare: func() (scanRunner, func() bool) {
			client, cache := GetClient(), k8s.GetResourceCache()
			var generation, epoch uint64
			if client != nil {
				generation, epoch = client.DiscoveryGeneration(), client.backendEpoch()
			}
			valid := func() bool {
				return GetClient() == client && k8s.GetResourceCache() == cache &&
					(client == nil || client.DiscoveryGeneration() == generation && client.backendEpoch() == epoch)
			}
			return func(ctx context.Context, scope RightsizingScanScope, publish func(RightsizingScanResponse)) RightsizingScanResponse {
				return runRightsizingScan(ctx, scope, client, cache, publish)
			}, valid
		},
	}
}

func (m *RightsizingScanManager) Generation() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.generation
}

func (m *RightsizingScanManager) Invalidate() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.generation++
	for _, job := range m.jobs {
		job.cancel()
		close(job.changed)
		job.changed = make(chan struct{})
	}
	clear(m.jobs)
}

// Namespaces reveals only the owning caller's original scope, for reauthorization
// without applying a different tab's current namespace view filter.
func (m *RightsizingScanManager) Namespaces(ctx context.Context, id string, generation uint64) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.jobs[id]
	if generation != m.generation || job == nil || job.owner != scanOwner(ctx) || !job.valid() {
		return nil, ErrRightsizingScanNotFound
	}
	return slices.Clone(job.namespaces), nil
}

func scanOwner(ctx context.Context) string {
	user := auth.UserFromContext(ctx)
	if user == nil {
		return "local"
	}
	data, _ := json.Marshal(struct {
		Name   string
		Groups []string
	}{user.Username, sortedScanStrings(user.Groups)})
	return string(data)
}

func sortedScanStrings(values []string) []string {
	out := slices.Clone(values)
	slices.Sort(out)
	return slices.Compact(out)
}

func scanScopeKey(namespaces []string, scope RightsizingScanScope) string {
	kinds := make(map[string][]string, len(scope.NamespacesByKind))
	for kind, namespaces := range scope.NamespacesByKind {
		kinds[kind] = sortedScanStrings(namespaces)
	}
	data, _ := json.Marshal(struct {
		Namespaces []string
		Kinds      map[string][]string
		Restricted []string
	}{sortedScanStrings(namespaces), kinds, sortedScanStrings(scope.RestrictedKinds)})
	return string(data)
}

// Resolve accepts an already-authorized scope. Results are immutable snapshots;
// consumers may shape copies but must not mutate their nested rows.
func (m *RightsizingScanManager) Resolve(ctx context.Context, request RightsizingScanRequest) (*RightsizingScanResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	owner, key := scanOwner(ctx), scanScopeKey(request.Namespaces, request.Scope)
	m.mu.Lock()
	if request.Generation != m.generation {
		m.mu.Unlock()
		return nil, ErrRightsizingScanScopeChanged
	}
	m.sweepLocked()
	var job *rightsizingScanJob
	if request.ID != "" {
		job = m.jobs[request.ID]
		if job == nil || job.owner != owner {
			m.mu.Unlock()
			return nil, ErrRightsizingScanNotFound
		}
		if job.key != key {
			m.mu.Unlock()
			return nil, ErrRightsizingScanScopeChanged
		}
	} else {
		for _, candidate := range m.jobs {
			if candidate.owner == owner && candidate.key == key && (job == nil || candidate.result.ScannedAt.After(job.result.ScannedAt)) {
				job = candidate
			}
		}
		if job != nil && request.Refresh && !job.active {
			job = nil
		}
	}
	if job == nil && request.Start {
		if m.active >= rightsizingScanMaxActive {
			m.mu.Unlock()
			return nil, ErrRightsizingScanBusy
		}
		run, valid := m.prepare()
		started := time.Now().UTC()
		deadline := started.Add(m.duration)
		workCtx, cancel := context.WithDeadline(context.Background(), deadline)
		result := newRightsizingScanResponse(started, request.Scope)
		result.RightsizingScanProgress = RightsizingScanProgress{ScanID: "rs_" + auth.NewSessionID(), ScanStatus: "running", DeadlineAt: &deadline, PollAfterSeconds: 5}
		result.State, result.Reason = RightsizingScanPartial, "scan_in_progress"
		job = &rightsizingScanJob{owner: owner, key: key, namespaces: slices.Clone(request.Namespaces), result: result, cancel: cancel, changed: make(chan struct{}), valid: valid, active: true}
		m.jobs[result.ScanID] = job
		m.active++
		go m.run(workCtx, job, request.Scope, run)
	}
	if job == nil {
		m.mu.Unlock()
		return nil, nil
	}
	if request.Cancel && job.active {
		job.cancel()
	}
	changed := job.changed
	shouldWait := request.Wait > 0 && job.active
	m.mu.Unlock()
	if shouldWait {
		timer := time.NewTimer(request.Wait)
		defer timer.Stop()
		select {
		case <-changed:
		case <-ctx.Done():
		case <-timer.C:
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.generation != request.Generation || m.jobs[job.result.ScanID] != job || !job.valid() {
		return nil, ErrRightsizingScanNotFound
	}
	out := cloneScanSnapshot(job.result)
	return &out, nil
}

func (m *RightsizingScanManager) run(ctx context.Context, job *rightsizingScanJob, scope RightsizingScanScope, run scanRunner) {
	defer job.cancel()
	publish := func(result RightsizingScanResponse) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.jobs[job.result.ScanID] != job || !job.valid() {
			job.cancel()
			return
		}
		result.RightsizingScanProgress = job.result.RightsizingScanProgress
		result.State, result.Reason = RightsizingScanPartial, "scan_in_progress"
		job.result = cloneScanSnapshot(result)
		close(job.changed)
		job.changed = make(chan struct{})
	}
	result := run(ctx, scope, publish)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active--
	job.active = false
	if m.jobs[job.result.ScanID] != job {
		return
	}
	result.RightsizingScanProgress = job.result.RightsizingScanProgress
	result.ScanStatus, result.PollAfterSeconds = "finished", 0
	if ctx.Err() != nil && result.State != RightsizingScanComplete {
		result.State = RightsizingScanPartial
		if result.Coverage.WorkloadsEvaluated == 0 {
			result.State = RightsizingScanUnavailable
		}
		result.ScanStatus, result.Reason = "cancelled", "scan_cancelled"
		result.Warnings = slices.DeleteFunc(result.Warnings, func(w RightsizingScanWarning) bool { return w.Code == ReasonScanDeadlineExceeded })
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			result.ScanStatus, result.Reason = "timed_out", ReasonScanDeadlineExceeded
		}
	}
	expires := time.Now().UTC().Add(m.retention)
	result.ExpiresAt = &expires
	job.result = cloneScanSnapshot(result)
	close(job.changed)
	job.changed = make(chan struct{})
	m.sweepLocked()
}

func (m *RightsizingScanManager) sweepLocked() {
	var terminal []*rightsizingScanJob
	rows := 0
	for id, job := range m.jobs {
		if !job.valid() || job.result.ExpiresAt != nil && time.Now().After(*job.result.ExpiresAt) {
			job.cancel()
			close(job.changed)
			delete(m.jobs, id)
			continue
		}
		if !job.active {
			terminal = append(terminal, job)
			for _, workload := range job.result.Workloads {
				rows += len(workload.Rows)
			}
		}
	}
	slices.SortFunc(terminal, func(a, b *rightsizingScanJob) int { return a.result.ExpiresAt.Compare(*b.result.ExpiresAt) })
	for len(terminal) > rightsizingScanMaxRetained || rows > rightsizingScanMaxRows {
		job := terminal[0]
		terminal = terminal[1:]
		delete(m.jobs, job.result.ScanID)
		for _, workload := range job.result.Workloads {
			rows -= len(workload.Rows)
		}
	}
}

func cloneScanSnapshot(result RightsizingScanResponse) RightsizingScanResponse {
	result.Workloads = slices.Clone(result.Workloads)
	result.Warnings = slices.Clone(result.Warnings)
	result.Coverage.RestrictedKinds = slices.Clone(result.Coverage.RestrictedKinds)
	result.Coverage.UnavailableKinds = slices.Clone(result.Coverage.UnavailableKinds)
	result.Coverage.PartiallyCachedKinds = slices.Clone(result.Coverage.PartiallyCachedKinds)
	return result
}
