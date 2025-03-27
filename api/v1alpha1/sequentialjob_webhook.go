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

// Package v1alpha1 contains API Schema definitions for the batch v1alpha1 API group
// +kubebuilder:object:generate=true
// +groupName=batch.example.com
package v1alpha1

import (
	"fmt"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// Logger for the SequentialJob webhook
var sequentialJobLog = logf.Log.WithName("sequentialjob-webhook")

//+kubebuilder:webhook:path=/mutate-batch-example-com-v1alpha1-sequentialjob,mutating=true,failurePolicy=fail,sideEffects=None,groups=batch.example.com,resources=sequentialjobs,verbs=create;update,versions=v1alpha1,name=msequentialjob.kb.io,admissionReviewVersions=v1

// SetupWebhookWithManager registers the webhook with the manager.
// This allows the webhook to be served by the manager's webhook server.
func (r *SequentialJob) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

// Default implements webhook.Defaulter so a webhook will be registered for the type.
// This webhook sets default values for the SequentialJob resource when it's created or updated.
func (r *SequentialJob) Default() {
	sequentialJobLog.Info("Applying defaults", "name", r.Name)

	// No need for complex defaulting since we have schema validation
	// in the CRD definition itself. If we wanted to add more sophisticated
	// defaulting, we could do it here.

	// Example: Default empty command array to ["sh", "-c"] if not provided
	for i := range r.Spec.Containers {
		job := &r.Spec.Containers[i]
		if len(job.Command) == 0 && len(job.Args) > 0 {
			job.Command = []string{"sh", "-c"}
		}
	}
}

// setDefaultResources sets standard resource requests for a container
// that hasn't specified any resources. This helps prevent resource starvation
// in the cluster and provides predictable performance.
func setDefaultResources(container *corev1.Container) {
	// Initialize the Requests map if it's nil
	if container.Resources.Requests == nil {
		container.Resources.Requests = corev1.ResourceList{}
	}

	// Set default CPU and memory requests if not already set
	if _, exists := container.Resources.Requests[corev1.ResourceCPU]; !exists {
		container.Resources.Requests[corev1.ResourceCPU] = resource.MustParse("100m")
	}

	if _, exists := container.Resources.Requests[corev1.ResourceMemory]; !exists {
		container.Resources.Requests[corev1.ResourceMemory] = resource.MustParse("128Mi")
	}
}

//+kubebuilder:webhook:path=/validate-batch-example-com-v1alpha1-sequentialjob,mutating=false,failurePolicy=fail,sideEffects=None,groups=batch.example.com,resources=sequentialjobs,verbs=create;update,versions=v1alpha1,name=vsequentialjob.kb.io,admissionReviewVersions=v1

func (r *SequentialJob) ValidateCreate() error {
	sequentialJobLog.Info("Validating create operation", "name", r.Name)
	return r.validateSequentialJob()
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type.
// This validates SequentialJob resources when they are updated to ensure they remain valid.
// It also checks whether updates are being made to running jobs and logs a warning if so.
func (r *SequentialJob) ValidateUpdate(old runtime.Object) error {
	sequentialJobLog.Info("Validating update operation", "name", r.Name)

	// Cast the old object to SequentialJob to access its fields
	oldJob, ok := old.(*SequentialJob)
	if !ok {
		return fmt.Errorf("expected a SequentialJob but got %T", old)
	}
	// Special handling for updates to running jobs
	// We allow updates while jobs are running, but log a warning since this could
	// lead to unexpected behavior if not handled carefully
	if isJobRunning(oldJob) && !reflect.DeepEqual(oldJob.Spec, r.Spec) {
		sequentialJobLog.Info("Warning: updating a running SequentialJob",
			"name", r.Name,
			"activeJobs", *oldJob.Status.State.Active)
	}

	return r.validateSequentialJob()
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type.
// This validates SequentialJob resources when they are deleted.
func (r *SequentialJob) ValidateDelete() error {
	sequentialJobLog.Info("Validating delete operation", "name", r.Name)
	// We don't need special validation for deletion
	return nil
}

// isJobRunning checks if a SequentialJob currently has any active jobs running.
// This is used to detect changes to running jobs which might have unexpected consequences.
func isJobRunning(sj *SequentialJob) bool {
	// Check if the Active field exists and has a value greater than 0
	return sj.Status.State.Active != nil && *sj.Status.State.Active > 0
}

// validateSequentialJob performs validation for the SequentialJob resource.
// It checks:
// - At least one job is specified
// - Each job has an image
// - Each environment variable has a name
func (r *SequentialJob) validateSequentialJob() error {
	var allErrs field.ErrorList

	// Create a root field path for structured error reporting
	path := field.NewPath("spec")

	// Validate that there's at least one job to run
	if len(r.Spec.Containers) == 0 {
		allErrs = append(allErrs, field.Required(
			path.Child("jobs"),
			"at least one job must be specified"))
	}

	// Validate each job in the sequence
	for i, job := range r.Spec.Containers {
		jobPath := path.Child("jobs").Index(i)

		// Ensure each job has an image specified
		if job.Image == "" {
			allErrs = append(allErrs, field.Required(
				jobPath.Child("image"),
				"image must be specified"))
		}

		// Validate environment variables
		for j, env := range job.Env {
			envPath := jobPath.Child("env").Index(j)
			if env.Name == "" {
				allErrs = append(allErrs, field.Required(
					envPath.Child("name"),
					"environment variable name must be specified"))
			}
		}
	}

	// If there are no validation errors, return nil to indicate success
	if len(allErrs) == 0 {
		return nil
	}

	// Otherwise, create a structured validation error with all the issues found
	return apierrors.NewInvalid(
		schema.GroupKind{Group: "batch.example.com", Kind: "SequentialJob"},
		r.Name, allErrs)
}
