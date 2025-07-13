# kube-janitor-go Helm Chart

This Helm chart deploys [kube-janitor-go](https://github.com/blaxel-ai/kube-janitor-go), a Kubernetes controller that automatically cleans up resources based on TTL (time to live) annotations or custom rules.

## Prerequisites

- Kubernetes 1.19+
- Helm 3.0+
- (Optional) Prometheus Operator for ServiceMonitor support

## Installation

### Add Helm repository

```bash
helm repo add kube-janitor-go https://blaxel-ai.github.io/kube-janitor-go
helm repo update
```

### Install the chart

```bash
# Install latest stable version
helm install kube-janitor-go kube-janitor-go/kube-janitor-go

# Install specific version
helm install kube-janitor-go kube-janitor-go/kube-janitor-go --version 1.0.0

# Install development version (from develop branch)
helm search repo kube-janitor-go --devel
helm install kube-janitor-go kube-janitor-go/kube-janitor-go --devel

# Install in a specific namespace
helm install kube-janitor-go kube-janitor-go/kube-janitor-go -n kube-janitor --create-namespace

# Install with custom values
helm install kube-janitor-go kube-janitor-go/kube-janitor-go -f values.yaml
```

### Development Versions

Development versions are published automatically from the `develop` branch with version numbers like `0.1.0-dev.20240115120000`. Use `--devel` flag to see and install these versions.

### Install from source

```bash
# Clone the repository
git clone https://github.com/blaxel-ai/kube-janitor-go.git
cd kube-janitor-go

# Install from local chart
helm install kube-janitor-go ./helm/kube-janitor-go
```

## Uninstallation

```bash
helm uninstall kube-janitor-go -n kube-janitor
```

## Configuration

The following table lists the configurable parameters of the kube-janitor-go chart and their default values.

## Values

| Parameter | Description | Default |
|-----------|-------------|---------|
| `affinity` | Affinity rules for pod assignment | `{}` |
| `autoscaling.enabled` | Enable HPA | `false` |
| `autoscaling.maxReplicas` | Maximum number of replicas | `5` |
| `autoscaling.minReplicas` | Minimum number of replicas | `1` |
| `autoscaling.targetCPUUtilizationPercentage` | Target CPU utilization percentage | `80` |
| `autoscaling.targetMemoryUtilizationPercentage` | Target memory utilization percentage | `80` |
| `commonAnnotations` | Additional annotations to add to all resources | `{}` |
| `commonLabels` | Example: - name: extra-volume mountPath: /extra readOnly: true Additional labels to add to all resources | `{}` |
| `extraEnvFrom` | Example: - name: FOO value: bar Extra environment variables from secrets or configmaps | `[]` |
| `extraEnvVars` | Extra environment variables | `[]` |
| `extraVolumeMounts` | Example: - name: extra-volume configMap: name: extra-configmap Extra volume mounts | `[]` |
| `extraVolumes` | Example: - secretRef: name: my-secret - configMapRef: name: my-configmap Extra volumes | `[]` |
| `fullnameOverride` | Override the full name of resources | `""` |
| `image.pullPolicy` | Image pull policy | `"IfNotPresent"` |
| `image.repository` | Container image repository | `"ghcr.io/blaxel-ai/kube-janitor-go"` |
| `image.tag` | Overrides the image tag whose default is the chart appVersion. | `""` |
| `imagePullSecrets` | Image pull secrets for private registries | `[]` |
| `janitor.dryRun` | Dry run mode - don't actually delete resources | `false` |
| `janitor.excludeNamespaces` | Namespaces to exclude | See values.yaml |
| `janitor.excludeResources` | Resource types to exclude | See values.yaml |
| `janitor.includeNamespaces` | Namespaces to include (empty means all) | `[]` |
| `janitor.includeResources` | Resource types to include (empty means all) | `[]` |
| `janitor.interval` | Run interval (default: 30s) | `"60s"` |
| `janitor.logLevel` | Log level: debug, info, warn, error | `"info"` |
| `janitor.maxWorkers` | Maximum number of concurrent workers | `10` |
| `janitor.rulesFile.enabled` | Enable rules file | `true` |
| `janitor.rulesFile.path` | Path to rules file (mounted from ConfigMap) | `"/config/rules.yaml"` |
| `janitor.rulesFile.rules` | Rules configuration | See values.yaml |
| `janitor.runOnce` | Run once and exit | `false` |
| `livenessProbe.enabled` | Enable liveness probe | `true` |
| `livenessProbe.failureThreshold` | Minimum consecutive failures | `3` |
| `livenessProbe.httpGet.path` | Probe path | `"/health"` |
| `livenessProbe.httpGet.port` | Probe port | `"metrics"` |
| `livenessProbe.initialDelaySeconds` | Initial delay before probing | `10` |
| `livenessProbe.periodSeconds` | How often to perform the probe | `30` |
| `livenessProbe.successThreshold` | Minimum consecutive successes | `1` |
| `livenessProbe.timeoutSeconds` | Probe timeout | `5` |
| `metrics.enabled` | Enable metrics endpoint | `true` |
| `metrics.path` | Metrics endpoint path | `"/metrics"` |
| `metrics.port` | Metrics server port | `8080` |
| `metrics.serviceMonitor.annotations` | Additional annotations for the ServiceMonitor | `{}` |
| `metrics.serviceMonitor.enabled` | Create ServiceMonitor resource | `false` |
| `metrics.serviceMonitor.interval` | Scrape interval | `"30s"` |
| `metrics.serviceMonitor.labels` | Additional labels for the ServiceMonitor | `{}` |
| `metrics.serviceMonitor.metricRelabelings` | Additional metric relabelings | `[]` |
| `metrics.serviceMonitor.namespace` | Namespace where the ServiceMonitor will be created (defaults to release namespace) | `""` |
| `metrics.serviceMonitor.relabelings` | Additional relabelings | `[]` |
| `metrics.serviceMonitor.scrapeTimeout` | Scrape timeout | `"10s"` |
| `nameOverride` | Override the chart name | `""` |
| `nodeSelector` | Node selector for pod assignment | `{}` |
| `podAnnotations` | Pod annotations | `{}` |
| `podDisruptionBudget.enabled` | Enable PodDisruptionBudget | `false` |
| `podDisruptionBudget.minAvailable` | Minimum available pods | `1` |
| `podSecurityContext` | Pod security context | `{}` |
| `priorityClassName` | Priority class name | `""` |
| `rbac.additionalRules` | Additional rules for the ClusterRole | `[]` |
| `rbac.create` | Create RBAC resources | `true` |
| `readinessProbe.enabled` | Enable readiness probe | `true` |
| `readinessProbe.failureThreshold` | Minimum consecutive failures | `3` |
| `readinessProbe.httpGet.path` | Probe path | `"/health"` |
| `readinessProbe.httpGet.port` | Probe port | `"metrics"` |
| `readinessProbe.initialDelaySeconds` | Initial delay before probing | `5` |
| `readinessProbe.periodSeconds` | How often to perform the probe | `10` |
| `readinessProbe.successThreshold` | Minimum consecutive successes | `1` |
| `readinessProbe.timeoutSeconds` | Probe timeout | `5` |
| `replicaCount` | Default values for kube-janitor-go Number of replicas for the deployment | `1` |
| `securityContext.allowPrivilegeEscalation` | Prevent privilege escalation | `false` |
| `securityContext.capabilities.drop` | Linux capabilities to drop | See values.yaml |
| `securityContext.readOnlyRootFilesystem` | Mount root filesystem as read-only | `true` |
| `securityContext.runAsNonRoot` | Run container as non-root user | `true` |
| `securityContext.runAsUser` | User ID to run the container as | `1000` |
| `service.annotations` | Service annotations | `{}` |
| `service.port` | Service port | `8080` |
| `service.type` | Service type | `"ClusterIP"` |
| `serviceAccount.annotations` | Annotations to add to the service account | `{}` |
| `serviceAccount.create` | Specifies whether a service account should be created | `true` |
| `serviceAccount.name` | The name of the service account to use. If not set and create is true, a name is generated using the fullname template | `""` |
| `tolerations` | Tolerations for pod assignment | `[]` |
### Global Parameters

| Parameter | Description | Default |
|-----------|-------------|---------|
| `replicaCount` | Number of replicas | `1` |
| `image.repository` | Image repository | `ghcr.io/blaxel-ai/kube-janitor-go` |
| `image.pullPolicy` | Image pull policy | `IfNotPresent` |
| `image.tag` | Image tag (defaults to chart appVersion) | `""` |
| `imagePullSecrets` | Image pull secrets | `[]` |
| `nameOverride` | Override chart name | `""` |
| `fullnameOverride` | Override full name | `""` |

### Service Account

| Parameter | Description | Default |
|-----------|-------------|---------|
| `serviceAccount.create` | Create service account | `true` |
| `serviceAccount.annotations` | Service account annotations | `{}` |
| `serviceAccount.name` | Service account name | `""` |

### Pod Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `podAnnotations` | Pod annotations | `{}` |
| `podSecurityContext` | Pod security context | `{}` |
| `securityContext` | Container security context | See values.yaml |
| `resources` | Resource limits and requests | See values.yaml |
| `nodeSelector` | Node selector | `{}` |
| `tolerations` | Tolerations | `[]` |
| `affinity` | Affinity rules | `{}` |
| `priorityClassName` | Priority class name | `""` |

### Janitor Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `janitor.interval` | Cleanup interval | `60s` |
| `janitor.dryRun` | Enable dry-run mode | `false` |
| `janitor.runOnce` | Run once and exit | `false` |
| `janitor.logLevel` | Log level (debug, info, warn, error) | `info` |
| `janitor.maxWorkers` | Maximum concurrent workers | `10` |
| `janitor.includeResources` | Resource types to include | `[]` |
| `janitor.excludeResources` | Resource types to exclude | `["events", "controllerrevisions"]` |
| `janitor.includeNamespaces` | Namespaces to include | `[]` |
| `janitor.excludeNamespaces` | Namespaces to exclude | `["kube-system", "kube-public", "kube-node-lease"]` |

### Rules Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `janitor.rulesFile.enabled` | Enable rules file | `true` |
| `janitor.rulesFile.path` | Path to rules file | `/config/rules.yaml` |
| `janitor.rulesFile.rules` | Rules configuration | See values.yaml |

### Metrics Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `metrics.enabled` | Enable metrics endpoint | `true` |
| `metrics.port` | Metrics port | `8080` |
| `metrics.path` | Metrics path | `/metrics` |
| `metrics.serviceMonitor.enabled` | Create ServiceMonitor | `false` |
| `metrics.serviceMonitor.interval` | Scrape interval | `30s` |
| `metrics.serviceMonitor.scrapeTimeout` | Scrape timeout | `10s` |
| `metrics.serviceMonitor.labels` | Additional labels | `{}` |
| `metrics.serviceMonitor.annotations` | Additional annotations | `{}` |
| `metrics.serviceMonitor.namespace` | ServiceMonitor namespace | `""` |

### Service Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `service.type` | Service type | `ClusterIP` |
| `service.port` | Service port | `8080` |
| `service.annotations` | Service annotations | `{}` |

### Autoscaling

| Parameter | Description | Default |
|-----------|-------------|---------|
| `autoscaling.enabled` | Enable HPA | `false` |
| `autoscaling.minReplicas` | Minimum replicas | `1` |
| `autoscaling.maxReplicas` | Maximum replicas | `5` |
| `autoscaling.targetCPUUtilizationPercentage` | Target CPU utilization | `80` |
| `autoscaling.targetMemoryUtilizationPercentage` | Target memory utilization | `80` |

### Pod Disruption Budget

| Parameter | Description | Default |
|-----------|-------------|---------|
| `podDisruptionBudget.enabled` | Enable PDB | `false` |
| `podDisruptionBudget.minAvailable` | Minimum available pods | `1` |
| `podDisruptionBudget.maxUnavailable` | Maximum unavailable pods | `""` |

### Probes

| Parameter | Description | Default |
|-----------|-------------|---------|
| `livenessProbe.enabled` | Enable liveness probe | `true` |
| `livenessProbe.*` | Liveness probe configuration | See values.yaml |
| `readinessProbe.enabled` | Enable readiness probe | `true` |
| `readinessProbe.*` | Readiness probe configuration | See values.yaml |

### RBAC

| Parameter | Description | Default |
|-----------|-------------|---------|
| `rbac.create` | Create RBAC resources | `true` |
| `rbac.additionalRules` | Additional ClusterRole rules | `[]` |

### Additional Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `commonLabels` | Labels to add to all resources | `{}` |
| `commonAnnotations` | Annotations to add to all resources | `{}` |
| `extraEnvVars` | Extra environment variables | `[]` |
| `extraEnvFrom` | Extra environment sources | `[]` |
| `extraVolumes` | Extra volumes | `[]` |
| `extraVolumeMounts` | Extra volume mounts | `[]` |

## Examples

### Dry-run mode

Test what would be deleted without actually deleting:

```bash
helm install kube-janitor-go kube-janitor-go/kube-janitor-go \
  --set janitor.dryRun=true
```

### Custom cleanup interval

Run cleanup every 5 minutes:

```bash
helm install kube-janitor-go kube-janitor-go/kube-janitor-go \
  --set janitor.interval=5m
```

### Include specific namespaces

Only clean up resources in specific namespaces:

```bash
helm install kube-janitor-go kube-janitor-go/kube-janitor-go \
  --set janitor.includeNamespaces='{dev,staging,feature-*}'
```

### Custom rules

Create a custom values file with rules:

```yaml
janitor:
  rulesFile:
    enabled: true
    rules:
      - id: cleanup-test-deployments
        resources:
          - deployments
        expression: 'object.metadata.name.contains("test")'
        ttl: 1h
      
      - id: cleanup-failed-jobs
        resources:
          - jobs
        expression: 'has(object.status.failed) && object.status.failed > 0'
        ttl: 30m
```

Then install:

```bash
helm install kube-janitor-go kube-janitor-go/kube-janitor-go -f custom-values.yaml
```

### Enable Prometheus monitoring

```bash
helm install kube-janitor-go kube-janitor-go/kube-janitor-go \
  --set metrics.serviceMonitor.enabled=true \
  --set metrics.serviceMonitor.labels.prometheus=kube-prometheus
```

### High availability setup

```bash
helm install kube-janitor-go kube-janitor-go/kube-janitor-go \
  --set replicaCount=3 \
  --set podDisruptionBudget.enabled=true \
  --set podDisruptionBudget.minAvailable=2
```

## Upgrading

### From 0.x to 1.x

When upgrading from version 0.x to 1.x, note the following breaking changes:

1. The `janitor.rules` parameter has been moved to `janitor.rulesFile.rules`
2. The default excluded namespaces now include the release namespace

To upgrade:

```bash
helm upgrade kube-janitor-go kube-janitor-go/kube-janitor-go
```

## Troubleshooting

### Check logs

```bash
kubectl logs -n kube-janitor deployment/kube-janitor-go
```

### Verify RBAC permissions

```bash
kubectl auth can-i list '*' --as=system:serviceaccount:kube-janitor:kube-janitor-go
kubectl auth can-i delete pods --as=system:serviceaccount:kube-janitor:kube-janitor-go
```

### Test with dry-run

```bash
helm upgrade kube-janitor-go kube-janitor-go/kube-janitor-go \
  --set janitor.dryRun=true \
  --set janitor.logLevel=debug
```

### Check metrics

```bash
kubectl port-forward -n kube-janitor service/kube-janitor-go 8080:8080
curl http://localhost:8080/metrics
```

## License

This Helm chart is licensed under the Apache License 2.0. See [LICENSE](https://github.com/blaxel-ai/kube-janitor-go/blob/main/LICENSE) for the full license text. 