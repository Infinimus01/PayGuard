package store

import (
	"context"
	_ "embed"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/001_init.sql
var migrationSQL string

// Postgres is the durable store. One database, tenant_id on every row.
type Postgres struct {
	pool *pgxpool.Pool
}

// OpenPostgres connects, applies the schema, and returns a store.
func OpenPostgres(ctx context.Context, dsn string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if _, err := pool.Exec(ctx, migrationSQL); err != nil {
		pool.Close()
		return nil, err
	}
	return &Postgres{pool: pool}, nil
}

// Close releases the pool.
func (p *Postgres) Close() {
	p.pool.Close()
}

func (p *Postgres) FindByIdempotency(ctx context.Context, tenantID, key string) (Decision, bool, error) {
	row := p.pool.QueryRow(ctx, decisionSelect+` WHERE d.tenant_id = $1 AND d.idempotency_key = $2`, tenantID, key)
	d, err := scanDecision(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Decision{}, false, nil
	}
	if err != nil {
		return Decision{}, false, err
	}
	return d, true, nil
}

func (p *Postgres) InsertDecision(ctx context.Context, d Decision) (Decision, bool, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return Decision{}, false, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `INSERT INTO tenants (id) VALUES ($1) ON CONFLICT (id) DO NOTHING`, d.TenantID); err != nil {
		return Decision{}, false, err
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO decisions (
			id, tenant_id, idempotency_key, payer_id, amount, currency,
			beneficiary_id, channel, action, score, degraded, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (tenant_id, idempotency_key) DO NOTHING`,
		d.ID, d.TenantID, d.IdempotencyKey, d.Intent.PayerID, d.Intent.Amount, d.Intent.Currency,
		d.Intent.BeneficiaryID, d.Intent.Channel, d.Action, d.Score, d.Degraded, d.CreatedAt,
	)
	if err != nil {
		return Decision{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Decision{}, false, err
	}
	if tag.RowsAffected() == 1 {
		return d, true, nil
	}
	existing, found, err := p.FindByIdempotency(ctx, d.TenantID, d.IdempotencyKey)
	if err != nil {
		return Decision{}, false, err
	}
	if !found {
		return Decision{}, false, errors.New("decision insert conflict but row missing")
	}
	return existing, false, nil
}

func (p *Postgres) ReportOutcome(ctx context.Context, tenantID, decisionID, status, failureReason string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var id string
	err = tx.QueryRow(ctx, `SELECT id::text FROM decisions WHERE id = $1 AND tenant_id = $2 FOR UPDATE`, decisionID, tenantID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}

	var existingStatus, existingReason string
	err = tx.QueryRow(ctx, `SELECT status, failure_reason FROM outcomes WHERE decision_id = $1`, decisionID).Scan(&existingStatus, &existingReason)
	if err == nil {
		if existingStatus != status || existingReason != failureReason {
			return ErrConflict
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO outcomes (decision_id, tenant_id, status, failure_reason, reported_at)
		VALUES ($1, $2, $3, $4, now())`,
		decisionID, tenantID, status, failureReason,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrConflict
		}
		return err
	}
	return tx.Commit(ctx)
}

const decisionSelect = `
	SELECT d.id::text, d.tenant_id, d.idempotency_key, d.payer_id, d.amount, d.currency,
	       d.beneficiary_id, d.channel, d.action, d.score, d.degraded, d.created_at,
	       COALESCE(o.status, ''), COALESCE(o.failure_reason, '')
	FROM decisions d
	LEFT JOIN outcomes o ON o.decision_id = d.id`

func scanDecision(row pgx.Row) (Decision, error) {
	var d Decision
	err := row.Scan(
		&d.ID, &d.TenantID, &d.IdempotencyKey, &d.Intent.PayerID, &d.Intent.Amount, &d.Intent.Currency,
		&d.Intent.BeneficiaryID, &d.Intent.Channel, &d.Action, &d.Score, &d.Degraded, &d.CreatedAt,
		&d.Outcome, &d.FailureReason,
	)
	return d, err
}
