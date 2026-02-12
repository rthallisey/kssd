# Server-Side Drain Driver for Kubernetes

An [SLM (Specialized Lifecycle Management)](https://github.com/kubernetes/enhancements/tree/master/keps/sig-node/4212-specialized-lifecycle-management) driver that implements **server-side node drain**.

Instead of `kubectl drain` running client-side, this driver runs as a kubelet plugin on every node. When a `LifecycleEvent` is created for a node, the kubelet claims it and calls the driver's gRPC methods to cordon the node, evict all pods, and report completion — all server-side.

## How it works

```
┌──────────────────────────────────────────────────────────┐
│                       API Server                         │
│                                                          │
│  Two LifecycleTransitions (AllNodes):                    │
│    drain.slm.k8s.io          drain-started/drain-complete│
│    drain.slm.k8s.io-uncordon uncordoning/maint-complete  │
│                                                          │
│  User creates LifecycleEvent for either transition       │
└──────────────────────────────────────────────────────────┘
                            |
                            | watch/update
                            |
┌──────────────────────────────────────────────────────────┐
│                        Kubelet                           │
│                                                          │
│  SLM Event Reconciler                                    │
│    1. Claim event                                        │
│    2. Call Start gRPC                                    │
│    3. Patch Node condition                               │
│    4. Call End gRPC                                      │
│    5. Patch Node condition                               │
│    6. Delete event                                       │
└──────────────────────────────────────────────────────────┘
                            |
                            | gRPC (unix socket)
                            |
┌──────────────────────────────────────────────────────────┐
│           Kubectl Server Side Drain Driver                │
│                                                          │
│  Drain transition:                                       │
│    Start: cordon node, evict pods (async)                │
│    End:   check remaining pods, return drain-complete    │
│                                                          │
│  Uncordon transition:                                    │
│    Start: uncordon node                                  │
│    End:   verify schedulable, return maintenance-complete│
└──────────────────────────────────────────────────────────┘
```

## Quick start

### Prerequisites

- A Kubernetes cluster (v1.36+) with the `SpecializedLifecycleManagement` feature gate enabled
- The `lifecycle.k8s.io/v1alpha1` API enabled via `--runtime-config=lifecycle.k8s.io/v1alpha1=true`
- `make drain NODE=worker-1` to drain the Pods from worker-1
- `make maintenance-complete NODE=worker-1` to return the Node from maintenance

### Deploy

```bash
# Build the image
docker build -t drain-driver:latest .

# For Kind clusters
kind load docker-image drain-driver:latest --name <cluster-name>

# Deploy RBAC and DaemonSet
kubectl apply -f deploy/rbac.yaml
kubectl apply -f deploy/daemonset.yaml
```

### Trigger a drain

Once the driver is running, it publishes two cluster-wide `LifecycleTransitions` (with `AllNodes=true`). To drain a node, create a `LifecycleEvent` referencing the drain transition:

```yaml
apiVersion: lifecycle.k8s.io/v1alpha1
kind: LifecycleEvent
metadata:
  name: drain-worker-1
spec:
  transitionName: drain.slm.k8s.io
  bindingNode: worker-1
```

```bash
kubectl apply -f - <<EOF
apiVersion: lifecycle.k8s.io/v1alpha1
kind: LifecycleEvent
metadata:
  name: drain-worker-1
spec:
  transitionName: drain.slm.k8s.io
  bindingNode: worker-1
EOF
```

After maintenance is done, uncordon the node by creating a second `LifecycleEvent` referencing the uncordon transition:

```bash
kubectl apply -f - <<EOF
apiVersion: lifecycle.k8s.io/v1alpha1
kind: LifecycleEvent
metadata:
  name: uncordon-worker-1
spec:
  transitionName: drain.slm.k8s.io-uncordon
  bindingNode: worker-1
EOF
```

Monitor progress:

```bash
# Watch the event status
kubectl get lifecycleevents -w

# Watch the node condition
kubectl get node worker-1 -o jsonpath='{.status.conditions[?(@.type=="LifecycleTransition")]}'
```

The drain flow:
1. The kubelet claims the event (status=`Claimed`, driver field populated)
2. The driver cordons the node and evicts pods, then Node condition reason = `drain-started`
3. The driver confirms all pods are evicted, then Node condition reason = `drain-complete`
4. The kubelet deletes the event

The uncordon flow:
1. The kubelet claims the event (status=`Claimed`)
2. The driver uncordons the node → Node condition reason = `uncordoning`
3. The driver confirms the node is schedulable → Node condition reason = `maintenance-complete`
4. The kubelet deletes the event

## Configuration

| Flag | Default | Description |
|------|---------|-------------|
| `--driver-name` | `drain.slm.k8s.io` | SLM driver identifier |
| `--node-name` | (required) | Name of the node this instance manages |
| `--sla` | `5m` | SLA duration for completing the drain |
| `--eviction-timeout` | `30s` | Timeout for individual pod evictions |
| `--grace-period` | `-1` | Override pod termination grace period (-1 = pod default) |
| `--kubeconfig` | (in-cluster) | Path to kubeconfig for out-of-cluster use |

## Development

```bash
# Build
go build ./cmd/drain-driver

# Run locally against a Kind cluster
go run ./cmd/drain-driver kubelet-plugin \
  --kubeconfig=$KUBECONFIG \
  --node-name=<node> \
  --datadir=/tmp/slm-plugins \
  --plugin-registration-path=/tmp/slm-registry \
  -v=5
```

## Community

- [SLM KEP (KEP-4212)](https://github.com/kubernetes/enhancements/tree/master/keps/sig-node/4212-specialized-lifecycle-management)
- [Slack](https://slack.k8s.io/) — #sig-node-lifecycle
- [Mailing List](https://groups.google.com/a/kubernetes.io/g/dev)

Participation is governed by the [Kubernetes Code of Conduct](code-of-conduct.md).
