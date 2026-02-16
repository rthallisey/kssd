# KSSD - Kubectl Server-Side-Drain Driver for Kubernetes

A lifecycle driver implemented using the libraries from Kubectl Drain and executed on the server-side.

This driver is a POC, leveraging the [Specialized Lifecycle Management](https://github.com/kubernetes/enhancements/pull/5769) framework to show how lifecycle business logic can be offloaded to a driver while maintaining a standard observability interface in core Kubernetes (Node Conditions).

Instead of `kubectl drain` running client-side, this driver runs as a kubelet plugin on every node.
When a `LifecycleEvent` is created for a node that this driver can claim, the kubelet
claims it and calls the driver's gRPC methods to cordon the node, evict all pods, and report completion.

## How it works

```
┌────────────────────────────────────────────────────────────┐
│                       API Server                           │
│                                                            │
│  Kssd has 2 LifecycleTransitions:                          │
│    1. kssd-drain                                           │
│    2. kssd-maintenance-completed                           │
│                                                            │
│  A user creates a LifecycleEvent to trigger the transition │
└────────────────────────────────────────────────────────────┘
                            |
                            | watch/update
                            |
          ┌───────────────────────────────────┐
          │              Kubelet              │
          │                                   │
          │  SLM LifecycleEvent Reconciler    │
          │    1. Claim LifecyleEvent         │
          │    2. Call Start gRPC             │
          │    3. Patch Node condition        │
          │    4. Call End gRPC               │
          │    5. Patch Node condition        │
          │    6. Delete event                │
          └───────────────────────────────────┘
                            |
                            | gRPC (unix socket)
                            |
 ┌──────────────────────────────────────────────────────────┐
 │              KSSD (Kubectl Server Side Drain)            │
 │                                                          │
 │  Drain transition:                                       │
 │    Start: cordon node, evict pods (async)                │
 │    End:   wait until pod drain                           │
 │                                                          │
 │  Maintenace Complete transition:                         │
 │    Start: uncordon node                                  │
 │    End:   verify schedulable                             │
 └──────────────────────────────────────────────────────────┘
```

## Quick start

### Prerequisites

- [kind](https://kind.sigs.k8s.io/) installed
- Docker installed
- `kubectl` installed

### Demo

The demo scripts create a Kind cluster from a pre-built node image that includes the
[Specialized Lifecycle Management](https://github.com/rthallisey/kubernetes/tree/specialized-lifecycle-mgmt)
feature. Then, build the driver, and deploy it:

```bash
# 1. Create a Kind cluster with SLM enabled
./demo/create-cluster.sh

# 2. Build the driver binary and container image
./demo/build-driver.sh

# 3. Deploy the driver (RBAC + DaemonSet)
./demo/deploy-driver.sh
```

The driver is now running on every node. Try it out by draining a busybox pod:

```bash
# Create the busybox pod on the worker node
./demo/deploy-busybox.sh

# Drain a worker node
make drain NODE=kssd-cluster-worker

# Watch the lifecycle event progress
kubectl get lifecycleevents -w

# Once drain completes, bring the node back
make maintenance-complete NODE=kssd-cluster-worker

# Clean up when done
./demo/delete-cluster.sh
```

The demo uses a pre-built Kind image (`ghcr.io/rthallisey/kindest-node:slm`) by default.
To build the Kind image from source instead, run:

```bash
BUILD_KIND_IMAGE=true ./demo/create-cluster.sh
```

### Manual setup

If you already have a Kubernetes cluster (v1.36+) with the `SpecializedLifecycleManagement`
feature gate and `--runtime-config=lifecycle.k8s.io/v1alpha1=true` enabled:

```bash
# Build the driver
make build

# Build the container image
docker build -t kssd:latest .

# For Kind clusters, load the image
kind load docker-image kssd:latest --name <cluster-name>

# Deploy RBAC and DaemonSet
kubectl apply -f deploy/rbac.yaml
kubectl apply -f deploy/daemonset.yaml
```

### Trigger a drain

Once the driver is running, it publishes two cluster-wide `LifecycleTransitions`. To drain a node, create a `LifecycleEvent` referencing the drain transition:

```yaml
apiVersion: lifecycle.k8s.io/v1alpha1
kind: LifecycleEvent
metadata:
  name: drain-worker-1
spec:
  transitionName: kssd-drain
  bindingNode: worker-1
```

After maintenance is done, uncordon the node by creating a second `LifecycleEvent` referencing the uncordon transition:

```bash
kubectl apply -f - <<EOF
apiVersion: lifecycle.k8s.io/v1alpha1
kind: LifecycleEvent
metadata:
  name: maint-complete-worker-1
spec:
  transitionName: kssd-maintenance-completed
  bindingNode: worker-1
EOF
```

Monitor progress:

```bash
# Watch the event status
kubectl get lifecycleevents -w

# Watch the node condition
kubectl get node kssd-cluster-worker -o jsonpath='{.status.conditions[?(@.type=="LifecycleTransition")]}'
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

## Development

```bash
# Build
go build ./cmd/kssd-driver

# Run locally against a Kind cluster
go run ./cmd/kssd-driver kubelet-plugin \
  --kubeconfig=$KUBECONFIG \
  --node-name=<node> \
  --datadir=/tmp/slm-plugins \
  --plugin-registration-path=/tmp/slm-registry \
  -v=5
```

## Community

- [Specialized Lifecycle Management - KEP-5769](https://github.com/kubernetes/enhancements/pull/5769)
- [Slack](https://slack.k8s.io/) — #sig-node-lifecycle
- [Mailing List](https://groups.google.com/a/kubernetes.io/g/dev)

Participation is governed by the [Kubernetes Code of Conduct](code-of-conduct.md).
