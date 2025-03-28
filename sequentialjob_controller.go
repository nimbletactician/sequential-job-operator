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

package controller

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/example/sequential-job-operator/api/v1alpha1"
	batchv1alpha1 "github.com/example/sequential-job-operator/api/v1alpha1"
	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Define condition types for SequentialJob status
const (
	ConditionTypeJobsRunning   = "JobsRunning"
	ConditionTypeJobsCompleted = "JobsCompleted"
	ConditionTypeJobsFailed    = "JobsFailed"

	// Define finalizer name
	sequentialJobFinalizer = "sequentialjob.myapp.example.com/finalizer"
)

// SequentialJobReconciler reconciles a SequentialJob object
// It manages the lifecycle of sequential batch jobs defined in the SequentialJob resource
type SequentialJobReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// initializeStatus sets up the initial status state for a new SequentialJob resource.
// This is critical for establishing the initial execution state before any jobs start running.
//
// The method:
// 1. Checks if initialization is needed by comparing ObservedGeneration
// 2. Sets timestamps, counters, and conditions to reflect the starting state
// 3. Initializes execution tracking structures (ExecutionState and JobProgress)
// 4. Updates the resource status through the Kubernetes API
//
// This initialization happens only once in the resource's lifecycle, when it's first created.
// All subsequent status changes happen during job processing.
func (r *SequentialJobReconciler) initializeStatus(ctx context.Context, sequentialJob *v1alpha1.SequentialJob, logger logr.Logger) error {
	// Check if status needs initialization (by checking if ObservedGeneration is zero)
	// ObservedGeneration=0 indicates this is the first time we're processing this resource
	if sequentialJob.Status.ObservedGeneration == 0 {
		logger.Info("Initializing status for new SequentialJob", "name", sequentialJob.Name)

		// Record the start time of the sequence execution
		now := metav1.Now()
		sequentialJob.Status.StartTime = &now

		// Initialize all counters with zeros - these will be incremented during execution
		// - active: currently running jobs (will be 0 or 1 for this controller)
		// - succeeded: count of successfully completed jobs
		// - failed: count of jobs that failed
		active := int32(0)
		succeeded := int32(0)
		failed := int32(0)
		currentIndex := int32(0) // Start with the first container (index 0)

		// Set initial execution state with zero counters
		// This structure tracks the high-level execution counts
		sequentialJob.Status.State = v1alpha1.ExecutionState{
			Active:    &active,
			Succeeded: &succeeded,
			Failed:    &failed,
		}

		// Set initial progress information with starting index
		// This structure tracks the detailed progress through the sequence
		sequentialJob.Status.Progress = v1alpha1.JobProgress{
			CurrentIndex:     &currentIndex, // Points to the container we're currently processing
			CompletedIndices: []int32{},     // Empty list since no jobs have completed yet
			// FailedIndex and FailureReason remain nil/empty since no failures yet
		}

		// Initialize standard Kubernetes conditions to reflect the resource is being set up
		// Using standard condition patterns helps with consistent observability
		sequentialJob.Status.Conditions = []metav1.Condition{
			{
				Type:               ConditionTypeJobsRunning,
				Status:             metav1.ConditionFalse, // Not running yet
				LastTransitionTime: now,
				Reason:             "Initializing",
				Message:            "Sequential jobs are being initialized",
			},
		}

		// Set observed generation to track updates
		// This helps detect when the spec is modified during execution
		sequentialJob.Status.ObservedGeneration = sequentialJob.Generation

		logger.Info("Updating status for initialized SequentialJob")
		// Persist the initial status through the status subresource
		return r.Status().Update(ctx, sequentialJob)
	}

	// Status already initialized, nothing to do
	return nil
}

// processSequentialJobs is the main state machine that orchestrates the sequential execution of jobs.
// This function makes decisions about what actions to take based on the current state of the SequentialJob.
//
// The function implements the following decision tree:
// 1. Check if the resource was modified since last observation
// 2. Check if all jobs are already complete or failed
// 3. Check if we've processed all containers in the sequence
// 4. Check if there's an active job running
// 5. Start the next job if needed
//
// Each path through this decision tree may result in:
// - Updating status and returning (completion case)
// - Delegating to another function for specific processing
// - Requeuing for future processing
func (r *SequentialJobReconciler) processSequentialJobs(ctx context.Context, sequentialJob *v1alpha1.SequentialJob, logger logr.Logger) (ctrl.Result, error) {
	// Check if resource has been modified since we last observed it
	// This is detected by comparing the resource Generation (incremented on spec changes)
	// with our ObservedGeneration (the last generation we processed)
	if sequentialJob.Generation != sequentialJob.Status.ObservedGeneration {
		logger.Info("SequentialJob was modified during execution",
			"current generation", sequentialJob.Generation,
			"observed generation", sequentialJob.Status.ObservedGeneration)

		// Handle the change based on current execution state
		// This may terminate running jobs and reset state as needed
		return r.handleResourceChange(ctx, sequentialJob, logger)
	}

	// Check if jobs are completed or failed
	// If either completion time is set or we have a failed index, the sequence is done
	if sequentialJob.Status.CompletionTime != nil || sequentialJob.Status.Progress.FailedIndex != nil {
		// Jobs are already completed or failed, nothing more to do
		// This is a terminal state - the resource will remain in this state until deleted or modified
		logger.Info("SequentialJob already completed or failed, no action needed",
			"completion time", sequentialJob.Status.CompletionTime,
			"failed index", sequentialJob.Status.Progress.FailedIndex)
		return ctrl.Result{}, nil
	}

	// Get the current index from the status
	// This tracks which container in the sequence we're currently processing
	currentIndex := *sequentialJob.Status.Progress.CurrentIndex

	// Check if we've processed all containers in the spec
	// This happens when currentIndex equals or exceeds the container count
	if int(currentIndex) >= len(sequentialJob.Spec.Containers) {
		// All jobs completed successfully, update the status to reflect completion
		// This is the successful terminal state
		logger.Info("All jobs in sequence completed successfully",
			"total containers", len(sequentialJob.Spec.Containers))

		// Record completion time
		now := metav1.Now()
		sequentialJob.Status.CompletionTime = &now

		// Update completion condition to indicate success
		// Using the standardized Kubernetes condition pattern
		meta.SetStatusCondition(&sequentialJob.Status.Conditions, metav1.Condition{
			Type:               ConditionTypeJobsCompleted,
			Status:             metav1.ConditionTrue,
			LastTransitionTime: now,
			Reason:             "AllJobsCompleted",
			Message:            "All sequential jobs completed successfully",
		})

		// Update running condition to indicate no jobs are running
		meta.SetStatusCondition(&sequentialJob.Status.Conditions, metav1.Condition{
			Type:               ConditionTypeJobsRunning,
			Status:             metav1.ConditionFalse,
			LastTransitionTime: now,
			Reason:             "NoJobsRunning",
			Message:            "No jobs are currently running",
		})

		logger.Info("Updating status for completed SequentialJob")
		return ctrl.Result{}, r.Status().Update(ctx, sequentialJob)
	}

	// Check if there's an active job running
	// We only allow one job at a time to maintain the sequential execution model
	if *sequentialJob.Status.State.Active > 0 {
		// There's an active job, check its status
		// This delegates to the checkActiveJob function to monitor the running job
		logger.Info("Active job detected, checking status", "current index", currentIndex)
		return r.checkActiveJob(ctx, sequentialJob, logger)
	}

	// No active job and not completed - start the next job in sequence
	// This happens when:
	// - We're just starting the sequence
	// - We've completed a job and need to start the next one
	// - A previously active job has disappeared and needs to be restarted
	logger.Info("No active job, starting next job", "index", currentIndex)
	return r.startNextJob(ctx, sequentialJob, logger)
}

// handleResourceChange gracefully manages modifications to the SequentialJob resource during execution.
// When a user modifies a SequentialJob spec while jobs are running, this function ensures:
// 1. Running jobs are properly terminated when necessary
// 2. The execution state is adjusted to a consistent state
// 3. The controller can continue execution with the updated configuration
//
// This function handles several change scenarios:
// - Container list shortened (current index now out of bounds)
// - Container list expanded (new containers to run)
// - Current container definition changed (needs restart)
// - Other spec changes (template, etc.)
//
// The approach prioritizes safety and consistency over preserving in-progress work.
func (r *SequentialJobReconciler) handleResourceChange(ctx context.Context, sequentialJob *v1alpha1.SequentialJob, logger logr.Logger) (ctrl.Result, error) {
	// Get the current execution state from status
	currentIndex := *sequentialJob.Status.Progress.CurrentIndex
	isActive := *sequentialJob.Status.State.Active > 0

	// Add a status condition to reflect that the resource was modified
	// This helps users track when and why changes were processed
	now := metav1.Now()
	meta.SetStatusCondition(&sequentialJob.Status.Conditions, metav1.Condition{
		Type:               "ResourceModified",
		Status:             metav1.ConditionTrue,
		LastTransitionTime: now,
		Reason:             "ConfigurationChanged",
		Message:            fmt.Sprintf("SequentialJob was modified at generation %d", sequentialJob.Generation),
	})

	// Check if there are significant changes to the containers
	// First, check if the current index is still valid after changes
	oldContainerCount := len(sequentialJob.Spec.Containers)
	if int(currentIndex) >= oldContainerCount {
		// Current index is beyond the number of containers
		// This happens if containers were removed from the spec while executing
		logger.Info("Current index is beyond container count after modification",
			"currentIndex", currentIndex,
			"containerCount", oldContainerCount)

		// If job was active, we need to find and terminate it since it's now invalid
		if isActive {
			if err := r.terminateActiveJob(ctx, sequentialJob, logger); err != nil {
				return ctrl.Result{}, err
			}
		}

		// Reset to a valid state based on the new container list
		if oldContainerCount > 0 {
			// If we still have containers, set to the last valid index
			// This ensures we won't try to access an invalid container
			newIndex := int32(oldContainerCount - 1)
			sequentialJob.Status.Progress.CurrentIndex = &newIndex
		} else {
			// No containers left in the spec, mark as complete
			// This is a special case where the job is considered complete
			// because there's nothing left to run
			sequentialJob.Status.CompletionTime = &now
			meta.SetStatusCondition(&sequentialJob.Status.Conditions, metav1.Condition{
				Type:               ConditionTypeJobsCompleted,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: now,
				Reason:             "NoContainersToRun",
				Message:            "No containers to run after modification",
			})
		}
	} else if isActive {
		// We have an active job and the index is still valid
		// Now check if the current container definition changed
		logger.Info("Checking if current container changed while job is active",
			"currentIndex", currentIndex)

		// For simplicity, we terminate the active job if any part of the spec changed
		// A more sophisticated approach could compare the specific container definition
		// to determine if a restart is actually necessary
		if err := r.terminateActiveJob(ctx, sequentialJob, logger); err != nil {
			return ctrl.Result{}, err
		}

		// Mark job as not active so we can start it fresh with new definition
		active := int32(0)
		sequentialJob.Status.State.Active = &active
	}

	// Update observed generation to match current
	// This prevents re-processing the same change on next reconcile
	sequentialJob.Status.ObservedGeneration = sequentialJob.Generation

	// Persist the updated status with our changes
	if err := r.Status().Update(ctx, sequentialJob); err != nil {
		logger.Error(err, "Failed to update status after resource change")
		return ctrl.Result{}, err
	}

	// Requeue to continue execution with updated configuration
	// This ensures we'll immediately process with the new configuration
	return ctrl.Result{Requeue: true}, nil
}

// terminateActiveJob finds and terminates the currently running job pod.
// This function is used when:
// 1. The SequentialJob spec is modified during execution
// 2. Jobs need to be manually stopped due to configuration changes
// 3. Cleanup is needed for preparation of next execution
//
// The function uses labels to identify pods associated with the current job index,
// and sends delete requests with background propagation to ensure complete cleanup.
// It handles not-found errors gracefully to account for racing conditions.
func (r *SequentialJobReconciler) terminateActiveJob(ctx context.Context, sequentialJob *v1alpha1.SequentialJob, logger logr.Logger) error {
	currentIndex := *sequentialJob.Status.Progress.CurrentIndex

	// Look for a pod with our controller reference and current index
	// We use label selectors to find the exact pod for the current job
	podList := &corev1.PodList{}

	// Define labels that uniquely identify the pod for the current job index
	labelSelector := map[string]string{
		"app.kubernetes.io/part-of":             sequentialJob.Name,
		"sequentialjob.myapp.example.com/index": fmt.Sprintf("%d", currentIndex),
	}

	// List pods matching our selector in the resource's namespace
	if err := r.List(ctx, podList,
		client.InNamespace(sequentialJob.Namespace),
		client.MatchingLabels(labelSelector)); err != nil {
		logger.Error(err, "Failed to list pods for termination")
		return err
	}

	// No pods found matching our criteria
	if len(podList.Items) == 0 {
		logger.Info("No active pods found to terminate")
		return nil
	}

	// Delete each pod found
	// Note: There should generally be only one pod per index,
	// but we handle multiple for robustness
	for _, pod := range podList.Items {
		logger.Info("Terminating active pod due to configuration change", "pod", pod.Name)

		// Delete the pod with background propagation
		// This ensures all associated resources are cleaned up properly
		if err := r.Delete(ctx, &pod, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil {
			// Ignore not-found errors, as pod might have been deleted already
			// This handles race conditions where pod is deleted between listing and deletion
			if !errors.IsNotFound(err) {
				logger.Error(err, "Failed to terminate pod", "pod", pod.Name)
				return err
			}
		}
	}

	// Successfully terminated all pods (or they were already gone)
	return nil
}

// checkActiveJob monitors the status of a running pod for the current job index.
// This function:
// 1. Finds the pod for the current job index using label selectors
// 2. Checks if the pod has completed successfully or failed
// 3. Updates status appropriately based on pod phase
// 4. Triggers the next job in sequence or terminates on failure
//
// This is the core function that implements the sequential progression logic,
// moving to the next job only after the current one succeeds.
func (r *SequentialJobReconciler) checkActiveJob(ctx context.Context, sequentialJob *v1alpha1.SequentialJob, logger logr.Logger) (ctrl.Result, error) {
	// Get the current job index from status
	currentIndex := *sequentialJob.Status.Progress.CurrentIndex
	podList := &corev1.PodList{}

	// Define labels to find the pod for current job
	// We use the same labeling scheme consistently across the controller
	labelSelector := map[string]string{
		"app.kubernetes.io/part-of":             sequentialJob.Name,
		"sequentialjob.myapp.example.com/index": fmt.Sprintf("%d", currentIndex),
	}

	// List pods with our labels in the resource's namespace
	logger.Info("Listing pods for active job", "labels", labelSelector)
	if err := r.List(ctx, podList,
		client.InNamespace(sequentialJob.Namespace),
		client.MatchingLabels(labelSelector)); err != nil {
		logger.Error(err, "Failed to list pods")
		return ctrl.Result{}, err
	}

	// No pods found, might have been deleted unexpectedly
	// This could happen if:
	// - The pod was deleted manually
	// - The pod was evicted by the cluster
	// - A node failure occurred
	if len(podList.Items) == 0 {
		logger.Info("No pods found for active job, resetting active count")
		// Reset active count so we can start a new job
		// This allows recovery from unexpected pod deletion
		active := int32(0)
		sequentialJob.Status.State.Active = &active

		// Update status and requeue to start the next job
		// The requeue will cause processSequentialJobs to restart this job
		if err := r.Status().Update(ctx, sequentialJob); err != nil {
			logger.Error(err, "Failed to update status after finding no active pods")
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Get the first pod (should be only one for current index)
	// If multiple pods exist (should not happen), we just use the first one
	pod := podList.Items[0]
	logger.Info("Found pod for active job", "pod", pod.Name, "phase", pod.Status.Phase)

	// Process based on pod phase - the key part of sequential execution logic
	// Check if the pod has completed (successfully or with failure)
	if pod.Status.Phase == corev1.PodSucceeded {
		// Pod completed successfully - we can proceed to the next job
		logger.Info("Pod completed successfully", "pod", pod.Name)

		// Update state counters
		// - Set active to 0 (no active job)
		// - Increment succeeded count
		active := int32(0)
		sequentialJob.Status.State.Active = &active

		succeeded := *sequentialJob.Status.State.Succeeded + 1
		sequentialJob.Status.State.Succeeded = &succeeded

		// Update progress tracking
		// - Add current index to completed indices list
		// - Increment current index to point to next job
		sequentialJob.Status.Progress.CompletedIndices = append(
			sequentialJob.Status.Progress.CompletedIndices, currentIndex)

		nextIndex := currentIndex + 1
		sequentialJob.Status.Progress.CurrentIndex = &nextIndex

		// Update status and requeue to start next job
		// The requeue will cause processSequentialJobs to start the next container
		logger.Info("Updating status and moving to next job", "next index", nextIndex)
		if err := r.Status().Update(ctx, sequentialJob); err != nil {
			logger.Error(err, "Failed to update status after pod completion")
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil

	} else if pod.Status.Phase == corev1.PodFailed {
		// Pod failed - this terminates the sequence execution
		logger.Info("Pod failed", "pod", pod.Name)

		// Update state counters
		// - Set active to 0 (no active job)
		// - Increment failed count
		active := int32(0)
		sequentialJob.Status.State.Active = &active

		failed := *sequentialJob.Status.State.Failed + 1
		sequentialJob.Status.State.Failed = &failed

		// Record failed index for user observability
		// This permanently marks which job in the sequence failed
		sequentialJob.Status.Progress.FailedIndex = &currentIndex

		// Extract detailed failure information from container status
		// This provides valuable debug information to users
		failureReason := "Unknown failure"
		for _, containerStatus := range pod.Status.ContainerStatuses {
			if containerStatus.State.Terminated != nil && containerStatus.State.Terminated.ExitCode != 0 {
				// Extract termination message or reason from container status
				failureReason = containerStatus.State.Terminated.Message
				if failureReason == "" {
					failureReason = containerStatus.State.Terminated.Reason
				}
				break
			}
		}
		sequentialJob.Status.Progress.FailureReason = failureReason

		// Update standard Kubernetes conditions to reflect failure
		now := metav1.Now()

		// Set JobsFailed condition to true with detailed message
		meta.SetStatusCondition(&sequentialJob.Status.Conditions, metav1.Condition{
			Type:               ConditionTypeJobsFailed,
			Status:             metav1.ConditionTrue,
			LastTransitionTime: now,
			Reason:             "JobFailed",
			Message:            fmt.Sprintf("Job at index %d failed: %s", currentIndex, failureReason),
		})

		// Update running condition to false since execution has stopped
		meta.SetStatusCondition(&sequentialJob.Status.Conditions, metav1.Condition{
			Type:               ConditionTypeJobsRunning,
			Status:             metav1.ConditionFalse,
			LastTransitionTime: now,
			Reason:             "NoJobsRunning",
			Message:            "No jobs are currently running due to failure",
		})

		// Update status - this is a terminal state, so no requeue
		logger.Info("Updating status for failed job", "failed index", currentIndex, "reason", failureReason)
		return ctrl.Result{}, r.Status().Update(ctx, sequentialJob)
	}

	// Pod still running, requeue after a short delay to check again
	// We use a polling approach with a reasonable interval to balance
	// responsiveness with API load
	logger.Info("Pod still running, requeuing", "pod", pod.Name, "phase", pod.Status.Phase)
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// startNextJob creates a pod for the next container in the sequence.
// This function:
// 1. Validates that the current index is valid
// 2. Builds a pod definition using the container spec and template
// 3. Sets owner references for proper garbage collection
// 4. Creates the pod in the Kubernetes cluster
// 5. Updates status to reflect the running job
//
// This is called whenever we need to start a new container in the sequence, either
// at the beginning or after a previous container completes successfully.
func (r *SequentialJobReconciler) startNextJob(ctx context.Context, sequentialJob *v1alpha1.SequentialJob, logger logr.Logger) (ctrl.Result, error) {
	// Get the current job index from status
	currentIndex := *sequentialJob.Status.Progress.CurrentIndex

	// Sanity check - verify the current index is valid
	// This is a defensive check that should never fail if the controller logic is correct
	if int(currentIndex) >= len(sequentialJob.Spec.Containers) {
		err := fmt.Errorf("current index %d exceeds container count %d",
			currentIndex, len(sequentialJob.Spec.Containers))
		logger.Error(err, "Invalid state in SequentialJob")
		return ctrl.Result{}, err
	}

	// Create a pod for the current container
	// This delegates to buildPodForIndex to handle the pod definition creation
	logger.Info("Building pod for container", "index", currentIndex)
	pod, err := r.buildPodForIndex(sequentialJob, currentIndex)
	if err != nil {
		logger.Error(err, "Failed to build pod")
		return ctrl.Result{}, err
	}

	// Set controller reference to establish ownership relationship
	// This is critical for:
	// 1. Automatic garbage collection when parent is deleted
	// 2. Finding pods owned by this SequentialJob
	// 3. Proper lifecycle management
	logger.Info("Setting controller reference")
	if err := controllerutil.SetControllerReference(sequentialJob, pod, r.Scheme); err != nil {
		logger.Error(err, "Failed to set controller reference")
		return ctrl.Result{}, err
	}

	// Create the pod in the Kubernetes cluster
	logger.Info("Creating pod", "pod", pod.Name, "index", currentIndex)
	if err := r.Create(ctx, pod); err != nil {
		logger.Error(err, "Failed to create pod")
		return ctrl.Result{}, err
	}

	// Update active count to indicate we have a running job
	// This flag is critical for the state machine logic
	active := int32(1)
	sequentialJob.Status.State.Active = &active

	// Update running condition to reflect job has started
	// Using standard Kubernetes condition pattern for observability
	now := metav1.Now()
	meta.SetStatusCondition(&sequentialJob.Status.Conditions, metav1.Condition{
		Type:               ConditionTypeJobsRunning,
		Status:             metav1.ConditionTrue,
		LastTransitionTime: now,
		Reason:             "JobStarted",
		Message:            fmt.Sprintf("Job at index %d started", currentIndex),
	})

	// Update status to persist our changes
	// No need to requeue as pod events will trigger reconciliation
	logger.Info("Updating status for started job", "index", currentIndex)
	return ctrl.Result{}, r.Status().Update(ctx, sequentialJob)
}

// buildPodForIndex constructs a Pod definition for a specific container in the sequence.
// This function:
// 1. Gets the container spec for the given index
// 2. Creates a properly named and labeled Pod
// 3. Configures the Pod with the right settings from the template
// 4. Applies shared settings like volumes, node selectors, etc.
//
// This separates the pod construction logic from the workflow management,
// making the code more modular and easier to maintain.
func (r *SequentialJobReconciler) buildPodForIndex(sequentialJob *v1alpha1.SequentialJob, index int32) (*corev1.Pod, error) {
	// Get the container for this index from the spec
	container := sequentialJob.Spec.Containers[index]

	// Generate a unique, deterministic name for the pod
	// Format: {resource-name}-{index}
	podName := fmt.Sprintf("%s-%d", sequentialJob.Name, index)

	// Create standard labels for the pod
	// These labels serve multiple purposes:
	// 1. Relationship tracking: link to parent SequentialJob
	// 2. Selection: find pods for specific indices
	// 3. Grouping: identify all pods from this controller type
	labels := map[string]string{
		// Standard Kubernetes recommended labels
		"app.kubernetes.io/name":     "sequentialjob",
		"app.kubernetes.io/instance": sequentialJob.Name,
		"app.kubernetes.io/part-of":  sequentialJob.Name,

		// Custom label for tracking job index
		// This is critical for finding pods for a specific job index
		"sequentialjob.myapp.example.com/index": fmt.Sprintf("%d", index),
	}

	// Merge with user-provided template labels if they exist
	// This allows users to add their own labels for selection, visualization, etc.
	if sequentialJob.Spec.Template.Labels != nil {
		for k, v := range sequentialJob.Spec.Template.Labels {
			labels[k] = v
		}
	}

	// Create the pod with basic configuration
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: sequentialJob.Namespace,
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
			// Never restart the pod on failure since we're handling sequential execution
			// The controller will detect failure and mark the sequence as failed
			RestartPolicy: corev1.RestartPolicyNever,

			// Use the container from the spec at the current index
			Containers: []corev1.Container{
				container,
			},
		},
	}

	// Apply template spec settings if available
	// This allows users to customize pod settings through the template field

	// Add node selection constraints if specified
	if sequentialJob.Spec.Template.Spec.NodeSelector != nil {
		pod.Spec.NodeSelector = sequentialJob.Spec.Template.Spec.NodeSelector
	}

	// Add affinity rules if specified
	if sequentialJob.Spec.Template.Spec.Affinity != nil {
		pod.Spec.Affinity = sequentialJob.Spec.Template.Spec.Affinity
	}

	// Add toleration rules if specified
	if sequentialJob.Spec.Template.Spec.Tolerations != nil {
		pod.Spec.Tolerations = sequentialJob.Spec.Template.Spec.Tolerations
	}

	// Add volumes if specified
	// This is essential for data sharing between containers in the sequence
	if len(sequentialJob.Spec.Template.Spec.Volumes) > 0 {
		pod.Spec.Volumes = sequentialJob.Spec.Template.Spec.Volumes
	}

	// Use specified service account if provided
	if sequentialJob.Spec.Template.Spec.ServiceAccountName != "" {
		pod.Spec.ServiceAccountName = sequentialJob.Spec.Template.Spec.ServiceAccountName
	}

	// Return the fully configured pod
	return pod, nil
}

// finalizeSequentialJob ensures proper cleanup of all resources when a SequentialJob is deleted.
// This function:
// 1. Finds all pods associated with the SequentialJob
// 2. Deletes each pod with proper propagation policy
// 3. Tracks and handles any deletion errors
//
// This finalizer provides an additional layer of cleanup beyond owner references.
// While owner references handle most cases automatically, the finalizer ensures
// complete cleanup even in edge cases or race conditions.
func (r *SequentialJobReconciler) finalizeSequentialJob(ctx context.Context, sequentialJob *v1alpha1.SequentialJob, logger logr.Logger) error {
	logger.Info("Finalizing SequentialJob", "name", sequentialJob.Name)

	// Find all associated pods by label selector
	// We use the app.kubernetes.io/part-of label to find all pods across all indices
	podList := &corev1.PodList{}
	labelSelector := map[string]string{
		"app.kubernetes.io/part-of": sequentialJob.Name,
	}

	// List all pods in the resource's namespace with our label
	if err := r.List(ctx, podList,
		client.InNamespace(sequentialJob.Namespace),
		client.MatchingLabels(labelSelector)); err != nil {
		logger.Error(err, "Failed to list pods during finalization")
		// We don't want to block deletion if we can't list pods
		// This could be a permissions issue or API server issue
		// Since k8s garbage collection will handle most cases, we can proceed
		return nil
	}

	// Track any deletion errors to report at the end
	// This allows us to attempt deletion of all pods even if some fail
	var deletionErrors []error

	// Delete each associated pod
	for _, pod := range podList.Items {
		logger.Info("Deleting pod", "name", pod.Name)

		// Delete with background propagation policy
		// This ensures all related resources are properly cleaned up
		if err := r.Delete(ctx, &pod, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil {
			// Ignore not-found errors (pod may have already been deleted)
			// This handles race conditions where pod is deleted between listing and deletion
			if !errors.IsNotFound(err) {
				logger.Error(err, "Failed to delete pod", "pod", pod.Name)
				deletionErrors = append(deletionErrors, err)
				// Continue trying to delete other pods rather than failing immediately
			}
		}
	}

	// If we had errors deleting pods, return an error to retry the finalizer
	// This ensures we don't remove the finalizer until all cleanup is complete
	if len(deletionErrors) > 0 {
		return fmt.Errorf("failed to delete %d pods during finalization", len(deletionErrors))
	}

	// All cleanup succeeded
	logger.Info("Successfully finalized SequentialJob", "name", sequentialJob.Name)
	return nil
}

//+kubebuilder:rbac:groups=myapp.example.com,resources=sequentialjobs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=myapp.example.com,resources=sequentialjobs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=myapp.example.com,resources=sequentialjobs/finalizers,verbs=update
//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete

// Reconcile is the main entry point for the SequentialJob controller reconciliation loop.
// This function:
// 1. Fetches the SequentialJob resource
// 2. Handles resource deletion with finalizers
// 3. Ensures finalizers are set up for proper cleanup
// 4. Initializes status for new resources
// 5. Processes the sequential job execution
//
// The reconciler follows standard controller patterns:
// - Level-triggered (works with current state, not events)
// - Idempotent (can be called multiple times safely)
// - Handles partial failures with proper error return
// - Uses status to track execution state
//
// The reconciliation is triggered by:
// - SequentialJob resource creation, update, or deletion
// - Pod status changes for pods owned by the SequentialJob
// - Periodic requeuing for active monitoring
func (r *SequentialJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// Get logger from context for consistent request-scoped logging
	logger := log.FromContext(ctx)
	logger.Info("Reconciling SequentialJob", "namespacedName", req.NamespacedName)

	// Fetch the SequentialJob instance from the Kubernetes API
	sequentialJob := &batchv1alpha1.SequentialJob{}
	err := r.Get(ctx, req.NamespacedName, sequentialJob)
	if err != nil {
		if errors.IsNotFound(err) {
			// Object not found, likely deleted
			// This is normal and happens after garbage collection completes
			logger.Info("SequentialJob resource not found, ignoring since it's likely been deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - could be API server issues, connectivity problems, etc.
		// Return error to retry with exponential backoff
		logger.Error(err, "Failed to get SequentialJob")
		return ctrl.Result{}, err
	}

	// Handle finalizer logic for resource deletion
	// Check if the resource is being deleted by looking at the deletion timestamp
	isSJMarkedToBeDeleted := sequentialJob.GetDeletionTimestamp() != nil
	if isSJMarkedToBeDeleted {
		// Resource is being deleted, check if our finalizer is still present
		if controllerutil.ContainsFinalizer(sequentialJob, sequentialJobFinalizer) {
			logger.Info("Performing finalizer operations for SequentialJob")

			// Run finalization logic to clean up resources
			if err := r.finalizeSequentialJob(ctx, sequentialJob, logger); err != nil {
				logger.Error(err, "Failed to run finalizer for SequentialJob")
				// Return error to retry finalizer execution
				return ctrl.Result{}, err
			}

			// All cleanup done, remove our finalizer
			// This allows Kubernetes to complete the deletion
			logger.Info("Removing finalizer from SequentialJob")
			controllerutil.RemoveFinalizer(sequentialJob, sequentialJobFinalizer)
			if err := r.Update(ctx, sequentialJob); err != nil {
				logger.Error(err, "Failed to remove finalizer from SequentialJob")
				return ctrl.Result{}, err
			}
		}
		// Resource is being deleted and finalizer is processed, nothing more to do
		return ctrl.Result{}, nil
	}

	// For new resources, add our finalizer to ensure proper cleanup
	// This protects against orphaned resources if the controller crashes
	if !controllerutil.ContainsFinalizer(sequentialJob, sequentialJobFinalizer) {
		logger.Info("Adding finalizer to SequentialJob")
		controllerutil.AddFinalizer(sequentialJob, sequentialJobFinalizer)
		if err := r.Update(ctx, sequentialJob); err != nil {
			logger.Error(err, "Failed to add finalizer to SequentialJob")
			return ctrl.Result{}, err
		}
		// Return here as the update will trigger another reconcile
		// This avoids processing resource immediately, letting the update settle first
		return ctrl.Result{}, nil
	}

	// Initialize status if needed for new resources
	// This sets up the initial tracking state before job execution begins
	if err := r.initializeStatus(ctx, sequentialJob, logger); err != nil {
		logger.Error(err, "Failed to initialize SequentialJob status")
		return ctrl.Result{}, err
	}

	// Process the sequential jobs - this is the main state machine
	// This function will handle all aspects of sequential job execution
	logger.Info("Processing sequential jobs", "current index", *sequentialJob.Status.Progress.CurrentIndex)
	return r.processSequentialJobs(ctx, sequentialJob, logger)
}

// SetupWithManager sets up the controller with the Manager.
func (r *SequentialJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&batchv1alpha1.SequentialJob{}).
		Owns(&batchv1.Job{}).
		Named("sequentialjob").
		Complete(r)
}
