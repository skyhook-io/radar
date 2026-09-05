// Package investigationrefs correlates private Radar MCP results with one
// active AI investigation turn. It is deliberately independent of both the AI
// runner and MCP server so neither package has to trust model-visible markers.
package investigationrefs

import (
	"crypto/rand"
	"errors"
	"strings"
	"sync"
)

const maxIssuedRefsPerScope = 256

var (
	ErrInvalidScope = errors.New("investigation evidence scope is empty")
	ErrScopeActive  = errors.New("investigation evidence scope is already active")
)

// Records is the immutable-by-convention snapshot returned when a turn scope
// closes. Keys are server-issued refs; values are the exact clean text payloads
// produced by Radar before its private marker was prepended.
type Records map[string]string

type scopeState struct {
	records Records
}

// Registry holds only currently active turn scopes. A private MCP request
// cannot allocate state: Begin is called exclusively by the AI runner, and
// Issue fails when its scope is absent or full.
type Registry struct {
	mu     sync.Mutex
	scopes map[string]*scopeState
}

func NewRegistry() *Registry {
	return &Registry{scopes: make(map[string]*scopeState)}
}

// Scope owns one active registry entry. Close is idempotent and returns the
// same point-in-time records on every call.
type Scope struct {
	registry *Registry
	id       string
	once     sync.Once
	records  Records
}

// Begin reserves a caller-generated turn scope. Reusing a live scope fails
// closed so two investigations can never share an issuance domain.
func (r *Registry) Begin(scope string) (*Scope, error) {
	if strings.TrimSpace(scope) == "" {
		return nil, ErrInvalidScope
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.scopes[scope]; exists {
		return nil, ErrScopeActive
	}
	r.scopes[scope] = &scopeState{records: make(Records)}
	return &Scope{registry: r, id: scope}, nil
}

// Active reports whether the AI runner currently owns scope. It is used to
// reject direct calls to the private transport before they execute a tool.
func (r *Registry) Active(scope string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, exists := r.scopes[scope]
	return exists
}

// Issue mints and records a ref only for a live turn. The per-scope limit keeps
// even a hammered active private endpoint bounded; once full, the tool result is
// still returned but receives no citable marker.
func (r *Registry) Issue(scope, payload string) (string, bool) {
	if r == nil {
		return "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state, active := r.scopes[scope]
	if !active || len(state.records) >= maxIssuedRefsPerScope {
		return "", false
	}
	for {
		ref := "ev_" + scope + "_" + strings.ToLower(rand.Text())
		if _, collision := state.records[ref]; collision {
			continue
		}
		state.records[ref] = payload
		return ref, true
	}
}

// Matches reports whether ref was issued for this exact payload while scope is
// still active. Agent-stream adapters can expose marker-shaped text from any MCP
// server in full-local mode; only the private Radar transport can create this
// live ledger entry, so callers must validate before persisting provenance.
func (r *Registry) Matches(scope, ref, payload string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state, active := r.scopes[scope]
	if !active {
		return false
	}
	issuedPayload, issued := state.records[ref]
	return issued && issuedPayload == payload
}

func (s *Scope) Close() Records {
	if s == nil {
		return nil
	}
	s.once.Do(func() {
		s.registry.mu.Lock()
		state := s.registry.scopes[s.id]
		delete(s.registry.scopes, s.id)
		if state != nil {
			s.records = cloneRecords(state.records)
		}
		s.registry.mu.Unlock()
	})
	return cloneRecords(s.records)
}

func cloneRecords(records Records) Records {
	if records == nil {
		return nil
	}
	clone := make(Records, len(records))
	for ref, payload := range records {
		clone[ref] = payload
	}
	return clone
}
