package store

import (
	"context"
	"encoding/json"
	"time"
)

// Checkpoint represents a single completed reasoning step for an agent pod.
type Checkpoint struct {
	ID        int64           `json:"id,omitempty"`
	PodName   string          `json:"pod_name"`
	Namespace string          `json:"namespace"`
	StepIndex int             `json:"step_index"`
	StepName  string          `json:"step_name"`
	TraceID   string          `json:"trace_id"`
	SpanID    string          `json:"span_id"`
	Context   json.RawMessage `json:"context"`
	CreatedAt time.Time       `json:"created_at,omitempty"`
}

// Store defines the interface for checkpoint persistence.
type Store interface {
	// WriteCheckpoint persists a completed reasoning step.
	WriteCheckpoint(ctx context.Context, cp Checkpoint) error

	// LastCheckpoint returns the most recent checkpoint for a pod.
	// Returns nil, nil if no checkpoint exists.
	LastCheckpoint(ctx context.Context, namespace, podName string) (*Checkpoint, error)

	// Cleanup removes old checkpoints beyond the retention limit.
	Cleanup(ctx context.Context, namespace, podName string, retentionLimit int) error
}
