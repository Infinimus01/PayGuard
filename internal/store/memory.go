package store

import (
	"context"
	"sync"
)

// Memory is a process-local store. Tests use it. Production uses Postgres.
type Memory struct {
	mu     sync.Mutex
	byID   map[string]Decision
	byIdem map[string]string
}

// NewMemory returns an empty store.
func NewMemory() *Memory {
	return &Memory{
		byID:   make(map[string]Decision),
		byIdem: make(map[string]string),
	}
}

func (m *Memory) FindByIdempotency(_ context.Context, tenantID, key string) (Decision, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.byIdem[idemSlot(tenantID, key)]
	if !ok {
		return Decision{}, false, nil
	}
	return m.byID[id], true, nil
}

func (m *Memory) InsertDecision(_ context.Context, d Decision) (Decision, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	slot := idemSlot(d.TenantID, d.IdempotencyKey)
	if id, ok := m.byIdem[slot]; ok {
		return m.byID[id], false, nil
	}
	m.byID[d.ID] = d
	m.byIdem[slot] = d.ID
	return d, true, nil
}

func (m *Memory) ReportOutcome(_ context.Context, tenantID, decisionID, status, failureReason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.byID[decisionID]
	if !ok || row.TenantID != tenantID {
		return ErrNotFound
	}
	if row.Outcome != "" && (row.Outcome != status || row.FailureReason != failureReason) {
		return ErrConflict
	}
	row.Outcome = status
	row.FailureReason = failureReason
	m.byID[decisionID] = row
	return nil
}

func idemSlot(tenantID, key string) string {
	return tenantID + "|" + key
}
