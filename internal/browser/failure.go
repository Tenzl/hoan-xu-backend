package browser

import "time"

// Failure contains only safe diagnostics, never upstream text, URLs or credentials.
type Failure struct {
	Code      string
	Phase     string
	Retryable bool
}

func (e *Failure) Error() string { return e.Code }

type LastFailure struct {
	Code  string    `json:"code"`
	Phase string    `json:"phase"`
	At    time.Time `json:"at"`
}

func failure(code, phase string, retryable bool) *Failure {
	return &Failure{Code: code, Phase: phase, Retryable: retryable}
}

func (m *Manager) RecordFailure(code, phase string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastFailure = &LastFailure{Code: code, Phase: phase, At: time.Now().UTC()}
}
