// Package store is the system of record for tenants, decisions, and outcomes.
package store

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound means the decision does not exist for this tenant.
var ErrNotFound = errors.New("decision not found")

// ErrConflict means a different outcome was already stored.
var ErrConflict = errors.New("outcome already reported")

// Intent is the payment the caller asked to score.
type Intent struct {
	PayerID       string
	Amount        int64
	Currency      string
	BeneficiaryID string
	Channel       string
}

// Decision is one scored payment. Outcome is empty until the caller reports it.
type Decision struct {
	ID             string
	TenantID       string
	IdempotencyKey string
	Intent         Intent
	Action         string
	Score          int
	Degraded       bool
	Outcome        string
	FailureReason  string
	CreatedAt      time.Time
}

// Store persists decisions. Implementations isolate rows by tenant id.
type Store interface {
	// FindByIdempotency returns the stored decision for this tenant and key.
	FindByIdempotency(ctx context.Context, tenantID, key string) (Decision, bool, error)
	// InsertDecision inserts a new decision. If the idempotency key already exists,
	// created is false and the existing row is returned.
	InsertDecision(ctx context.Context, d Decision) (saved Decision, created bool, err error)
	// ReportOutcome records the caller's outcome. Repeating the same outcome is success.
	ReportOutcome(ctx context.Context, tenantID, decisionID, status, failureReason string) error
}
