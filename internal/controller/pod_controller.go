package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/AgentRewind/agentrewind/internal/store"
)

const (
	// LabelEnabled is the label that opts pods into AgentRewind monitoring.
	LabelEnabled = "agentrewind.io/enabled"

	// DefaultRecoveryAnnotation is the default annotation key for recovery context.
	DefaultRecoveryAnnotation = "agentrewind.io/last-checkpoint"
)

// PodReconciler reconciles Pod objects to detect crashes and inject recovery context.
type PodReconciler struct {
	client.Client
	Store              store.Store
	Recorder           record.EventRecorder
	RecoveryAnnotation string
	Log                *slog.Logger
}

// Reconcile handles a pod event. It checks for CrashLoopBackOff, queries the
// last checkpoint, and patches the recovery annotation onto the pod.
func (r *PodReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.With("namespace", req.Namespace, "pod", req.Name)

	var pod corev1.Pod
	if err := r.Get(ctx, req.NamespacedName, &pod); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("getting pod: %w", err)
	}

	if !isCrashing(&pod) {
		return ctrl.Result{}, nil
	}

	log.Info("detected crashing pod")

	cp, err := r.Store.LastCheckpoint(ctx, req.Namespace, req.Name)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("querying last checkpoint: %w", err)
	}
	if cp == nil {
		log.Info("no checkpoint found for crashing pod")
		return ctrl.Result{}, nil
	}

	log.Info("found checkpoint", "step_index", cp.StepIndex, "step_name", cp.StepName)

	annotation := r.recoveryAnnotation()

	// Check if annotation already matches to avoid unnecessary patches.
	if existing, ok := pod.Annotations[annotation]; ok {
		var existingCP store.Checkpoint
		if err := json.Unmarshal([]byte(existing), &existingCP); err == nil {
			if existingCP.StepIndex == cp.StepIndex && existingCP.SpanID == cp.SpanID {
				return ctrl.Result{}, nil
			}
		}
	}

	cpJSON, err := json.Marshal(cp)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("marshaling checkpoint: %w", err)
	}

	patch := client.MergeFrom(pod.DeepCopy())
	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}
	pod.Annotations[annotation] = string(cpJSON)

	if err := r.Patch(ctx, &pod, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patching pod annotation: %w", err)
	}

	r.Recorder.Eventf(&pod, corev1.EventTypeNormal, "CheckpointRestored",
		"Injected recovery context from step %d (%s)", cp.StepIndex, cp.StepName)

	log.Info("patched recovery annotation",
		"step_index", cp.StepIndex,
		"step_name", cp.StepName,
	)

	return ctrl.Result{}, nil
}

// SetupWithManager registers the controller with the manager.
func (r *PodReconciler) SetupWithManager(mgr ctrl.Manager) error {
	labelPredicate, err := predicate.LabelSelectorPredicate(metav1.LabelSelector{
		MatchLabels: map[string]string{
			LabelEnabled: "true",
		},
	})
	if err != nil {
		return fmt.Errorf("creating label predicate: %w", err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		WithEventFilter(labelPredicate).
		Complete(r)
}

func (r *PodReconciler) recoveryAnnotation() string {
	if r.RecoveryAnnotation != "" {
		return r.RecoveryAnnotation
	}
	return DefaultRecoveryAnnotation
}

// isCrashing returns true if any container in the pod is in CrashLoopBackOff
// or if the pod phase is Failed.
func isCrashing(pod *corev1.Pod) bool {
	if pod.Status.Phase == corev1.PodFailed {
		return true
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff" {
			return true
		}
	}
	for _, cs := range pod.Status.InitContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff" {
			return true
		}
	}
	return false
}
