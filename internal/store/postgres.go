package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore implements Store using PostgreSQL via pgx/v5.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore creates a new PostgresStore from a connection pool.
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

// EnsureSchema creates the checkpoints table and index if they don't exist.
func (s *PostgresStore) EnsureSchema(ctx context.Context) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS checkpoints (
    id          BIGSERIAL PRIMARY KEY,
    pod_name    TEXT NOT NULL,
    namespace   TEXT NOT NULL,
    step_index  INTEGER NOT NULL,
    step_name   TEXT NOT NULL,
    trace_id    TEXT NOT NULL,
    span_id     TEXT NOT NULL,
    context     JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_checkpoints_pod
    ON checkpoints (namespace, pod_name, created_at DESC);
`
	_, err := s.pool.Exec(ctx, ddl)
	if err != nil {
		return fmt.Errorf("ensuring schema: %w", err)
	}
	return nil
}

// WriteCheckpoint persists a completed reasoning step.
func (s *PostgresStore) WriteCheckpoint(ctx context.Context, cp Checkpoint) error {
	ctxJSON := cp.Context
	if ctxJSON == nil {
		ctxJSON = json.RawMessage("{}")
	}

	const query = `
INSERT INTO checkpoints (pod_name, namespace, step_index, step_name, trace_id, span_id, context)
VALUES ($1, $2, $3, $4, $5, $6, $7)
`
	_, err := s.pool.Exec(ctx, query,
		cp.PodName, cp.Namespace, cp.StepIndex, cp.StepName,
		cp.TraceID, cp.SpanID, ctxJSON,
	)
	if err != nil {
		return fmt.Errorf("writing checkpoint: %w", err)
	}
	return nil
}

// LastCheckpoint returns the most recent checkpoint for a pod.
func (s *PostgresStore) LastCheckpoint(ctx context.Context, namespace, podName string) (*Checkpoint, error) {
	const query = `
SELECT id, pod_name, namespace, step_index, step_name, trace_id, span_id, context, created_at
FROM checkpoints
WHERE namespace = $1 AND pod_name = $2
ORDER BY created_at DESC
LIMIT 1
`
	var cp Checkpoint
	err := s.pool.QueryRow(ctx, query, namespace, podName).Scan(
		&cp.ID, &cp.PodName, &cp.Namespace,
		&cp.StepIndex, &cp.StepName,
		&cp.TraceID, &cp.SpanID,
		&cp.Context, &cp.CreatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying last checkpoint: %w", err)
	}
	return &cp, nil
}

// Cleanup removes old checkpoints beyond the retention limit for a pod.
func (s *PostgresStore) Cleanup(ctx context.Context, namespace, podName string, retentionLimit int) error {
	const query = `
DELETE FROM checkpoints
WHERE id IN (
    SELECT id FROM checkpoints
    WHERE namespace = $1 AND pod_name = $2
    ORDER BY created_at DESC
    OFFSET $3
)
`
	_, err := s.pool.Exec(ctx, query, namespace, podName, retentionLimit)
	if err != nil {
		return fmt.Errorf("cleaning up checkpoints: %w", err)
	}
	return nil
}
