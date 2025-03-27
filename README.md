# SequentialJob Kubernetes Operator

A Kubernetes operator for executing a sequence of jobs in order. Built using the [kubebuilder](https://book.kubebuilder.io/) framework.

## Overview

The SequentialJob operator manages the execution of a series of containers that need to run sequentially, with each container running to completion before the next one starts. This pattern is useful for workflows where steps must be executed in a specific order, such as:

- Data processing pipelines
- ETL workflows
- Multi-stage builds
- Deployment processes
- Batch reporting jobs

## Features

- **Sequential Execution**: Run containers one after another, ensuring each completes before starting the next
- **Standard Container Spec**: Use familiar Kubernetes container definitions
- **Rich Status Reporting**: Detailed status with conditions and progress tracking
- **Failure Handling**: Clear reporting of failures with specific error information
- **Robust Validation**: Validation at multiple levels ensures correct usage
- **Cleanup**: Reliable cleanup of resources via owner references and finalizers

## Installation

### Prerequisites

- Kubernetes cluster (v1.19+)
- kubectl configured to access your cluster
- [cert-manager](https://cert-manager.io/docs/installation/) installed (for webhook support)

### Deploy the Operator

```bash
# Clone the repository
git clone https://github.com/example/sequential-job-operator
cd sequential-job-operator

# Install the CRDs
make install

# Deploy the controller
make deploy
```

## Usage

### Basic Example

Create a YAML file `example-sequence.yaml`:

```yaml
apiVersion: batch.example.com/v1alpha1
kind: SequentialJob
metadata:
  name: data-processing-pipeline
spec:
  containers:
    - name: extract
      image: data-tools/extractor:v1
      command: ["extract"]
      args: ["--source=/data/source", "--target=/data/raw"]
      resources:
        requests:
          memory: "256Mi"
          cpu: "500m"
        limits:
          memory: "512Mi"
          cpu: "1000m"
      volumeMounts:
        - name: data-volume
          mountPath: /data
          
    - name: transform
      image: data-tools/transformer:v1
      command: ["transform"]
      args: ["--source=/data/raw", "--target=/data/transformed"]
      resources:
        requests:
          memory: "512Mi"
          cpu: "1000m"
        limits:
          memory: "1Gi"
          cpu: "2000m"
      volumeMounts:
        - name: data-volume
          mountPath: /data
          
    - name: load
      image: data-tools/loader:v1
      command: ["load"]
      args: ["--source=/data/transformed", "--db-connection=$(DB_CONN)"]
      env:
        - name: DB_CONN
          valueFrom:
            secretKeyRef:
              name: db-credentials
              key: connection-string
      resources:
        requests:
          memory: "256Mi"
          cpu: "500m"
        limits:
          memory: "512Mi"
          cpu: "1000m"
      volumeMounts:
        - name: data-volume
          mountPath: /data
          
  template:
    metadata:
      labels:
        app: data-pipeline
    spec:
      volumes:
        - name: data-volume
          persistentVolumeClaim:
            claimName: pipeline-data
```

Apply the configuration:

```bash
kubectl apply -f example-sequence.yaml
```

### Monitoring Progress

Check the status of your SequentialJob:

```bash
kubectl get sequentialjob data-processing-pipeline
```

Output:
```
NAME                    STATUS      AGE
data-processing-pipeline Running     2m
```

For more details:

```bash
kubectl describe sequentialjob data-processing-pipeline
```

Output:
```
Name:         data-processing-pipeline
Namespace:    default
...
Status:
  Conditions:
    Last Transition Time:  2023-06-08T14:22:31Z
    Message:               Job 1 completed successfully
    Reason:                JobCompleted
    Status:                True
    Type:                  Progressing
    ...
  Progress:
    Current Index:         2
    Completed Indices:     [0, 1]
  State:
    Active:                1
    Succeeded:             2
    Failed:                0
  Start Time:              2023-06-08T14:20:00Z
...
Events:
  Type     Reason         Age    From                   Message
  ----     ------         ----   ----                   -------
  Normal   JobStarted     5m     sequentialjob-controller  Started job 0
  Normal   JobCompleted   4m     sequentialjob-controller  Job 0 completed successfully
  Normal   JobStarted     4m     sequentialjob-controller  Started job 1
  Normal   JobCompleted   2m     sequentialjob-controller  Job 1 completed successfully
  Normal   JobStarted     2m     sequentialjob-controller  Started job 2
```

## API Reference

### SequentialJob

| Field | Type | Description |
|-------|------|-------------|
| `spec.containers` | `[]corev1.Container` | List of containers to run sequentially |
| `spec.template` | `corev1.PodTemplateSpec` | Template for pod settings shared across all containers |

### Container Specification

The container spec follows the standard [Kubernetes Container](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.26/#container-v1-core) definition, allowing you to specify:

- Image and tags
- Commands and arguments
- Environment variables
- Resource requirements
- Volume mounts
- Security contexts
- Liveness/readiness probes

### Status Fields

| Field | Type | Description |
|-------|------|-------------|
| `status.conditions` | `[]metav1.Condition` | Standard Kubernetes conditions |
| `status.state` | `ExecutionState` | Current execution state (active/succeeded/failed counts) |
| `status.progress` | `JobProgress` | Progress information (current index, completed jobs, etc.) |
| `status.startTime` | `*metav1.Time` | Time when the sequence started |
| `status.completionTime` | `*metav1.Time` | Time when the sequence completed (success or failure) |

#### Conditions

| Type | Status | Description |
|------|--------|-------------|
| `Available` | `True` | SequentialJob has completed successfully |
| `Available` | `False` | SequentialJob is still running or has failed |
| `Progressing` | `True` | SequentialJob is actively progressing |
| `Progressing` | `False` | SequentialJob is not currently progressing (completed or failed) |
| `Failed` | `True` | SequentialJob has failed |
| `Failed` | `False` | SequentialJob has not failed |

## Architecture

### Controller Overview

The SequentialJob operator follows the Kubernetes operator pattern:

1. **API Definition**: Custom Resource Definition (CRD) defining the SequentialJob type
2. **Controller**: Watches for SequentialJob resources and manages their lifecycle
3. **Admission Webhook**: Validates SequentialJob resources before they're accepted
4. **Reconciliation Loop**: Continuously ensures the actual state matches the desired state

### Reconciliation Process

The controller manages the sequence through the following steps:

1. **Initialization**: Set up initial status and conditions
2. **Job Creation**: Create the first Kubernetes Job for the sequence
3. **Job Monitoring**: Watch for Job completion or failure
4. **Sequence Progression**: After successful completion, start the next Job
5. **Status Updates**: Continuously update status with progress information
6. **Completion**: Mark as complete when all Jobs finish or on first failure

### Validation

The operator implements validation at multiple levels:

1. **CRD Schema Validation**: Basic validation through OpenAPI schema
2. **Admission Webhook**: Dynamic validation when resources are created/updated
3. **Controller Validation**: Runtime validation during reconciliation

### Status Reporting

The operator provides comprehensive status information through:

1. **Kubernetes Conditions**: Following standard Kubernetes patterns
2. **Detailed Status Fields**: For programmatic consumption
3. **Kubernetes Events**: For human-readable audit trails
4. **Controller Logs**: For detailed debugging information

## Advanced Usage

### Using Shared Volumes

To share data between jobs in the sequence:

```yaml
spec:
  containers:
    - name: generate-data
      # ... container spec ...
      volumeMounts:
        - name: shared-data
          mountPath: /data
    - name: process-data
      # ... container spec ...
      volumeMounts:
        - name: shared-data
          mountPath: /data
  template:
    spec:
      volumes:
        - name: shared-data
          persistentVolumeClaim:
            claimName: my-pvc
```

### Using ConfigMaps and Secrets

Provide configuration to your jobs:

```yaml
spec:
  containers:
    - name: db-job
      # ... container spec ...
      env:
        - name: DB_PASSWORD
          valueFrom:
            secretKeyRef:
              name: db-credentials
              key: password
        - name: APP_CONFIG
          valueFrom:
            configMapKeyRef:
              name: app-config
              key: config.json
```

### Setting Resource Requirements

Ensure your jobs have the resources they need:

```yaml
spec:
  containers:
    - name: resource-intensive-job
      # ... container spec ...
      resources:
        requests:
          memory: "2Gi"
          cpu: "1000m"
        limits:
          memory: "4Gi"
          cpu: "2000m"
```

## Troubleshooting

### Common Issues

1. **Job Failures**: Check the SequentialJob status and events for failure details
   ```bash
   kubectl describe sequentialjob <name>
   ```

2. **Pending Jobs**: Ensure your cluster has sufficient resources for the requested pods
   ```bash
   kubectl describe job <job-name>
   ```

3. **Webhook Validation Failures**: Check validation errors in the kubectl response
   ```bash
   kubectl get events --field-selector reason=Failed
   ```

4. **Controller Issues**: Check the controller logs
   ```bash
   kubectl logs -n sequential-operator-system deployment/sequential-operator-controller-manager
   ```

## Development

### Setting Up Development Environment

```bash
# Install kubebuilder
curl -L -o kubebuilder https://go.kubebuilder.io/dl/latest/$(go env GOOS)/$(go env GOARCH)
chmod +x kubebuilder && mv kubebuilder /usr/local/bin/

# Clone the repository
git clone https://github.com/example/sequential-job-operator
cd sequential-job-operator

# Install CRDs to a test cluster
make install

# Run the controller locally
make run
```

### Running Tests

```bash
# Run unit tests
make test

# Run integration tests
make test-integration
```

### Building Custom Images

```bash
# Set the image name
export IMG=my-registry/sequential-job-operator:v1

# Build and push the image
make docker-build docker-push

# Deploy with the custom image
make deploy IMG=my-registry/sequential-job-operator:v1
```

## Project Structure

- **api/**: Contains the API definitions and CRD generation code
- **controllers/**: Contains the controller implementation
- **config/**: Contains Kubernetes manifests for deployment
- **hack/**: Contains scripts for development and testing
- **docs/**: Contains additional documentation

## Contributing

Contributions are welcome! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for details.

## License

This project is licensed under the Apache License 2.0 - see the [LICENSE](LICENSE) file for details.