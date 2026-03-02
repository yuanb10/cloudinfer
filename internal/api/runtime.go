package api

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
}

func (s RuntimeState) Mode() string {
	if len(s.Backends) == 0 {
		return "mock"
	}

	return "backends"
}

func (s RuntimeState) Ready() bool {
	if len(s.Backends) == 0 {
		return true
	}

	for _, backend := range s.Backends {
		if backend.Initialized {
			return true
		}
	}

	return false
}

func (s RuntimeState) ConfiguredBackends() int {
	return len(s.Backends)
}

func (s RuntimeState) InitializedBackends() int {
	count := 0
	for _, backend := range s.Backends {
		if backend.Initialized {
			count++
		}
	}

	return count
}

func (s RuntimeState) FailedBackends() int {
	count := 0
	for _, backend := range s.Backends {
		if !backend.Initialized {
			count++
		}
	}

	return count
}
