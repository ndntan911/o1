package ai

import (
	"context"
	"sync"
)

// Stub answers deterministically. It exists so the feature can be built,
// tested and demonstrated without a provider key: the request is recorded, and
// Reply is returned verbatim.
//
// In production the provider name "stub" is only reachable when someone sets
// O1_AI_PROVIDER=stub deliberately.
type Stub struct {
	Reply string
	Err   error

	mu      sync.Mutex
	lastReq Request
	calls   int
}

// Complete records the request and returns Reply.
func (s *Stub) Complete(_ context.Context, req Request) (Response, error) {
	s.mu.Lock()
	s.lastReq = req
	s.calls++
	s.mu.Unlock()

	if s.Err != nil {
		return Response{}, s.Err
	}
	return Response{Text: s.Reply, Model: "stub", InputTokens: len(req.User) / 4, OutputTokens: len(s.Reply) / 4}, nil
}

// Calls reports how many completions the stub served.
func (s *Stub) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// LastRequest returns the most recent request, for assertions in tests.
func (s *Stub) LastRequest() Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastReq
}
