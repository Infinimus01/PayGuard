package store

import (
	"context"
	"testing"
	"time"
)

func sample(id, tenant, idem string) Decision {
	return Decision{
		ID:             id,
		TenantID:       tenant,
		IdempotencyKey: idem,
		Intent: Intent{
			PayerID: "user_1", Amount: 100, Currency: "INR",
			BeneficiaryID: "vpa@bank", Channel: "UPI",
		},
		Action:    "ALLOW",
		CreatedAt: time.Now().UTC(),
	}
}

func TestMemoryIdempotencyAndOutcome(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()
	saved, created, err := m.InsertDecision(ctx, sample("11111111-1111-4111-8111-111111111111", "tenant_a", "k1"))
	if err != nil || !created || saved.ID == "" {
		t.Fatalf("insert: created=%v err=%v", created, err)
	}
	_, created, err = m.InsertDecision(ctx, sample("22222222-2222-4222-8222-222222222222", "tenant_a", "k1"))
	if err != nil || created {
		t.Fatalf("second insert should not create: created=%v err=%v", created, err)
	}
	got, found, err := m.FindByIdempotency(ctx, "tenant_a", "k1")
	if err != nil || !found || got.ID != saved.ID {
		t.Fatalf("find: %+v found=%v err=%v", got, found, err)
	}
	if err := m.ReportOutcome(ctx, "tenant_b", saved.ID, "completed", ""); err != ErrNotFound {
		t.Fatalf("cross tenant: %v", err)
	}
	if err := m.ReportOutcome(ctx, "tenant_a", saved.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if err := m.ReportOutcome(ctx, "tenant_a", saved.ID, "completed", ""); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if err := m.ReportOutcome(ctx, "tenant_a", saved.ID, "chargeback", ""); err != ErrConflict {
		t.Fatalf("conflict: %v", err)
	}
}
