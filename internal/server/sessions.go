package server

import (
	"context"
	"io"
	"sync"
)

type sessionRegistry struct {
	mu      sync.Mutex
	closing bool
	active  map[io.Closer]context.CancelFunc
	done    chan struct{}
}

func (s *sessionRegistry) track(parent context.Context, conn io.Closer) (context.Context, func(), bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		_ = conn.Close()
		return parent, func() {}, false
	}
	if s.active == nil {
		s.active = make(map[io.Closer]context.CancelFunc)
	}
	ctx, cancel := context.WithCancel(parent)
	s.active[conn] = cancel
	return ctx, func() {
		cancel()
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.active, conn)
		if s.closing && len(s.active) == 0 && s.done != nil {
			close(s.done)
			s.done = nil
		}
	}, true
}

func (s *sessionRegistry) closeAll() <-chan struct{} {
	s.mu.Lock()
	s.closing = true
	if s.done == nil {
		s.done = make(chan struct{})
	}
	done := s.done
	connections := make(map[io.Closer]context.CancelFunc, len(s.active))
	for conn, cancel := range s.active {
		connections[conn] = cancel
	}
	if len(s.active) == 0 {
		close(done)
		s.done = nil
	}
	s.mu.Unlock()
	for conn, cancel := range connections {
		cancel()
		_ = conn.Close()
	}
	return done
}
