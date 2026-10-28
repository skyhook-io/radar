package k8s

import "sync"

// DetectScope holds the cluster-wide lookups that one compose's detector runs
// share. A namespace-filtered compose runs the detectors once per namespace;
// the scope lets those runs read the cluster once, and because it lives for
// one compose, every answer it gives comes from the same reads. Entry points
// given no scope start their own, so a single call still reads each lookup
// once.
type DetectScope struct {
	mu     sync.Mutex
	values map[string]any
}

func NewDetectScope() *DetectScope {
	return &DetectScope{values: map[string]any{}}
}

// get returns the value for key, loading it on first use. The lock covers
// only the map: loaders call get for the lookups they build on, and two runs
// racing on one key just load it twice.
func (s *DetectScope) get(key string, load func() any) any {
	if s == nil {
		return load()
	}
	s.mu.Lock()
	v, ok := s.values[key]
	s.mu.Unlock()
	if ok {
		return v
	}
	v = load()
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.values[key]; ok {
		return prev
	}
	s.values[key] = v
	return v
}
