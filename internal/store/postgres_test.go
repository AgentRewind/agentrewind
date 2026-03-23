package store

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

// These tests require a running Postgres instance.
// Set TEST_POSTGRES_DSN to enable them (e.g., "postgres://user:pass@localhost:5432/testdb").

func testPool(t *testing.T) *PostgresStore {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set, skipping Postgres integration tests")
	}

	ctx := context.Background()

	pool, err := Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connecting to postgres: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	s := NewPostgresStore(pool)
	if err := s.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensuring schema: %v", err)
	}

	// Clean up test data before each test.
	_, _ = pool.Exec(ctx, "DELETE FROM checkpoints WHERE namespace = 'test-ns'")

	return s
}

func TestWriteAndLastCheckpoint(t *testing.T) {
	s := testPool(t)
	ctx := context.Background()

	tests := []struct {
		name      string
		cp        Checkpoint
		wantStep  int
		wantName  string
	}{
		{
			name: "single checkpoint",
			cp: Checkpoint{
				PodName:   "agent-pod-1",
				Namespace: "test-ns",
				StepIndex: 1,
				StepName:  "fetch_pr",
				TraceID:   "trace-001",
				SpanID:    "span-001",
				Context:   json.RawMessage(`{"pr_number": 42}`),
			},
			wantStep: 1,
			wantName: "fetch_pr",
		},
		{
			name: "second checkpoint overwrites latest",
			cp: Checkpoint{
				PodName:   "agent-pod-1",
				Namespace: "test-ns",
				StepIndex: 2,
				StepName:  "analyze_diff",
				TraceID:   "trace-001",
				SpanID:    "span-002",
				Context:   json.RawMessage(`{"diff_lines": 150}`),
			},
			wantStep: 2,
			wantName: "analyze_diff",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := s.WriteCheckpoint(ctx, tt.cp); err != nil {
				t.Fatalf("WriteCheckpoint: %v", err)
			}
			got, err := s.LastCheckpoint(ctx, tt.cp.Namespace, tt.cp.PodName)
			if err != nil {
				t.Fatalf("LastCheckpoint: %v", err)
			}
			if got == nil {
				t.Fatal("LastCheckpoint returned nil")
			}
			if got.StepIndex != tt.wantStep {
				t.Errorf("StepIndex = %d, want %d", got.StepIndex, tt.wantStep)
			}
			if got.StepName != tt.wantName {
				t.Errorf("StepName = %q, want %q", got.StepName, tt.wantName)
			}
		})
	}
}

func TestLastCheckpoint_NotFound(t *testing.T) {
	s := testPool(t)
	ctx := context.Background()

	got, err := s.LastCheckpoint(ctx, "test-ns", "nonexistent-pod")
	if err != nil {
		t.Fatalf("LastCheckpoint: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for nonexistent pod, got %+v", got)
	}
}

func TestCleanup(t *testing.T) {
	s := testPool(t)
	ctx := context.Background()

	// Write 5 checkpoints.
	for i := 0; i < 5; i++ {
		cp := Checkpoint{
			PodName:   "cleanup-pod",
			Namespace: "test-ns",
			StepIndex: i,
			StepName:  "step",
			TraceID:   "trace-cleanup",
			SpanID:    "span-cleanup",
			Context:   json.RawMessage("{}"),
		}
		if err := s.WriteCheckpoint(ctx, cp); err != nil {
			t.Fatalf("WriteCheckpoint: %v", err)
		}
	}

	// Retain only 2.
	if err := s.Cleanup(ctx, "test-ns", "cleanup-pod", 2); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	// Count remaining.
	var count int
	err := s.pool.QueryRow(ctx,
		"SELECT count(*) FROM checkpoints WHERE namespace = $1 AND pod_name = $2",
		"test-ns", "cleanup-pod",
	).Scan(&count)
	if err != nil {
		t.Fatalf("counting: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 checkpoints after cleanup, got %d", count)
	}
}
