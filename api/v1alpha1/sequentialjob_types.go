/*
Copyright 2025.

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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ExecutionState tracks the state of jobs in the sequence
type ExecutionState struct {
	// Number of actively running jobs
	// +optional
	Active *int32 `json:"active,omitempty"`

	// Number of successfully completed jobs
	// +optional
	Succeeded *int32 `json:"succeeded,omitempty"`

	// Number of failed jobs
	// +optional
	Failed *int32 `json:"failed,omitempty"`
}

// JobProgress tracks the sequential execution progress
type JobProgress struct {
	// Index of the currently running job
	// +optional
	CurrentIndex *int32 `json:"currentIndex,omitempty"`

	// List of indices for completed jobs
	// +optional
	CompletedIndices []int32 `json:"completedIndices,omitempty"`

	// If a job has failed, this records which job index failed
	// +optional
	FailedIndex *int32 `json:"failedIndex,omitempty"`

	// Reason for failure if a job failed
	// +optional
	FailureReason string `json:"failureReason,omitempty"`
}

// SequentialJobSpec defines the desired state of SequentialJob.
type SequentialJobSpec struct {

	// List of jobs to run sequentially
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=5
	// List of containers to run sequentially
	// +kubebuilder:validation:MinItems=1
	Containers []corev1.Container `json:"containers"`

	// Template for the pod to use for each job (shared settings)
	// +optional
	Template corev1.PodTemplateSpec `json:"template,omitempty"`
}

// SequentialJobStatus defines the observed state of SequentialJob.
type SequentialJobStatus struct {
	// Standard Kubernetes-style conditions
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge

	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Current execution state of jobs
	// +optional
	State ExecutionState `json:"state,omitempty"`

	// Progress information about job execution
	// +optional
	Progress JobProgress `json:"progress,omitempty"`

	// Timing information
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// For tracking generation changes
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// SequentialJob is the Schema for the sequentialjobs API.
type SequentialJob struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SequentialJobSpec   `json:"spec,omitempty"`
	Status SequentialJobStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SequentialJobList contains a list of SequentialJob.
type SequentialJobList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SequentialJob `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SequentialJob{}, &SequentialJobList{})
}
