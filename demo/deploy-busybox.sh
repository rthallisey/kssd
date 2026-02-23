#!/usr/bin/env bash

# Copyright 2026 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Deploy a busybox pod on a worker node and wait for it to be ready.

set -ex
set -o pipefail

: ${NODE_NAME:="kssd-cluster-worker"}

kubectl apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: busybox
spec:
  nodeName: ${NODE_NAME}
  containers:
  - name: busybox
    image: busybox:latest
    command: ["sleep", "infinity"]
EOF

echo "Waiting for busybox pod to be ready..."
kubectl wait --for=condition=Ready pod/busybox --timeout=60s

set +x
printf '\033[0;32m'
echo "Busybox pod is running on node ${NODE_NAME}."
printf '\033[0m'
