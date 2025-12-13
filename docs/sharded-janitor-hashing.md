# Sharded Janitor Architecture

This document describes how kube-janitor-go implements horizontal scaling using consistent hashing and peer discovery.

## Overview

When running multiple instances of kube-janitor, sharding ensures that each resource is processed by exactly one janitor instance. This prevents duplicate deletions, reduces API server load, and enables horizontal scaling.

## Architecture

```mermaid
flowchart TB
    subgraph Kubernetes Cluster
        subgraph Headless Service
            HS[kube-janitor-headless<br/>clusterIP: None]
        end

        subgraph StatefulSet
            J0[kube-janitor-0]
            J1[kube-janitor-1]
            J2[kube-janitor-2]
        end

        subgraph Resources
            R1[Pod A<br/>namespace/pod-a]
            R2[Pod B<br/>namespace/pod-b]
            R3[Deployment C<br/>namespace/deploy-c]
            R4[ConfigMap D<br/>namespace/cm-d]
            R5[Secret E<br/>namespace/secret-e]
            R6[Job F<br/>namespace/job-f]
        end
    end

    HS --> J0
    HS --> J1
    HS --> J2

    J0 -.->|hash assigns| R1
    J0 -.->|hash assigns| R4
    J1 -.->|hash assigns| R2
    J1 -.->|hash assigns| R5
    J2 -.->|hash assigns| R3
    J2 -.->|hash assigns| R6
```

## Components

### 1. Hash Ring (Consistent Hashing)

The hash ring distributes resources across janitor instances using the FNV-1a hash algorithm with virtual nodes for even distribution.

```mermaid
graph LR
    subgraph Hash Ring
        direction TB
        V0_0["janitor-0#0"]
        V1_0["janitor-1#0"]
        V2_0["janitor-2#0"]
        V0_1["janitor-0#1"]
        V1_1["janitor-1#1"]
        V2_1["janitor-2#1"]
        V0_2["janitor-0#..."]
        V1_2["janitor-1#..."]
        V2_2["janitor-2#..."]
    end

    R1["namespace/pod-a<br/>hash: 0x3F..."] --> V0_0
    R2["namespace/pod-b<br/>hash: 0x7A..."] --> V1_1
    R3["namespace/deploy-c<br/>hash: 0xB2..."] --> V2_0
```

**Key characteristics:**

| Property | Value | Description |
|----------|-------|-------------|
| Hash Algorithm | FNV-1a (64-bit) | Fast, non-cryptographic hash |
| Virtual Nodes | 150 per physical node | Improves distribution uniformity |
| Resource Key | `namespace/name` | Unique identifier for each resource |
| Lookup | O(log n) | Binary search on sorted ring |

### 2. Peer Discovery

Peer discovery uses Kubernetes DNS to find other janitor instances through a headless service.

```mermaid
sequenceDiagram
    participant J0 as kube-janitor-0
    participant DNS as Kubernetes DNS
    participant HS as Headless Service
    participant HR as Hash Ring

    Note over J0: Startup or refresh interval

    J0->>DNS: LookupHost(kube-janitor.ns.svc.cluster.local)
    DNS->>HS: Query A records
    HS-->>DNS: [10.0.0.1, 10.0.0.2, 10.0.0.3]
    DNS-->>J0: IP addresses

    loop For each IP
        J0->>DNS: LookupAddr(IP)
        DNS-->>J0: kube-janitor-N.kube-janitor.ns.svc.cluster.local
    end

    J0->>HR: SetNodes([janitor-0, janitor-1, janitor-2])
    HR-->>J0: Ring updated

    Note over J0: Wait refresh interval (30s)
```

## Resource Processing Flow

```mermaid
flowchart TD
    A[List Resources] --> B{For each resource}
    B --> C[Extract namespace/name]
    C --> D{Sharding enabled?}
    D -->|No| F[Process resource]
    D -->|Yes| E{ShouldProcess?}
    E -->|Yes| F
    E -->|No| G[Skip resource<br/>Increment skipped metric]
    F --> H[Evaluate TTL/rules]
    H --> I{Should delete?}
    I -->|Yes| J[Queue for deletion]
    I -->|No| K[Continue]
    G --> B
    J --> B
    K --> B
```

### ShouldProcess Decision

```mermaid
flowchart TD
    A[ShouldProcess<br/>namespace, name] --> B[Build key:<br/>namespace/name]
    B --> C[Hash key with FNV-1a]
    C --> D[Binary search in ring<br/>for first hash >= key]
    D --> E{Found node?}
    E -->|No wrap around| F[Use first node in ring]
    E -->|Yes| G[Use found node]
    F --> H{Node == selfName?}
    G --> H
    H -->|Yes| I[Return true<br/>Process locally]
    H -->|No| J[Return false<br/>Skip resource]
```

## Configuration

### CLI Flags

```bash
kube-janitor-go \
  --sharding-enabled \
  --sharding-service-name=kube-janitor-headless \
  --sharding-namespace=kube-system \
  --sharding-refresh-interval=30s
```

| Flag | Default | Description |
|------|---------|-------------|
| `--sharding-enabled` | `false` | Enable sharding mode |
| `--sharding-service-name` | `kube-janitor` | Headless service name for DNS discovery |
| `--sharding-namespace` | Current namespace | Namespace where headless service exists |
| `--sharding-refresh-interval` | `30s` | How often to refresh peer list |

### Helm Values

```yaml
sharding:
  enabled: true
  serviceName: ""  # Auto-generated: <release>-headless
  refreshInterval: 30s

replicaCount: 3  # Number of janitor instances
```

### Required Environment Variables

| Variable | Source | Description |
|----------|--------|-------------|
| `HOSTNAME` | Downward API | Pod name for self-identification |
| `POD_NAMESPACE` | Downward API | Namespace for DNS queries |

## Kubernetes Resources

When sharding is enabled, the Helm chart creates:

```mermaid
flowchart LR
    subgraph Sharding Mode
        SS[StatefulSet] --> P0[kube-janitor-0]
        SS --> P1[kube-janitor-1]
        SS --> P2[kube-janitor-2]

        HLS[Headless Service<br/>clusterIP: None] -.->|DNS| P0
        HLS -.->|DNS| P1
        HLS -.->|DNS| P2
    end

    subgraph Regular Mode
        D[Deployment] --> RP[kube-janitor-xxx]
        SVC[ClusterIP Service] --> RP
    end
```

### StatefulSet (sharding enabled)

- Provides stable, predictable pod names (`kube-janitor-0`, `kube-janitor-1`, etc.)
- Required for consistent hashing identity
- Pods are created/deleted in order

### Headless Service

- `clusterIP: None` enables direct pod DNS resolution
- Returns all pod IPs when queried
- Enables peer discovery without external dependencies

## Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `kube_janitor_resources_skipped_total` | Counter | resource, namespace | Resources skipped due to sharding |
| `kube_janitor_sharding_peers` | Gauge | - | Number of active peer instances |

## Scaling Behavior

### Adding Instances

```mermaid
sequenceDiagram
    participant J0 as janitor-0
    participant J1 as janitor-1
    participant New as janitor-2 (new)
    participant DNS as DNS

    Note over New: Pod starts
    New->>DNS: Register in headless service

    Note over J0,J1: Next refresh interval
    J0->>DNS: LookupHost
    DNS-->>J0: [J0, J1, New]
    J0->>J0: SetNodes([J0, J1, J2])

    J1->>DNS: LookupHost
    DNS-->>J1: [J0, J1, New]
    J1->>J1: SetNodes([J0, J1, J2])

    New->>DNS: LookupHost
    DNS-->>New: [J0, J1, New]
    New->>New: SetNodes([J0, J1, J2])

    Note over J0,New: ~1/3 of resources now assigned to J2
```

### Removing Instances

When a janitor instance is removed:

1. Kubernetes removes the pod from headless service endpoints
2. Remaining instances detect the change on next DNS refresh
3. Resources previously assigned to the removed instance are redistributed
4. Only ~1/N resources need reassignment (consistent hashing property)

## Best Practices

### Replica Count

- **Minimum:** 2 replicas for high availability
- **Recommended:** 3-5 replicas for production
- **Maximum:** Limited by API server capacity and resource count

### Refresh Interval

- **Default:** 30 seconds
- **Lower (10-15s):** Faster reaction to scaling, higher DNS load
- **Higher (60s+):** Less DNS overhead, slower reaction to changes

### Resource Distribution

With 150 virtual nodes per physical node, expect:
- ~98% uniform distribution across instances
- Minimal redistribution during scale events

## Troubleshooting

### Peers Not Discovered

```bash
# Check headless service endpoints
kubectl get endpoints kube-janitor-headless -n <namespace>

# Check DNS resolution from a pod
kubectl exec -it kube-janitor-0 -- nslookup kube-janitor-headless.<namespace>.svc.cluster.local
```

### Uneven Resource Distribution

Enable trace logging to see sharding decisions:

```bash
kube-janitor-go --log-level=trace
```

Look for logs like:
```
Sharding decision for resource namespace=default name=my-pod assignedNode=kube-janitor-1 selfName=kube-janitor-0 shouldProcess=false
```

### Resources Not Being Processed

Check if the hash ring has nodes:

```bash
# Look for this log message
"Set nodes in hash ring" nodes=[janitor-0, janitor-1, janitor-2]
```

If the ring is empty, peer discovery is failing. Check DNS connectivity and service configuration.

