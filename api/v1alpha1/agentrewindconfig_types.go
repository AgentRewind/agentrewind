/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SecretKeyRef references a key in a Secret.
type SecretKeyRef struct {
	// name is the name of the Secret.
	// +required
	Name string `json:"name"`

	// key is the key within the Secret.
	// +required
	Key string `json:"key"`
}

// CheckpointStoreConfig defines how to connect to the checkpoint store.
type CheckpointStoreConfig struct {
	// secretRef references a Secret containing the Postgres DSN.
	// +required
	SecretRef SecretKeyRef `json:"secretRef"`
}

// AgentRewindConfigSpec defines the desired state of AgentRewindConfig.
type AgentRewindConfigSpec struct {
	// podSelector selects which pods are monitored for crash recovery.
	// +required
	PodSelector metav1.LabelSelector `json:"podSelector"`

	// checkpointStore configures the Postgres connection for span storage.
	// +required
	CheckpointStore CheckpointStoreConfig `json:"checkpointStore"`

	// retentionLimit is the maximum number of checkpoints to retain per pod.
	// +kubebuilder:default=100
	// +kubebuilder:validation:Minimum=1
	// +optional
	RetentionLimit int `json:"retentionLimit,omitempty"`

	// recoveryAnnotation is the annotation key used to inject recovery context into pods.
	// +kubebuilder:default="agentrewind.io/last-checkpoint"
	// +optional
	RecoveryAnnotation string `json:"recoveryAnnotation,omitempty"`
}

// AgentRewindConfigStatus defines the observed state of AgentRewindConfig.
type AgentRewindConfigStatus struct {
	// conditions represent the current state of the AgentRewindConfig resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// AgentRewindConfig is the Schema for the agentrewindconfigs API.
type AgentRewindConfig struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec AgentRewindConfigSpec `json:"spec"`

	// +optional
	Status AgentRewindConfigStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// AgentRewindConfigList contains a list of AgentRewindConfig.
type AgentRewindConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []AgentRewindConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AgentRewindConfig{}, &AgentRewindConfigList{})
}
