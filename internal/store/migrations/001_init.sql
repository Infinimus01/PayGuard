CREATE TABLE IF NOT EXISTS tenants (
    id TEXT PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS decisions (
    id UUID PRIMARY KEY,
    tenant_id TEXT NOT NULL REFERENCES tenants (id),
    idempotency_key TEXT NOT NULL,
    payer_id TEXT NOT NULL,
    amount BIGINT NOT NULL CHECK (amount > 0),
    currency CHAR(3) NOT NULL,
    beneficiary_id TEXT NOT NULL,
    channel TEXT NOT NULL,
    action TEXT NOT NULL,
    score INTEGER NOT NULL,
    degraded BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL,
    UNIQUE (tenant_id, idempotency_key)
);

CREATE INDEX IF NOT EXISTS decisions_tenant_created_idx
    ON decisions (tenant_id, created_at DESC);

CREATE TABLE IF NOT EXISTS outcomes (
    decision_id UUID PRIMARY KEY REFERENCES decisions (id),
    tenant_id TEXT NOT NULL,
    status TEXT NOT NULL,
    failure_reason TEXT NOT NULL DEFAULT '',
    reported_at TIMESTAMPTZ NOT NULL
);
