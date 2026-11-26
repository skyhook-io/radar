package portforward

import (
	"context"
	"errors"
	"sync"
)

type Handles struct {
	mu     sync.Mutex
	active map[*Handle]struct{}
	closed bool
}

func (s *Handles) Track(h *Handle) error {
	if h == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		h.Stop()
		return ErrClosed
	}
	if s.active == nil {
		s.active = make(map[*Handle]struct{})
	}
	s.active[h] = struct{}{}
	s.mu.Unlock()
	go func() {
		<-h.Done()
		s.mu.Lock()
		delete(s.active, h)
		s.mu.Unlock()
	}()
	return nil
}

func (s *Handles) Close() {
	s.mu.Lock()
	s.closed = true
	handles := s.active
	s.active = nil
	s.mu.Unlock()
	for h := range handles {
		h.Stop()
	}
}

func WithLifetime(ctx, lifetime context.Context) (context.Context, context.CancelFunc) {
	if lifetime == nil {
		return ctx, func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	if lifetime.Err() != nil {
		cancel()
	}
	stop := context.AfterFunc(lifetime, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

func IsMemberClosed(ctx context.Context, err error) bool {
	return ctx.Err() == nil && (errors.Is(err, context.Canceled) || errors.Is(err, ErrClosed))
}
