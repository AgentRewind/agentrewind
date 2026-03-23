package controller

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/AgentRewind/agentrewind/internal/store"
)

// fakeStore implements store.Store for testing.
type fakeStore struct {
	checkpoints map[string]*store.Checkpoint
	written     []store.Checkpoint
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		checkpoints: make(map[string]*store.Checkpoint),
	}
}

func (f *fakeStore) WriteCheckpoint(_ context.Context, cp store.Checkpoint) error {
	f.written = append(f.written, cp)
	key := cp.Namespace + "/" + cp.PodName
	f.checkpoints[key] = &cp
	return nil
}

func (f *fakeStore) LastCheckpoint(_ context.Context, namespace, podName string) (*store.Checkpoint, error) {
	key := namespace + "/" + podName
	return f.checkpoints[key], nil
}

func (f *fakeStore) Cleanup(_ context.Context, _, _ string, _ int) error {
	return nil
}

func TestIsCrashing(t *testing.T) {
	tests := []struct {
		name string
		pod  corev1.Pod
		want bool
	}{
		{
			name: "running pod",
			pod: corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
				},
			},
			want: false,
		},
		{
			name: "failed pod",
			pod: corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodFailed,
				},
			},
			want: true,
		},
		{
			name: "CrashLoopBackOff container",
			pod: corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{
							State: corev1.ContainerState{
								Waiting: &corev1.ContainerStateWaiting{
									Reason: "CrashLoopBackOff",
								},
							},
						},
					},
				},
			},
			want: true,
		},
		{
			name: "CrashLoopBackOff init container",
			pod: corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodPending,
					InitContainerStatuses: []corev1.ContainerStatus{
						{
							State: corev1.ContainerState{
								Waiting: &corev1.ContainerStateWaiting{
									Reason: "CrashLoopBackOff",
								},
							},
						},
					},
				},
			},
			want: true,
		},
		{
			name: "waiting but not CrashLoopBackOff",
			pod: corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodPending,
					ContainerStatuses: []corev1.ContainerStatus{
						{
							State: corev1.ContainerState{
								Waiting: &corev1.ContainerStateWaiting{
									Reason: "ImagePullBackOff",
								},
							},
						},
					},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isCrashing(&tt.pod)
			if got != tt.want {
				t.Errorf("isCrashing() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRecoveryAnnotation(t *testing.T) {
	tests := []struct {
		name     string
		override string
		want     string
	}{
		{
			name: "default annotation",
			want: DefaultRecoveryAnnotation,
		},
		{
			name:     "custom annotation",
			override: "custom.io/checkpoint",
			want:     "custom.io/checkpoint",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &PodReconciler{
				RecoveryAnnotation: tt.override,
				Log:                slog.Default(),
			}
			got := r.recoveryAnnotation()
			if got != tt.want {
				t.Errorf("recoveryAnnotation() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCheckpointJSON(t *testing.T) {
	cp := store.Checkpoint{
		PodName:   "agent-1",
		Namespace: "default",
		StepIndex: 2,
		StepName:  "analyze_diff",
		TraceID:   "trace-1",
		SpanID:    "span-2",
		Context:   json.RawMessage(`{"key":"value"}`),
	}

	data, err := json.Marshal(cp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded store.Checkpoint
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.StepIndex != cp.StepIndex {
		t.Errorf("StepIndex = %d, want %d", decoded.StepIndex, cp.StepIndex)
	}
	if decoded.StepName != cp.StepName {
		t.Errorf("StepName = %q, want %q", decoded.StepName, cp.StepName)
	}
}

func TestPodWithLabel(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				LabelEnabled: "true",
			},
		},
	}

	if pod.Labels[LabelEnabled] != "true" {
		t.Errorf("expected label %s=true", LabelEnabled)
	}
}
