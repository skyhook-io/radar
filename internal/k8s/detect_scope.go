package k8s

import "sync"

// DetectScope holds the cluster-wide lookups that one compose's detector runs
// share. A namespace-filtered compose runs the detectors once per namespace;
// the scope lets those runs read the cluster once, and because it lives for
// one compose, every answer it gives comes from the same reads. A nil scope
// loads every lookup afresh.
type DetectScope struct {
	mu     sync.Mutex
	values map[string]any
}

func NewDetectScope() *DetectScope {
	return &DetectScope{values: map[string]any{}}
}

func (s *DetectScope) get(key string, load func() any) any {
	if s == nil {
		return load()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.values[key]
	if !ok {
		v = load()
		s.values[key] = v
	}
	return v
}
