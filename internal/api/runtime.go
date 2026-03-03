package api

import "sync/atomic"

type BackendStatus struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	DefaultModel string `json:"default_model,omitempty"`
	Initialized  bool   `json:"initialized"`
	InitError    string `json:"init_error,omitempty"`
}

type RuntimeState struct {
	RoutingEnabled bool            `json:"routing_enabled"`
	Backends       []BackendStatus `json:"backends"`

	baseReady     bool
	listenerReady atomic.Bool
	draining      atomic.Bool
}

func NewRuntimeState(routingEnabled bool, backends []BackendStatus) *RuntimeState {
	cloned := append([]BackendStatus(nil), backends...)
	state := &RuntimeState{
		RoutingEnabled: routingEnabled,
		Backends:       cloned,
		baseReady:      backendsReady(cloned),
	}
	return state
}

func (s *RuntimeState) Mode() string {
	if len(s.Backends) == 0 {
		return "mock"
	}

	return "backends"
}

func (s *RuntimeState) Ready() bool {
	return s.baseReady && s.listenerReady.Load() && !s.draining.Load()
}

func (s *RuntimeState) SetListenerReady() {
	s.listenerReady.Store(true)
}

func (s *RuntimeState) StartDrain() {
	s.draining.Store(true)
}

func (s *RuntimeState) ConfiguredBackends() int {
	return len(s.Backends)
}

func (s *RuntimeState) InitializedBackends() int {
	count := 0
	for _, backend := range s.Backends {
		if backend.Initialized {
			count++
		}
	}

	return count
}

func (s *RuntimeState) FailedBackends() int {
	count := 0
	for _, backend := range s.Backends {
		if !backend.Initialized {
			count++
		}
	}

	return count
}

func backendsReady(backends []BackendStatus) bool {
	if len(backends) == 0 {
		return true
	}

	for _, backend := range backends {
		if backend.Initialized {
			return true
		}
	}

	return false
}
