package substrate

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/skyhook-io/radar/internal/substrate/ateapi"
	model "github.com/skyhook-io/radar/pkg/substrate"
)

// Read caps. Beyond them the kind is partial with code capExceeded and holds
// the items read so far, never a silent truncation.
const (
	pageSize    = 1000
	readTimeout = 20 * time.Second
	fanOut      = 8
)

// MaxActors and MaxListItems are each kind's budget for one snapshot, across
// every atespace it reads; vars so tests can reach them.
var (
	MaxActors    = 50_000
	MaxListItems = 10_000
)

// Coverage states of one kind.
const (
	CoverageFull    = "full"
	CoveragePartial = "partial"
	CoverageDenied  = "denied"
	CoverageFailed  = "failed"
)

// Coverage says how much of one kind Radar read. What a completed read holds
// was observed over the read's interval (APITier.ReadStartedAt to ReadAt):
// Substrate's pages are not one point in time, so an actor created or
// deleted during the read may be missed. Radar keeps one entry per actor,
// the newest it saw.
type Coverage struct {
	// State is full (every page of every scope read), partial (some
	// atespaces denied, unread or not named, or the budget reached), denied,
	// or failed. full matches the Kubernetes side's KindCoverage word.
	State string `json:"state"`
	// Code is set unless State is full: denied, scoped, capExceeded,
	// unreachable, unauthenticated or failed.
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	// ReadAtespaces are the atespaces read one by one: in a snapshot, when
	// Substrate denied the list across atespaces; on /actors?atespace=, the
	// one atespace read directly. Absent when the kind was read in one list.
	// Being listed does not make an atespace complete: with code capExceeded
	// the budget may have cut any of them short.
	ReadAtespaces []string `json:"readAtespaces,omitzero"`
	// DeniedAtespaces are the atespaces Substrate refused, when reading
	// atespace by atespace.
	DeniedAtespaces []string `json:"deniedAtespaces,omitempty"`
	// UnreadAtespaces are atespaces not read because the budget ran out
	// first; what they hold is unknown.
	UnreadAtespaces []string `json:"unreadAtespaces,omitempty"`
	// Count is how many distinct items were kept; absent when none could be.
	Count *int `json:"count,omitempty"`
	// Cap is the budget reached, with code capExceeded.
	Cap int `json:"cap,omitempty"`
}

// Usable reports whether the kind's items can be shown: all or some of them.
func (c Coverage) Usable() bool { return c.State == CoverageFull || c.State == CoveragePartial }

// SnapshotCoverage is the coverage of each kind in a snapshot.
type SnapshotCoverage struct {
	Actors    Coverage `json:"actors"`
	Templates Coverage `json:"templates"`
	Workers   Coverage `json:"workers"`
	Atespaces Coverage `json:"atespaces"`
}

// Snapshot is one read of Substrate's API, shared by every request within
// its freshness window. Its kinds were read concurrently and its pages
// one after another, so it is an observation over [ReadStartedAt, ReadAt],
// not a census at one instant.
type Snapshot struct {
	ReadStartedAt time.Time
	// ReadAt is when the read completed.
	ReadAt    time.Time
	Actors    []model.Actor
	Templates []model.Template
	Workers   []model.Worker
	Atespaces []model.Atespace
	// ScopedAtespaces are the settings' atespaces, read in place of an
	// atespace list Substrate denied; nil when the list was read or no scope
	// is set.
	ScopedAtespaces []string
	Coverage        SnapshotCoverage

	indexOnce sync.Once
	index     *Index
}

type page[T any] func(ctx context.Context, token string) ([]T, string, error)

// collector gathers one kind under one budget across every scope it reads.
// Substrate's pagination may repeat an item, or show it twice in different
// versions, when the system changes mid-read; the collector keeps one entry
// per identity, the newer one.
type collector[T any] struct {
	mu     sync.Mutex
	limit  int
	items  []T
	at     map[string]int
	ident  func(T) string
	newer  func(a, b T) bool
	capped bool
}

func newCollector[T any](limit int, ident func(T) string, newer func(a, b T) bool) *collector[T] {
	return &collector[T]{limit: limit, at: map[string]int{}, ident: ident, newer: newer}
}

// add keeps what fits and reports whether the budget is spent.
func (c *collector[T]) add(batch []T) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, item := range batch {
		id := c.ident(item)
		if i, ok := c.at[id]; ok {
			if c.newer(item, c.items[i]) {
				c.items[i] = item
			}
			continue
		}
		if len(c.items) >= c.limit {
			c.capped = true
			continue
		}
		c.at[id] = len(c.items)
		c.items = append(c.items, item)
	}
	return len(c.items) >= c.limit
}

func (c *collector[T]) spent() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items) >= c.limit
}

func (c *collector[T]) markCapped() {
	c.mu.Lock()
	c.capped = true
	c.mu.Unlock()
}

// result returns the items in a slice of their own size, so the snapshot
// does not hold the larger array append grew.
func (c *collector[T]) result() ([]T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.items), c.capped
}

// listInto follows next_page_token until the end or the budget, skipping
// empty pages, which Substrate may return mid-list.
func listInto[T any](ctx context.Context, col *collector[T], fetch page[T]) error {
	token := ""
	for {
		batch, next, err := fetch(ctx, token)
		if err != nil {
			return err
		}
		if col.add(batch) {
			if next != "" {
				col.markCapped()
			}
			return nil
		}
		if next == "" {
			return nil
		}
		token = next
	}
}

type kindResult[T any] struct {
	items    []T
	coverage Coverage
	tierErr  *TierError
}

func done[T any](col *collector[T]) kindResult[T] {
	items, capped := col.result()
	n := len(items)
	cov := Coverage{State: CoverageFull, Count: &n}
	if capped {
		cov = Coverage{State: CoveragePartial, Code: CodeCapExceeded, Count: &n, Cap: col.limit,
			Message: "Substrate holds more than Radar reads at once; these are the ones read first."}
	}
	return kindResult[T]{items: items, coverage: cov}
}

func failed[T any](err error, creds *bearerCreds) kindResult[T] {
	code, msg := classify(err, creds)
	r := kindResult[T]{coverage: Coverage{State: CoverageFailed, Code: code, Message: msg}}
	if code == CodeDenied {
		r.coverage.State = CoverageDenied
	}
	if connectionLevel(code) {
		r.tierErr = &TierError{Code: code, Message: msg}
	}
	return r
}

// readKind lists one atespaced kind across all atespaces and, when Substrate
// denies the cross-atespace list, atespace by atespace through names. names
// is the atespace list (complete) or the settings' scope (not complete, so
// what it leaves out stays unknown); nil when neither is available. One
// budget covers every atespace: once it is spent no further atespace is
// started, and those left are reported unread.
func readKind[T any](ctx context.Context, c *conn, newCol func() *collector[T], names []string, complete bool, list func(ctx context.Context, atespace, token string) ([]T, string, error)) kindResult[T] {
	col := newCol()
	err := listInto(ctx, col, func(ctx context.Context, token string) ([]T, string, error) {
		return list(ctx, "", token)
	})
	if err == nil {
		return done(col)
	}
	r := failed[T](err, c.creds)
	if r.coverage.Code != CodeDenied || len(names) == 0 {
		return r
	}
	col = newCol()
	errs := make([]error, len(names))
	visited := make([]bool, len(names))
	sem := make(chan struct{}, fanOut)
	var wg sync.WaitGroup
	for i, name := range names {
		sem <- struct{}{}
		if col.spent() {
			<-sem
			break
		}
		visited[i] = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			errs[i] = listInto(ctx, col, func(ctx context.Context, token string) ([]T, string, error) {
				return list(ctx, name, token)
			})
		}()
	}
	wg.Wait()
	var denied, read, unread []string
	for i, name := range names {
		switch {
		case !visited[i]:
			unread = append(unread, name)
		case errs[i] != nil:
			sub := failed[T](errs[i], c.creds)
			if sub.coverage.Code != CodeDenied {
				return sub
			}
			denied = append(denied, name)
		default:
			read = append(read, name)
		}
	}
	slices.Sort(denied)
	slices.Sort(read)
	slices.Sort(unread)
	if len(read) == 0 && len(unread) == 0 {
		r.coverage.DeniedAtespaces = denied
		return r
	}
	if len(unread) > 0 {
		col.markCapped()
	}
	out := done(col)
	out.coverage.ReadAtespaces, out.coverage.DeniedAtespaces, out.coverage.UnreadAtespaces = read, denied, unread
	if len(denied) == 0 && complete && out.coverage.State == CoverageFull {
		return out
	}
	out.coverage.State = CoveragePartial
	var notes []string
	if !complete {
		notes = append(notes, "Substrate would not list atespaces to Radar's identity, so Radar read only the atespaces named in its settings; others are unknown.")
	}
	if len(denied) > 0 {
		notes = append(notes, "Substrate denied some atespaces to Radar's identity.")
	}
	if out.coverage.Code == CodeCapExceeded {
		notes = append(notes, out.coverage.Message)
		if len(unread) > 0 {
			notes = append(notes, "Atespaces Radar did not reach are unknown.")
		}
	} else if len(denied) > 0 {
		out.coverage.Code = CodeDenied
	} else {
		out.coverage.Code = CodeScoped
	}
	out.coverage.Message = strings.Join(notes, " ")
	return out
}

func refKey(atespace, name string) string { return atespace + "/" + name }

// newerActor: the same actor (UID) in a later version, or a recreation of
// the name (another UID) created later. On a tie the later page wins.
func newerActor(a, b model.Actor) bool {
	if a.UID == b.UID || a.CreateTime.Equal(b.CreateTime) {
		return !a.UpdateTime.Before(b.UpdateTime)
	}
	return a.CreateTime.After(b.CreateTime)
}

func newerTemplate(a, b model.Template) bool {
	if a.UID == b.UID || a.CreateTime.Equal(b.CreateTime) {
		return !a.UpdateTime.Before(b.UpdateTime)
	}
	return a.CreateTime.After(b.CreateTime)
}

func newActors() *collector[model.Actor] {
	return newCollector(MaxActors, func(a model.Actor) string { return refKey(a.Atespace, a.Name) }, newerActor)
}

func newTemplates() *collector[model.Template] {
	return newCollector(MaxListItems, func(t model.Template) string { return refKey(t.Atespace, t.Name) }, newerTemplate)
}

func listActorsPage(c *conn) func(ctx context.Context, atespace, token string) ([]model.Actor, string, error) {
	return func(ctx context.Context, atespace, token string) ([]model.Actor, string, error) {
		resp, err := c.client.ListActors(ctx, &ateapi.ListActorsRequest{Atespace: atespace, Options: &ateapi.ListOptions{PageSize: pageSize, PageToken: token}})
		if err != nil {
			return nil, "", err
		}
		out := make([]model.Actor, 0, len(resp.GetActors()))
		for _, a := range resp.GetActors() {
			out = append(out, ActorOf(a))
		}
		return out, resp.GetNextPageToken(), nil
	}
}

// readSnapshot reads every kind once. It returns a TierError only when the
// connection or identity fails as a whole.
//
// scope is the settings' atespaces, read when Substrate will not list them.
func readSnapshot(parent context.Context, c *conn, now time.Time, scope []string) (*Snapshot, *TierError) {
	ctx, cancel := context.WithTimeout(parent, readTimeout)
	defer cancel()
	snap := &Snapshot{ReadStartedAt: now}

	atespaces := func() kindResult[model.Atespace] {
		col := newCollector(MaxListItems, func(a model.Atespace) string { return a.Name }, func(model.Atespace, model.Atespace) bool { return true })
		err := listInto(ctx, col, func(ctx context.Context, token string) ([]model.Atespace, string, error) {
			resp, err := c.client.ListAtespaces(ctx, &ateapi.ListAtespacesRequest{Options: &ateapi.ListOptions{PageSize: pageSize, PageToken: token}})
			if err != nil {
				return nil, "", err
			}
			out := make([]model.Atespace, 0, len(resp.GetAtespaces()))
			for _, a := range resp.GetAtespaces() {
				out = append(out, model.Atespace{Name: a.GetMetadata().GetName(), CreateTime: ts(a.GetMetadata().GetCreateTime())})
			}
			return out, resp.GetNextPageToken(), nil
		})
		if err != nil {
			return failed[model.Atespace](err, c.creds)
		}
		return done(col)
	}()
	if atespaces.tierErr != nil {
		return nil, atespaces.tierErr
	}
	snap.Atespaces, snap.Coverage.Atespaces = atespaces.items, atespaces.coverage
	slices.SortFunc(snap.Atespaces, func(a, b model.Atespace) int { return strings.Compare(a.Name, b.Name) })
	var names []string
	complete := false
	switch {
	case snap.Coverage.Atespaces.State == CoverageFull:
		complete = true
		for _, a := range snap.Atespaces {
			names = append(names, a.Name)
		}
	case snap.Coverage.Atespaces.Usable():
		for _, a := range snap.Atespaces {
			names = append(names, a.Name)
		}
	case len(scope) > 0:
		names = slices.Sorted(slices.Values(scope))
		snap.ScopedAtespaces = names
	}

	var wg sync.WaitGroup
	var workers kindResult[model.Worker]
	var templates kindResult[model.Template]
	var actors kindResult[model.Actor]
	wg.Add(3)
	go func() {
		defer wg.Done()
		col := newCollector(MaxListItems, func(w model.Worker) string { return w.Name }, func(a, b model.Worker) bool { return !a.UpdateTime.Before(b.UpdateTime) })
		err := listInto(ctx, col, func(ctx context.Context, token string) ([]model.Worker, string, error) {
			resp, err := c.client.ListWorkers(ctx, &ateapi.ListWorkersRequest{Options: &ateapi.ListOptions{PageSize: pageSize, PageToken: token}})
			if err != nil {
				return nil, "", err
			}
			out := make([]model.Worker, 0, len(resp.GetWorkers()))
			for _, w := range resp.GetWorkers() {
				out = append(out, workerOf(w))
			}
			return out, resp.GetNextPageToken(), nil
		})
		if err != nil {
			workers = failed[model.Worker](err, c.creds)
			return
		}
		workers = done(col)
	}()
	go func() {
		defer wg.Done()
		templates = readKind(ctx, c, newTemplates, names, complete, func(ctx context.Context, atespace, token string) ([]model.Template, string, error) {
			resp, err := c.client.ListActorTemplates(ctx, &ateapi.ListActorTemplatesRequest{Atespace: atespace, Options: &ateapi.ListOptions{PageSize: pageSize, PageToken: token}})
			if err != nil {
				return nil, "", err
			}
			out := make([]model.Template, 0, len(resp.GetActorTemplates()))
			for _, t := range resp.GetActorTemplates() {
				out = append(out, TemplateOf(t))
			}
			return out, resp.GetNextPageToken(), nil
		})
	}()
	go func() {
		defer wg.Done()
		actors = readKind(ctx, c, newActors, names, complete, listActorsPage(c))
	}()
	wg.Wait()
	for _, e := range []*TierError{workers.tierErr, templates.tierErr, actors.tierErr} {
		if e != nil {
			return nil, e
		}
	}
	snap.Workers, snap.Coverage.Workers = workers.items, workers.coverage
	snap.Templates, snap.Coverage.Templates = templates.items, templates.coverage
	snap.Actors, snap.Coverage.Actors = actors.items, actors.coverage
	return snap, nil
}

// readAtespaceActors reads one atespace's actors under the actor budget, for
// a query the shared snapshot cannot answer in full.
func readAtespaceActors(parent context.Context, c *conn, atespace string) kindResult[model.Actor] {
	ctx, cancel := context.WithTimeout(parent, readTimeout)
	defer cancel()
	col := newActors()
	if err := listInto(ctx, col, func(ctx context.Context, token string) ([]model.Actor, string, error) {
		return listActorsPage(c)(ctx, atespace, token)
	}); err != nil {
		return failed[model.Actor](err, c.creds)
	}
	r := done(col)
	r.coverage.ReadAtespaces = []string{atespace}
	return r
}
