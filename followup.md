# SequentialJob Kubernetes Operator Implementation

## How do users specify the list of containers? (Do you use the core types?)

In our implementation, we use Kubernetes core types:

```go
import (
    corev1 "k8s.io/api/core/v1"
)

type SequentialJobSpec struct {
    // List of containers to run sequentially
    // +kubebuilder:validation:MinItems=1
    Containers []corev1.Container `json:"containers"`
    
    // Template for the pod to use for each job (shared settings)
    // +optional
    Template corev1.PodTemplateSpec `json:"template,omitempty"`
}
```

This provides several advantages:

- Users leverage their existing knowledge of Kubernetes container specifications
- We inherit all the capabilities of standard Kubernetes containers (volume mounts, security contexts, resource requests/limits)
- Standard tools like kubectl already know how to validate and display these types
- Users can specify the full range of container configuration options without us having to reimplement them

Example usage:

```yaml
apiVersion: batch.example.com/v1alpha1
kind: SequentialJob
metadata:
  name: example-job-sequence
spec:
  containers:
    - name: step1
      image: busybox
      command: ["sh", "-c"]
      args: ["echo 'Processing step 1'; sleep 10"]
      resources:
        requests:
          memory: "64Mi"
          cpu: "100m"
```

## Do you report status? How is status calculated? How do you surface job failures?

Our implementation uses a comprehensive status reporting structure:

### Kubernetes Conditions for overall status:

```go
type SequentialJobStatus struct {
    // Standard Kubernetes-style conditions
    Conditions []metav1.Condition `json:"conditions,omitempty"`
    // ...other fields
}
```

We track three conditions:

- **Available**: Indicates resource is operational
- **Progressing**: Indicates resource is making progress
- **Failed**: Indicates a failure has occurred

### Nested Status Types for detailed information:

- **ExecutionState**: Tracks counts of active, succeeded, and failed jobs
- **JobProgress**: Tracks the current job index, completed jobs, and failure details

### Job Failures are surfaced through multiple channels:

- `Failed` condition with detailed error message
- `FailedIndex` and `FailureReason` fields in the status
- Kubernetes events for operator actions and failures
- Controllers logs for debugging

Status is calculated during each reconciliation by:

- Tracking state transitions of jobs (created → active → succeeded/failed)
- Updating condition reasons and messages based on current state
- Propagating detailed failure information from Kubernetes Jobs

Example of failed job status:

```yaml
status:
  conditions:
    - type: Failed
      status: "True"
      reason: "JobFailed"
      message: "Job 1 failed: BackoffLimitExceeded"
  state:
    active: 0
    succeeded: 1
    failed: 1
  progress:
    currentIndex: 1
    completedIndices: [0]
    failedIndex: 1
    failureReason: "BackoffLimitExceeded"
```

## Where do you validate user inputs? Where do you report reconciliation failures?

We implement validation at three levels:

### 1. CRD Schema Validation (static validation):

```go
// +kubebuilder:validation:MinItems=1
Containers []corev1.Container `json:"containers"`
```

This validation runs before the object is stored in etcd.

### 2. Admission Webhook (dynamic validation):

```go
func (r *SequentialJob) validateSequentialJob() error {
    // Business logic validation
    if len(r.Spec.Containers) == 0 {
        allErrs = append(allErrs, field.Required(
            path.Child("containers"), 
            "at least one container must be specified"))
    }
    // Resource validation, security checks, etc.
}
```

This allows for complex validation logic not expressible in OpenAPI schema.

### 3. Controller Reconciliation Validation (runtime validation):

```go
// Validate dynamic conditions during reconciliation
if !isClusterResourceAvailable(ctx, r.Client) {
    // Update status condition
    meta.SetStatusCondition(&sequentialJob.Status.Conditions, metav1.Condition{
        Type:    "ResourcesUnavailable",
        Status:  metav1.ConditionTrue,
        Reason:  "ClusterOverloaded",
        Message: "Cluster resources temporarily unavailable",
    })
}
```

Reconciliation failures are reported through:

#### Status Conditions: Updated to reflect failures

```go
meta.SetStatusCondition(&sj.Status.Conditions, metav1.Condition{
    Type:    ConditionFailed,
    Status:  metav1.ConditionTrue,
    Reason:  "ReconciliationError",
    Message: "Failed to reconcile: " + err.Error(),
})
```

#### Kubernetes Events: For human-readable messages

```go
r.recorder.Eventf(sj, corev1.EventTypeWarning, "ReconciliationFailed", 
    "Failed to reconcile: %v", err)
```

#### Controller Logs: For detailed debugging

```go
log.Error(err, "Failed to reconcile", 
    "resource", req.NamespacedName,
    "phase", "job creation")
```

## What happens if the SequentialJob changes while the jobs are running?

We track generation changes using `ObservedGeneration`:

```go
// In status
ObservedGeneration int64 `json:"observedGeneration,omitempty"`

// In controller
if sequentialJob.Generation != sequentialJob.Status.ObservedGeneration {
    // Spec has changed
    sequentialJob.Status.ObservedGeneration = sequentialJob.Generation
    
    if sequentialJob.Status.State.Active != nil && *sequentialJob.Status.State.Active > 0 {
        // We're in the middle of processing
        r.recorder.Event(sequentialJob, corev1.EventTypeWarning, 
            "SpecChanged", "SequentialJob spec changed while jobs running")
    }
}
```

When a SequentialJob is modified during execution:

1. We detect the generation change in the reconciliation loop
2. We emit a warning event about the change
3. We update the ObservedGeneration to track that we've seen this change
4. We continue running the current job sequence to completion
5. Future jobs will use the updated spec

This approach favors stability by not interrupting running jobs. Alternative strategies could include:

- Making specs immutable once started via webhook validation
- Adding a specific field to control change behavior (.spec.updateStrategy)
- Implementing rolling updates similar to Deployments

## How are the child resources you created cleaned up?

Our implementation uses two complementary cleanup mechanisms:

### 1. Owner References (automatic cleanup):

```go
ctrl.SetControllerReference(sj, job, r.Scheme)
```

This makes Kubernetes automatically garbage collect child resources when the parent is deleted.

### 2. Finalizers (controlled cleanup):

```go
const sequentialJobFinalizer = "batch.example.com/sequentialjob-cleanup"

// Check if resource is being deleted
if sequentialJob.DeletionTimestamp.IsZero() {
    // Add finalizer if not present
    if !containsString(sequentialJob.Finalizers, sequentialJobFinalizer) {
        sequentialJob.Finalizers = append(sequentialJob.Finalizers, sequentialJobFinalizer)
        if err := r.Update(ctx, &sequentialJob); err != nil {
            return ctrl.Result{}, err
        }
    }
} else {
    // Resource is being deleted, run cleanup
    if containsString(sequentialJob.Finalizers, sequentialJobFinalizer) {
        if err := r.runCleanupLogic(ctx, &sequentialJob); err != nil {
            return ctrl.Result{}, err
        }
        
        // Remove finalizer to allow deletion
        sequentialJob.Finalizers = removeString(sequentialJob.Finalizers, sequentialJobFinalizer)
        if err := r.Update(ctx, &sequentialJob); err != nil {
            return ctrl.Result{}, err
        }
    }
}
```

The cleanup logic includes:

- Gracefully terminating any running jobs
- Cleaning up any resources without owner references
- Ensuring all jobs are properly deleted
- Recording deletion events for audit trails

This combination ensures reliable cleanup even in complex scenarios or partial failures.